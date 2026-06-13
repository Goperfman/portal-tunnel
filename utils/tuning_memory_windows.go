//go:build windows

package utils

import (
	"syscall"
	"unsafe"
)

var (
	modkernel32                            = syscall.NewLazyDLL("kernel32.dll")
	procGetPhysicallyInstalledSystemMemory = modkernel32.NewProc("GetPhysicallyInstalledSystemMemory")
)

func systemMemory() int64 {
	var totalKB uint64
	ret, _, _ := procGetPhysicallyInstalledSystemMemory.Call(uintptr(unsafe.Pointer(&totalKB)))
	if ret == 0 {
		return 0
	}
	return int64(totalKB) * 1024
}
