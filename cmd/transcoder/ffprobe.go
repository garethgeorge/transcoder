package main

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"

	"go.uber.org/zap"
)

type streamData struct {
	CodecType string `json:"codec_type"`
	Channels  int    `json:"channels"`
	// HDR metadata fields
	ColorSpace     string `json:"color_space"`
	ColorTransfer  string `json:"color_transfer"`
	ColorPrimaries string `json:"color_primaries"`
	// Size
	Width  int `json:"width"`
	Height int `json:"height"`
}

type probeData struct {
	videoFileName string `json:"-"`

	Format struct {
		BitRate string `json:"bit_rate"`
	} `json:"format"`

	Streams []streamData `json:"streams"`
}

func getFfprobeInfo(videoFileName string) (probeData, error) {
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
		return probeData{}, fmt.Errorf("ffprobe failed: %w", err)
	}

	var pd probeData
	if err := json.Unmarshal(probeOutput, &pd); err != nil {
		return probeData{}, fmt.Errorf("failed to parse ffprobe output: %w", err)
	}

	pd.videoFileName = videoFileName

	return pd, nil
}

func (pd *probeData) HasHDR() bool {
	for _, stream := range pd.Streams {
		if stream.CodecType == "video" {
			if stream.ColorSpace == "bt2020nc" && (stream.ColorTransfer == "arib-std-b67" || stream.ColorTransfer == "smpte2084") {
				return true
			}
		}
	}
	return false
}

func (pd *probeData) HasSurroundAudio() bool {
	for _, stream := range pd.Streams {
		if stream.CodecType == "audio" && stream.Channels > 2 {
			return true
		}
	}
	return false
}

func (pd *probeData) GetVideoStream() *streamData {
	for _, stream := range pd.Streams {
		if stream.CodecType == "video" {
			return &stream
		}
	}
	return nil
}

func (pd *probeData) HasSubtitles() bool {
	for _, stream := range pd.Streams {
		if stream.CodecType == "subtitle" {
			return true
		}
	}
	return false
}

func (pd *probeData) GetBitrateBPS() int {
	bitrate, err := strconv.Atoi(pd.Format.BitRate)
	if err != nil {
		zap.S().Warnf("failed to parse bitrate: %v", err)
		return 0
	}
	return bitrate
}
