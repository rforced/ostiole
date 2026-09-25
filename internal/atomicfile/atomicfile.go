// Package atomicfile replaces files so that whoever reads one, including a
// router coming back from a crash, finds the old content or the new and
// never half of either.
package atomicfile

import (
	"os"
	"path/filepath"
)

// File is a temporary file beside the one it replaces. Write to it, then
// Commit to put it in place. Close before Commit throws it away, so a
// deferred Close cleans up after an error.
type File struct {
	*os.File
	path string
	perm os.FileMode
	done bool
}

// Create opens a temporary file in path's directory, where a rename over
// path is atomic.
func Create(path string, perm os.FileMode) (*File, error) {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return nil, err
	}
	return &File{File: f, path: path, perm: perm}, nil
}

// Commit gives the file its mode, syncs it, renames it over path and syncs
// the directory, so the new content is what a crash leaves behind. The
// temporary file is gone either way.
func (f *File) Commit() error {
	if f.done {
		return os.ErrClosed
	}
	f.done = true
	err := f.Chmod(f.perm)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.File.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), f.path)
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return err
	}
	SyncDir(filepath.Dir(f.path))
	return nil
}

// Close throws the file away and leaves path as it was. After Commit it
// does nothing.
func (f *File) Close() error {
	if f.done {
		return nil
	}
	f.done = true
	_ = f.File.Close()
	return os.Remove(f.Name())
}

// Write replaces path with data.
func Write(path string, data []byte, perm os.FileMode) error {
	f, err := Create(path, perm)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return err
	}
	return f.Commit()
}

// SyncDir makes the renames in dir durable. It is best effort: the file is
// in place either way, and not every filesystem can sync a directory.
func SyncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}
