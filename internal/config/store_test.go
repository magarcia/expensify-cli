package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/magarcia/expensify-cli/internal/fault"
)

func TestMain(m *testing.M) {
	if err := os.Setenv("EXPENSIFY_CREDENTIAL_STORE", "file"); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

func isolateEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"EXPENSIFY_CONFIG_DIR", "XDG_CONFIG_HOME", "EXPENSIFY_AUTH_TOKEN",
		"EXPENSIFY_PARTNER_USER_ID", "EXPENSIFY_PARTNER_USER_SECRET",
		"EXPENSIFY_OUTPUT", "EXPENSIFY_POLICY_ID", "EXPENSIFY_CURRENCY", "EXPENSIFY_TIMEOUT",
	} {
		t.Setenv(name, "")
	}
}

func TestNewDirectoryPrecedence(t *testing.T) {
	isolateEnvironment(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if got := New("").Dir(); got != filepath.Join(home, ".config", "expensify") {
		t.Fatalf("home directory: %q", got)
	}
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	if got := New("").Dir(); got != filepath.Join(xdg, "expensify") {
		t.Fatalf("XDG directory: %q", got)
	}
	environment := t.TempDir()
	t.Setenv("EXPENSIFY_CONFIG_DIR", environment)
	if got := New("").Dir(); got != environment {
		t.Fatalf("environment directory: %q", got)
	}
	explicit := t.TempDir()
	if got := New(explicit).Dir(); got != explicit {
		t.Fatalf("explicit directory: %q", got)
	}
}

func TestStoreMissingCredentials(t *testing.T) {
	store := New(filepath.Join(t.TempDir(), "missing"))
	got, err := store.Load()
	if err != nil || got != (Credentials{}) {
		t.Fatalf("Load() = %#v, %v", got, err)
	}
	if err := store.Logout(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.Dir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("read-only operations created the configuration directory")
	}
}

func TestStoreCredentialLifecycle(t *testing.T) {
	store := New(filepath.Join(t.TempDir(), "private"))
	want := Credentials{AuthToken: "token", PartnerUserID: "device", PartnerUserSecret: "secret", Email: "person@example.com", AccountID: 42}
	if err := store.Save(want); err != nil {
		t.Fatal(err)
	}
	for path, wantMode := range map[string]os.FileMode{store.Dir(): 0o700, store.Path(): 0o600} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); runtime.GOOS != "windows" && got != wantMode {
			t.Errorf("%s permissions = %04o, want %04o", path, got, wantMode)
		}
		must(t, checkPrivatePermissions(path, info))
	}
	got, err := store.Load()
	if err != nil || got != want {
		t.Fatalf("Load() = %#v, %v", got, err)
	}
	want.AuthToken = "replacement"
	if err := store.Save(want); err != nil {
		t.Fatal(err)
	}
	got, err = store.Load()
	if err != nil || got != want {
		t.Fatalf("replacement Load() = %#v, %v", got, err)
	}
	if err := store.SaveSettings(Settings{Output: "json"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Logout(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("logout retained credentials")
	}
	if _, err := os.Stat(filepath.Join(store.Dir(), "config.json")); err != nil {
		t.Fatal("logout removed nonsecret settings:", err)
	}
	if err := store.Logout(); err != nil {
		t.Fatal("logout must be idempotent:", err)
	}
}

func TestStoreRejectsUnsafeDirectory(t *testing.T) {
	for _, kind := range []string{"permissions", "symlink", "file"} {
		t.Run(kind, func(t *testing.T) {
			if kind == "permissions" && runtime.GOOS == "windows" {
				t.Skip("Windows ACL security is tested separately")
			}
			path := filepath.Join(t.TempDir(), "config")
			switch kind {
			case "permissions":
				must(t, os.Mkdir(path, 0o700))
				must(t, os.Chmod(path, 0o755))
			case "symlink":
				makeSymlink(t, t.TempDir(), path)
			case "file":
				must(t, os.WriteFile(path, []byte("not a directory"), 0o600))
			}
			store := New(path)
			_, err := store.Load()
			assertFaultCode(t, err, "CONFIG-002")
			assertFaultCode(t, store.Save(Credentials{AuthToken: "token"}), "CONFIG-002")
			assertFaultCode(t, store.Logout(), "CONFIG-002")
		})
	}
}

func TestStoreRejectsUnsafeCredentialFile(t *testing.T) {
	for _, kind := range []string{"permissions", "symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			if kind == "permissions" && runtime.GOOS == "windows" {
				t.Skip("Windows ACL security is tested separately")
			}
			store := New(privateDir(t))
			target := filepath.Join(t.TempDir(), "target")
			must(t, os.WriteFile(target, []byte(`{"authToken":"keep-me"}`), 0o600))
			switch kind {
			case "permissions":
				must(t, os.WriteFile(store.Path(), []byte(`{}`), 0o600))
				must(t, os.Chmod(store.Path(), 0o644))
			case "symlink":
				makeSymlink(t, target, store.Path())
			case "directory":
				must(t, os.Mkdir(store.Path(), 0o700))
			}
			_, err := store.Load()
			assertFaultCode(t, err, "CONFIG-002")
			assertFaultCode(t, store.Save(Credentials{AuthToken: "new"}), "CONFIG-002")
			assertFaultCode(t, store.Logout(), "CONFIG-002")
			data, err := os.ReadFile(target)
			must(t, err)
			if string(data) != `{"authToken":"keep-me"}` {
				t.Fatal("operation changed the symlink target")
			}
		})
	}
}

func TestStoreRejectsCorruptCredentialsWithoutLeakingSecrets(t *testing.T) {
	for name, data := range map[string]string{
		"invalid":   `{"authToken":"secret-do-not-leak",`,
		"trailing":  `{} {}`,
		"unknown":   `{"password":"secret-do-not-leak"}`,
		"null":      "null",
		"array":     "[]",
		"empty":     "",
		"oversized": `{"authToken":"` + strings.Repeat("x", 1<<20) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			store := New(privateDir(t))
			must(t, os.WriteFile(store.Path(), []byte(data), 0o600))
			_, err := store.Load()
			assertFaultCode(t, err, "CONFIG-001")
			if strings.Contains(err.Error(), "secret-do-not-leak") {
				t.Fatal("error exposed credential contents")
			}
		})
	}
}

func TestStoreRejectsIncompleteDeviceCredentials(t *testing.T) {
	store := New(privateDir(t))
	assertFaultCode(t, store.Save(Credentials{PartnerUserID: "device"}), "AUTH-002")
	must(t, os.WriteFile(store.Path(), []byte(`{"partnerUserSecret":"secret"}`), 0o600))
	_, err := store.Load()
	assertFaultCode(t, err, "AUTH-002")
}

func TestConcurrentSavesAreAtomic(t *testing.T) {
	store := New(privateDir(t))
	must(t, store.Save(Credentials{AuthToken: "initial", PartnerUserID: "initial", PartnerUserSecret: "initial"}))
	stop := make(chan struct{})
	readerDone := make(chan error, 1)
	go func() {
		reads := 0
		for {
			select {
			case <-stop:
				if reads == 0 {
					readerDone <- errors.New("reader did not observe any credential snapshots")
				} else {
					readerDone <- nil
				}
				return
			default:
			}
			data, err := os.ReadFile(store.Path())
			// Windows can deny opening the file briefly while replacing it.
			const sharingViolation syscall.Errno = 32
			if runtime.GOOS == "windows" && errors.Is(err, sharingViolation) {
				continue
			}
			if err != nil {
				readerDone <- err
				return
			}
			var credentials Credentials
			if err := json.Unmarshal(data, &credentials); err != nil {
				readerDone <- fmt.Errorf("reader observed incomplete JSON: %w", err)
				return
			}
			if credentials.AuthToken != credentials.PartnerUserID || credentials.AuthToken != credentials.PartnerUserSecret {
				readerDone <- errors.New("reader observed mixed credentials")
				return
			}
			reads++
		}
	}()
	var writers sync.WaitGroup
	errors := make(chan error, 4)
	for writer := range 4 {
		writers.Go(func() {
			for iteration := range 10 {
				id := fmt.Sprintf("writer-%d-%d", writer, iteration)
				if err := store.Save(Credentials{AuthToken: id, PartnerUserID: id, PartnerUserSecret: id}); err != nil {
					errors <- err
					return
				}
			}
		})
	}
	writers.Wait()
	close(stop)
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	if err := <-readerDone; err != nil {
		t.Error(err)
	}
	_, err := store.Load()
	must(t, err)
	entries, err := os.ReadDir(store.Dir())
	must(t, err)
	if len(entries) != 1 || entries[0].Name() != "credentials.json" {
		t.Fatal("atomic save left temporary files behind")
	}
}

func TestResolveCredentials(t *testing.T) {
	stored := Credentials{AuthToken: "stored-token", PartnerUserID: "stored-device", PartnerUserSecret: "stored-secret", Email: "stored@example.com", AccountID: 42}
	for _, test := range []struct {
		name, token, userID, secret, source string
		want                                Credentials
		wantError                           bool
	}{
		{name: "stored", source: "stored", want: stored},
		{name: "token only", token: "env-token", source: "environment", want: Credentials{AuthToken: "env-token"}},
		{name: "device only", userID: "env-device", secret: "env-secret", source: "environment", want: Credentials{PartnerUserID: "env-device", PartnerUserSecret: "env-secret"}},
		{name: "token and device", token: "env-token", userID: "env-device", secret: "env-secret", source: "environment", want: Credentials{AuthToken: "env-token", PartnerUserID: "env-device", PartnerUserSecret: "env-secret"}},
		{name: "missing secret", userID: "env-device", wantError: true},
		{name: "missing user ID", secret: "env-secret", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			isolateEnvironment(t)
			t.Setenv("EXPENSIFY_AUTH_TOKEN", test.token)
			t.Setenv("EXPENSIFY_PARTNER_USER_ID", test.userID)
			t.Setenv("EXPENSIFY_PARTNER_USER_SECRET", test.secret)
			got, source, err := Resolve(stored)
			if test.wantError {
				assertFaultCode(t, err, "AUTH-002")
				return
			}
			if err != nil || got != test.want || source != test.source {
				t.Fatalf("Resolve() = %#v, %q, %v; want %#v, %q", got, source, err, test.want, test.source)
			}
		})
	}
	t.Run("none", func(t *testing.T) {
		isolateEnvironment(t)
		got, source, err := Resolve(Credentials{})
		if err != nil || got != (Credentials{}) || source != "none" {
			t.Fatalf("Resolve() = %#v, %q, %v", got, source, err)
		}
	})
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func privateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	must(t, setPrivatePermissions(dir, true))
	return dir
}

func makeSymlink(t *testing.T, target, path string) {
	t.Helper()
	if err := os.Symlink(target, path); err != nil {
		if runtime.GOOS == "windows" {
			t.Skip("creating symbolic links requires Windows Developer Mode or administrator privileges")
		}
		t.Fatal(err)
	}
}

func assertFaultCode(t *testing.T, err error, want string) {
	t.Helper()
	var problem *fault.Error
	if !errors.As(err, &problem) || problem.Code != want {
		t.Fatalf("error = %v; want fault code %s", err, want)
	}
}
