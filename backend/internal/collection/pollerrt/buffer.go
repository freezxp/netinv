package pollerrt

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/freezxp/netinv/backend/internal/platform/wire"
)

// DiskBuffer is the bounded overflow store for metric batches when the core
// link is down (FR-COLL-08): one JSON file per batch, FIFO drop-oldest at the
// byte cap, drained oldest-first on reconnect.
type DiskBuffer struct {
	Dir      string
	MaxBytes int64 // default 64 MiB

	mu      sync.Mutex
	dropped int64
}

func NewDiskBuffer(dir string) (*DiskBuffer, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("buffer: %w", err)
	}
	return &DiskBuffer{Dir: dir, MaxBytes: 64 << 20}, nil
}

func (b *DiskBuffer) Put(batch wire.MetricBatch) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	raw, err := json.Marshal(batch)
	if err != nil {
		return err
	}
	name := filepath.Join(b.Dir, fmt.Sprintf("%d.batch", time.Now().UnixNano()))
	if err := os.WriteFile(name, raw, 0o600); err != nil {
		return err
	}
	b.enforceCap()
	return nil
}

func (b *DiskBuffer) enforceCap() {
	files, total := b.inventory()
	for _, f := range files {
		if total <= b.MaxBytes {
			break
		}
		total -= f.size
		_ = os.Remove(f.path)
		b.dropped++
	}
}

type bufFile struct {
	path string
	size int64
}

// inventory returns buffered files oldest-first plus total bytes.
func (b *DiskBuffer) inventory() ([]bufFile, int64) {
	entries, _ := os.ReadDir(b.Dir)
	var files []bufFile
	var total int64
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".batch" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, bufFile{path: filepath.Join(b.Dir, e.Name()), size: info.Size()})
		total += info.Size()
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	return files, total
}

func (b *DiskBuffer) Depth() (count int, bytes int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	files, total := b.inventory()
	return len(files), total
}

func (b *DiskBuffer) Dropped() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.dropped
}

// Drain replays buffered batches oldest-first through send, stopping at the
// first failure (link still down) or when empty. Rate-limited so a big
// backlog doesn't flood the broker on reconnect (doc 07 §6).
func (b *DiskBuffer) Drain(ctx context.Context, send func(wire.MetricBatch) error) (replayed int) {
	for {
		select {
		case <-ctx.Done():
			return replayed
		default:
		}
		b.mu.Lock()
		files, _ := b.inventory()
		b.mu.Unlock()
		if len(files) == 0 {
			return replayed
		}
		raw, err := os.ReadFile(files[0].path)
		if err != nil {
			_ = os.Remove(files[0].path)
			continue
		}
		var batch wire.MetricBatch
		if err := json.Unmarshal(raw, &batch); err != nil {
			_ = os.Remove(files[0].path) // corrupt file — discard
			continue
		}
		if err := send(batch); err != nil {
			return replayed // still down; retry on next drain cycle
		}
		_ = os.Remove(files[0].path)
		replayed++
		select {
		case <-ctx.Done():
			return replayed
		case <-time.After(50 * time.Millisecond): // drain pacing
		}
	}
}
