package main

import (
	"bufio"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
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
)

func main() {
	flag.Parse()
	if flag.NArg() < 2 {
		fmt.Printf("Usage: %s <input directory> <out directory>\n", os.Args[0])
		return
	}

	inDir := flag.Arg(0)
	outDir := flag.Arg(1)

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

	fmt.Printf("Found %d video files\n", len(matches))

	for _, match := range matches {
		relativePath := strings.TrimPrefix(match, inDir)
		outfile := filepath.Join(outDir, relativePath)
		outfile = strings.TrimSuffix(outfile, filepath.Ext(outfile)) + ".mkv"
		transcodeMatch(match, outfile)
	}

	fmt.Println("All items processed")
}

func transcodeMatch(infile, outfile string) {
	fmt.Printf("Checking item %q -> %q\n", infile, outfile)
	if _, err := os.Stat(outfile); err == nil {
		fmt.Printf("Item %q already transcoded\n", infile)
		return
	}

	namehash := sha1.Sum([]byte(outfile))
	lockfile := filepath.Join(tempdir, hex.EncodeToString(namehash[0:1])+".lock") // use first byte of hash as lockfile name -- 256 possible lockfiles
	lock := flock.New(lockfile)
	if ok, err := lock.TryLock(); err != nil {
		fmt.Printf("Item %q lock error: %v\n", infile, err)
		return
	} else if !ok {
		fmt.Printf("Item %q already transcoding by another process\n", infile)
		return
	}
	defer lock.Unlock()

	if _, err := os.Stat(outfile); err == nil {
		fmt.Printf("Item %q already transcoded\n", infile)
		return
	}

	if err := os.MkdirAll(filepath.Dir(outfile), 0755); err != nil {
		fmt.Printf("Item %q error: %v\n", infile, err)
		return
	}

	args := []string{
		"ffmpeg",
		"-i", infile,
		"-c:v", "libx265",
		"-crf", "28",
		"-preset", "fast",
		"-c:a", "libopus",
		"-b:a", "128k",
		"-y",
		outfile + ".transcode.mkv",
	}

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
		baseLog.Duration = time.Since(time.Now()).String()
		appendLog(*logFile, baseLog)
	} else {
		fmt.Printf("Item %q transcoded\n", infile)
		baseLog.Duration = time.Since(time.Now()).String()
		appendLog(*logFile, baseLog)
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

func loadLog(filename string) ([]logFileEntry, error) {
	logMu.Lock()
	defer logMu.Unlock()

	// read file by line
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	var entries []logFileEntry
	for scanner.Scan() {
		line := scanner.Bytes()
		var entry logFileEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, nil
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
