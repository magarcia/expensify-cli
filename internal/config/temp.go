package config

import "os"

// PrivateTempDir creates a private directory before a browser can write session data into it.
func PrivateTempDir(parent, prefix string) (string, error) {
	store := New(parent)
	if err := store.checkDir(true); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(store.Dir(), prefix)
	if err != nil {
		return "", fileError("create", "temporary login profile")
	}
	if err := setPrivatePermissions(dir, true); err != nil {
		_ = os.Remove(dir)
		return "", fileError("secure", "temporary login profile")
	}
	return dir, nil
}
