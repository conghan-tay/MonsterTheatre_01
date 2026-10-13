package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// contentTypeSuffix names the sidecar file that holds an object's content type.
const contentTypeSuffix = ".contenttype"

// FS stores objects as files under a root directory.
type FS struct {
	root string
}

// NewFS returns a store rooted at dir, creating it if needed.
func NewFS(dir string) (*FS, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create object store root: %w", err)
	}
	return &FS{root: dir}, nil
}

// Put writes the content type first and the object second, each through a
// temporary file and a rename, so a reader never sees a partial object and an
// object that exists always has its content type.
func (s *FS) Put(_ context.Context, key string, body io.Reader, contentType string) error {
	target, err := s.path(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create object directory: %w", err)
	}
	if err := writeAtomic(target+contentTypeSuffix, strings.NewReader(contentType)); err != nil {
		return err
	}
	return writeAtomic(target, body)
}

func (s *FS) Get(_ context.Context, key string) (*Object, error) {
	target, err := s.path(key)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(target)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("open object: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("stat object: %w", err)
	}
	if info.IsDir() {
		file.Close()
		return nil, ErrNotFound
	}
	contentType, err := os.ReadFile(target + contentTypeSuffix)
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("read object content type: %w", err)
	}
	return &Object{Body: file, Size: info.Size(), ContentType: string(contentType)}, nil
}

// DeletePrefix removes the directory the prefix names, with everything in it.
func (s *FS) DeletePrefix(_ context.Context, prefix string) (int, error) {
	if err := checkPrefix(prefix); err != nil {
		return 0, err
	}
	dir := filepath.Join(s.root, filepath.FromSlash(prefix))
	removed := 0
	err := filepath.WalkDir(dir, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Each object has a second file holding its content type.
		if !entry.IsDir() && !strings.HasSuffix(entry.Name(), contentTypeSuffix) {
			removed++
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("list objects: %w", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		return 0, fmt.Errorf("delete objects: %w", err)
	}
	return removed, nil
}

// path maps a key to a file path, refusing anything that is not a plain
// relative path inside the root.
func (s *FS) path(key string) (string, error) {
	if err := checkKey(key); err != nil {
		return "", err
	}
	// A key ending like this would collide with another object's sidecar.
	if strings.HasSuffix(key, contentTypeSuffix) {
		return "", fmt.Errorf("%w: %q", ErrInvalidKey, key)
	}
	return filepath.Join(s.root, filepath.FromSlash(key)), nil
}

// checkKey refuses a key that is not a plain relative path. Every store
// applies it, so a key accepted locally is accepted on AWS and the reverse.
func checkKey(key string) error {
	if key == "" ||
		strings.HasPrefix(key, "/") ||
		strings.Contains(key, "\\") ||
		path.Clean(key) != key {
		return fmt.Errorf("%w: %q", ErrInvalidKey, key)
	}
	for _, segment := range strings.Split(key, "/") {
		if segment == ".." || segment == "." {
			return fmt.Errorf("%w: %q", ErrInvalidKey, key)
		}
	}
	return nil
}

func writeAtomic(target string, body io.Reader) error {
	tmp, err := os.CreateTemp(filepath.Dir(target), ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary object: %w", err)
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename

	if _, err := io.Copy(tmp, body); err != nil {
		tmp.Close()
		return fmt.Errorf("write object: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close object: %w", err)
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		return fmt.Errorf("publish object: %w", err)
	}
	return nil
}
