package flags

import (
	"flag"
	"os"
	"path/filepath"
)

var (
	logFile = flag.String("log", "", "Log file, defaults to ~/.local/share/gtranscoder/transcode.log")
)

func LogFilePath() string {
	if *logFile == "" {
		homedir, err := os.UserHomeDir()
		if err != nil {
			panic(err)
		}
		return filepath.Join(homedir, ".local", "share", "gtranscoder", "transcode.log")
	}
	return *logFile
}
