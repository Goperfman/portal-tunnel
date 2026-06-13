//go:build linux

package utils

import "golang.org/x/sys/unix"

func systemMemory() int64 {
	if v := readIntFile("/sys/fs/cgroup/memory/memory.limit_in_bytes"); v > 0 && v < maxCgroupLimit {
		return v
	}
	if v := readIntFile("/sys/fs/cgroup/memory.max"); v > 0 && v < maxCgroupLimit {
		return v
	}
	var si unix.Sysinfo_t
	if err := unix.Sysinfo(&si); err == nil {
		return int64(si.Totalram) * int64(si.Unit)
	}
	return 0
}
