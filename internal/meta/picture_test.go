package meta

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// fakePNG is a stand-in cover image: Parse never decodes the bytes, it only
// moves them from the PICTURE block into Info.Picture.
var fakePNG = []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0xFF}

// vorbisComment builds a minimal VORBIS_COMMENT body: vendor length, vendor
// string, comment count, then one length-prefixed TITLE=... comment. The
// vendor length is NOT the count — reading it as one hides every real tag.
func vorbisComment(title string) []byte {
	var b bytes.Buffer
	vendor := []byte("reference libFLAC")
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(vendor)))
	b.Write(vendor)
	_ = binary.Write(&b, binary.LittleEndian, uint32(1))
	cm := []byte("TITLE=" + title)
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(cm)))
	b.Write(cm)
	return b.Bytes()
}

// pictureBlock builds a PICTURE block body. All lengths are big-endian, the
// opposite of the VORBIS_COMMENT layout above.
func pictureBlock(picType uint32, mime string, w, h uint32, data []byte, declaredLen uint32) []byte {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.BigEndian, picType)
	// The spec length-prefixes both strings: a u32 byte count, then the bytes.
	_ = binary.Write(&b, binary.BigEndian, uint32(len(mime)))
	b.WriteString(mime)
	desc := []byte("front")
	_ = binary.Write(&b, binary.BigEndian, uint32(len(desc)))
	b.Write(desc)
	for _, v := range []uint32{w, h, 24, 0} {
		_ = binary.Write(&b, binary.BigEndian, v)
	}
	_ = binary.Write(&b, binary.BigEndian, declaredLen)
	b.Write(data)
	return b.Bytes()
}

// metaBlock wraps a body in the 4-byte FLAC block header and marks it last.
func metaBlock(typ byte, body []byte) []byte {
	h := []byte{typ, 0, byte(len(body) >> 8), byte(len(body))}
	return append(h, body...)
}

// flacFile assembles a minimal FLAC: magic, STREAMINFO, then the given blocks.
func flacFile(blocks ...[]byte) []byte {
	var b bytes.Buffer
	b.WriteString("fLaC")
	si := make([]byte, 34)
	binary.BigEndian.PutUint32(si[10:14], 44100<<12) // 44.1 kHz, 2ch, 16-bit
	b.Write(si)
	b.Write(metaBlock(0, nil)) // STREAMINFO, not last
	for i, blk := range blocks {
		typ := blk[0] & 0x7F
		last := byte(0)
		if i == len(blocks)-1 {
			last = 0x80
		}
		body := blk[4:]
		h := []byte{last | typ, byte(len(body) >> 16), byte(len(body) >> 8), byte(len(body))}
		b.Write(h)
		b.Write(body)
	}
	return b.Bytes()
}

func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestParsePicture checks the full path: a PICTURE block in a real file comes
// back with its bytes, MIME type and dimensions intact, and alongside the
// tags from the VORBIS_COMMENT in the same file.
func TestParsePicture(t *testing.T) {
	p := writeTemp(t, "cover.flac", flacFile(
		metaBlock(4, vorbisComment("Ashes")),
		metaBlock(6, pictureBlock(3, "image/png", 1400, 1400, fakePNG, uint32(len(fakePNG)))),
	))
	info, err := Parse(p)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if info.Title == nil || *info.Title != "Ashes" {
		t.Errorf("Title = %v, want Ashes", info.Title)
	}
	if !bytes.Equal(info.Picture, fakePNG) {
		t.Errorf("Picture = %x, want %x", info.Picture, fakePNG)
	}
	if info.PictureMIME != "image/png" {
		t.Errorf("PictureMIME = %q, want image/png", info.PictureMIME)
	}
	if info.PictureW != 1400 || info.PictureH != 1400 {
		t.Errorf("dims = %dx%d, want 1400x1400", info.PictureW, info.PictureH)
	}
}

// TestParsePictureTruncated covers a block that declares more data than it
// holds. A broken picture must not fail the file: the tags still parse.
func TestParsePictureTruncated(t *testing.T) {
	blk := pictureBlock(3, "image/png", 1400, 1400, fakePNG, uint32(len(fakePNG))+9999)
	p := writeTemp(t, "short.flac", flacFile(
		metaBlock(4, vorbisComment("Ashes")),
		metaBlock(6, blk),
	))
	info, err := Parse(p)
	if err != nil {
		t.Fatalf("Parse on truncated picture: %v", err)
	}
	if info.Title == nil || *info.Title != "Ashes" {
		t.Errorf("Title = %v, want Ashes", info.Title)
	}
	if len(info.Picture) != 0 {
		t.Errorf("Picture = %x, want empty", info.Picture)
	}
}

// TestParsePicturePrefersFrontCover checks the type preference: a media image
// is kept when it is all we have, but a front cover later in the file wins.
func TestParsePicturePrefersFrontCover(t *testing.T) {
	p := writeTemp(t, "two.flac", flacFile(
		metaBlock(6, pictureBlock(4, "image/jpeg", 20, 20, []byte("jpg"), 3)),
		metaBlock(6, pictureBlock(3, "image/png", 10, 10, fakePNG, uint32(len(fakePNG)))),
	))
	info, err := Parse(p)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if info.PictureMIME != "image/png" {
		t.Errorf("PictureMIME = %q, want image/png (front cover wins)", info.PictureMIME)
	}
}

// TestParsePictureNonCoverAlone checks the fallback: with no front cover in
// the file, a media image is still usable art.
func TestParsePictureNonCoverAlone(t *testing.T) {
	p := writeTemp(t, "media.flac", flacFile(
		metaBlock(6, pictureBlock(4, "image/jpeg", 20, 20, []byte("jpg"), 3)),
	))
	info, err := Parse(p)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if string(info.Picture) != "jpg" {
		t.Errorf("Picture = %q, want jpg", info.Picture)
	}
}
