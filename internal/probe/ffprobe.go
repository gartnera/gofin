package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
)

// FFProbe probes media files by shelling out to the ffprobe binary.
type FFProbe struct {
	// Bin is the ffprobe executable name or path. Defaults to "ffprobe".
	Bin string
}

// Available reports whether an ffprobe binary can be found, returning a ready
// FFProbe prober if so.
func Available() (*FFProbe, bool) {
	for _, bin := range []string{"ffprobe"} {
		if _, err := exec.LookPath(bin); err == nil {
			return &FFProbe{Bin: bin}, true
		}
	}
	return nil, false
}

// Probe runs ffprobe against path and parses its JSON output.
func (f *FFProbe) Probe(ctx context.Context, path string) (Result, error) {
	bin := f.Bin
	if bin == "" {
		bin = "ffprobe"
	}
	cmd := exec.CommandContext(ctx, bin,
		"-v", "quiet",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		path,
	)
	out, err := cmd.Output()
	if err != nil {
		return Result{}, fmt.Errorf("ffprobe %q: %w", path, err)
	}
	return parseFFProbe(out)
}

// ffprobeOutput mirrors the subset of ffprobe JSON we consume.
type ffprobeOutput struct {
	Streams []struct {
		Index       int32             `json:"index"`
		CodecName   string            `json:"codec_name"`
		CodecType   string            `json:"codec_type"`
		Width       int32             `json:"width"`
		Height      int32             `json:"height"`
		Channels    int32             `json:"channels"`
		SampleRate  string            `json:"sample_rate"`
		BitRate     string            `json:"bit_rate"`
		Tags        map[string]string `json:"tags"`
		Disposition map[string]int    `json:"disposition"`
	} `json:"streams"`
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

// parseFFProbe converts ffprobe JSON into a Result. It is separated from the
// exec call so it can be unit-tested with canned output.
func parseFFProbe(data []byte) (Result, error) {
	var raw ffprobeOutput
	if err := json.Unmarshal(data, &raw); err != nil {
		return Result{}, fmt.Errorf("parse ffprobe json: %w", err)
	}

	res := Result{}
	if secs, err := strconv.ParseFloat(raw.Format.Duration, 64); err == nil {
		// Jellyfin runtime ticks are 100-nanosecond units (1s = 1e7 ticks).
		res.RunTimeTicks = int64(secs * 1e7)
	}
	for _, s := range raw.Streams {
		stream := Stream{
			Index:    s.Index,
			Type:     normalizeType(s.CodecType),
			Codec:    s.CodecName,
			Width:    s.Width,
			Height:   s.Height,
			Channels: s.Channels,
		}
		if rate, err := strconv.Atoi(s.SampleRate); err == nil {
			stream.SampleRate = int32(rate)
		}
		if br, err := strconv.ParseInt(s.BitRate, 10, 64); err == nil {
			stream.BitRate = br
		}
		if s.Tags != nil {
			stream.Language = s.Tags["language"]
		}
		stream.IsDefault = s.Disposition["default"] == 1
		res.Streams = append(res.Streams, stream)
	}
	return res, nil
}

// normalizeType maps ffprobe codec_type values to Jellyfin MediaStream types.
func normalizeType(codecType string) string {
	switch codecType {
	case "video":
		return "Video"
	case "audio":
		return "Audio"
	case "subtitle":
		return "Subtitle"
	default:
		return "Data"
	}
}
