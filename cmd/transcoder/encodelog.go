package main

import (
	"bufio"
	"encoding/json"
	"os"

	"github.com/gofrs/flock"
	"go.uber.org/zap"
)

type logFileEntry struct {
	InputPath  string   `json:"input"`
	OutputPath string   `json:"output"`
	StartTime  string   `json:"start_time"`
	Duration   string   `json:"duration"`
	Args       []string `json:"args"`
	Error      string   `json:"error"`
	Skipped    string   `json:"skipped"` // a string reason for skipping.
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

func readLog(filename string) ([]logFileEntry, error) {
	lock := flock.New(filename + ".lock")
	if err := lock.RLock(); err != nil {
		return nil, err
	}
	defer lock.Unlock()

	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	// parse the file line by line as NDJSON
	var entries []logFileEntry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var entry logFileEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			zap.S().Warnf("failed to parse transcode log entry: %v", err)
			continue
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}
