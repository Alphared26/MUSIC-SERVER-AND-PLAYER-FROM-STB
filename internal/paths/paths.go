// Package paths resolves indexed track paths back to absolute filesystems.
//
// Indexed paths are "rootname/relative/path.flac" (see package scanner), so a
// lookup is: split the root name off, join with the configured root, then
// verify the result still sits under that root. The final check is what makes
// path traversal in an id or filename impossible.
package paths

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

// ErrOutsideRoot is returned when a relative path escapes its root.
var ErrOutsideRoot = errors.New("path escapes music root")

// Resolver maps indexed paths to absolute paths.
type Resolver struct {
	roots map[string]string // root name -> absolute path
}

// New builds a Resolver from root name -> absolute path.
func New(roots map[string]string) *Resolver {
	cp := make(map[string]string, len(roots))
	for k, v := range roots {
		cp[k] = filepath.Clean(v)
	}
	return &Resolver{roots: cp}
}

// Roots returns the configured roots.
func (r *Resolver) Roots() map[string]string { return r.roots }

// Abs resolves an indexed path such as "library/Artist/01.flac".
func (r *Resolver) Abs(indexed string) (string, error) {
	name, rel, ok := strings.Cut(indexed, "/")
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrOutsideRoot, indexed)
	}
	root, ok := r.roots[name]
	if !ok {
		return "", fmt.Errorf("%w: unknown root %q", ErrOutsideRoot, name)
	}
	// Reject traversal and absolute paths before touching the filesystem.
	cleaned := path.Clean("/" + filepath.ToSlash(rel))[1:]
	if cleaned == "" || cleaned == "." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("%w: %q", ErrOutsideRoot, indexed)
	}
	abs := filepath.Join(root, filepath.FromSlash(cleaned))
	// filepath.Join already cleaned, but double-check against symlink-free logic.
	if abs != root && !strings.HasPrefix(abs, root+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q", ErrOutsideRoot, indexed)
	}
	return abs, nil
}

// RelOf returns the indexed path for an absolute file under one of the roots.
func (r *Resolver) RelOf(abs string) (string, error) {
	abs = filepath.Clean(abs)
	for name, root := range r.roots {
		if abs == root || strings.HasPrefix(abs, root+string(filepath.Separator)) {
			rel, err := filepath.Rel(root, abs)
			if err != nil {
				continue
			}
			return name + "/" + filepath.ToSlash(rel), nil
		}
	}
	return "", fmt.Errorf("no root contains %q", abs)
}

// WritableRoot is the single root that accepts uploads and deletes.
type WritableRoot struct {
	Name string
	Path string
}

// SanitizeJoin joins user-supplied relative segments onto the writable root,
// rejecting anything that escapes it. Used by upload and delete endpoints.
func SanitizeJoin(root string, segments ...string) (string, error) {
	cleaned := make([]string, 0, len(segments))
	for _, s := range segments {
		s = filepath.ToSlash(strings.TrimSpace(s))
		if s == "" {
			continue
		}
		cleaned = append(cleaned, path.Clean("/"+s)[1:])
	}
	abs := filepath.Join(append([]string{filepath.Clean(root)}, cleaned...)...)
	if abs != filepath.Clean(root) && !strings.HasPrefix(abs, filepath.Clean(root)+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %v", ErrOutsideRoot, segments)
	}
	return abs, nil
}
