package probe

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestParseFFProbe(t *testing.T) {
	data := []byte(`{
      "streams": [
        {"index":0,"codec_name":"h264","codec_type":"video","width":1920,"height":1080,"bit_rate":"5000000","disposition":{"default":1},"tags":{"language":"eng"}},
        {"index":1,"codec_name":"aac","codec_type":"audio","channels":6,"sample_rate":"48000"}
      ],
      "format": {"duration":"7.250000"}
    }`)

	res, err := parseFFProbe(data)
	if err != nil {
		t.Fatalf("parseFFProbe: %v", err)
	}
	if res.RunTimeTicks != 72_500_000 {
		t.Errorf("RunTimeTicks = %d, want 72500000", res.RunTimeTicks)
	}
	if len(res.Streams) != 2 {
		t.Fatalf("streams = %d, want 2", len(res.Streams))
	}
	v := res.Streams[0]
	if v.Type != "Video" || v.Codec != "h264" || v.Width != 1920 || v.Height != 1080 {
		t.Errorf("unexpected video stream: %+v", v)
	}
	if v.BitRate != 5_000_000 || v.Language != "eng" || !v.IsDefault {
		t.Errorf("unexpected video metadata: %+v", v)
	}
	a := res.Streams[1]
	if a.Type != "Audio" || a.Channels != 6 || a.SampleRate != 48000 {
		t.Errorf("unexpected audio stream: %+v", a)
	}
}

func TestParseFFProbeInvalid(t *testing.T) {
	if _, err := parseFFProbe([]byte("not json")); err == nil {
		t.Fatal("expected error for invalid json")
	}
}

func TestNoopProber(t *testing.T) {
	res, err := Noop{}.Probe(context.Background(), "anything")
	if err != nil || res.RunTimeTicks != 0 || len(res.Streams) != 0 {
		t.Errorf("Noop returned %+v, %v", res, err)
	}
}

// TestFFProbeReal exercises the real ffprobe binary against a tiny media file
// generated with ffmpeg. It is skipped when those binaries are unavailable.
func TestFFProbeReal(t *testing.T) {
	ff, ok := Available()
	if !ok {
		t.Skip("ffprobe not installed")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}

	path := filepath.Join(t.TempDir(), "tone.mp3")
	// 1-second sine tone -> a real, probeable audio file.
	cmd := exec.Command("ffmpeg", "-v", "quiet", "-f", "lavfi",
		"-i", "sine=frequency=440:duration=1", "-y", path)
	if err := cmd.Run(); err != nil {
		t.Skipf("ffmpeg generate failed: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}

	res, err := ff.Probe(context.Background(), path)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if res.RunTimeTicks <= 0 {
		t.Errorf("RunTimeTicks = %d, want > 0", res.RunTimeTicks)
	}
	if len(res.Streams) == 0 || res.Streams[0].Type != "Audio" {
		t.Errorf("expected an audio stream, got %+v", res.Streams)
	}
}
