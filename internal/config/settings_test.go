package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSettingsPrecedence(t *testing.T) {
	isolateEnvironment(t)
	t.Chdir(t.TempDir())
	store := New(privateDir(t))
	settings, err := LoadSettings(store)
	must(t, err)
	if settings != DefaultSettings() {
		t.Fatalf("defaults = %#v", settings)
	}
	user := Settings{Output: "text", PolicyID: "user-policy", Currency: "EUR", Timeout: "45s"}
	must(t, store.SaveSettings(user))
	settings, err = LoadSettings(store)
	must(t, err)
	if settings != user {
		t.Fatalf("user settings = %#v", settings)
	}
	must(t, os.WriteFile(".expensify.json", []byte(`{"output":"json","policyID":"project-policy","timeout":"2m"}`), 0o644))
	settings, err = LoadSettings(store)
	must(t, err)
	want := Settings{Output: "json", PolicyID: "project-policy", Currency: "EUR", Timeout: "2m"}
	if settings != want {
		t.Fatalf("project merge = %#v, want %#v", settings, want)
	}
	t.Setenv("EXPENSIFY_OUTPUT", "llm")
	t.Setenv("EXPENSIFY_POLICY_ID", "environment-policy")
	t.Setenv("EXPENSIFY_CURRENCY", "GBP")
	t.Setenv("EXPENSIFY_TIMEOUT", "3m")
	settings, err = LoadSettings(store)
	must(t, err)
	want = Settings{Output: "llm", PolicyID: "environment-policy", Currency: "GBP", Timeout: "3m"}
	if settings != want {
		t.Fatalf("environment merge = %#v, want %#v", settings, want)
	}
	persisted, err := store.LoadSettings()
	must(t, err)
	if persisted != user {
		t.Fatalf("project or environment settings leaked into stored settings: %#v", persisted)
	}
}

func TestRejectProjectCredentials(t *testing.T) {
	isolateEnvironment(t)
	t.Chdir(t.TempDir())
	must(t, os.WriteFile(".expensify.json", []byte(`{"authToken":"untrusted"}`), 0o644))
	_, err := LoadSettings(New(privateDir(t)))
	assertFaultCode(t, err, "CONFIG-001")
}

func TestRejectProjectSymlink(t *testing.T) {
	isolateEnvironment(t)
	t.Chdir(t.TempDir())
	target := filepath.Join(t.TempDir(), "settings.json")
	must(t, os.WriteFile(target, []byte(`{"output":"json"}`), 0o600))
	makeSymlink(t, target, ".expensify.json")
	_, err := LoadSettings(New(privateDir(t)))
	assertFaultCode(t, err, "CONFIG-002")
}

func TestRejectCorruptAndUnsafeUserSettings(t *testing.T) {
	store := New(privateDir(t))
	path := filepath.Join(store.Dir(), "config.json")
	must(t, os.WriteFile(path, []byte(`{"output":`), 0o600))
	_, err := store.LoadSettings()
	assertFaultCode(t, err, "CONFIG-001")
	if runtime.GOOS == "windows" {
		return
	}
	must(t, os.WriteFile(path, []byte(`{}`), 0o600))
	must(t, os.Chmod(path, 0o644))
	_, err = store.LoadSettings()
	assertFaultCode(t, err, "CONFIG-002")
	assertFaultCode(t, store.SaveSettings(Settings{Output: "json"}), "CONFIG-002")
}

func TestValidateSettings(t *testing.T) {
	for _, settings := range []Settings{
		{Output: "xml"}, {Currency: "eur"}, {Currency: "EU"}, {Currency: "€€€"},
		{Timeout: "later"}, {Timeout: "0s"}, {Timeout: "-1s"},
	} {
		assertFaultCode(t, ValidateSettings(settings), "CLI-001")
		assertFaultCode(t, New(privateDir(t)).SaveSettings(settings), "CLI-001")
	}
	for _, output := range []string{"", "auto", "text", "json", "llm"} {
		must(t, ValidateSettings(Settings{Output: output, Currency: "EUR", Timeout: "2m"}))
	}
}

func TestInvalidEnvironmentSettings(t *testing.T) {
	isolateEnvironment(t)
	t.Chdir(t.TempDir())
	t.Setenv("EXPENSIFY_TIMEOUT", "never")
	_, err := LoadSettings(New(privateDir(t)))
	assertFaultCode(t, err, "CLI-001")
}
