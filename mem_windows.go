//go:build windows

package main

func platformMemAvailable() uint64 {
	return 1 << 40
}
