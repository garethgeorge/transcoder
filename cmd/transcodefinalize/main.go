package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/garethgeorge/media-toolkit/internal/encodelog"
	"github.com/garethgeorge/media-toolkit/internal/flags"
	"github.com/garethgeorge/media-toolkit/internal/fsutil"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var (
	dryRun = flag.Bool("dry-run", true, "Dry run mode")
)

func main() {
	flag.Parse()

	if flag.NArg() < 1 {
		fmt.Println("Usage: transcodefinalize <finalized directory>")
		return
	}

	finalizeDir := flag.Arg(0)

	fmt.Printf("Finalizing directory: %s\n", finalizeDir)

	matches, err := fsutil.MediaInDir(finalizeDir)
	if err != nil {
		zap.S().Fatalf("Error listing input directory: %v", err)
	}

	zap.S().Infof("Found %d video files\n", len(matches))

	transcodeLog, err := encodelog.ReadLog(flags.LogFilePath())
	if err != nil {
		zap.S().Fatalf("Error reading transcode log: %v", err)
	}

	transcodeLogMap := make(map[string]encodelog.LogFileEntry)
	for _, entry := range transcodeLog {
		transcodeLogMap[entry.OutputPath] = entry
	}

	for _, match := range matches {
		zap.S().Debugf("Checking if media file %q exists in transcode log", match)
		logEntry, ok := transcodeLogMap[match]
		if !ok {
			zap.S().Debugf("Media file %q does not exist in transcode log", match)
			continue
		}
		if logEntry.Error != "" {
			zap.S().Warnf("Media file %q has errors in transcode log, keeping: %s", match, logEntry.Error)
			continue
		}
		if logEntry.Skipped != "" {
			zap.S().Warnf("Media file %q was skipped in transcode log, keeping: %s", match, logEntry.Skipped)
			continue
		}

		// Is it a dry run?
		if *dryRun {
			zap.S().Infof("Would remove original media file %q", match)
			continue
		}

		zap.S().Infof("Removing original media file %q", match)
		if err := os.Remove(match); err != nil {
			zap.S().Warnf("Failed to remove original media file %q: %v", match, err)
		}
	}
}

func init() {
	// Create a colored zap console logger
	consoleConfig := zap.NewDevelopmentConfig()
	consoleConfig.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	consoleLogger, _ := consoleConfig.Build()
	zap.ReplaceGlobals(consoleLogger)
}
