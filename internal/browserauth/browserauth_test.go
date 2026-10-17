package browserauth

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/magarcia/expensify-cli/internal/config"
	"github.com/magarcia/expensify-cli/internal/fault"
)

type fakeProcess struct {
	done    chan struct{}
	once    sync.Once
	stopped bool
}

func (p *fakeProcess) Done() <-chan struct{} { return p.done }
func (p *fakeProcess) Stop() {
	p.stopped = true
	p.once.Do(func() { close(p.done) })
}

type fakeProtocol struct {
	call   func(context.Context, string, any, string, any) error
	closed bool
}

func (p *fakeProtocol) Call(ctx context.Context, method string, params any, session string, result any) error {
	return p.call(ctx, method, params, session, result)
}
func (p *fakeProtocol) Close() { p.closed = true }

func assign(result, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, result)
}

func testDependencies(t *testing.T, connection protocol) (dependencies, *fakeProcess, *string) {
	t.Helper()
	process := &fakeProcess{done: make(chan struct{})}
	var created string
	deps := dependencies{
		resolve: func(string) (string, error) { return "/browser", nil },
		profile: func(parent, prefix string) (string, error) {
			var err error
			created, err = os.MkdirTemp(t.TempDir(), prefix)
			if err != nil {
				return "", err
			}
			if err := os.WriteFile(filepath.Join(created, "DevToolsActivePort"), []byte("12345\n/devtools/browser/test-123\n"), 0o600); err != nil {
				return "", err
			}
			return created, nil
		},
		start: func(string, string) (browserProcess, error) { return process, nil },
		dial: func(context.Context, string) (protocol, error) {
			return connection, nil
		},
		remove: os.RemoveAll,
	}
	return deps, process, &created
}

func signedInProtocol(t *testing.T, credentials config.Credentials, evaluate func() error) *fakeProtocol {
	t.Helper()
	return &fakeProtocol{call: func(ctx context.Context, method string, params any, session string, result any) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		switch method {
		case "Target.createTarget", "Browser.close", "Target.detachFromTarget":
			return nil
		case "Target.getTargets":
			return assign(result, map[string]any{"targetInfos": []target{
				{ID: "foreign", Type: "page", URL: "https://evil.example"},
				{ID: "worker", Type: "service_worker", URL: "https://new.expensify.com"},
				{ID: "expensify", Type: "page", URL: "https://new.expensify.com/home"},
			}})
		case "Target.attachToTarget":
			if params.(map[string]any)["targetId"] != "expensify" {
				t.Errorf("attached to a page outside Expensify: %v", params)
			}
			return assign(result, map[string]string{"sessionId": "owned-page"})
		case "Runtime.evaluate":
			if session != "owned-page" {
				t.Errorf("evaluation was not scoped to owned session: %q", session)
			}
			if evaluate != nil {
				if err := evaluate(); err != nil {
					return err
				}
			}
			return assign(result, map[string]any{"result": map[string]any{"value": credentials}})
		default:
			t.Errorf("unexpected browser command %s", method)
			return errors.New("unexpected command")
		}
	}}
}

func sampleCredentials() config.Credentials {
	return config.Credentials{PartnerUserID: "device-login", PartnerUserSecret: "device-secret", AuthToken: "session-secret", Email: "person@example.com", AccountID: 42}
}

func assertCleanup(t *testing.T, process *fakeProcess, profile string, connection *fakeProtocol) {
	t.Helper()
	if !process.stopped {
		t.Error("owned browser was not stopped")
	}
	if _, err := os.Stat(profile); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("temporary profile still exists: %v", err)
	}
	if connection != nil && !connection.closed {
		t.Error("browser connection was not closed")
	}
}

func TestLoginReturnsCredentialsAndCleansOwnedProfile(t *testing.T) {
	want := sampleCredentials()
	connection := signedInProtocol(t, want, nil)
	deps, process, profile := testDependencies(t, connection)
	got, err := login(context.Background(), Options{Timeout: time.Second}, deps)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Login returned %#v, %v", got, err)
	}
	assertCleanup(t, process, *profile, connection)
}

func TestLoginRetriesNavigationErrors(t *testing.T) {
	queries := 0
	connection := signedInProtocol(t, sampleCredentials(), func() error {
		queries++
		if queries == 1 {
			return &protocolError{}
		}
		return nil
	})
	deps, process, profile := testDependencies(t, connection)
	if _, err := login(context.Background(), Options{Timeout: 2 * time.Second}, deps); err != nil {
		t.Fatal(err)
	}
	if queries != 2 {
		t.Fatalf("got %d evaluation attempts, want 2", queries)
	}
	assertCleanup(t, process, *profile, connection)
}

func TestLoginTimeoutCleansUpWithoutCredentials(t *testing.T) {
	connection := signedInProtocol(t, config.Credentials{}, nil)
	deps, process, profile := testDependencies(t, connection)
	got, err := login(context.Background(), Options{Timeout: 20 * time.Millisecond}, deps)
	var failure *fault.Error
	if !errors.As(err, &failure) || failure.Code != "AUTH-009" || failure.Exit != 3 || !reflect.DeepEqual(got, config.Credentials{}) {
		t.Fatalf("got %#v, %v; want empty credentials and AUTH-009", got, err)
	}
	assertCleanup(t, process, *profile, connection)
}

func TestLoginCancellationCleansUp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	connection := signedInProtocol(t, config.Credentials{}, func() error {
		cancel()
		return ctx.Err()
	})
	deps, process, profile := testDependencies(t, connection)
	if _, err := login(ctx, Options{}, deps); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context cancellation", err)
	}
	assertCleanup(t, process, *profile, connection)
}

func TestLoginSanitizesBrowserErrorsAndCleansUp(t *testing.T) {
	const secret = "secret-from-page-or-browser"
	connection := signedInProtocol(t, sampleCredentials(), func() error { return errors.New(secret) })
	deps, process, profile := testDependencies(t, connection)
	_, err := login(context.Background(), Options{}, deps)
	var failure *fault.Error
	if !errors.As(err, &failure) || failure.Code != "AUTH-008" || strings.Contains(err.Error(), secret) {
		t.Fatalf("unsafe or incorrect browser error: %v", err)
	}
	assertCleanup(t, process, *profile, connection)
}

func TestLoginStartFailureRemovesProfile(t *testing.T) {
	deps, _, profile := testDependencies(t, nil)
	deps.start = func(string, string) (browserProcess, error) { return nil, errors.New("private details") }
	if _, err := login(context.Background(), Options{}, deps); err == nil || strings.Contains(err.Error(), "private details") {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(*profile); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("temporary profile still exists: %v", err)
	}
}

func TestLoginBrowserClosureCleansUp(t *testing.T) {
	connection := signedInProtocol(t, config.Credentials{}, nil)
	deps, process, profile := testDependencies(t, connection)
	deps.start = func(string, string) (browserProcess, error) {
		process.once.Do(func() { close(process.done) })
		return process, nil
	}
	deps.dial = func(ctx context.Context, _ string) (protocol, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	_, err := login(context.Background(), Options{}, deps)
	var failure *fault.Error
	if !errors.As(err, &failure) || failure.Code != "AUTH-008" {
		t.Fatalf("got %v, want AUTH-008", err)
	}
	assertCleanup(t, process, *profile, nil)
}

func TestLoginReportsProfileCleanupFailure(t *testing.T) {
	connection := signedInProtocol(t, sampleCredentials(), nil)
	deps, process, _ := testDependencies(t, connection)
	deps.remove = func(path string) error {
		_ = os.RemoveAll(path)
		return errors.New("private details")
	}
	credentials, err := login(context.Background(), Options{}, deps)
	var failure *fault.Error
	if !errors.As(err, &failure) || failure.Code != "AUTH-008" || !reflect.DeepEqual(credentials, config.Credentials{}) || strings.Contains(err.Error(), "private details") {
		t.Fatalf("cleanup failure returned %#v, %v", credentials, err)
	}
	if !process.stopped || !connection.closed {
		t.Fatal("cleanup failure left browser running")
	}
}

func TestLoginRejectsInvalidOptionsBeforeLaunching(t *testing.T) {
	called := false
	deps := dependencies{resolve: func(string) (string, error) { called = true; return "", nil }}
	if _, err := login(context.Background(), Options{Timeout: -time.Second}, deps); err == nil || called {
		t.Fatalf("invalid timeout reached browser discovery: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := login(ctx, Options{}, deps); !errors.Is(err, context.Canceled) || called {
		t.Fatalf("cancelled invocation reached browser discovery: %v", err)
	}
}

func TestParseEndpointAcceptsOnlyLoopbackBrowserEndpoint(t *testing.T) {
	valid := "12345\n/devtools/browser/ABCD-1234\n"
	got, err := parseEndpoint(valid)
	if err != nil || got != "ws://127.0.0.1:12345/devtools/browser/ABCD-1234" {
		t.Fatalf("got %q, %v", got, err)
	}
	for _, value := range []string{"0\n/devtools/browser/id", "65536\n/devtools/browser/id", "0123\n/devtools/browser/id", "123\nws://evil.example", "123\n/devtools/page/id", "123\n/devtools/browser/id?token=secret", "123\n/devtools/browser/../page", "123\n/devtools/browser/id\nextra"} {
		if endpoint, err := parseEndpoint(value); err == nil {
			t.Errorf("accepted unsafe endpoint %q as %q", value, endpoint)
		}
	}
}

func TestAllowedPageRejectsSpoofedOrigins(t *testing.T) {
	for _, page := range []string{"https://new.expensify.com", "https://new.expensify.com/home", "https://www.expensify.com/home"} {
		if !allowedPage(page) {
			t.Errorf("rejected supported page %q", page)
		}
	}
	for _, page := range []string{"http://new.expensify.com", "https://new.expensify.com.evil.example", "https://new.expensify.com@evil.example", "https://evil.example@new.expensify.com", "https://new.expensify.com:444", "file:///new.expensify.com", "https://expensify.com"} {
		if allowedPage(page) {
			t.Errorf("accepted unsafe page %q", page)
		}
	}
}

func TestReadEndpointRejectsSymlinkAndOversizedFile(t *testing.T) {
	directory := t.TempDir()
	file := filepath.Join(directory, "DevToolsActivePort")
	if err := os.WriteFile(file, []byte(strings.Repeat("x", 4097)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readEndpoint(file); err == nil {
		t.Error("accepted oversized endpoint file")
	}
	link := filepath.Join(directory, "endpoint-link")
	if err := os.Symlink(file, link); err != nil {
		t.Skip("symbolic links unavailable")
	}
	if _, err := readEndpoint(link); err == nil {
		t.Error("accepted symbolic link endpoint file")
	}
}

func TestBrowserArgumentsUseOnlyIsolatedProfile(t *testing.T) {
	args := browserArguments("/private/owned-profile")
	for _, required := range []string{"--user-data-dir=/private/owned-profile", "--remote-debugging-port=0", "--remote-debugging-address=127.0.0.1", "--disable-sync"} {
		if !strings.Contains(strings.Join(args, "\n"), required) {
			t.Errorf("missing %s", required)
		}
	}
	for _, forbidden := range []string{"--no-sandbox", "--profile-directory", "--remote-allow-origins"} {
		if strings.Contains(strings.Join(args, "\n"), forbidden) {
			t.Errorf("unsafe browser option %s", forbidden)
		}
	}
}
