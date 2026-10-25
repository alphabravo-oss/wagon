// Package safepath confines caller-supplied paths to a folder before they are
// handed to hauler, which runs as root. Error messages never name the folder,
// so they are safe to return to the caller.
package safepath

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Confine resolves p (relative to root when not absolute) with symlinks
// evaluated and returns it only when it lies strictly below root. Parts of p
// that do not exist yet are allowed, since hauler creates them, but a dangling
// symlink is not: hauler would follow it.
func Confine(root, p string) (string, error) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", errors.New("the allowed folder cannot be resolved")
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(realRoot, p)
	}
	resolved, err := resolveExisting(filepath.Clean(p))
	if err != nil {
		return "", err
	}
	if resolved == realRoot || !Within(realRoot, resolved) {
		return "", errors.New("outside the allowed folder")
	}
	return resolved, nil
}

// ConfineOutside is Confine that also refuses p when it resolves to excluded
// or anything below it.
func ConfineOutside(root, excluded, p string) (string, error) {
	resolved, err := Confine(root, p)
	if err != nil {
		return "", err
	}
	if ex, err := filepath.EvalSymlinks(excluded); err != nil || Within(ex, resolved) {
		return "", errors.New("inside an excluded folder")
	}
	return resolved, nil
}

// ConfineFile is Confine for an existing regular file; "" stays "".
func ConfineFile(root, p string) (string, error) {
	if p == "" {
		return "", nil
	}
	resolved, err := Confine(root, p)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", errors.New("not found")
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("not a regular file")
	}
	return resolved, nil
}

// resolveExisting evaluates symlinks in the longest existing prefix of an
// absolute, clean path and appends the missing rest unchanged.
func resolveExisting(p string) (string, error) {
	rest := ""
	for {
		real, err := filepath.EvalSymlinks(p)
		if err == nil {
			return filepath.Join(real, rest), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", errors.New("cannot be resolved")
		}
		if _, lerr := os.Lstat(p); lerr == nil {
			return "", errors.New("contains a dangling symlink")
		}
		parent := filepath.Dir(p)
		if parent == p {
			return "", errors.New("cannot be resolved")
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
}

// Within reports whether p is root or below it. Both must be clean and absolute.
func Within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
