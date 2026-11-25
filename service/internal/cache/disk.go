// Package cache implements the L1 disk cache.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Disk is a size-bounded, process-local LRU index over a directory of files.
// Writes are atomic (temp file + rename), so multiple processes may share the
// same directory safely.
type Disk struct {
	dir      string
	maxBytes int64

	mu      sync.Mutex
	entries map[string]*entry
	size    int64
	clock   uint64
}

type entry struct {
	size     int64
	lastUsed uint64
}

// NewDisk opens (and creates) a cache directory. Existing files are indexed.
func NewDisk(dir string, maxBytes int64) (*Disk, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	d := &Disk{dir: dir, maxBytes: maxBytes, entries: make(map[string]*entry)}
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, de := range des {
		if de.IsDir() || filepath.Ext(de.Name()) == ".tmp" {
			continue
		}
		info, err := de.Info()
		if err != nil {
			continue
		}
		key := de.Name()
		d.entries[key] = &entry{size: info.Size(), lastUsed: d.tick()}
		d.size += info.Size()
	}
	return d, nil
}

func (d *Disk) tick() uint64 {
	d.clock++
	return d.clock
}

// Path returns the file path for a key.
func (d *Disk) Path(key string) string { return filepath.Join(d.dir, key) }

// Get returns the cached bytes and their modification time.
func (d *Disk) Get(key string) ([]byte, time.Time, bool) {
	p := d.Path(key)
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, time.Time{}, false
	}
	info, err := os.Stat(p)
	if err != nil {
		return nil, time.Time{}, false
	}
	d.mu.Lock()
	if e, ok := d.entries[key]; ok {
		e.lastUsed = d.tick()
	} else {
		d.entries[key] = &entry{size: info.Size(), lastUsed: d.tick()}
		d.size += info.Size()
	}
	d.mu.Unlock()
	return data, info.ModTime(), true
}

// Put writes an entry and evicts least-recently-used files when over budget.
func (d *Disk) Put(key string, data []byte) error {
	p := d.Path(key)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		// On Windows rename over an existing file fails. If another writer won
		// the race, prefer the existing entry and drop the temp file.
		if _, statErr := os.Stat(p); statErr == nil {
			_ = os.Remove(tmp)
			return nil
		}
		_ = os.Remove(tmp)
		return err
	}
	d.mu.Lock()
	old := int64(0)
	if e, ok := d.entries[key]; ok {
		old = e.size
	}
	d.entries[key] = &entry{size: int64(len(data)), lastUsed: d.tick()}
	d.size += int64(len(data)) - old
	d.evictLocked()
	d.mu.Unlock()
	return nil
}

func (d *Disk) evictLocked() {
	if d.size <= d.maxBytes {
		return
	}
	type victim struct {
		key  string
		used uint64
		size int64
	}
	for d.size > d.maxBytes {
		var oldest *victim
		for k, e := range d.entries {
			if oldest == nil || e.lastUsed < oldest.used {
				oldest = &victim{key: k, used: e.lastUsed, size: e.size}
			}
		}
		if oldest == nil {
			return
		}
		if err := os.Remove(d.Path(oldest.key)); err == nil || os.IsNotExist(err) {
			d.size -= oldest.size
		}
		delete(d.entries, oldest.key)
	}
}

// Stats returns entry count and total bytes.
func (d *Disk) Stats() (int, int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.entries), d.size
}

// Key builds the cache key from the transform version, canonical ops, source,
// and negotiated output format.
func Key(version, opsCanonical, source, format string) string {
	h := sha256.New()
	h.Write([]byte(version))
	h.Write([]byte{0})
	h.Write([]byte(opsCanonical))
	h.Write([]byte{0})
	h.Write([]byte(source))
	h.Write([]byte{0})
	h.Write([]byte(format))
	return hex.EncodeToString(h.Sum(nil))[:40]
}

// Delete removes a key from disk and the index. Missing keys are ignored.
func (d *Disk) Delete(key string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if e, ok := d.entries[key]; ok {
		d.size -= e.size
		delete(d.entries, key)
	}
	_ = os.Remove(d.Path(key))
}

// Clear removes every entry in the cache directory.
func (d *Disk) Clear() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	des, err := os.ReadDir(d.dir)
	if err != nil {
		return err
	}
	for _, de := range des {
		if de.IsDir() {
			continue
		}
		_ = os.Remove(filepath.Join(d.dir, de.Name()))
	}
	d.entries = make(map[string]*entry)
	d.size = 0
	return nil
}

// DeletePrefix removes every key whose name starts with prefix.
func (d *Disk) DeletePrefix(prefix string) int {
	d.mu.Lock()
	keys := make([]string, 0, len(d.entries))
	for k := range d.entries {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	d.mu.Unlock()
	for _, k := range keys {
		d.Delete(k)
	}
	return len(keys)
}
