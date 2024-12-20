package fsutil

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/garethgeorge/media-toolkit/internal/ffmpegutil"
	"go.uber.org/zap"
)

func MediaInDir(dir string) ([]string, error) {
	var matches []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			zap.S().Errorf("Failed to access directory: %v", err)
			return fmt.Errorf("failed to access directory: %w", err)
		}
		if info.IsDir() || !slices.Contains(ffmpegutil.VideoFileExts, filepath.Ext(path)) {
			return nil
		}
		matches = append(matches, path)
		return nil
	})
	slices.Sort(matches)
	return matches, err
}
