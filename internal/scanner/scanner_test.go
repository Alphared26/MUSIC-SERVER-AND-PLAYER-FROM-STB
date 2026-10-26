package scanner

import (
	"testing"

	"flac-server/internal/db"
)

func str(s string) *string { return &s }

// An untagged file must still produce an album, otherwise cover art can never
// be discovered: the album table is only populated from album tags.
func TestFillFromDirInfersAlbum(t *testing.T) {
	tr := &db.Track{Path: "upload/TestArt/CoverTest/01 Track.wav"}
	fillFromDir(tr)
	if tr.Album == nil || *tr.Album != "CoverTest" {
		t.Fatalf("album = %v, want CoverTest", tr.Album)
	}
	if tr.Artist == nil || *tr.Artist != "TestArt" {
		t.Fatalf("artist = %v, want TestArt", tr.Artist)
	}
}

// A real tag must never be overwritten by the folder name.
func TestFillFromDirKeepsTags(t *testing.T) {
	tr := &db.Track{
		Path:   "library/Dabin/Remixes/01.wav",
		Album:  str("Shooting Star"),
		Artist: str("XG"),
	}
	fillFromDir(tr)
	if *tr.Album != "Shooting Star" || *tr.Artist != "XG" {
		t.Fatalf("tags clobbered: album=%q artist=%q", *tr.Album, *tr.Artist)
	}
}

// A loose file directly in a root has no folder to name an album after.
func TestFillFromDirSkipsShallowPaths(t *testing.T) {
	for _, p := range []string{"library/loose.flac", "upload/single/one.flac"} {
		tr := &db.Track{Path: p}
		fillFromDir(tr)
		if tr.Album != nil {
			t.Errorf("%s: album = %q, want nil", p, *tr.Album)
		}
	}
}
