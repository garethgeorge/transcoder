package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/garethgeorge/media-toolkit/internal/encodelog"
	"github.com/garethgeorge/media-toolkit/internal/ffmpegutil"
	"github.com/garethgeorge/media-toolkit/internal/flags"
	"github.com/garethgeorge/media-toolkit/internal/fsutil"
	"github.com/garethgeorge/media-toolkit/internal/lockutil"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var (
	errSkip = errors.New("skip")
)

var (
	dockerImage = flag.String("docker-image", "", "Docker image to use for ffmpeg")
	dockerCpus  = flag.String("docker-cpus", "", "CPU set CPUs to use for encoding e.g. by index 0,1,2,3,....")

	preset = flag.Int("preset", 8, "Preset to use for encoding. Preset = 8 is fast and disables filmgrain detection / synthesis. Preset = 6 is good for movies and provides a good quality balance.")

	// files with these suffixes are already encoded and are ignored
	encoderSuffixes []string = []string{
		"svtav1enc.mkv",
		"svtav1enc.mp4",
		".transcode.mkv",
	}
)

const (
	bitrateTarget       = 3000000 // target bitrate if re-encoding is 2 Mbps AV1 at 1080p
	lowBitrateThreshold = 4000000 // don't encode anything that's already below this at 1080p
)

func main() {
	flag.Parse()
	if flag.NArg() < 1 {
		fmt.Printf("Usage: %s <input directory>\n", os.Args[0])
		return
	}

	fmt.Printf("Using docker image %q\n", *dockerImage)

	inDir := flag.Arg(0)

	zap.S().Infof("Input directory: %s\n", inDir)

	logFile := flags.LogFilePath()

	if err := os.MkdirAll(filepath.Dir(logFile), 0755); err != nil {
		zap.S().Fatalf("Error creating log directory: %v", err)
	}

	matches, err := fsutil.MediaInDir(inDir)
	if err != nil {
		zap.S().Fatalf("Error listing input directory: %v", err)
	}

	zap.S().Infof("Found %d video files\n", len(matches))

	// refresh the transcode log every minute from disk. This should do a reasonably good job of catching new entries.
	type tlogDictKey struct {
		InputPath  string
		OutputPath string
	}
	lastTranscodeLogUpdate := time.Time{}
	transcodeLogDict := make(map[tlogDictKey]encodelog.LogFileEntry)

	refreshTranscodeLog := func() {
		if time.Since(lastTranscodeLogUpdate) > 60*time.Second {
			zap.S().Infof("Refreshing transcode log")
			updated, err := encodelog.ReadLog(logFile)
			if err != nil {
				zap.S().Warnf("Error reading transcode log: %v", err)
				return
			}
			for _, entry := range updated {
				key := tlogDictKey{
					InputPath:  entry.InputPath,
					OutputPath: entry.OutputPath,
				}
				transcodeLogDict[key] = entry
			}
			zap.S().Infof("Refreshed transcode log, loaded %d entries", len(transcodeLogDict))
			lastTranscodeLogUpdate = time.Now()
		}
	}

	for _, match := range matches {
		// resolve absolute paths
		match, err := filepath.Abs(match)
		if err != nil {
			fmt.Printf("Error resolving absolute path: %v\n", err)
			return
		}

		// skip files that are already encoded
		if isEncodedFile(match) {
			continue
		}

		outfile := deriveFilename(match)
		zap.S().Infof("Item %q", match)

		// skip previously transcoded files
		refreshTranscodeLog()
		found, ok := transcodeLogDict[tlogDictKey{
			InputPath:  match,
			OutputPath: outfile,
		}]
		if ok {
			if found.Error != "" {
				zap.S().Infof("Item %q was previously attempted but failed, skipping: %s\n", match, found.Error)
				continue
			}
			if found.Skipped != "" {
				zap.S().Infof("Item %q was previously skipped: %s\n", match, found.Skipped)
				continue
			}
			if found.Duration != "" {
				zap.S().Infof("Item %q was previously transcoded: took %s\n", match, found.Duration)
				continue
			}
			zap.S().Infof("Item %q was previously transcoded, skipping\n", match)
			continue
		}

		// examine whether we should encode the file or not
		ffprobeData, err := ffmpegutil.GetFfprobeInfo(match)
		if err != nil {
			zap.S().Errorf("Item %q ffprobe error: %v\n", match, err)
			continue
		}
		if ffprobeData.GetBitrateBPS() < lowBitrateThreshold {
			zap.S().Infof("Item %q is already low bitrate (%d bps), skipping\n", match, ffprobeData.GetBitrateBPS())
			encodelog.AppendLog(logFile, encodelog.LogFileEntry{
				InputPath:  match,
				OutputPath: outfile,
				Skipped:    fmt.Sprintf("already low bitrate (%d bps)", ffprobeData.GetBitrateBPS()),
			})
			continue
		}

		zap.S().Infof("Item %q is high bitrate (%d bps), encoding it to AV1\n", match, ffprobeData.GetBitrateBPS())
		transcodeMatch(ffprobeData, match, outfile)
	}

	zap.S().Infof("All items processed")
}

func init() {
	// Create a colored zap console logger
	consoleConfig := zap.NewDevelopmentConfig()
	consoleConfig.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	consoleLogger, _ := consoleConfig.Build()
	zap.ReplaceGlobals(consoleLogger)
}

func deriveFilename(inFile string) string {
	ext := filepath.Ext(inFile)
	inFile = strings.TrimSuffix(inFile, ext)
	return fmt.Sprintf("%s-svtav1enc.mkv", inFile)
}

func isEncodedFile(filename string) bool {
	for _, suffix := range encoderSuffixes {
		if strings.HasSuffix(filename, suffix) {
			return true
		}
	}
	return false
}

func transcodeMatch(probeData ffmpegutil.ProbeData, infile, outfile string) {
	// Check if the output file already exists
	if _, err := os.Stat(outfile); err == nil {
		zap.S().Warnf("Outfile for item %q already exists, skipping\n", infile)
		return
	}

	namedLockSet := &lockutil.NamedLockSet{File: os.TempDir() + "/gtranscoder.lockset"}
	if err := namedLockSet.TryAcquire(infile); err != nil {
		if errors.Is(err, lockutil.ErrLockAlreadyHeld) {
			fmt.Printf("Item %q already transcoding by another proces: %v\n", infile, err)
			return
		}
		fmt.Printf("Item %q failed to acquire lock unknown error: %v\n", infile, err)
		return
	}
	defer namedLockSet.Release(infile)

	if _, err := os.Stat(outfile); err == nil {
		fmt.Printf("Item %q already transcoded\n", infile)
		return
	}

	if err := os.MkdirAll(filepath.Dir(outfile), 0755); err != nil {
		fmt.Printf("Item %q error: %v\n", infile, err)
		return
	}

	args, err := createFfmpegCommand(probeData, infile, outfile+".transcode.mkv")
	if err != nil {
		if errors.Is(err, errSkip) {
			return
		}
		fmt.Printf("Item %q error forming ffmpeg command: %v\n", infile, err)
		return
	}

	zap.S().Infof("Item %q command: %s\n", infile, strings.Join(args, " "))

	startTime := time.Now()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	baseLog := encodelog.LogFileEntry{
		InputPath:  infile,
		OutputPath: outfile,
		StartTime:  time.Now().Format(time.RFC3339),
		Duration:   "0s",
		Args:       args,
	}

	if err := cmd.Run(); err != nil {
		fmt.Printf("Item %q error: %v\n", infile, err)
		baseLog.Error = err.Error()
		baseLog.Duration = time.Since(startTime).String()
		if err := encodelog.AppendLog(flags.LogFilePath(), baseLog); err != nil {
			fmt.Printf("Log write error %q: %v\n", infile, err)
		}

		if err := os.Remove(outfile + ".transcode.mkv"); err != nil {
			fmt.Printf("Item %q failure cleanup error: %v\n", infile, err)
		}
		return
	} else {
		fmt.Printf("Item %q transcoded\n", infile)
		baseLog.Duration = time.Since(startTime).String()
		if err := encodelog.AppendLog(flags.LogFilePath(), baseLog); err != nil {
			fmt.Printf("Log write error %q: %v\n", infile, err)
		}
	}

	if err := os.Rename(outfile+".transcode.mkv", outfile); err != nil {
		fmt.Printf("Item %q error: %v\n", infile, err)
	}
}

func createFfmpegCommand(probeData ffmpegutil.ProbeData, videoFileName string, outputFileName string) ([]string, error) {
	args := []string{
		"nice", "-n", "19",
		"ffmpeg",
	}

	if *dockerImage != "" {
		// touch output file path
		if err := os.MkdirAll(filepath.Dir(outputFileName), 0755); err != nil {
			return nil, fmt.Errorf("failed to create output directory: %w", err)
		}
		if err := os.WriteFile(outputFileName, []byte{}, 0644); err != nil {
			return nil, fmt.Errorf("failed to create output file: %w", err)
		}

		newVideoFileName := "/input" + filepath.Ext(videoFileName)
		newOutputFileName := "/output" + filepath.Ext(outputFileName)

		oldArgs := args
		args = append([]string{
			"docker", "run", "--rm", "--privileged",
			"-v", videoFileName + ":" + newVideoFileName,
			"-v", outputFileName + ":" + newOutputFileName,
		})
		if *dockerCpus != "" {
			args = append(args, "--cpuset-cpus", fmt.Sprintf("%s", *dockerCpus))
		}
		args = append(args,
			*dockerImage,
		)
		args = append(args, oldArgs...)

		videoFileName = newVideoFileName
		outputFileName = newOutputFileName
	}

	args = append(args,
		"-i", videoFileName,
	)

	// Step 1: encode video
	// map the video stream
	videoStream := probeData.GetVideoStream()
	if videoStream == (ffmpegutil.StreamData{}) {
		return nil, fmt.Errorf("no video stream")
	}

	targetMinRateBPS := scaleBitrateToResolution(bitrateTarget, videoStream.Width, videoStream.Height)
	zap.S().Debugf("Target min bitrate scaled for resolution %dx%d: %d", videoStream.Width, videoStream.Height, targetMinRateBPS)

	// Documentation on SVTAV1 params https://gitlab.com/AOMediaCodec/SVT-AV1/-/blob/master/Docs/Ffmpeg.md#example-2-encoding-for-personal-use
	args = append(args,
		"-map", "0:v", "-c:v", "libsvtav1", "-crf", "24", "-preset", fmt.Sprintf("%d", *preset),
	)

	if *preset <= 6 {
		args = append(args, "-svtav1-params", "tune=0:film-grain=8") // optimized for subjective visual quality and will detect and add / film grain.
	} else {
		args = append(args, "-svtav1-params", "tune=0:film-grain=0") // optimized for subjective visual quality and do nothing with film grain.
	}

	args = append(args,
		"-minrate", fmt.Sprintf("%dk", targetMinRateBPS/1000),
		"-bufsize", fmt.Sprintf("%dk", targetMinRateBPS/1000))

	// Handle HDR settings
	if probeData.HasHDR() {
		args = append(args,
			"-colorspace", "bt2020nc",
			"-color_primaries", "bt2020",
			"-color_trc", "smpte2084",
			"-strict", "experimental",
		)
	} else {
		// Let's always encode in 10 bit color
		args = append(args, "-pix_fmt", "yuv420p10le")
	}

	// Step 2: map and convert audio as needed, only maps audio if the language looks like it should be english.
	outAudioIdx := 0
	for idx, stream := range probeData.Streams {
		if !stream.IsAudio() {
			continue
		}
		audioIdx := probeData.MapStreamIdx("audio", idx)
		args = append(args, "-map", fmt.Sprintf("0:a:%d", audioIdx))
		if stream.IsSurroundAudio() {
			args = append(args, fmt.Sprintf("-c:a:%d", outAudioIdx), "copy") // copy any surround audio channel
		} else {
			args = append(args, fmt.Sprintf("-c:a:%d", outAudioIdx), "libopus", "-b:a", "192k", "-ac", "2")
		}
		outAudioIdx++
	}

	// Step 3: copy all subtitles
	if probeData.HasSubtitles() {
		args = append(args, "-c:s", "copy")
	}

	args = append(args, "-y", outputFileName) // allow overwriting output

	return args, nil
}

func scaleBitrateToResolution(bitrate int, videoWidth int, videoHeight int) int {
	ratio := float64(videoWidth*videoHeight) / float64(1920*1080)
	if ratio < 0.5 {
		ratio = 0.5
	}
	if ratio > 4 {
		ratio = 4
	}
	return int(float64(bitrate) * ratio)
}
