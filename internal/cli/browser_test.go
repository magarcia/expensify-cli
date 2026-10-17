package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/magarcia/expensify-cli/internal/browserauth"
	"github.com/magarcia/expensify-cli/internal/config"
)

type loginTransport func(*http.Request) (*http.Response, error)

func (f loginTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func browserApplication(t *testing.T, args ...string) (*application, *bytes.Buffer) {
	t.Helper()
	t.Setenv("EXPENSIFY_API_URL", "https://api.fixture.test")
	for _, key := range []string{"EXPENSIFY_AUTH_TOKEN", "EXPENSIFY_PARTNER_USER_ID", "EXPENSIFY_PARTNER_USER_SECRET"} {
		t.Setenv(key, "")
	}
	var out, stderr bytes.Buffer
	a := &application{in: strings.NewReader(""), out: &out, errOut: &stderr, values: map[string]*string{}, bools: map[string]*bool{}, store: config.New(filepath.Join(t.TempDir(), "config")), settings: config.DefaultSettings()}
	a.opts.format = "json"
	cmd := findCommand(allCommands(), []string{"auth", "login"})
	a.flags = a.flagSet(cmd)
	if err := a.flags.Parse(args); err != nil {
		t.Fatal(err)
	}
	return a, &out
}

func TestBrowserLoginVerifiesBeforeSaving(t *testing.T) {
	a, out := browserApplication(t, "--browser", "chrome", "--login-timeout", "12s", "--no-input")
	t.Setenv("EXPENSIFY_AUTH_TOKEN", "another-account")
	t.Setenv("EXPENSIFY_API_URL", "https://api.fixture.test")
	called := false
	a.browserLogin = func(ctx context.Context, opts browserauth.Options) (config.Credentials, error) {
		called = true
		if opts.Browser != "chrome" || opts.Timeout != 12*time.Second || opts.ProfileParent != a.store.Dir() {
			t.Fatalf("options: %+v", opts)
		}
		return config.Credentials{AuthToken: "captured-session", PartnerUserID: "captured-device", PartnerUserSecret: "captured-secret", Email: "person@example.test", AccountID: 42}, nil
	}
	previous := http.DefaultTransport
	http.DefaultTransport = loginTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/Authenticate" {
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("partnerUserID") != "captured-device" || r.Form.Get("partnerUserSecret") != "captured-secret" {
			t.Fatal("wrong credential identity")
		}
		if saved, err := a.store.Load(); err != nil || saved.AuthToken != "" {
			t.Fatalf("credentials saved before verification: %v", err)
		}
		recorder := httptest.NewRecorder()
		_, _ = recorder.WriteString(`{"jsonCode":200,"authToken":"verified-session","accountID":42}`)
		return recorder.Result(), nil
	})
	t.Cleanup(func() { http.DefaultTransport = previous })
	if err := a.login(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("browser wasn't opened")
	}
	saved, err := a.store.Load()
	if err != nil || saved.AuthToken != "verified-session" || saved.PartnerUserID != "captured-device" {
		t.Fatalf("wrong saved session: %v", err)
	}
	if !json.Valid(out.Bytes()) || strings.Contains(out.String(), "captured-") || strings.Contains(out.String(), "verified-session") {
		t.Fatalf("output leaked credentials: %s", out.String())
	}
	var result map[string]any
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["storage"] != "file" || result["path"] != a.store.Path() {
		t.Fatalf("login omitted storage location: %#v", result)
	}
}

func TestLoginDoesNotImplicitlyOpenBrowserWithoutTerminal(t *testing.T) {
	a, _ := browserApplication(t, "--no-input")
	a.browserLogin = func(context.Context, browserauth.Options) (config.Credentials, error) {
		t.Fatal("implicit browser launch")
		return config.Credentials{}, nil
	}
	if err := a.login(context.Background()); err == nil {
		t.Fatal("expected missing credentials error")
	}
}

func TestBrowserLoginDoesNotSaveRejectedOrMismatchedCredentials(t *testing.T) {
	for _, response := range []string{`{"jsonCode":401}`, `{"jsonCode":200,"authToken":"other-session","accountID":43}`} {
		t.Run(response, func(t *testing.T) {
			a, _ := browserApplication(t, "--browser", "auto")
			if err := a.store.Save(config.Credentials{AuthToken: "existing-session"}); err != nil {
				t.Fatal(err)
			}
			a.browserLogin = func(context.Context, browserauth.Options) (config.Credentials, error) {
				return config.Credentials{AuthToken: "captured-session", PartnerUserID: "device", PartnerUserSecret: "secret", AccountID: 42}, nil
			}
			previous := http.DefaultTransport
			http.DefaultTransport = loginTransport(func(r *http.Request) (*http.Response, error) {
				recorder := httptest.NewRecorder()
				_, _ = recorder.WriteString(response)
				return recorder.Result(), nil
			})
			t.Cleanup(func() { http.DefaultTransport = previous })
			if err := a.login(context.Background()); err == nil {
				t.Fatal("expected authentication failure")
			}
			saved, err := a.store.Load()
			if err != nil || saved.AuthToken != "existing-session" {
				t.Fatalf("saved session changed on failure: %v", err)
			}
		})
	}
}

func TestBrowserLoginVerifiesIdentityWhenAuthenticateOmitsIt(t *testing.T) {
	for _, tc := range []struct {
		name, response string
		success        bool
	}{
		{"matching", `{"jsonCode":200,"onyxData":[{"onyxMethod":"set","key":"session","value":{"accountID":42}}]}`, true},
		{"different", `{"jsonCode":200,"onyxData":[{"onyxMethod":"set","key":"session","value":{"accountID":43}}]}`, false},
		{"unverified", `{"jsonCode":200,"onyxData":[]}`, false},
		{"merge other field", `{"jsonCode":200,"onyxData":[{"onyxMethod":"set","key":"session","value":{"accountID":42}},{"onyxMethod":"merge","key":"session","value":{"email":"person@example.test"}}]}`, true},
		{"merge deletes identity", `{"jsonCode":200,"onyxData":[{"onyxMethod":"set","key":"session","value":{"accountID":42}},{"onyxMethod":"merge","key":"session","value":{"accountID":null}}]}`, false},
		{"merge replaces identity with string", `{"jsonCode":200,"onyxData":[{"onyxMethod":"set","key":"session","value":{"accountID":42}},{"onyxMethod":"merge","key":"session","value":{"accountID":"42"}}]}`, false},
		{"merge replaces session with scalar", `{"jsonCode":200,"onyxData":[{"onyxMethod":"set","key":"session","value":{"accountID":42}},{"onyxMethod":"merge","key":"session","value":true}]}`, false},
		{"set replaces session", `{"jsonCode":200,"onyxData":[{"onyxMethod":"set","key":"session","value":{"accountID":42}},{"onyxMethod":"set","key":"session","value":{"email":"person@example.test"}}]}`, false},
		{"merge deletes session", `{"jsonCode":200,"onyxData":[{"onyxMethod":"set","key":"session","value":{"accountID":42}},{"onyxMethod":"merge","key":"session","value":null}]}`, false},
		{"unsupported session method", `{"jsonCode":200,"onyxData":[{"onyxMethod":"set","key":"session","value":{"accountID":42}},{"onyxMethod":"unknown","key":"session","value":{"accountID":42}}]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := browserApplication(t, "--browser", "auto")
			if err := a.store.Save(config.Credentials{AuthToken: "existing-session"}); err != nil {
				t.Fatal(err)
			}
			a.browserLogin = func(context.Context, browserauth.Options) (config.Credentials, error) {
				return config.Credentials{AuthToken: "captured", PartnerUserID: "device", PartnerUserSecret: "secret", Email: "person@example.test", AccountID: 42}, nil
			}
			previous := http.DefaultTransport
			http.DefaultTransport = loginTransport(func(r *http.Request) (*http.Response, error) {
				recorder := httptest.NewRecorder()
				switch r.URL.Path {
				case "/Authenticate":
					_, _ = recorder.WriteString(`{"jsonCode":200,"authToken":"verified"}`)
				case "/OpenApp":
					_, _ = recorder.WriteString(tc.response)
				default:
					t.Fatalf("unexpected request %s", r.URL.Path)
				}
				return recorder.Result(), nil
			})
			t.Cleanup(func() { http.DefaultTransport = previous })
			err := a.login(context.Background())
			if (err == nil) != tc.success {
				t.Fatalf("success=%v: %v", tc.success, err)
			}
			saved, err := a.store.Load()
			if err != nil {
				t.Fatal(err)
			}
			if tc.success && (saved.AccountID != 42 || saved.AuthToken != "verified") {
				t.Fatal("verified identity wasn't saved")
			}
			if !tc.success && saved.AuthToken != "existing-session" {
				t.Fatal("saved session changed after identity verification failed")
			}
		})
	}
}

func TestBrowserLoginDryRunAndInvalidOptions(t *testing.T) {
	for _, args := range [][]string{
		{"--browser", "chrome", "--manual"},
		{"--browser", "chrome", "--credentials-stdin"},
		{"--manual", "--credentials-file", "device.json"},
		{"--login-timeout", "0s"},
		{"--login-timeout", "forever"},
		{"--browser", ""},
	} {
		a, _ := browserApplication(t, args...)
		a.browserLogin = func(context.Context, browserauth.Options) (config.Credentials, error) {
			t.Fatal("browser launched before validation")
			return config.Credentials{}, nil
		}
		if err := a.login(context.Background()); err == nil {
			t.Fatalf("expected validation error: %v", args)
		}
	}
	a, out := browserApplication(t, "--browser", "chrome", "--dry-run")
	a.browserLogin = func(context.Context, browserauth.Options) (config.Credentials, error) {
		t.Fatal("browser launched during dry run")
		return config.Credentials{}, nil
	}
	if err := a.login(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"mode": "browser"`) {
		t.Fatal(out.String())
	}
}
