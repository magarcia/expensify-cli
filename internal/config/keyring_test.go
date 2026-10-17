package config

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/magarcia/expensify-cli/internal/fault"
	keyringlib "github.com/zalando/go-keyring"
)

func TestSystemKeyringSetRejectsOversizedSecretsBeforeProviderAccess(t *testing.T) {
	providerError := errors.New("mock provider reached")
	keyringlib.MockInitWithError(providerError)
	t.Cleanup(func() {
		keyringlib.MockInitWithError(errors.New("native keyring disabled in tests"))
	})
	for _, test := range []struct {
		name     string
		password string
		want     error
	}{
		{name: "2560 bytes reaches provider", password: strings.Repeat("x", 2560), want: providerError},
		{name: "2561 bytes blocked", password: strings.Repeat("x", 2561), want: keyringlib.ErrSetDataTooBig},
		{name: "multibyte 2560 bytes reaches provider", password: strings.Repeat("é", 1280), want: providerError},
		{name: "multibyte 2561 bytes blocked", password: strings.Repeat("é", 1280) + "x", want: keyringlib.ErrSetDataTooBig},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := (systemKeyring{}).Set("test-service", "test-account", test.password)
			if !errors.Is(err, test.want) {
				t.Fatalf("Set() error = %v, want %v", err, test.want)
			}
		})
	}
}

type keyringCall struct {
	service string
	account string
}

type fakeCredentialKeyring struct {
	values    map[keyringCall]string
	getErr    error
	setErr    error
	deleteErr error
	gets      []keyringCall
	sets      []keyringCall
	deletes   []keyringCall
}

func (k *fakeCredentialKeyring) Get(service, account string) (string, error) {
	call := keyringCall{service, account}
	k.gets = append(k.gets, call)
	if k.getErr != nil {
		return "", k.getErr
	}
	value, ok := k.values[call]
	if !ok {
		return "", keyringlib.ErrNotFound
	}
	return value, nil
}

func (k *fakeCredentialKeyring) Set(service, account, password string) error {
	call := keyringCall{service, account}
	k.sets = append(k.sets, call)
	if k.setErr != nil {
		return k.setErr
	}
	if k.values == nil {
		k.values = make(map[keyringCall]string)
	}
	k.values[call] = password
	return nil
}

func (k *fakeCredentialKeyring) Delete(service, account string) error {
	call := keyringCall{service, account}
	k.deletes = append(k.deletes, call)
	if k.deleteErr != nil {
		return k.deleteErr
	}
	if _, ok := k.values[call]; !ok {
		return keyringlib.ErrNotFound
	}
	delete(k.values, call)
	return nil
}

func newKeyringTestStore(t *testing.T, mode string, backend *fakeCredentialKeyring) *Store {
	t.Helper()
	isolateEnvironment(t)
	t.Setenv("EXPENSIFY_CREDENTIAL_STORE", mode)
	store := New(privateDir(t))
	store.keyring = backend
	return store
}

func sampleKeyringCredentials() Credentials {
	return Credentials{AuthToken: "session-token", PartnerUserID: "device-user", PartnerUserSecret: "device-secret", Email: "person@example.com", AccountID: 42}
}

func keyringAccount(t *testing.T, dir string) keyringCall {
	t.Helper()
	abs, err := filepath.Abs(dir)
	must(t, err)
	return keyringCall{service: "expensify-cli", account: fmt.Sprintf("%x", sha256.Sum256([]byte(abs)))}
}

func keyringJSON(t *testing.T, credentials Credentials) string {
	t.Helper()
	data, err := json.Marshal(credentials)
	must(t, err)
	return string(data)
}

func assertNoCredentialFile(t *testing.T, store *Store) {
	t.Helper()
	if _, err := os.Lstat(store.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("credential file remains: %v", err)
	}
}

func TestKeyringCredentialLifecycle(t *testing.T) {
	backend := &fakeCredentialKeyring{}
	store := newKeyringTestStore(t, "auto", backend)
	if store.Storage() != "none" {
		t.Fatalf("initial storage = %q", store.Storage())
	}
	want := sampleKeyringCredentials()
	must(t, store.Save(want))
	if store.Storage() != "keyring" {
		t.Fatalf("saved storage = %q", store.Storage())
	}
	assertNoCredentialFile(t, store)
	account := keyringAccount(t, store.Dir())
	if len(backend.sets) != 1 || backend.sets[0] != account {
		t.Fatalf("keyring write namespace = %#v, want %#v", backend.sets, account)
	}
	var saved Credentials
	must(t, json.Unmarshal([]byte(backend.values[account]), &saved))
	if saved != want {
		t.Fatal("keyring did not preserve all credential fields")
	}
	got, err := store.Load()
	must(t, err)
	if got != want || store.Storage() != "keyring" {
		t.Fatal("keyring load did not preserve credentials and storage")
	}
	want.AuthToken = "refreshed-session"
	must(t, store.Save(want))
	got, err = store.Load()
	must(t, err)
	if got != want {
		t.Fatal("keyring refresh did not replace the session")
	}
	must(t, store.Logout())
	if len(backend.values) != 0 {
		t.Fatal("logout retained keyring credentials")
	}
	assertNoCredentialFile(t, store)
	must(t, store.Logout())
}

func TestKeyringMissingCredentials(t *testing.T) {
	store := newKeyringTestStore(t, "auto", &fakeCredentialKeyring{})
	got, err := store.Load()
	must(t, err)
	if got != (Credentials{}) || store.Storage() != "none" {
		t.Fatalf("missing credentials returned a session or storage = %q", store.Storage())
	}
	assertNoCredentialFile(t, store)
}

func TestCredentialStoreSelectionDoesNotAccessSecrets(t *testing.T) {
	for _, mode := range []string{"", "auto", "keyring", "file", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			backend := &fakeCredentialKeyring{}
			store := newKeyringTestStore(t, mode, backend)
			got, err := store.CredentialStore()
			if mode == "invalid" {
				assertFaultCode(t, err, "CONFIG-003")
			} else {
				must(t, err)
				want := mode
				if want == "" {
					want = "auto"
				}
				if got != want {
					t.Fatalf("credential store = %q, want %q", got, want)
				}
			}
			if len(backend.gets)+len(backend.sets)+len(backend.deletes) != 0 {
				t.Fatal("selecting a credential store accessed the backend")
			}
			assertNoCredentialFile(t, store)
		})
	}
}

func TestInvalidCredentialStorePreventsStorageChanges(t *testing.T) {
	backend := &fakeCredentialKeyring{}
	store := newKeyringTestStore(t, "invalid", backend)
	want := sampleKeyringCredentials()
	must(t, store.write("credentials.json", want))
	_, err := store.Load()
	assertFaultCode(t, err, "CONFIG-003")
	assertFaultCode(t, store.Save(Credentials{AuthToken: "replacement"}), "CONFIG-003")
	assertFaultCode(t, store.Logout(), "CONFIG-003")
	var remaining Credentials
	must(t, store.read("credentials.json", &remaining))
	if remaining != want {
		t.Fatal("invalid selection changed existing credentials")
	}
	if len(backend.gets)+len(backend.sets)+len(backend.deletes) != 0 {
		t.Fatal("invalid selection accessed the keyring")
	}
}

func TestKeyringReadFailureIsSanitizedWithoutCreatingFallback(t *testing.T) {
	for _, mode := range []string{"auto", "keyring"} {
		t.Run(mode, func(t *testing.T) {
			backend := &fakeCredentialKeyring{getErr: errors.New("backend error containing secret-do-not-leak")}
			store := newKeyringTestStore(t, mode, backend)
			got, err := store.Load()
			assertFaultCode(t, err, "AUTH-011")
			if got != (Credentials{}) {
				t.Fatal("failed keyring read returned credentials")
			}
			if strings.Contains(err.Error(), "secret-do-not-leak") {
				t.Fatal("read error exposed backend details containing a secret")
			}
			assertNoCredentialFile(t, store)
		})
	}
}

func TestKeyringMigratesPrivateCredentialFile(t *testing.T) {
	backend := &fakeCredentialKeyring{}
	store := newKeyringTestStore(t, "auto", backend)
	want := sampleKeyringCredentials()
	must(t, store.write("credentials.json", want))
	backend.values = map[keyringCall]string{keyringAccount(t, store.Dir()): keyringJSON(t, Credentials{AuthToken: "stale-keyring-session"})}
	got, err := store.Load()
	must(t, err)
	if got != want || store.Storage() != "keyring" {
		t.Fatal("migration did not return the authoritative file credentials")
	}
	assertNoCredentialFile(t, store)
	if len(backend.gets) != 0 {
		t.Fatal("migration consulted stale keyring credentials before the file")
	}
	got, err = store.Load()
	must(t, err)
	if got != want {
		t.Fatal("migration did not replace stale keyring credentials")
	}
}

func TestKeyringRejectsUnsafeFallbackBeforeBackendAccess(t *testing.T) {
	for _, kind := range []string{"permissions", "symlink", "directory", "invalid-json"} {
		t.Run(kind, func(t *testing.T) {
			if kind == "permissions" && runtime.GOOS == "windows" {
				t.Skip("Windows ACL security is tested separately")
			}
			backend := &fakeCredentialKeyring{}
			store := newKeyringTestStore(t, "auto", backend)
			switch kind {
			case "permissions":
				must(t, store.write("credentials.json", sampleKeyringCredentials()))
				must(t, os.Chmod(store.Path(), 0o644))
			case "symlink":
				target := filepath.Join(privateDir(t), "credentials.json")
				must(t, os.WriteFile(target, []byte(keyringJSON(t, sampleKeyringCredentials())), 0o600))
				makeSymlink(t, target, store.Path())
			case "directory":
				must(t, os.Mkdir(store.Path(), 0o700))
			case "invalid-json":
				must(t, os.WriteFile(store.Path(), []byte(`{"authToken":"secret-do-not-leak",`), 0o600))
			}
			_, err := store.Load()
			if err == nil {
				t.Fatal("unsafe fallback was accepted")
			}
			if strings.Contains(err.Error(), "secret-do-not-leak") {
				t.Fatal("migration exposed credential file contents")
			}
			if len(backend.gets)+len(backend.sets)+len(backend.deletes) != 0 {
				t.Fatal("unsafe fallback accessed the keyring before validation")
			}
		})
	}
}

func TestKeyringSaveDoesNotCreateConfigurationDirectory(t *testing.T) {
	backend := &fakeCredentialKeyring{}
	store := newKeyringTestStore(t, "auto", backend)
	store = New(filepath.Join(store.Dir(), "missing"))
	store.keyring = backend
	want := sampleKeyringCredentials()
	must(t, store.Save(want))
	got, err := store.Load()
	must(t, err)
	if got != want {
		t.Fatal("keyring-only storage did not preserve credentials")
	}
	must(t, store.Logout())
	if _, err := os.Stat(store.Dir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("keyring-only operations created a configuration directory")
	}
}

func TestKeyringFailedMigrationRetainsPrivateCredentialFile(t *testing.T) {
	for _, mode := range []string{"auto", "keyring"} {
		t.Run(mode, func(t *testing.T) {
			backend := &fakeCredentialKeyring{setErr: errors.New("keyring unavailable")}
			store := newKeyringTestStore(t, mode, backend)
			want := sampleKeyringCredentials()
			must(t, store.write("credentials.json", want))
			before, err := os.ReadFile(store.Path())
			must(t, err)
			got, err := store.Load()
			if mode == "keyring" {
				if err == nil {
					t.Fatal("strict keyring mode accepted a failed migration")
				}
			} else if err != nil || got != want || store.Storage() != "file" {
				t.Fatalf("fallback migration did not retain usable file credentials: %v", err)
			}
			after, err := os.ReadFile(store.Path())
			must(t, err)
			if string(before) != string(after) {
				t.Fatal("failed migration changed the credential file")
			}
		})
	}
}

func TestKeyringSaveFailureUsesPrivateFileOnlyInAutoMode(t *testing.T) {
	for _, mode := range []string{"auto", "keyring"} {
		t.Run(mode, func(t *testing.T) {
			backend := &fakeCredentialKeyring{setErr: errors.New("keyring unavailable")}
			store := newKeyringTestStore(t, mode, backend)
			want := sampleKeyringCredentials()
			err := store.Save(want)
			if mode == "keyring" {
				if err == nil {
					t.Fatal("strict keyring mode accepted a failed save")
				}
				assertNoCredentialFile(t, store)
				return
			}
			must(t, err)
			if store.Storage() != "file" {
				t.Fatalf("fallback storage = %q", store.Storage())
			}
			info, err := os.Stat(store.Path())
			must(t, err)
			must(t, checkPrivatePermissions(store.Path(), info))
			got, err := store.Load()
			must(t, err)
			if got != want {
				t.Fatal("fallback did not preserve credentials")
			}
		})
	}
}

func TestStrictKeyringOversizedSecretReturnsActionableSanitizedFault(t *testing.T) {
	backend := &fakeCredentialKeyring{setErr: fmt.Errorf("secret-do-not-leak: %w", keyringlib.ErrSetDataTooBig)}
	store := newKeyringTestStore(t, "keyring", backend)
	err := store.Save(sampleKeyringCredentials())
	assertFaultCode(t, err, "AUTH-011")
	var problem *fault.Error
	if !errors.As(err, &problem) {
		t.Fatal("oversized credentials did not return a structured fault")
	}
	if !strings.Contains(problem.Message, "size limit") || !strings.Contains(problem.Hint, "EXPENSIFY_CREDENTIAL_STORE=file") {
		t.Fatal("oversized credential error did not explain the limit and explicit fallback")
	}
	if strings.Contains(err.Error()+problem.Hint, "secret-do-not-leak") {
		t.Fatal("oversized credential error exposed backend details containing a secret")
	}
	assertNoCredentialFile(t, store)
	if store.Storage() != "none" {
		t.Fatal("strict keyring rejection recorded successful credential storage")
	}
}

func TestKeyringSaveRemovesOldPlaintextCredentials(t *testing.T) {
	backend := &fakeCredentialKeyring{}
	store := newKeyringTestStore(t, "auto", backend)
	must(t, store.write("credentials.json", Credentials{AuthToken: "old-file-token"}))
	want := sampleKeyringCredentials()
	must(t, store.Save(want))
	assertNoCredentialFile(t, store)
	got, err := store.Load()
	must(t, err)
	if got != want {
		t.Fatal("save did not replace old plaintext credentials")
	}
}

func TestKeyringFileModeBypassesBackend(t *testing.T) {
	backend := &fakeCredentialKeyring{getErr: errors.New("must not read keyring"), setErr: errors.New("must not write keyring"), deleteErr: errors.New("must not delete keyring")}
	store := newKeyringTestStore(t, "file", backend)
	want := sampleKeyringCredentials()
	must(t, store.Save(want))
	got, err := store.Load()
	must(t, err)
	if got != want || store.Storage() != "file" {
		t.Fatal("file mode failed to preserve credentials and storage")
	}
	must(t, store.Logout())
	assertNoCredentialFile(t, store)
	if len(backend.gets)+len(backend.sets)+len(backend.deletes) != 0 {
		t.Fatal("file mode accessed the OS keyring")
	}
}

func TestKeyringNamespacesFollowConfigurationDirectory(t *testing.T) {
	backend := &fakeCredentialKeyring{}
	first := newKeyringTestStore(t, "auto", backend)
	second := New(privateDir(t))
	second.keyring = backend
	firstCredentials := sampleKeyringCredentials()
	secondCredentials := firstCredentials
	secondCredentials.AuthToken = "second-account-session"
	must(t, first.Save(firstCredentials))
	must(t, second.Save(secondCredentials))
	if len(backend.values) != 2 {
		t.Fatal("configuration directories shared a keyring account")
	}
	t.Chdir(filepath.Dir(first.Dir()))
	wd, err := os.Getwd()
	must(t, err)
	relative, err := filepath.Rel(wd, first.Dir())
	must(t, err)
	alias := New(filepath.Join(relative, "."))
	alias.keyring = backend
	got, err := alias.Load()
	must(t, err)
	if got != firstCredentials {
		t.Fatal("relative and absolute configuration paths did not share their keyring account")
	}
	must(t, first.Logout())
	got, err = second.Load()
	must(t, err)
	if got != secondCredentials {
		t.Fatal("logout removed another configuration directory's credentials")
	}
}

func TestKeyringLogoutDeletesBothStorageLocations(t *testing.T) {
	backend := &fakeCredentialKeyring{}
	store := newKeyringTestStore(t, "auto", backend)
	want := sampleKeyringCredentials()
	must(t, store.Save(want))
	must(t, store.write("credentials.json", want))
	must(t, store.SaveSettings(Settings{Output: "json"}))
	must(t, store.Logout())
	assertNoCredentialFile(t, store)
	if len(backend.values) != 0 {
		t.Fatal("logout retained keyring credentials")
	}
	if _, err := os.Stat(filepath.Join(store.Dir(), "config.json")); err != nil {
		t.Fatal("logout removed nonsecret settings:", err)
	}
}

func TestKeyringLogoutReportsUndeletedSecret(t *testing.T) {
	backend := &fakeCredentialKeyring{}
	store := newKeyringTestStore(t, "auto", backend)
	must(t, store.Save(sampleKeyringCredentials()))
	backend.deleteErr = errors.New("backend leaked device-secret")
	err := store.Logout()
	if err == nil {
		t.Fatal("logout reported success while keyring credentials remain")
	}
	if strings.Contains(err.Error(), "device-secret") {
		t.Fatal("logout exposed a backend error containing a secret")
	}
	if len(backend.values) != 1 {
		t.Fatal("fake backend did not preserve the undeleted secret")
	}
}

func TestKeyringRejectsCorruptCredentialsWithoutExposingContents(t *testing.T) {
	for name, value := range map[string]string{
		"invalid": `{"authToken":"secret-do-not-leak",`,
		"unknown": `{"password":"secret-do-not-leak"}`,
		"null":    `null`,
		"array":   `[]`,
		"empty":   ``,
	} {
		t.Run(name, func(t *testing.T) {
			backend := &fakeCredentialKeyring{}
			store := newKeyringTestStore(t, "auto", backend)
			backend.values = map[keyringCall]string{keyringAccount(t, store.Dir()): value}
			_, err := store.Load()
			if err == nil {
				t.Fatal("corrupt keyring credentials were accepted")
			}
			if strings.Contains(err.Error(), "secret-do-not-leak") {
				t.Fatal("error exposed keyring contents")
			}
			if len(backend.sets)+len(backend.deletes) != 0 {
				t.Fatal("loading corrupt keyring data changed the backend")
			}
			assertNoCredentialFile(t, store)
		})
	}
}

func TestKeyringRejectsIncompleteDeviceCredentials(t *testing.T) {
	backend := &fakeCredentialKeyring{}
	store := newKeyringTestStore(t, "auto", backend)
	assertFaultCode(t, store.Save(Credentials{PartnerUserID: "device-user"}), "AUTH-002")
	if len(backend.sets) != 0 {
		t.Fatal("incomplete credentials reached the keyring")
	}
	backend.values = map[keyringCall]string{keyringAccount(t, store.Dir()): `{"partnerUserSecret":"secret-do-not-leak"}`}
	_, err := store.Load()
	assertFaultCode(t, err, "AUTH-002")
	if strings.Contains(err.Error(), "secret-do-not-leak") {
		t.Fatal("incomplete credential error exposed a secret")
	}
}
