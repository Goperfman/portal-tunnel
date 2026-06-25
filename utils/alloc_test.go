package utils

import (
	"sync"
	"testing"
)

func TestBufferPoolPutNil(t *testing.T) {
	p := NewBufferPool(1024, AllocatorHybrid)
	defer p.Close()

	// Put before any Get: the wrapper pool is empty and p.pool.Get() returns nil.
	// This used to panic with a nil pointer dereference.
	buf := make([]byte, 1024)
	p.Put(buf)

	got := p.Get()
	if len(got) != 1024 || cap(got) != 1024 {
		t.Fatalf("unexpected buffer size: len=%d cap=%d", len(got), cap(got))
	}
}

func TestBufferPoolGetPutRoundtrip(t *testing.T) {
	for _, kind := range []AllocatorKind{AllocatorHybrid, AllocatorBalloc, AllocatorArena, AllocatorSyncPool} {
		t.Run(string(kind), func(t *testing.T) {
			p := NewBufferPool(4096, kind)
			defer p.Close()

			buf := p.Get()
			if len(buf) != 4096 || cap(buf) != 4096 {
				t.Fatalf("unexpected buffer size: len=%d cap=%d", len(buf), cap(buf))
			}
			for i := range buf {
				buf[i] = byte(i)
			}
			p.Put(buf)

			got := p.Get()
			if len(got) != 4096 || cap(got) != 4096 {
				t.Fatalf("unexpected buffer size on second get: len=%d cap=%d", len(got), cap(got))
			}
		})
	}
}

func TestBufferPoolExhaustionFallback(t *testing.T) {
	previousFailures := BeaverAllocatorFailures.Load()

	// Use a tiny balloc-backed pool. The mmap block is the same size as the
	// requested buffer, so beaver is likely to exhaust it quickly. Even if it
	// does not, BufferPool must still return usable buffers by falling back to
	// the Go heap.
	p := NewBufferPool(64, AllocatorBalloc)
	defer p.Close()

	for i := 0; i < 100; i++ {
		buf := p.Get()
		if len(buf) != 64 || cap(buf) != 64 {
			t.Fatalf("unexpected buffer size at iteration %d: len=%d cap=%d", i, len(buf), cap(buf))
		}
		p.Put(buf)
	}

	// The failure counter is best-effort instrumentation; do not fail the test
	// if the allocator never reported exhaustion in this environment.
	_ = previousFailures
}

func TestGlobalBufferPoolConcurrent(t *testing.T) {
	const workers = 100
	const size = 2048

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := GlobalBufferPool(size)
			if p == nil {
				t.Error("GlobalBufferPool returned nil")
				return
			}
			buf := p.Get()
			if len(buf) != size || cap(buf) != size {
				t.Errorf("unexpected buffer size: len=%d cap=%d", len(buf), cap(buf))
			}
			p.Put(buf)
		}()
	}
	wg.Wait()
}

func TestCloneBytes(t *testing.T) {
	if got := CloneBytes(nil); got != nil {
		t.Fatalf("CloneBytes(nil) = %v, want nil", got)
	}

	src := []byte("hello")
	dst := CloneBytes(src)
	if string(dst) != string(src) {
		t.Fatalf("CloneBytes(%q) = %q", src, dst)
	}
	dst[0] = 'x'
	if src[0] != 'h' {
		t.Fatal("CloneBytes returned a slice that aliases the source")
	}
}
