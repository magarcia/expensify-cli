//go:build !windows

package config

import "os"

func readConcurrentSnapshot(path string) ([]byte, error) {
	return os.ReadFile(path)
}
