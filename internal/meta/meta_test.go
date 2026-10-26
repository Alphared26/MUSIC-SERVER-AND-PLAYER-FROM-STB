package meta

import (
	"os"
	"path/filepath"
	"testing"
)

// buildWAV writes a minimal RIFF/WAVE file and returns its path.
func buildWAV(t *testing.T, name string, junk bool, dataBytes int) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	// The 'data' chunk header takes 8 bytes, and the junk case adds 28, so the
	// payload starts at 72 bytes there vs 44 for a plain file.
	head := 44
	if junk {
		head = 72
	}
	body := make([]byte, head+dataBytes)
	copy(body[0:], "RIFF")
	le32(body[4:], uint32(len(body)-8))
	copy(body[8:], "WAVE")
	off := 12
	if junk {
		// Many real files put a JUNK chunk before 'fmt '.
		copy(body[off:], "JUNK")
		le32(body[off+4:], 20)
		off += 28
	}
	copy(body[off:], "fmt ")
	le32(body[off+4:], 16)
	le16(body[off+8:], 1)   // PCM
	le16(body[off+10:], 2)  // stereo
	le32(body[off+12:], 44100)
	le32(body[off+16:], 44100*4)
	le16(body[off+20:], 4)
	le16(body[off+22:], 16)
	off += 24
	copy(body[off:], "data")
	le32(body[off+4:], uint32(dataBytes))
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func le16(b []byte, v uint16) { b[0], b[1] = byte(v), byte(v>>8) }
func le32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
}

func TestParseWAVDuration(t *testing.T) {
	// 44100 Hz * 2 ch * 2 bytes = 176400 bytes/s, so 352800 bytes = 2 seconds.
	p := buildWAV(t, "Two Seconds.wav", false, 352800)
	info, err := Parse(p)
	if err != nil {
		t.Fatal(err)
	}
	if info.Format != "wav" {
		t.Fatalf("format = %q, want wav", info.Format)
	}
	if info.DurationMs != 2000 {
		t.Errorf("duration = %dms, want 2000ms", info.DurationMs)
	}
	if info.SampleRate != 44100 || info.Channels != 2 || info.BitsPerSample != 16 {
		t.Errorf("format spec = %dHz/%dch/%dbit, want 44100/2/16",
			info.SampleRate, info.Channels, info.BitsPerSample)
	}
	if info.Title == nil || *info.Title != "Two Seconds" {
		t.Errorf("title = %v, want \"Two Seconds\"", info.Title)
	}
	if info.Bitrate != 1411 {
		t.Errorf("bitrate = %d, want 1411", info.Bitrate)
	}
}

func TestParseWAVWithJunkChunk(t *testing.T) {
	p := buildWAV(t, "Junked.wav", true, 176400) // 1 second
	info, err := Parse(p)
	if err != nil {
		t.Fatal(err)
	}
	if info.DurationMs != 1000 {
		t.Errorf("duration = %dms, want 1000ms (fmt not at offset 12)", info.DurationMs)
	}
}
