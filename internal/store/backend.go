package store

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// backend holds the files of each gist: meta.json and r<N>.json.
type backend interface {
	Read(id, name string) ([]byte, error) // fs.ErrNotExist when missing
	Write(id, name string, data []byte) error
	Remove(id string) error
	IDs() ([]string, error)
}

// fileBackend keeps gists in <dir>/gists/<id[0:2]>/<id>/.
type fileBackend struct{ root string }

func newFileBackend(dir string) (*fileBackend, error) {
	root := filepath.Join(dir, "gists")
	if err := os.MkdirAll(root, 0o750); err != nil {
		return nil, fmt.Errorf("data directory: %w", err)
	}
	// Fail now, not on the first gist, when the volume is read-only.
	probe, err := os.CreateTemp(root, ".probe-*")
	if err != nil {
		return nil, fmt.Errorf("data directory is not writable: %w", err)
	}
	probe.Close()
	if err := os.Remove(probe.Name()); err != nil {
		return nil, err
	}
	return &fileBackend{root: root}, nil
}

func (b *fileBackend) dir(id string) string { return filepath.Join(b.root, id[:2], id) }

func (b *fileBackend) Read(id, name string) ([]byte, error) {
	return os.ReadFile(filepath.Join(b.dir(id), name))
}

// Write replaces a file atomically: write a temp file, sync it, rename it.
func (b *fileBackend) Write(id, name string, data []byte) error {
	dir := b.dir(id)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, name))
}

func (b *fileBackend) Remove(id string) error { return os.RemoveAll(b.dir(id)) }

func (b *fileBackend) IDs() ([]string, error) {
	shards, err := os.ReadDir(b.root)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, shard := range shards {
		if !shard.IsDir() {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(b.root, shard.Name()))
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() && ValidID(e.Name()) {
				ids = append(ids, e.Name())
			}
		}
	}
	return ids, nil
}

// memBackend keeps gists in memory.
type memBackend struct {
	mu    sync.Mutex
	gists map[string]map[string][]byte
}

func newMemBackend() *memBackend { return &memBackend{gists: map[string]map[string][]byte{}} }

func (b *memBackend) Read(id, name string) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	data, ok := b.gists[id][name]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return append([]byte(nil), data...), nil
}

func (b *memBackend) Write(id, name string, data []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.gists[id] == nil {
		b.gists[id] = map[string][]byte{}
	}
	b.gists[id][name] = append([]byte(nil), data...)
	return nil
}

func (b *memBackend) Remove(id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.gists, id)
	return nil
}

func (b *memBackend) IDs() ([]string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ids := make([]string, 0, len(b.gists))
	for id := range b.gists {
		ids = append(ids, id)
	}
	return ids, nil
}
