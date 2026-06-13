//go:build !windows

package utils

import (
	"net/http"

	"github.com/gosuda/beaver/alloc"
)


func newBeaverMiddleware(poolSize int) func(http.Handler) http.Handler {
	factory := beaverFactory(DefaultAllocatorKind(), poolSize)
	pool := alloc.NewPool(factory)
	return alloc.Middleware(pool)
}

func beaverFactory(kind AllocatorKind, poolSize int) func() (alloc.Allocator, error) {
	switch kind {
	case AllocatorBalloc:
		return alloc.BallocFactory(uintptr(poolSize))
	case AllocatorArena:
		return alloc.ArenaFactory()
	case AllocatorHybrid:
		return alloc.HybridFactory(uintptr(poolSize))
	default:
		return alloc.HybridFactory(uintptr(poolSize))
	}
}
