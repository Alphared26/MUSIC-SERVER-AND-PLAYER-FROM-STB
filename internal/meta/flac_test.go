package meta

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// flacBytes builds a structurally real FLAC: "fLaC" magic, a STREAMINFO block
// and an optional VORBIS_COMMENT block, each preceded by its 4-byte header.
// 44100 Hz stereo 16-bit with 44100 total samples is exactly 1.000 s.
func flacBytes(tags []string) []byte {
	var b []byte
	b = append(b, "fLaC"...)

	si := make([]byte, 34)
	binary.BigEndian.PutUint16(si[0:2], 4096) // min block size
	binary.BigEndian.PutUint16(si[2:4], 4096) // max block size
	// si[10:18] packs sample rate (20b), channels-1 (3b), bps-1 (5b),
	// total samples (36b), big endian. The intermediate shifts are done in
	// uint32/uint64 because a constant expression would overflow byte.
	sr := uint32(44100)
	total := uint64(44100)
	si[10] = uint8(sr >> 12)
	si[11] = uint8(sr >> 4)
	si[12] = uint8(sr<<4) | uint8((2-1)<<1) | uint8((16-1)>>4)
	si[13] = uint8((16-1)<<4) | uint8(total>>32)
	si[14] = uint8(total >> 24)
	si[15] = uint8(total >> 16)
	si[16] = byte(total >> 8)
	si[17] = byte(total)
	b = append(b, 0x00, 0x00, 0x00, byte(len(si))) // not-last, type 0
	b = append(b, si...)

	if len(tags) == 0 {
		// A last padding block so the walk terminates without a VORBIS_COMMENT.
		return append(b, 0x81, 0x00, 0x00, 0x06, 0, 0, 0, 0, 0, 0)
	}
	// VORBIS_COMMENT is vendor_length + vendor string + comment_count +
	// comments. Building it with a vendor string is deliberate: reading
	// vendor_length as the comment count silently loses every tag.
	var vc []byte
	vendor := []byte("reference libFLAC")
	vl := make([]byte, 4)
	binary.LittleEndian.PutUint32(vl, uint32(len(vendor)))
	vc = append(vc, vl...)
	vc = append(vc, vendor...)
	cnt := make([]byte, 4)
	binary.LittleEndian.PutUint32(cnt, uint32(len(tags)))
	vc = append(vc, cnt...)
	for _, f := range tags {
		n := make([]byte, 4)
		binary.LittleEndian.PutUint32(n, uint32(len(f)))
		vc = append(vc, n...)
		vc = append(vc, f...)
	}
	b = append(b, 0x84, byte(len(vc)>>16), byte(len(vc)>>8), byte(len(vc))) // last, type 4
	return append(b, vc...)
}

func writeTestFLAC(t *testing.T, name string, tags []string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, flacBytes(tags), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// STREAMINFO lives at offset 8, after the magic AND its 4-byte block header.
// Reading it at 4 silently yields a garbage sample rate and a multi-hour
// duration rather than failing loudly.
func TestParseFLACStreamInfoOffset(t *testing.T) {
	i, err := Parse(writeTestFLAC(t, "x.flac", nil))
	if err != nil {
		t.Fatal(err)
	}
	if i.SampleRate != 44100 {
		t.Errorf("SampleRate = %d, want 44100 (STREAMINFO read at the wrong offset?)", i.SampleRate)
	}
	if i.Channels != 2 {
		t.Errorf("Channels = %d, want 2", i.Channels)
	}
	if i.BitsPerSample != 16 {
		t.Errorf("BitsPerSample = %d, want 16", i.BitsPerSample)
	}
	if i.DurationMs != 1000 {
		t.Errorf("DurationMs = %d, want 1000", i.DurationMs)
	}
	if i.Format != "flac" {
		t.Errorf("Format = %q, want flac", i.Format)
	}
}

func TestParseFLACVorbisComment(t *testing.T) {
	i, err := Parse(writeTestFLAC(t, "x.flac", []string{
		"TITLE=Afterlife", "ARTIST=Illenium", "ALBUM=Ashes",
		"ALBUMARTIST=Illenium", "TRACKNUMBER=10",
	}))
	if err != nil {
		t.Fatal(err)
	}
	deref := func(s *string) string {
		if s == nil {
			return "<nil>"
		}
		return *s
	}
	if got := deref(i.Title); got != "Afterlife" {
		t.Errorf("Title = %q, want Afterlife", got)
	}
	if got := deref(i.Artist); got != "Illenium" {
		t.Errorf("Artist = %q, want Illenium", got)
	}
	if got := deref(i.Album); got != "Ashes" {
		t.Errorf("Album = %q, want Ashes", got)
	}
	if got := deref(i.AlbumArtist); got != "Illenium" {
		t.Errorf("AlbumArtist = %q, want Illenium", got)
	}
	if i.TrackNum == nil || *i.TrackNum != 10 {
		t.Errorf("TrackNum = %v, want 10", i.TrackNum)
	}
}

// An untagged file still needs a title, taken from its filename.
func TestParseFLACTitleFallback(t *testing.T) {
	i, err := Parse(writeTestFLAC(t, "Some Song.flac", nil))
	if err != nil {
		t.Fatal(err)
	}
	if i.Title == nil || !strings.Contains(*i.Title, "Some Song") {
		t.Errorf("Title = %v, want it derived from the filename", i.Title)
	}
}
