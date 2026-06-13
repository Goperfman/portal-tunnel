//go:build windows

package utils

import "net/http"

func newBeaverMiddleware(poolSize int) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler { return next }
}
