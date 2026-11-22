//go:build windows

package main

func platformDiskFree(path string) (uint64, bool) {
	return 0, false
}
