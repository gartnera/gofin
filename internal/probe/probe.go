package probe

import "context"

// Stream describes a single media stream (video/audio/subtitle) within a file.
// It is stored as JSON on a MediaItem and mapped to api.MediaStream on the wire.
type Stream struct {
	Index      int32  `json:"index"`
	Type       string `json:"type"` // Video, Audio or Subtitle
	Codec      string `json:"codec,omitempty"`
	Width      int32  `json:"width,omitempty"`
	Height     int32  `json:"height,omitempty"`
	Channels   int32  `json:"channels,omitempty"`
	SampleRate int32  `json:"sampleRate,omitempty"`
	BitRate    int64  `json:"bitRate,omitempty"`
	Language   string `json:"language,omitempty"`
	IsDefault  bool   `json:"isDefault,omitempty"`
}

// Result is the probed metadata for a media file.
type Result struct {
	RunTimeTicks int64    `json:"runTimeTicks"`
	Streams      []Stream `json:"streams"`
}

// Prober extracts duration and stream metadata from a media file.
type Prober interface {
	Probe(ctx context.Context, path string) (Result, error)
}

// Noop is a Prober that returns no metadata. It is the fallback used when no
// media probing tool is available.
type Noop struct{}

// Probe implements Prober.
func (Noop) Probe(context.Context, string) (Result, error) { return Result{}, nil }
