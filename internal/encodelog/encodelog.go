package encodelog

import (
	"bufio"
	"encoding/json"
	"os"

	"github.com/gofrs/flock"
	"go.uber.org/zap"
)

type LogFileEntry struct {
	InputPath  string   `json:"input,omitempty"`
	OutputPath string   `json:"output,omitempty"`
	StartTime  string   `json:"start_time,omitempty"`
	Duration   string   `json:"duration,omitempty"`
	Args       []string `json:"args,omitempty"`
	Error      string   `json:"error,omitempty"`
	Skipped    string   `json:"skipped,omitempty"`
}

func AppendLog(filename string, entry LogFileEntry) error {
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

func ReadLog(filename string) ([]LogFileEntry, error) {
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
	var entries []LogFileEntry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var entry LogFileEntry
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
