//go:build windows

package utils

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// AllocatorKind selects the backing allocator for process-level buffer pools.
// On Windows beaver/balloc is unavailable, so all kinds fall back to sync.Pool.
type AllocatorKind string

const (
	AllocatorHybrid   AllocatorKind = "hybrid"
	AllocatorBalloc   AllocatorKind = "balloc"
	AllocatorArena    AllocatorKind = "arena"
	AllocatorSyncPool AllocatorKind = "sync"
)

var defaultAllocatorKind = AllocatorHybrid

// SetDefaultAllocatorKind is a no-op on Windows; sync.Pool is always used.
func SetDefaultAllocatorKind(kind AllocatorKind) {
	switch kind {
	case AllocatorHybrid, AllocatorBalloc, AllocatorArena, AllocatorSyncPool:
		defaultAllocatorKind = kind
	default:
		defaultAllocatorKind = AllocatorHybrid
	}
}

// DefaultAllocatorKind returns the currently configured default allocator kind.
func DefaultAllocatorKind() AllocatorKind {
	return defaultAllocatorKind
}

// ParseAllocatorKind parses a string into a known AllocatorKind.
func ParseAllocatorKind(s string) AllocatorKind {
	switch s {
	case string(AllocatorBalloc):
		return AllocatorBalloc
	case string(AllocatorArena):
		return AllocatorArena
	case string(AllocatorSyncPool):
		return AllocatorSyncPool
	default:
		return AllocatorHybrid
	}
}

// DefaultRuntimeAllocatorKind returns the default kind string used in config.
func DefaultRuntimeAllocatorKind() string {
	return string(AllocatorHybrid)
}

// ValidateAllocatorKind returns an error if s is not a supported kind.
func ValidateAllocatorKind(s string) error {
	switch ParseAllocatorKind(s) {
	case AllocatorHybrid, AllocatorBalloc, AllocatorArena, AllocatorSyncPool:
		return nil
	}
	return fmt.Errorf("unsupported allocator kind %q", s)
}

// BufferPool is a size-classed pool of fixed-size []byte buffers. On Windows
// it uses the standard library sync.Pool because beaver/balloc relies on mmap.
type BufferPool struct {
	size int
	pool sync.Pool
}

// NewBufferPool creates a pool for buffers of exactly size bytes. The kind
// argument is accepted for API compatibility but is ignored on Windows.
func NewBufferPool(size int, _ AllocatorKind) *BufferPool {
	return &BufferPool{
		size: size,
		pool: sync.Pool{
			New: func() any {
				buf := make([]byte, size)
				return &buf
			},
		},
	}
}

// Get returns a buffer of length size and capacity size.
func (p *BufferPool) Get() []byte {
	buf := p.pool.Get().(*[]byte)
	return (*buf)[:p.size]
}

// Put returns a buffer to the pool. Buffers with the wrong capacity are dropped.
func (p *BufferPool) Put(buf []byte) {
	if cap(buf) != p.size {
		return
	}
	p.pool.Put(&buf)
}

// Close is a no-op for the Windows sync.Pool implementation.
func (p *BufferPool) Close() {}

var (
	globalPoolsMu sync.Mutex
	globalPools   = make(map[int]*BufferPool)
)

// GlobalBufferPool returns a shared BufferPool for the requested size. Pools are
// created lazily and cached for the process lifetime.
func GlobalBufferPool(size int) *BufferPool {
	if size <= 0 {
		panic("GlobalBufferPool: size must be positive")
	}

	globalPoolsMu.Lock()
	defer globalPoolsMu.Unlock()

	if p, ok := globalPools[size]; ok {
		return p
	}
	p := NewBufferPool(size, defaultAllocatorKind)
	globalPools[size] = p
	return p
}

// MarshalJSON returns the JSON encoding of v. On Windows it always falls back
// to the standard library because beaver/balloc is unavailable.
func MarshalJSON(_ context.Context, v any) ([]byte, error) {
	return json.Marshal(v)
}

// CloneBytes returns a heap-allocated copy of src.
func CloneBytes(src []byte) []byte {
	if len(src) == 0 {
		return nil
	}
	dst := make([]byte, len(src))
	copy(dst, src)
	return dst
}
