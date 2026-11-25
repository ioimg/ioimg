// Package cache implements the L1 disk cache.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ioimg/ioimg/service/internal/uringread"
)

// Disk is a size-bounded, process-local LRU index over a directory of files.
// Writes are atomic (temp file + rename), so multiple processes may share the
// same directory safely.
type Disk struct {
	dir      string
	maxBytes int64
	mode     readMode

	mu      sync.Mutex
	entries map[string]*entry
	size    int64
	clock   uint64
}

type entry struct {
	size     int64
	lastUsed uint64
	mtime    time.Time
}

// ReadMode selects how a cache object is read.
type ReadMode string

const (
	// ReadAuto picks io_uring when the kernel accepts a ring, else ReadOpenStat.
	ReadAuto ReadMode = "auto"
	// ReadOS is os.ReadFile followed by os.Stat (two opens per hit).
	ReadOS ReadMode = "os"
	// ReadOpenStat is os.Open + f.Stat + io.ReadFull: one fd, one fewer open.
	ReadOpenStat ReadMode = "openstat"
	// ReadUring is openat -> read -> close over io_uring (three enters).
	ReadUring ReadMode = "uring"
	// ReadUringLinked is the same sequence linked with fixed files and buffers,
	// so one cache read costs a single io_uring_enter.
	ReadUringLinked ReadMode = "uringlink"
)

// readMode is the resolved internal representation.
type readMode uint8

const (
	readOS readMode = iota
	readOpenStat
	readUring
	readUringLinked
)

func (m ReadMode) resolve() readMode {
	switch m {
	case ReadOS:
		return readOS
	case ReadUring:
		return readUring
	case ReadUringLinked:
		return readUringLinked
	case ReadOpenStat:
		return readOpenStat
	case ReadAuto, "":
		if UringAvailable() {
			return readUring
		}
		return readOpenStat
	}
	return readOpenStat
}

func (m readMode) String() string {
	switch m {
	case readOS:
		return string(ReadOS)
	case readUring:
		return string(ReadUring)
	case readUringLinked:
		return string(ReadUringLinked)
	}
	return string(ReadOpenStat)
}

// defaultMode is what NewDisk uses unless the caller overrides it. It is set by
// the transport selector (celeris wants blocking reads, fiber wants io_uring).
var defaultMode = ReadAuto

// ReadModeFromEnv reads IOIMG_CACHE_READ, falling back to auto.
func ReadModeFromEnv() ReadMode {
	switch m := ReadMode(strings.TrimSpace(os.Getenv("IOIMG_CACHE_READ"))); m {
	case ReadOS, ReadOpenStat, ReadUring, ReadUringLinked, ReadAuto:
		return m
	}
	return ReadAuto
}

// SetDefaultReadMode sets the mode for disks created afterwards.
func SetDefaultReadMode(m ReadMode) { defaultMode = m }

// DefaultReadMode returns the mode used for new disks.
func DefaultReadMode() ReadMode { return defaultMode }

// NewDisk opens (and creates) a cache directory. Existing files are indexed.
func NewDisk(dir string, maxBytes int64) (*Disk, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	mode := ReadModeFromEnv()
	if mode == ReadAuto {
		mode = defaultMode
	}
	d := &Disk{dir: dir, maxBytes: maxBytes, mode: mode.resolve(), entries: make(map[string]*entry)}
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
		d.entries[key] = &entry{size: info.Size(), lastUsed: d.tick(), mtime: info.ModTime()}
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

	var (
		data []byte
		mod  time.Time
		err  error
		size int64
	)

	switch d.mode {
	case readOpenStat, readUring, readUringLinked:
		// io_uring reads cannot return mtime without a stat; the LRU index
		// already carries it from Put/index time, so no extra syscall.
		d.mu.Lock()
		e := d.entries[key]
		d.mu.Unlock()
		if e != nil && !e.mtime.IsZero() {
			mod = e.mtime
		} else {
			mod = time.Now()
		}
	}

	switch d.mode {
	case readOpenStat:
		f, openErr := os.Open(p)
		if openErr != nil {
			return nil, time.Time{}, false
		}
		info, statErr := f.Stat()
		if statErr != nil {
			f.Close()
			return nil, time.Time{}, false
		}
		mod = info.ModTime()
		buf := make([]byte, info.Size())
		if _, readErr := io.ReadFull(f, buf); readErr != nil {
			f.Close()
			return nil, time.Time{}, false
		}
		f.Close()
		data = buf
		size = info.Size()
	case readUring:
		data, err = uringread.ReadFile(p)
		if err != nil {
			return nil, time.Time{}, false
		}
		size = int64(len(data))
	case readUringLinked:
		data, err = uringread.ReadFileLinked(p)
		if err != nil {
			return nil, time.Time{}, false
		}
		size = int64(len(data))
	default:
		data, err = os.ReadFile(p)
		if err != nil {
			return nil, time.Time{}, false
		}
		info, statErr := os.Stat(p)
		if statErr != nil {
			return nil, time.Time{}, false
		}
		mod = info.ModTime()
		size = info.Size()
	}

	d.mu.Lock()
	if e, ok := d.entries[key]; ok {
		e.lastUsed = d.tick()
		if e.mtime.IsZero() {
			e.mtime = mod
		}
	} else {
		d.entries[key] = &entry{size: size, lastUsed: d.tick(), mtime: mod}
		d.size += size
	}
	d.mu.Unlock()
	return data, mod, true
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
	d.entries[key] = &entry{size: int64(len(data)), lastUsed: d.tick(), mtime: time.Now()}
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

// ReadMode reports the resolved read mode.
func (d *Disk) ReadMode() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.mode.String()
}

// SetReadMode switches how subsequent reads are served. Used by the transport
// selector when a fallback tier needs a different read path.
func (d *Disk) SetReadMode(m ReadMode) {
	resolved := m.resolve()
	d.mu.Lock()
	d.mode = resolved
	d.mu.Unlock()
}

var (
	uringOnce sync.Once
	uringOK   bool
)

// UringAvailable reports whether this kernel accepts an io_uring ring. The probe
// creates a small ring once and caches the answer; blocked io_uring_setup (seccomp,
// gVisor, k8s policy) yields false so callers can fall back.
func UringAvailable() bool {
	uringOnce.Do(func() { uringOK = uringread.Available() })
	return uringOK
}
