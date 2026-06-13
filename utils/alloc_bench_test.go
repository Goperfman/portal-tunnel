package utils

import (
	"sync"
	"testing"
)

func BenchmarkBufferPoolBeaverHybrid(b *testing.B) {
	p := NewBufferPool(64*1024, AllocatorHybrid)
	defer p.Close()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf := p.Get()
		p.Put(buf)
	}
}

func BenchmarkBufferPoolBeaverBalloc(b *testing.B) {
	p := NewBufferPool(64*1024, AllocatorBalloc)
	defer p.Close()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf := p.Get()
		p.Put(buf)
	}
}

func BenchmarkBufferPoolBeaverArena(b *testing.B) {
	p := NewBufferPool(64*1024, AllocatorArena)
	defer p.Close()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf := p.Get()
		p.Put(buf)
	}
}

func BenchmarkBufferPoolSyncPool(b *testing.B) {
	p := sync.Pool{
		New: func() any {
			buf := make([]byte, 64*1024)
			return &buf
		},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf := p.Get().(*[]byte)
		p.Put(buf)
	}
}

func BenchmarkCloneBytes1350(b *testing.B) {
	src := make([]byte, 1350)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = CloneBytes(src)
	}
}
