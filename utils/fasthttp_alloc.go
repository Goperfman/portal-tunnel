//go:build !windows

package utils

import (
	"context"

	"github.com/gosuda/beaver/alloc"
)

func fastHTTPAllocatorContext(ctx context.Context) (context.Context, func()) {
	a := alloc.NewArena()
	return alloc.WithAllocator(ctx, a), a.Close
}
