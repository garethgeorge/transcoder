package main

import (
	"encoding/json"
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

	"github.com/gofrs/flock"
)

var (
	logFile = flag.String("log", "transcodelog.ndjson", "Log file")

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

	tempdir = filepath.Join(os.TempDir(), "go-transcoder")

	presets = []string{"h265", "h264"}
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

	fmt.Printf("Using preset %q\n", flag.Arg(0))
	fmt.Printf("Temp directory: %s\n", tempdir)

	preset := flag.Arg(0)
	inDir := flag.Arg(1)
	outDir := flag.Arg(2)

	fmt.Printf("Input directory: %s\n", inDir)
	fmt.Printf("Output directory: %s\n", outDir)

	if err := os.MkdirAll(tempdir, 0755); err != nil {
		fmt.Printf("Error creating temp directory: %v\n", err)
		return
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

	fmt.Printf("Found %d video files\n", len(matches))

	for _, match := range matches {
		relativePath := strings.TrimPrefix(match, inDir)
		outfile := filepath.Join(outDir, relativePath)
		outfile = strings.TrimSuffix(outfile, filepath.Ext(outfile)) + ".mkv"
		transcodeMatch(preset, match, outfile)
	}

	fmt.Println("All items processed")
}

func transcodeMatch(preset, infile, outfile string) {
	fmt.Printf("Checking item %q -> %q\n", infile, outfile)
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

	args, err := createFfmpegCommand(preset, infile, outfile+".transcode.mkv")
	if err != nil {
		fmt.Printf("Item %q error forming ffmpeg command: %v\n", infile, err)
		return
	}

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

type logFileEntry struct {
	InputPath  string   `json:"input"`
	OutputPath string   `json:"output"`
	StartTime  string   `json:"start_time"`
	Duration   string   `json:"duration"`
	Args       []string `json:"args"`
	Error      string   `json:"error"`
}

func appendLog(filename string, entry logFileEntry) error {
	lock := flock.New(filename + ".lock")
	if err := lock.Lock(); err != nil {
		return err
	}
	defer lock.Unlock()
	f, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	if err := enc.Encode(entry); err != nil {
		return err
	}
	return nil
}

type probeData struct {
	Streams []struct {
		CodecType string `json:"codec_type"`
		Channels  int    `json:"channels"`
		// HDR metadata fields
		ColorSpace     string `json:"color_space"`
		ColorTransfer  string `json:"color_transfer"`
		ColorPrimaries string `json:"color_primaries"`
	} `json:"streams"`
}

func createFfmpegCommand(preset string, videoFileName string, outputFileName string) ([]string, error) {
	// Get file metadata using ffprobe
	probeCmd := exec.Command("ffprobe",
		"-v", "quiet",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		videoFileName,
	)
	probeOutput, err := probeCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe failed: %w", err)
	}

	var probeData probeData
	if err := json.Unmarshal(probeOutput, &probeData); err != nil {
		return nil, fmt.Errorf("failed to parse ffprobe output: %w", err)
	}

	args := []string{
		"ffmpeg",
		"-i", videoFileName,
		"-map", "0:v:0", // Map first video stream
		"-map", "0:a", // Map all audio streams
		"-c:s", "copy", // Copy all subtitle streams
	}

	switch preset {
	case "h265":
		args = append(args, "-c:v", "libx265", "-crf", "28", "-preset", "fast")
	case "h264":
		args = append(args, "-c:v", "libx264", "-crf", "24", "-preset", "fast")
	default:
		panic("unknown preset: " + preset)
	}

	// Handle HDR settings
	if isHDR(probeData) {
		switch preset {
		case "h265":
			args = append(args,
				"-colorspace", "bt2020nc",
				"-color_primaries", "bt2020",
				"-color_trc", "smpte2084",
				"-x265-params", "hdr-opt=1:repeat-headers=1",
			)
		case "h264":
			args = append(args,
				"-colorspace", "bt2020nc",
				"-color_primaries", "bt2020",
				"-color_trc", "smpte2084",
			)
		default:
			panic("unknown preset: " + preset)
		}
	} else {
		// Let's convert to a 10 bit color space
		args = append(args, "-pix_fmt", "yuv420p10le")
	}

	// TODO: properly handle surround audio
	for _, stream := range probeData.Streams {
		if stream.CodecType == "audio" {
			if stream.Channels > 2 {
				// Downmix to stereo for all audio streams
				args = append(args,
					"-c:a", "libopus",
					"-ac", "2",
					"-b:a", "128k",
				)
			} else {
				// Keep original channel count for all audio streams
				args = append(args,
					"-c:a", "libopus",
					"-b:a", "128k",
				)
			}
			break
		}
	}

	args = append(args, "-y", outputFileName)
	return args, nil
}

func isHDR(probeData probeData) bool {
	for _, stream := range probeData.Streams {
		if stream.CodecType == "video" {
			if stream.ColorSpace == "bt2020nc" && (stream.ColorTransfer == "arib-std-b67" || stream.ColorTransfer == "smpte2084") {
				return true
			}
		}
	}
	return false
}
