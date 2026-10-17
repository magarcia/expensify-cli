//go:build !windows

package config

import (
	"os"

	"github.com/magarcia/expensify-cli/internal/fault"
)

func createPrivateDir(path string) error { return os.MkdirAll(path, 0o700) }

func makeFilePrivate(file *os.File) error { return file.Chmod(0o600) }

func replacePrivateFile(source, target string) error { return os.Rename(source, target) }

func setPrivatePermissions(path string, directory bool) error {
	mode := os.FileMode(0o600)
	if directory {
		mode = 0o700
	}
	return os.Chmod(path, mode)
}

func checkPrivatePermissions(_ string, info os.FileInfo) error {
	if info.Mode().Perm()&0o077 == 0 {
		return nil
	}
	if info.IsDir() {
		return fault.New("CONFIG-002", "The configuration directory is accessible to other users.", "Set its permissions to 0700 before continuing.", 1)
	}
	return fault.New("CONFIG-002", "A configuration file is accessible to other users.", "Set its permissions to 0600 before continuing.", 1)
}
