package ffmpegutil

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"go.uber.org/zap"
)

type StreamData struct {
	CodecType string `json:"codec_type"`
	CodecName string `json:"codec_name"`
	Channels  int    `json:"channels"`
	// HDR metadata fields
	ColorSpace     string `json:"color_space"`
	ColorTransfer  string `json:"color_transfer"`
	ColorPrimaries string `json:"color_primaries"`
	// Size
	Width  int `json:"width"`
	Height int `json:"height"`

	// Tags
	Tags struct {
		Language string `json:"language"`
	} `json:"tags"`
}

func (sd *StreamData) IsVideo() bool {
	return sd.CodecType == "video"
}

func (sd *StreamData) IsAudio() bool {
	return sd.CodecType == "audio"
}

func (sd *StreamData) IsMaybeEnglishAudio() bool {
	langLower := strings.ToLower(sd.Tags.Language)
	return langLower == "" || strings.Contains(langLower, "und") || strings.Contains(langLower, "en")
}

func (sd *StreamData) IsSubtitle() bool {
	return sd.CodecType == "subtitle"
}

func (sd *StreamData) IsSurroundAudio() bool {
	return sd.CodecType == "audio" && sd.Channels > 2
}

type ProbeData struct {
	videoFileName string `json:"-"`

	Format struct {
		BitRate string `json:"bit_rate"`
	} `json:"format"`

	Streams []StreamData `json:"streams"`
}

func GetFfprobeInfo(videoFileName string) (ProbeData, error) {
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
		return ProbeData{}, fmt.Errorf("ffprobe failed: %w", err)
	}

	var pd ProbeData
	if err := json.Unmarshal(probeOutput, &pd); err != nil {
		return ProbeData{}, fmt.Errorf("failed to parse ffprobe output: %w", err)
	}

	pd.videoFileName = videoFileName

	return pd, nil
}

func (pd *ProbeData) HasHDR() bool {
	for _, stream := range pd.Streams {
		if stream.CodecType == "video" {
			if stream.ColorSpace == "bt2020nc" && (stream.ColorTransfer == "arib-std-b67" || stream.ColorTransfer == "smpte2084") {
				return true
			}
		}
	}
	return false
}

func (pd *ProbeData) HasSurroundAudio() bool {
	for _, stream := range pd.Streams {
		if stream.CodecType == "audio" && stream.Channels > 2 {
			return true
		}
	}
	return false
}

func (pd *ProbeData) GetVideoStream() StreamData {
	for _, stream := range pd.Streams {
		if stream.CodecType == "video" {
			return stream
		}
	}
	return StreamData{}
}

func (pd *ProbeData) HasSubtitles() bool {
	for _, stream := range pd.Streams {
		if stream.CodecType == "subtitle" {
			return true
		}
	}
	return false
}

func (pd *ProbeData) GetBitrateBPS() int {
	bitrate, err := strconv.Atoi(pd.Format.BitRate)
	if err != nil {
		zap.S().Warnf("failed to parse bitrate: %v", err)
		return 0
	}
	return bitrate
}

func (pd *ProbeData) MapStreamIdx(codecType string, rawStreamIdx int) int {
	idx := 0
	for i := 0; i < len(pd.Streams) && i < rawStreamIdx; i++ {
		if pd.Streams[i].CodecType != codecType {
			continue
		}
		idx++
	}
	return idx
}
