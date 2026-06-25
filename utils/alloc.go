//go:build !windows

package utils

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/gosuda/beaver/alloc"
)

// AllocatorKind selects the backing beaver allocator for process-level buffer pools.
type AllocatorKind string

const (
	// AllocatorHybrid uses beaver's hybrid allocator: fast pure-Go slabs for
	// small allocations and mmap-backed balloc for large ones.
	AllocatorHybrid AllocatorKind = "hybrid"
	// AllocatorBalloc uses a single mmap-backed block allocator per pool.
	AllocatorBalloc AllocatorKind = "balloc"
	// AllocatorArena uses GC-friendly arenas.
	AllocatorArena AllocatorKind = "arena"
	// AllocatorSyncPool is the baseline using only the standard library sync.Pool.
	AllocatorSyncPool AllocatorKind = "sync"
)

var defaultAllocatorKind = AllocatorHybrid

// SetDefaultAllocatorKind sets the allocator kind used by GlobalBufferPool.
// It is called once during InitRuntimeTuning.
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

func (k AllocatorKind) factory(size int) func() (alloc.Allocator, error) {
	switch k {
	case AllocatorBalloc:
		return alloc.BallocFactory(uintptr(size))
	case AllocatorArena:
		return alloc.ArenaFactory()
	case AllocatorHybrid:
		return alloc.HybridFactory(uintptr(size))
	default:
		return nil
	}
}

// BufferPool is a size-classed pool of fixed-size []byte buffers backed by a
// beaver allocator pool. Buffers returned by Get have length and capacity equal
// to the pool's size. The pool owns the allocator lifecycle; callers only need
// to Put buffers back when they are no longer used.
type BufferPool struct {
	size      int
	kind      AllocatorKind
	allocPool *alloc.Pool

	mu      sync.Mutex
	current alloc.Allocator
	gen     atomic.Uint64

	pool sync.Pool
}

// pooledBuf wraps a buffer together with the allocator generation it belongs
// to. If the allocator is replaced, older generations are discarded lazily.
type pooledBuf struct {
	gen uint64
	buf []byte
}

// NewBufferPool creates a pool for buffers of exactly size bytes using the
// given allocator kind. If kind is AllocatorSyncPool, the pool behaves like a
// plain sync.Pool of []byte.
func NewBufferPool(size int, kind AllocatorKind) *BufferPool {
	p := &BufferPool{
		size: size,
		kind: kind,
	}
	if kind != AllocatorSyncPool {
		p.allocPool = alloc.NewPool(kind.factory(size))
		if a, err := p.allocPool.Get(); err == nil {
			p.current = a
		}
		p.pool.New = func() any { return &pooledBuf{} }
	} else {
		p.pool.New = func() any { return make([]byte, size) }
	}
	return p
}

// Get returns a buffer of length size and capacity size. The buffer is zeroed
// only insofar as the allocator guarantees; callers must overwrite it before
// reading sensitive data out.
func (p *BufferPool) Get() []byte {
	if p.kind == AllocatorSyncPool {
		return p.pool.Get().([]byte)
	}

	v := p.pool.Get()
	if v != nil {
		pb := v.(*pooledBuf)
		if pb.buf != nil && cap(pb.buf) == p.size && pb.gen == p.gen.Load() {
			return pb.buf[:p.size]
		}
		// stale or wrong-sized buffer; reuse the wrapper
		pb.buf = nil
		p.pool.Put(pb)
	}
	return p.alloc()
}

func (p *BufferPool) alloc() []byte {
	if p.kind == AllocatorSyncPool || p.current == nil {
		return make([]byte, p.size)
	}

	ctx := alloc.WithAllocator(context.Background(), p.current)
	b, err := alloc.MakeBytes(ctx, p.size, p.size)
	if err == nil {
		return b
	}

	// Allocator exhausted: replace it. Buffers already in p.pool will be
	// rejected lazily by the generation check on Get.
	p.mu.Lock()
	if p.current != nil {
		p.current.Close()
	}
	a, err := p.allocPool.Get()
	if err != nil {
		p.mu.Unlock()
		BeaverAllocatorFailures.Add(1)
		return make([]byte, p.size)
	}
	p.current = a
	p.gen.Add(1)
	ctx = alloc.WithAllocator(context.Background(), a)
	b, err = alloc.MakeBytes(ctx, p.size, p.size)
	p.mu.Unlock()
	if err != nil {
		BeaverAllocatorFailures.Add(1)
		return make([]byte, p.size)
	}
	return b
}

// Put returns a buffer to the pool. Buffers with the wrong capacity are
// dropped so they cannot leak into a different size class.
func (p *BufferPool) Put(buf []byte) {
	if cap(buf) != p.size {
		return
	}
	if p.kind == AllocatorSyncPool {
		p.pool.Put(buf[:p.size])
		return
	}
	var pb *pooledBuf
	if v := p.pool.Get(); v != nil {
		pb = v.(*pooledBuf)
	} else {
		pb = &pooledBuf{}
	}
	pb.gen = p.gen.Load()
	pb.buf = buf[:cap(buf)]
	p.pool.Put(pb)
}

// Close releases the underlying allocator. The pool must not be used after Close.
func (p *BufferPool) Close() {
	p.mu.Lock()
	if p.current != nil {
		p.current.Close()
		p.current = nil
	}
	p.mu.Unlock()
}

// BeaverAllocatorFailures counts how many times a beaver-backed BufferPool had
// to fall back to heap allocation because the allocator was exhausted.
var BeaverAllocatorFailures atomic.Uint64

var (
	globalPoolsMu sync.Mutex
	globalPools   atomic.Value // holds map[poolKey]*BufferPool
)

func init() {
	globalPools.Store(make(map[poolKey]*BufferPool))
}

type poolKey struct {
	size int
	kind AllocatorKind
}

// GlobalBufferPool returns a shared BufferPool for the requested size. The
// default allocator kind is configured by SetDefaultAllocatorKind. Pools are
// created lazily and cached for the process lifetime.
func GlobalBufferPool(size int) *BufferPool {
	if size <= 0 {
		panic("GlobalBufferPool: size must be positive")
	}
	kind := defaultAllocatorKind
	key := poolKey{size: size, kind: kind}

	// Fast path: load the map atomically and check if the pool already exists
	m, ok := globalPools.Load().(map[poolKey]*BufferPool)
	if ok {
		if p, found := m[key]; found {
			return p
		}
	}

	// Slow path: acquire mutex, recreate the map and store it
	globalPoolsMu.Lock()
	defer globalPoolsMu.Unlock()

	// Double check
	m, _ = globalPools.Load().(map[poolKey]*BufferPool)
	if m != nil {
		if p, found := m[key]; found {
			return p
		}
	}

	p := NewBufferPool(size, kind)
	
	// Copy-on-write update
	newMap := make(map[poolKey]*BufferPool, len(m)+1)
	for k, v := range m {
		newMap[k] = v
	}
	newMap[key] = p
	globalPools.Store(newMap)
	return p
}

// CloneBytes returns a heap-allocated copy of src. It is a convenience helper
// used to make short-lifetime copies explicit in hot paths. Unlike using a
// beaver-backed buffer directly, the returned slice has no allocator lifetime
// constraints.
func CloneBytes(src []byte) []byte {
	if len(src) == 0 {
		return nil
	}
	dst := make([]byte, len(src))
	copy(dst, src)
	return dst
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

// MarshalJSON returns the JSON encoding of v, using a beaver-backed buffer
// when an allocator is present in ctx. Falls back to json.Marshal otherwise.
func MarshalJSON(ctx context.Context, v any) ([]byte, error) {
	return alloc.MarshalJSON(ctx, v)
}
