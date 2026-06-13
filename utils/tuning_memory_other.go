//go:build !linux && !windows

package utils

func systemMemory() int64 {
	return 0
}
