package meta

import (
	"strings"
	"testing"
	"unicode/utf16"
)

// buildUSLT assembles an ID3v2 USLT frame body for the given encoding:
// 1 byte encoding, 3 bytes language, NUL-terminated descriptor, lyric text.
func buildUSLT(enc byte, descriptor, text string) string {
	b := []byte{enc, 'e', 'n', 'g'}
	switch enc {
	case 1: // UTF-16: descriptor and text are both UTF-16, unit terminated
		put := func(s string) {
			for _, u := range utf16.Encode([]rune(s)) {
				b = append(b, byte(u), byte(u>>8))
			}
			b = append(b, 0, 0)
		}
		put(descriptor)
		put(text)
	case 3: // UTF-8: NUL-terminated descriptor, then NUL-terminated text
		b = append(b, descriptor...)
		b = append(b, 0)
		b = append(b, text...)
		b = append(b, 0)
	}
	return string(b)
}

func TestID3LyricsText(t *testing.T) {
	cases := []struct {
		name string
		enc  byte
		desc string
		text string
	}{
		{"utf8", 3, "", "hello\nworld"},
		{"utf8 with descriptor", 3, "eng", "hello"},
		{"utf16", 1, "", "héllo"},
		{"utf16 with descriptor", 1, "desc", "line one"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := id3LyricsText(buildUSLT(c.enc, c.desc, c.text))
			if !strings.Contains(got, c.text) {
				t.Fatalf("got %q, want it to contain %q", got, c.text)
			}
		})
	}
}

func TestID3LyricsTextMalformed(t *testing.T) {
	// No descriptor terminator: treat as no lyrics rather than returning the
	// descriptor as if it were the lyric text.
	if got := id3LyricsText(string([]byte{3, 'e', 'n', 'g', 'n', 'o', 'p', 'e'})); got != "" {
		t.Fatalf("malformed frame returned %q, want empty", got)
	}
	// Too short to hold a header.
	if got := id3LyricsText("ab"); got != "" {
		t.Fatalf("short frame returned %q, want empty", got)
	}
}
