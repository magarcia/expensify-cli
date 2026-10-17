package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateTempDir(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "config")
	dir, err := PrivateTempDir(parent, ".browser-login-")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(dir) != parent {
		t.Fatalf("profile escaped config directory: %s", dir)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkPrivatePermissions(dir, info); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("profile isn't empty: %v %v", entries, err)
	}
}
