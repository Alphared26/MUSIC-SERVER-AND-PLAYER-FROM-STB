package meta

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf16"
)

// Info is the normalized metadata for one audio file.
type Info struct {
	Format        string // "flac", "mp3", or "wav"
	Title         *string
	Artist        *string
	Album         *string
	AlbumArtist   *string
	Composer      *string
	Genre         *string
	Year          *int
	TrackNum      *int
	DiscNum       *int
	DurationMs    int64
	SampleRate    int
	BitsPerSample int
	Channels      int
	Bitrate       int // kbps

	// Embedded cover art from a FLAC PICTURE block. Empty when the file has
	// no picture, or the block was malformed: art is optional, so callers
	// must treat these as best-effort and never fail a scan over them.
	Picture     []byte
	PictureMIME string
	PictureW    int
	PictureH    int

	// Lyrics from an embedded tag (Vorbis LYRICS/UNSYNCEDLYRICS, or ID3
	// USLT/ULT). Empty when absent. Read on demand by the lyrics endpoint
	// rather than stored in the DB, so re-tagging a file takes effect without
	// a rescan.
	Lyrics string
}

// ErrUnsupported is returned for non-FLAC/MP3 input.
var ErrUnsupported = errors.New("unsupported format")

// readHead returns up to n leading bytes of the file.
func readHead(path string, n int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, n)
	read, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, err
	}
	return buf[:read], nil
}

// fileSize returns the file size in bytes.
func fileSize(path string) (int64, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

// Parse dispatches on file content, not extension.
func Parse(path string) (*Info, error) {
	head, err := readHead(path, 1<<20) // 1 MB covers FLAC blocks + ID3v2 header
	if err != nil {
		return nil, err
	}
	if len(head) < 16 {
		return nil, ErrUnsupported
	}
	if bytes.HasPrefix(head, []byte("fLaC")) {
		return parseFLAC(path, head)
	}
	if bytes.HasPrefix(head, []byte("ID3")) {
		return parseMP3(path, head)
	}
	// Frame-sync fallback for MP3 with no ID3 tag.
	if len(head) > 1 && head[0] == 0xFF && head[1]&0xE0 == 0xE0 {
		return parseMP3(path, head)
	}
	// WAV/RIFF detection
	if len(head) >= 12 && string(head[:4]) == "RIFF" && string(head[8:12]) == "WAVE" {
		return parseWAV(path, head)
	}
	return nil, ErrUnsupported
}

// ---------------------------------------------------------------- FLAC ----

func parseFLAC(path string, head []byte) (*Info, error) {
	info := &Info{Format: "flac", SampleRate: 44100, BitsPerSample: 16, Channels: 2}

	// STREAMINFO is mandatory and first. It starts at offset 8, not 4: the
	// "fLaC" magic is 4 bytes and each metadata block is preceded by a 4-byte
	// header (1 flag byte + 3 length bytes), so the 34 STREAMINFO bytes are
	// head[8:42]. Reading at 4 lands on the block header instead, which yields
	// garbage for every field.
	if len(head) < 8+34 {
		return info, nil
	}
	si := head[8 : 8+34]
	minBlock := uint16(si[0])<<8 | uint16(si[1])
	maxBlock := uint16(si[2])<<8 | uint16(si[3])
	if maxBlock == 0 {
		maxBlock = 4096
	}
	// 20 bits sample rate, 3 bits (channels-1), 5 bits (bps-1), 36 bits samples.
	sr := uint32(si[10])<<12 | uint32(si[11])<<4 | uint32(si[12])>>4
	ch := int((si[12]>>1)&0x7) + 1
	// The stored width is bps-1, so add one back: a 16-bit file is stored as 15.
	bps := (int(si[12]&0x1)<<4 | int(si[13]>>4)) + 1
	_ = minBlock
	if sr > 0 {
		info.SampleRate = int(sr)
	}
	info.Channels = ch
	if bps > 1 {
		info.BitsPerSample = bps
	}
	totalSamples := uint64(si[13]&0x0F)<<32 | uint64(si[14])<<24 |
		uint64(si[15])<<16 | uint64(si[16])<<8 | uint64(si[17])
	if info.SampleRate > 0 {
		info.DurationMs = int64(totalSamples) * 1000 / int64(info.SampleRate)
	}
	// Average bitrate in kbps. durationMs is milliseconds, so the division
	// needs a second /1000 to turn bits-per-millisecond into kilobits-per-second.
	if size, err := fileSize(path); err == nil && info.DurationMs > 0 {
		info.Bitrate = int(int64(size) * 8 / 1000 / (info.DurationMs / 1000))
	}

	// Walk metadata blocks for VORBIS_COMMENT, starting past STREAMINFO and
	// its own 4-byte block header.
	off := 8 + 34
	for off+4 <= len(head) {
		last := head[off]&0x80 != 0
		typ := head[off] & 0x7F
		length := int(head[off+1])<<16 | int(head[off+2])<<8 | int(head[off+3])
		body := off + 4
		if body+length > len(head) || length < 0 {
			break
		}
		switch typ {
		case 4: // VORBIS_COMMENT
			applyVorbisComment(info, head[body:body+length])
		case 6: // PICTURE
			applyPicture(info, head[body:body+length])
		}
		if last {
			break
		}
		off = body + length
	}
	applyFilenameFallbacks(info, path)
	return info, nil
}

// applyPicture decodes a FLAC PICTURE block body into Info's picture fields.
//
// Every field is big-endian, the opposite of VORBIS_COMMENT: picture type
// (u32), the MIME type as a length-prefixed string, the description likewise,
// then width, height, color depth and colors-used (u32 each), the data length
// (u32) and the data itself.
//
// The two strings are LENGTH-prefixed, not NUL-terminated. Reading a NUL here
// yields an empty MIME and shifts every following field, which is how this
// parser first failed: on a real file the byte after the type is 00 00 00 0a,
// the length of "image/jpeg". Some encoders do write a trailing NUL instead,
// so stringField accepts both and only the spec form takes the fast path.
//
// Type 3 is the front cover and always wins; otherwise the first usable
// picture is kept so artist or media art still renders something. A truncated
// or malformed block leaves the fields empty: a broken picture must never
// cost us the tags of an otherwise good file.
func applyPicture(info *Info, b []byte) {
	if len(b) < 32 {
		return
	}
	picType := int(binary.BigEndian.Uint32(b[0:4]))
	mime, pos, ok := stringField(b, 4)
	if !ok {
		return
	}
	// The description is skipped, not kept: it is a caption, not data.
	_, pos, ok = stringField(b, pos)
	if !ok || pos+20 > len(b) {
		return
	}
	width := int(binary.BigEndian.Uint32(b[pos : pos+4]))
	height := int(binary.BigEndian.Uint32(b[pos+4 : pos+8]))
	// pos+8 and pos+12 are color depth and colors used: not needed here.
	dataLen := int(binary.BigEndian.Uint32(b[pos+16 : pos+20]))
	pos += 20
	if dataLen <= 0 || pos+dataLen > len(b) {
		return
	}
	if picType != 3 && len(info.Picture) > 0 {
		return // a front cover is already stored
	}
	info.Picture = b[pos : pos+dataLen]
	info.PictureMIME = mime
	info.PictureW, info.PictureH = width, height
}

// stringField reads one picture-block string at b[pos] and returns the value
// with the offset just past it. It first tries the spec's u32 length prefix
// and falls back to a NUL terminator, which some taggers emit instead.
func stringField(b []byte, pos int) (string, int, bool) {
	if pos+4 <= len(b) {
		n := int(binary.BigEndian.Uint32(b[pos : pos+4]))
		// A length of 0 is a legal empty string, but only accept a length
		// that both fits in the block and leaves room for the fixed fields
		// and data length that follow it.
		if n >= 0 && pos+4+n <= len(b) {
			return string(b[pos+4 : pos+4+n]), pos + 4 + n, true
		}
	}
	for i := pos; i < len(b); i++ {
		if b[i] == 0 {
			return string(b[pos:i]), i + 1, true
		}
	}
	return "", 0, false // unterminated: the block is truncated
}

func applyVorbisComment(info *Info, b []byte) {
	if len(b) < 8 {
		return
	}
	// A VORBIS_COMMENT block is: vendor_length (u32 LE), the vendor string,
	// then comment_count (u32 LE) followed by that many length-prefixed
	// comments. The vendor length is NOT the comment count — treating it as
	// one walks off into the vendor string and the real tags are never parsed.
	vendorLen := int(binary.LittleEndian.Uint32(b[:4]))
	pos := 4 + vendorLen
	if vendorLen < 0 || pos+4 > len(b) {
		return
	}
	n := int(binary.LittleEndian.Uint32(b[pos : pos+4]))
	pos += 4
	fields := make([][2]string, 0, 24)
	for i := 0; i < n; i++ {
		if pos+4 > len(b) {
			break
		}
		l := int(binary.LittleEndian.Uint32(b[pos : pos+4]))
		pos += 4
		if l < 0 || pos+l > len(b) {
			break
		}
		s := string(b[pos : pos+l])
		pos += l
		if k, v, ok := strings.Cut(s, "="); ok {
			fields = append(fields, [2]string{strings.ToUpper(k), v})
		}
	}
	for _, f := range fields {
		v := strings.TrimSpace(f[1])
		if v == "" {
			continue
		}
		p := &v
		switch f[0] {
		case "TITLE":
			info.Title = p
		case "ARTIST":
			if info.Artist == nil {
				info.Artist = p
			} else if !strings.Contains(*info.Artist, v) {
				combined := *info.Artist + ", " + v
				info.Artist = &combined
			}
		case "ALBUM":
			info.Album = p
		case "ALBUMARTIST", "ALBUM ARTIST":
			if info.AlbumArtist == nil {
				info.AlbumArtist = p
			} else if !strings.Contains(*info.AlbumArtist, v) {
				combined := *info.AlbumArtist + ", " + v
				info.AlbumArtist = &combined
			}
		case "COMPOSER":
			info.Composer = p
		case "GENRE":
			info.Genre = p
		case "DATE", "YEAR", "ORIGINALDATE":
			if y := parseYear(v); y > 0 {
				info.Year = &y
			}
		case "TRACKNUMBER", "TRACK":
			if n := parseIntPrefix(v); n > 0 {
				info.TrackNum = &n
			}
		case "DISCNUMBER", "DISC":
			if n := parseIntPrefix(v); n > 0 {
				info.DiscNum = &n
			}
		case "LYRICS", "UNSYNCEDLYRICS", "UNSYNCED LYRICS":
			if info.Lyrics == "" {
				info.Lyrics = v
			}
		}
	}
}

// ----------------------------------------------------------------- MP3 ----

func parseMP3(path string, head []byte) (*Info, error) {
	info := &Info{Format: "mp3", SampleRate: 44100, BitsPerSample: 16, Channels: 2}

	off := 0
	if bytes.HasPrefix(head, []byte("ID3")) {
		size := synchsafe(head[6:10])
		off = 10 + int(size)
		applyID3(info, head[10:10+int(size)])
	}
	// Find the first MPEG audio frame header at/after off.
	idx := findFrame(head, off)
	if idx < 0 {
		applyFilenameFallbacks(info, path)
		return info, nil
	}
	if sr, ch, br, samples := parseFrameHeader(head[idx:]); samples > 0 {
		info.SampleRate, info.Channels, info.Bitrate = sr, ch, br
		if size, err := fileSize(path); err == nil {
			info.DurationMs = size * 8 * 1000 / (int64(br) * 1000)
		}
		_ = samples
	}
	// Fallback duration from Xing/Info header.
	if off2 := findXing(head, off); off2 >= 0 {
		if frames, bytes_ := readXing(head[off2:]); frames > 0 {
			if info.SampleRate > 0 {
				info.DurationMs = int64(frames) * 1152 * 1000 / int64(info.SampleRate)
			}
			if info.DurationMs > 0 && bytes_ > 0 {
				info.Bitrate = int(int64(bytes_) * 8 / 1000 / info.DurationMs)
			}
		}
	}
	applyFilenameFallbacks(info, path)
	return info, nil
}

// id3LyricsText extracts the lyric text from an ID3v2 USLT/ULT frame body.
//
// Layout: text-encoding byte, 3-byte ISO language, a NUL-terminated content
// descriptor, then the lyric text. The descriptor terminator is one NUL for
// single-byte encodings and a 0x0000 unit for UTF-16/UTF-16BE, so it is found
// on the unit boundary rather than with a plain byte scan.
func id3LyricsText(frame string) string {
	b := []byte(frame)
	if len(b) < 4 { // encoding byte + 3-byte language, nothing to extract
		return ""
	}
	enc := b[0]
	body := b[4:] // skip encoding byte + 3-byte language

	if enc == 1 || enc == 2 {
		// Find the descriptor terminator, scanning whole UTF-16 code units.
		i := 0
		for ; i+1 < len(body); i += 2 {
			if body[i] == 0 && body[i+1] == 0 {
				break
			}
		}
		if i+1 >= len(body) {
			return "" // no terminator: malformed, treat as no lyrics
		}
		return strings.TrimSpace(decodeUTF16(body[i+2:], enc == 2))
	}

	// Single-byte encodings: the descriptor must be NUL-terminated. Without
	// that terminator the frame is malformed, and returning the remainder
	// would surface language/descriptor bytes as lyric text.
	i := bytes.IndexByte(body, 0)
	if i < 0 {
		return ""
	}
	return strings.TrimSpace(string(body[i+1:]))
}

func decodeUTF16(b []byte, bigEndian bool) string {
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		if bigEndian {
			u = append(u, uint16(b[i])<<8|uint16(b[i+1]))
		} else {
			u = append(u, uint16(b[i])|uint16(b[i+1])<<8)
		}
	}
	return string(utf16.Decode(u))
}

func applyID3(info *Info, b []byte) {
	if len(b) < 10 {
		return
	}
	version := b[3]
	pos := 0
	unsync := version >= 2 && version < 3 && b[5]&0x80 != 0
	if unsync {
		b = bytes.ReplaceAll(b, []byte{0xFF, 0x00}, []byte{0xFF})
	}
	size := func(h []byte) int {
		if version >= 4 {
			return synchsafe(h)
		}
		return int(h[0])<<21 | int(h[1])<<14 | int(h[2])<<7 | int(h[3])
	}
	readFrame := func() (id, val string, ok bool) {
		if pos >= len(b) {
			return "", "", false
		}
		if version == 2 {
			if pos+3 > len(b) {
				return "", "", false
			}
			id = strings.ToUpper(string(b[pos : pos+3]))
			pos += 3
		} else {
			if pos+4 > len(b) {
				return "", "", false
			}
			id = strings.ToUpper(string(b[pos : pos+4]))
			pos += 4
		}
		if pos+4 > len(b) {
			return "", "", false
		}
		n := size(b[pos : pos+4])
		pos += 4
		if n <= 0 || pos+n > len(b) {
			return "", "", false
		}
		val = strings.TrimRight(string(b[pos:pos+n]), "\x00")
		pos += n
		return id, val, true
	}
	for {
		id, val, ok := readFrame()
		if !ok || id == "" {
			return
		}
		val = strings.TrimSpace(strings.Trim(val, "\x00"))
		if val == "" {
			continue
		}
		p := &val
		switch id {
		case "USLT", "ULT", "SYLT":
			// Unsynchronised lyrics frame: 1 byte text encoding, 3 bytes
			// ISO language, a NUL-terminated content descriptor, then the
			// lyric text. Only the text part is wanted.
			if info.Lyrics == "" {
				if s := id3LyricsText(val); s != "" {
					info.Lyrics = s
				}
			}
		case "TIT2", "TT2":
			info.Title = p
		case "TPE1", "TP1":
			info.Artist = p
		case "TALB", "TAL":
			info.Album = p
		case "TPE2", "TP2":
			info.AlbumArtist = p
		case "TCOM", "TCM":
			info.Composer = p
		case "TCON", "TCO":
			if i := strings.IndexByte(val, ')'); i > 0 && strings.HasPrefix(val, "(") {
				val = val[i+1:] // strip (17) style genre refs
			}
			info.Genre = p
		case "TDRC", "TYER", "TYE", "TDRL":
			if y := parseYear(val); y > 0 {
				info.Year = &y
			}
		case "TRCK", "TRK":
			if n := parseIntPrefix(val); n > 0 {
				info.TrackNum = &n
			}
		case "TPOS", "TPA":
			if n := parseIntPrefix(val); n > 0 {
				info.DiscNum = &n
			}
		}
	}
}

func synchsafe(b []byte) int {
	return int(b[0]&0x7F)<<21 | int(b[1]&0x7F)<<14 | int(b[2]&0x7F)<<7 | int(b[3]&0x7F)
}

func findFrame(b []byte, from int) int {
	for i := from; i+4 <= len(b); i++ {
		if b[i] == 0xFF && b[i+1]&0xE0 == 0xE0 {
			version := (b[i+1] >> 3) & 0x3
			layer := (b[i+1] >> 1) & 0x3
			if version != 1 && layer != 0 {
				return i
			}
		}
	}
	return -1
}

// bitrate table in kbps, indexed [versionIDX][layer][bitrateIDX]; 0 = invalid.
// versionID: 0 = MPEG2.5, 1 = reserved, 2 = MPEG2, 3 = MPEG1.
// layer: 0 = reserved, 1 = Layer III, 2 = Layer II, 3 = Layer I.
var bitrateTable = [4][4][16]int{
	0: {0: {0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
		1: {0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0},
		2: {0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0},
		3: {0, 32, 48, 56, 64, 80, 96, 112, 128, 144, 160, 176, 192, 224, 256, 0}},
	1: {0: {0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
		1: {0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0},
		2: {0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0},
		3: {0, 32, 48, 56, 64, 80, 96, 112, 128, 144, 160, 176, 192, 224, 256, 0}},
	2: {0: {0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
		1: {0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0},
		2: {0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0},
		3: {0, 32, 48, 56, 64, 80, 96, 112, 128, 144, 160, 176, 192, 224, 256, 0}},
	3: {0: {0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
		1: {0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0},
		2: {0, 32, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 384, 0},
		3: {0, 32, 64, 96, 128, 160, 192, 224, 256, 288, 320, 352, 384, 416, 448, 0}},
}

var sampleRates = [4][3]int{
	{11025, 12000, 8000},  // MPEG2.5
	{0, 0, 0},             // reserved
	{22050, 24000, 16000}, // MPEG2
	{44100, 48000, 32000}, // MPEG1
}

func parseFrameHeader(b []byte) (sampleRate, channels, bitrate, samples int) {
	if len(b) < 4 {
		return 0, 0, 0, 0
	}
	versionID := int((b[1] >> 3) & 0x3)
	layerBits := int((b[1] >> 1) & 0x3)
	layer := 3 - layerBits // 0=III, 1=II, 2=I
	brIdx := int((b[2] >> 4) & 0xF)
	srIdx := int((b[2] >> 2) & 0x3)
	pad := int((b[2] >> 1) & 0x1)
	mode := int((b[3] >> 6) & 0x3)
	if versionID == 1 || layerBits == 0 || srIdx == 3 {
		return 0, 0, 0, 0
	}
	br := bitrateTable[versionID][layer][brIdx]
	sr := sampleRates[versionID][srIdx]
	if br == 0 || sr == 0 {
		return sr, 0, 0, 0
	}
	channels = 2
	if mode == 3 {
		channels = 1
	}
	samples = 1152
	if layer == 2 { // Layer II
		samples = 1152
	} else if layer != 3 { // Layer I
		samples = 384
	}
	_ = pad
	return sr, channels, br, samples
}

func findXing(b []byte, from int) int {
	for i := from; i+4 <= len(b); i++ {
		if bytes.HasPrefix(b[i:], []byte("Xing")) {
			return i + 4
		}
		if bytes.HasPrefix(b[i:], []byte("Info")) {
			return i + 4
		}
	}
	return -1
}

func readXing(b []byte) (frames, totalBytes uint32) {
	if len(b) < 8 {
		return 0, 0
	}
	flags := binary.BigEndian.Uint32(b[:4])
	pos := 4
	if flags&0x1 != 0 && pos+4 <= len(b) {
		frames = binary.BigEndian.Uint32(b[pos : pos+4])
		pos += 4
	}
	if flags&0x2 != 0 && pos+4 <= len(b) {
		totalBytes = binary.BigEndian.Uint32(b[pos : pos+4])
	}
	return
}

// -------------------------------------------------------------- helpers ----

func applyFilenameFallbacks(info *Info, path string) {
	base := path
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	base = strings.TrimSuffix(base, ".flac")
	base = strings.TrimSuffix(base, ".FLAC")
	base = strings.TrimSuffix(base, ".mp3")
	base = strings.TrimSuffix(base, ".MP3")
	if info.Title == nil {
		t := strings.TrimSpace(base)
		if t != "" {
			info.Title = &t
		}
	}
	if info.Artist == nil {
		// "01 - Title.flac" or "Artist - Title.flac" style fallbacks.
		s := strings.TrimLeft(base, "0123456789. -_")
		if s != "" && s != base {
			if info.Title != nil {
				*info.Title = strings.TrimSpace(s)
			}
		}
	}
}

func parseYear(s string) int {
	if len(s) >= 4 {
		if y, err := strconv.Atoi(s[:4]); err == nil && y > 1000 && y < 3000 {
			return y
		}
	}
	return 0
}

func parseIntPrefix(s string) int {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "/-"); i > 0 {
		s = s[:i]
	}
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

// ------------------------------------------------------------------ WAV ----

// parseWAV extracts PCM info from a WAV/RIFF file by walking the chunk list
// rather than assuming 'fmt ' sits at offset 12 (many muxers insert LIST/JUNK
// chunks first). Duration comes from the data chunk size.
func parseWAV(path string, head []byte) (*Info, error) {
	if len(head) < 12 {
		return nil, ErrUnsupported
	}
	info := &Info{
		Format: "wav", SampleRate: 44100, BitsPerSample: 16, Channels: 2,
	}
	if s := stem(path); s != "" {
		title := s
		info.Title = &title
	}
	var bytesPerSample int
	// A RIFF size field of 0xFFFFFFFF means "unknown, stream until EOF".
	for off := 12; off+8 <= len(head); {
		id := string(head[off : off+4])
		size := int(binary.LittleEndian.Uint32(head[off+4 : off+8]))
		body := off + 8
		switch id {
		case "fmt ":
			if body+16 > len(head) {
				return info, nil
			}
			formatTag := binary.LittleEndian.Uint16(head[body : body+2])
			info.Channels = int(binary.LittleEndian.Uint16(head[body+2 : body+4]))
			info.SampleRate = int(binary.LittleEndian.Uint32(head[body+4 : body+8]))
			info.BitsPerSample = int(binary.LittleEndian.Uint16(head[body+14 : body+16]))
			// formatTag 3 (IEEE float) and 0xFFFE (WAVE_FORMAT_EXTENSIBLE)
			// both carry a normal sample width, so nothing extra to do.
			_ = formatTag
			bytesPerSample = info.Channels * info.BitsPerSample / 8
		case "data":
			if bytesPerSample > 0 && info.SampleRate > 0 {
				if size == 0 || size > len(head)-body {
					// head is capped at 1 MB, so for anything longer the only
					// reliable size is the rest of the file on disk.
					if fs, err := fileSize(path); err == nil {
						if rem := fs - int64(body); rem > 0 {
							size = int(rem)
						} else {
							size = len(head) - body
						}
					} else {
						size = len(head) - body
					}
				}
				frames := size / bytesPerSample
				info.DurationMs = int64(frames) * 1000 / int64(info.SampleRate)
			}
			info.Bitrate = info.SampleRate * info.Channels * info.BitsPerSample / 1000
			return info, nil
		}
		if size <= 0 {
			break
		}
		off = body + size
		if size%2 == 1 {
			off++ // chunks are word-aligned
		}
	}
	if info.Channels == 0 {
		info.Channels = 2
	}
	if info.SampleRate == 0 {
		info.SampleRate = 44100
	}
	if info.BitsPerSample == 0 {
		info.BitsPerSample = 16
	}
	info.Bitrate = info.SampleRate * info.Channels * info.BitsPerSample / 1000
	return info, nil
}

// stem is the filename without directories or extension.
func stem(path string) string {
	b := filepath.Base(path)
	return strings.TrimSuffix(b, filepath.Ext(b))
}