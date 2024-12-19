package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var (
	errSkip = errors.New("skip")
)

var (
	logFile       = flag.String("log", "", "Log file, defaults to ~/.local/share/gtranscoder/transcode.log")
	surroundSound = flag.Bool("surround", false, "Use surround sound if possible")
	dockerImage   = flag.String("docker-image", "", "Docker image to use for ffmpeg")

	videoFileExts []string = []string{
		".mp4",
		".mkv",
		".avi",
		".flv",
		".webm",
		".mov",
		".wmv",
		".mpg",
		".mpeg",
		".m4v",
		".3gp",
		".3g2",
	}
	logMu sync.Mutex

	presets = []string{"h265", "h264", "h264_low", "svtav1", "svtav1_fast"}
)

func main() {
	flag.Parse()
	if flag.NArg() < 3 {
		fmt.Printf("Usage: %s <preset> <input directory> <out directory>\n", os.Args[0])
		fmt.Printf("Valid presets:\n")
		for _, preset := range presets {
			fmt.Printf("  %s\n", preset)
		}
		return
	}

	if !slices.Contains(presets, flag.Arg(0)) {
		fmt.Printf("Invalid preset %q\n", flag.Arg(0))
		return
	}

	fmt.Printf("Using docker image %q\n", *dockerImage)
	fmt.Printf("Using preset %q\n", flag.Arg(0))

	preset := flag.Arg(0)
	inDir := flag.Arg(1)
	outDir := flag.Arg(2)

	zap.S().Infof("Input directory: %s\n", inDir)
	zap.S().Infof("Output directory: %s\n", outDir)

	if *logFile == "" {
		*logFile = filepath.Join(os.Getenv("HOME"), ".local", "share", "gtranscoder", "transcode.log")
	}
	if err := os.MkdirAll(filepath.Dir(*logFile), 0755); err != nil {
		zap.S().Fatalf("Error creating log directory: %v", err)
	}

	var matches []string
	filepath.Walk(inDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			fmt.Printf("Error walking path %q: %v\n", path, err)
			return nil
		}
		if info.IsDir() || !slices.Contains(videoFileExts, filepath.Ext(path)) {
			return nil
		}
		matches = append(matches, path)
		return nil
	})

	slices.Sort(matches)

	zap.S().Infof("Found %d video files\n", len(matches))

	// refresh the transcode log every minute from disk. This should do a reasonably good job of catching new entries.
	var transcodeLogMu sync.Mutex
	type tlogDictKey struct {
		InputPath  string
		OutputPath string
	}
	transcodeLogDict := make(map[tlogDictKey]logFileEntry)

	go func() {
		for {
			time.Sleep(300 * time.Second)
			transcodeLogMu.Lock()
			updated, err := readLog(*logFile)
			if err != nil {
				zap.S().Warnf("Error reading transcode log: %v", err)
				continue
			}
			for _, entry := range updated {
				key := tlogDictKey{
					InputPath:  entry.InputPath,
					OutputPath: entry.OutputPath,
				}
				transcodeLogDict[key] = entry
			}
			transcodeLogMu.Unlock()
		}
	}()

	for _, match := range matches {
		relativePath := strings.TrimPrefix(match, inDir)
		outfile := filepath.Join(outDir, relativePath)
		outfile = strings.TrimSuffix(outfile, filepath.Ext(outfile)) + ".mkv"

		// resolve absolute paths
		match, err := filepath.Abs(match)
		if err != nil {
			fmt.Printf("Error resolving absolute path: %v\n", err)
			return
		}
		outfile, err = filepath.Abs(outfile)
		if err != nil {
			fmt.Printf("Error resolving absolute path: %v\n", err)
			return
		}

		// skip previously transcoded files
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
		ffprobeData, err := getFfprobeInfo(match)
		if err != nil {
			zap.S().Errorf("Item %q ffprobe error: %v\n", match, err)
			continue
		}
		if ffprobeData.GetBitrateBPS() < 2000000 {
			zap.S().Infof("Item %q is already low bitrate (%d bps), skipping\n", match, ffprobeData.GetBitrateBPS())
			continue
		}

		zap.S().Infof("Item %q is high bitrate (%d bps), encoding it to AV1\n", match, ffprobeData.GetBitrateBPS())
		transcodeMatch(preset, ffprobeData, match, outfile)
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

func transcodeMatch(preset string, probeData probeData, infile, outfile string) {
	fmt.Printf("Checking item %q -> %q\n", infile, outfile)

	// Check the whole transcode log ... this is a bit expensive but should be absolutely dwarfed by encoding times.
	entries, err := readLog(*logFile)
	if err != nil {
		zap.S().Warnf("Error reading log: %v", err)
	}
	for _, entry := range entries {
		if entry.InputPath == infile && entry.OutputPath == outfile {
			fmt.Printf("Item %q already transcoded\n", infile)
			return
		}
	}

	// Check if the output file already exists
	if _, err := os.Stat(outfile); err == nil {
		fmt.Printf("Item %q already transcoded\n", infile)
		return
	}

	namedLockSet := &NamedLockSet{File: os.TempDir() + "/gtranscoder.lockset"}
	if err := namedLockSet.TryAcquire(infile); err != nil {
		if errors.Is(err, ErrLockAlreadyHeld) {
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

	args, err := createFfmpegCommand(preset, probeData, infile, outfile)
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

	baseLog := logFileEntry{
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
		if err := appendLog(*logFile, baseLog); err != nil {
			fmt.Printf("Log write error %q: %v\n", infile, err)
		}

		if err := os.Remove(outfile + ".transcode.mkv"); err != nil {
			fmt.Printf("Item %q failure cleanup error: %v\n", infile, err)
		}
		return
	} else {
		fmt.Printf("Item %q transcoded\n", infile)
		baseLog.Duration = time.Since(startTime).String()
		if err := appendLog(*logFile, baseLog); err != nil {
			fmt.Printf("Log write error %q: %v\n", infile, err)
		}
	}

	if err := os.Rename(outfile+".transcode.mkv", outfile); err != nil {
		fmt.Printf("Item %q error: %v\n", infile, err)
	}
}

func createFfmpegCommand(preset string, probeData probeData, videoFileName string, outputFileName string) ([]string, error) {
	args := []string{
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

		args = append([]string{
			"docker", "run", "--rm", "--privileged",
			"-v", videoFileName + ":" + newVideoFileName,
			"-v", outputFileName + ":" + newOutputFileName,
			*dockerImage,
		}, args...)

		videoFileName = newVideoFileName
		outputFileName = newOutputFileName
	}

	args = append(args,
		"-i", videoFileName,
		"-map", "0:v:0", // Map first video stream
		"-map", "0:a", // Map all audio streams
		"-c:s", "copy", // Copy all subtitle streams
	)

	switch preset {
	case "h265":
		args = append(args, "-c:v", "libx265", "-crf", "28", "-preset", "fast")
	case "h264":
		args = append(args, "-c:v", "libx264", "-crf", "24", "-preset", "fast")
	case "h264_low":
		args = append(args, "-c:v", "libx264", "-crf", "28", "-preset", "fast")
	case "svtav1":
		// SVT-AV1 presets documented here: https://gitlab.com/AOMediaCodec/SVT-AV1/-/blob/master/Docs/CommonQuestions.md#what-presets-do
		// Good graphs for choosing presets: https://ottverse.com/analysis-of-svt-av1-presets-and-crf-values/
		args = append(args, "-c:v", "libsvtav1", "-crf", "24", "-preset", "6")
	case "svtav1_fast":
		// This encoder uses preset=8 which is ffmpeg's default preset. It produces decent results at crf=24
		// and is very fast to encode as it's intended for streaming use cases.
		// When time is of less concern, however, presets 4-6 e.g. in the default "svtav1" profile are preferred.
		// SVT-AV1 presets documented here: https://gitlab.com/AOMediaCodec/SVT-AV1/-/blob/master/Docs/CommonQuestions.md#what-presets-do
		args = append(args, "-c:v", "libsvtav1", "-crf", "24", "-preset", "8")
	default:
		panic("unknown preset: " + preset)
	}

	targetRateKbps1080p := 2000 // 2 MBps 1080p SVTAV1
	videoWidth, videoHeight := getResolution(probeData)
	resolutionRatio := float64(videoWidth*videoHeight) / float64(1920*1080)
	fmt.Printf("Resolution ratio: %f\n", resolutionRatio)
	if resolutionRatio < 0.5 {
		resolutionRatio = 0.5
	}
	targetMinRate := int(float64(targetRateKbps1080p) * resolutionRatio)

	args = append(args,
		"-minrate", fmt.Sprintf("%dk", targetMinRate),
		"-bufsize", fmt.Sprintf("%dk", targetMinRate),
	)

	// Handle HDR settings
	if probeData.HasHDR() {
		switch preset {
		case "h265":
			args = append(args,
				"-colorspace", "bt2020nc",
				"-color_primaries", "bt2020",
				"-color_trc", "smpte2084",
				"-x265-params", "hdr-opt=1:repeat-headers=1",
			)
		case "h264", "h264_low", "svtav1", "svtav1_fast":
			args = append(args,
				"-colorspace", "bt2020nc",
				"-color_primaries", "bt2020",
				"-color_trc", "smpte2084",
			)
		default:
			panic("unknown preset: " + preset)
		}
	} else {
		// Let's convert to a 10 bit color space, compatible with all encoders
		args = append(args, "-pix_fmt", "yuv420p10le")
	}

	if probeData.HasSurroundAudio() && *surroundSound {
		// Keep original audio format for all audio streams
		args = append(args,
			"-c:a", "copy",
		)
	} else {
		// Downmix to stereo for all audio streams
		args = append(args,
			"-c:a", "libopus",
			"-ac", "2",
			"-b:a", "128k",
		)
	}

	args = append(args, "-y", outputFileName)
	return args, nil
}

func getResolution(probeData probeData) (int, int) {
	videoIdx := videoStreamIndex(probeData)
	if videoIdx == -1 {
		return 0, 0
	}
	return probeData.Streams[videoIdx].Width, probeData.Streams[videoIdx].Height
}

func videoStreamIndex(probeData probeData) int {
	for i, stream := range probeData.Streams {
		if stream.CodecType == "video" {
			return i
		}
	}
	return -1
}
