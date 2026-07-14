//go:build windows

package utils

import "context"

func fastHTTPAllocatorContext(ctx context.Context) (context.Context, func()) {
	return ctx, func() {}
}
