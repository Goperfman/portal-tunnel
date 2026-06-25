//go:build !windows

package utils

import (
	"net/http"
	"sync/atomic"

	"github.com/rs/zerolog/log"

	"github.com/gosuda/beaver/alloc"
)

func newBeaverMiddleware(poolSize int) func(http.Handler) http.Handler {
	factory := beaverFactory(DefaultAllocatorKind(), poolSize)
	pool := alloc.NewPool(factory)
	var failures atomic.Uint64
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			a, err := pool.Get()
			if err != nil {
				if failures.Add(1)%100 == 1 {
					log.Warn().Err(err).Msg("beaver allocator exhausted, serving request without allocator")
				}
				next.ServeHTTP(w, r)
				return
			}
			defer pool.Put(a)
			ctx := alloc.WithAllocator(r.Context(), a)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
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
