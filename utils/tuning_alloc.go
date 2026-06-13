//go:build !windows

package utils

import (
	"net/http"

	"github.com/gosuda/beaver/alloc"
)

func newBeaverMiddleware(poolSize int) func(http.Handler) http.Handler {
	pool := alloc.NewPool(alloc.HybridFactory(uintptr(poolSize)))
	return alloc.Middleware(pool)
}
