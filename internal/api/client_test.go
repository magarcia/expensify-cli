package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/magarcia/expensify-cli/internal/config"
	"github.com/magarcia/expensify-cli/internal/fault"
)

func testClient(t *testing.T, handler http.HandlerFunc, credentials config.Credentials, save func(config.Credentials) error) *Client {
	t.Helper()
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		defer request.Body.Close()
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response.Result(), nil
	})
	client, err := New(Options{BaseURL: "https://expensify.test/api/", HTTPClient: &http.Client{Transport: transport}, Credentials: credentials, SaveCredentials: save})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func errorCode(t *testing.T, err error, want string) {
	t.Helper()
	var failure *fault.Error
	if !errors.As(err, &failure) || failure.Code != want {
		t.Fatalf("error = %v, want code %s", err, want)
	}
}

func TestCallWireAndPrecision(t *testing.T) {
	t.Parallel()
	params := url.Values{"reportID": {"9223372036854775807"}, "authToken": {"override"}, "platform": {"other"}, "tag": {"a", "b"}}
	wantParams := params.Encode()
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/OpenReport" || r.URL.RawQuery != "" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Errorf("content type = %s", r.Header.Get("Content-Type"))
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		for key, want := range map[string]string{"authToken": "session-secret", "platform": "web", "referer": "ecash", "api_setCookie": "false", "doNotRetry": "true", "reportID": "9223372036854775807"} {
			if got := r.PostForm.Get(key); got != want {
				t.Errorf("%s = %q, want %q", key, got, want)
			}
		}
		if !reflect.DeepEqual(r.PostForm["tag"], []string{"a", "b"}) {
			t.Errorf("repeated values = %v", r.PostForm["tag"])
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("unexpected Authorization header")
		}
		io.WriteString(w, `{"jsonCode":"200","reportID":9223372036854775807}`)
	}, config.Credentials{AuthToken: "session-secret"}, nil)
	data, err := client.Call(context.Background(), "OpenReport", params)
	if err != nil {
		t.Fatal(err)
	}
	if data["reportID"] != json.Number("9223372036854775807") {
		t.Errorf("reportID lost precision: %#v", data["reportID"])
	}
	if params.Encode() != wantParams {
		t.Error("caller params were mutated")
	}
}

func TestAuthenticateDeviceAndSave(t *testing.T) {
	t.Parallel()
	var saved config.Credentials
	var calls int
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/Authenticate" {
			t.Errorf("path = %s", r.URL.Path)
		}
		r.ParseForm()
		for key, want := range map[string]string{"partnerName": partnerName, "partnerPassword": partnerPassword, "partnerUserID": "device-id", "partnerUserSecret": "device-secret", "useExpensifyLogin": "false", "authToken": ""} {
			if r.PostForm.Get(key) != want {
				t.Errorf("wrong %s", key)
			}
		}
		io.WriteString(w, `{"jsonCode":200,"authToken":"new-secret","email":"member@example.com","accountID":9223372036854775806}`)
	}, config.Credentials{AuthToken: "old-secret", PartnerUserID: "device-id", PartnerUserSecret: "device-secret"}, func(value config.Credentials) error { saved = value; return nil })
	got, err := client.Authenticate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || got.AuthToken != "new-secret" || got.Email != "member@example.com" || got.AccountID != 9223372036854775806 {
		t.Fatalf("unexpected credentials or calls: %#v, %d", got, calls)
	}
	if !reflect.DeepEqual(got, saved) || !reflect.DeepEqual(got, client.Credentials()) {
		t.Error("credentials were not retained and saved")
	}
}

func TestExpiredSessionRenewedOnce(t *testing.T) {
	t.Parallel()
	for _, secondCode := range []int{200, 407} {
		t.Run(fmt.Sprint(secondCode), func(t *testing.T) {
			var commands, auths, saves int
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				r.ParseForm()
				if r.URL.Path == "/api/Authenticate" {
					auths++
					io.WriteString(w, `{"jsonCode":200,"authToken":"new-token"}`)
					return
				}
				commands++
				if commands == 1 {
					if r.PostForm.Get("authToken") != "old-token" {
						t.Error("initial token incorrect")
					}
					io.WriteString(w, `{"jsonCode":407}`)
					return
				}
				if r.PostForm.Get("authToken") != "new-token" {
					t.Error("renewed token not used")
				}
				fmt.Fprintf(w, `{"jsonCode":%d}`, secondCode)
			}, config.Credentials{AuthToken: "old-token", PartnerUserID: "device", PartnerUserSecret: "secret"}, func(config.Credentials) error { saves++; return nil })
			_, err := client.Call(context.Background(), "DeleteMoneyRequest", url.Values{"transactionID": {"123"}})
			if secondCode == 200 && err != nil {
				t.Fatal(err)
			}
			if secondCode == 407 {
				errorCode(t, err, "AUTH-002")
			}
			if commands != 2 || auths != 1 || saves != 1 {
				t.Errorf("commands/auths/saves = %d/%d/%d", commands, auths, saves)
			}
		})
	}
}

func TestMissingTokenAuthenticatesBeforeCommand(t *testing.T) {
	t.Parallel()
	var paths []string
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/api/Authenticate" {
			io.WriteString(w, `{"jsonCode":200,"authToken":"new-token"}`)
			return
		}
		io.WriteString(w, `{"jsonCode":200}`)
	}, config.Credentials{PartnerUserID: "device", PartnerUserSecret: "secret"}, nil)
	if _, err := client.Call(context.Background(), "OpenApp", nil); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(paths, []string{"/api/Authenticate", "/api/OpenApp"}) {
		t.Errorf("paths = %v", paths)
	}
}

func TestMissingCredentialsAndTokenOnly(t *testing.T) {
	t.Parallel()
	var calls int
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) { calls++; io.WriteString(w, `{"jsonCode":407}`) }, config.Credentials{}, nil)
	_, err := client.Call(context.Background(), "OpenApp", nil)
	errorCode(t, err, "AUTH-001")
	if calls != 0 {
		t.Errorf("missing credentials sent %d requests", calls)
	}
	client.credentials = config.Credentials{AuthToken: "token-only"}
	_, err = client.Call(context.Background(), "OpenApp", nil)
	errorCode(t, err, "AUTH-001")
	if calls != 1 {
		t.Errorf("token-only sent %d requests", calls)
	}
}

func TestFailedSavePreventsWriteReplay(t *testing.T) {
	t.Parallel()
	var commands int
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/Authenticate" {
			io.WriteString(w, `{"jsonCode":200,"authToken":"new-token"}`)
			return
		}
		commands++
		io.WriteString(w, `{"jsonCode":407}`)
	}, config.Credentials{AuthToken: "old-token", PartnerUserID: "device", PartnerUserSecret: "secret"}, func(config.Credentials) error { return errors.New("SECRET file path") })
	_, err := client.Call(context.Background(), "DeleteMoneyRequest", nil)
	errorCode(t, err, "AUTH-005")
	if strings.Contains(err.Error(), "SECRET") {
		t.Error("save error leaked")
	}
	if commands != 1 {
		t.Errorf("write replayed after failed save: %d", commands)
	}
}

func TestAuthenticationRejectsMissingToken(t *testing.T) {
	t.Parallel()
	for _, body := range []string{`{"jsonCode":200}`, `{"jsonCode":200,"authToken":" "}`} {
		client := testClient(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }, config.Credentials{PartnerUserID: "id", PartnerUserSecret: "secret"}, nil)
		_, err := client.Authenticate(context.Background())
		errorCode(t, err, "AUTH-004")
	}
}

func TestResponseFailuresAreRedactedAndNotRetried(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		status     int
		body, code string
	}{
		{"html", 200, `<html>SECRET</html>`, "API-002"},
		{"null", 200, `null`, "API-002"},
		{"array", 200, `[]`, "API-002"},
		{"trailing", 200, `{"jsonCode":200} SECRET`, "API-002"},
		{"missing code", 200, `{"message":"SECRET"}`, "API-002"},
		{"bad code", 200, `{"jsonCode":200.2,"message":"SECRET"}`, "API-002"},
		{"server", 503, `SECRET`, "API-HTTP"},
		{"unauthorized", 401, `SECRET`, "AUTH-002"},
		{"forbidden", 403, `SECRET`, "AUTH-003"},
		{"not found", 404, `SECRET`, "API-404"},
		{"proxy authentication", 407, `SECRET`, "API-HTTP"},
		{"rate limited", 429, `SECRET`, "API-429"},
		{"api rate limited", 200, `{"jsonCode":429,"message":"SECRET"}`, "API-429"},
		{"api forbidden", 200, `{"jsonCode":403,"message":"SECRET"}`, "AUTH-003"},
		{"api not found", 200, `{"jsonCode":404,"message":"SECRET"}`, "API-404"},
		{"api failure", 200, `{"jsonCode":500,"message":"SECRET"}`, "API-001"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls int
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(test.status)
				io.WriteString(w, test.body)
			}, config.Credentials{AuthToken: "token"}, nil)
			_, err := client.Call(context.Background(), "DeleteMoneyRequest", nil)
			errorCode(t, err, test.code)
			if strings.Contains(err.Error(), "SECRET") {
				t.Error("response text leaked")
			}
			if calls != 1 {
				t.Errorf("ambiguous write sent %d requests", calls)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTransportErrorsRedactedAndNotRetried(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		err  error
		code string
	}{
		{"network", errors.New("SECRET URL and request"), "NET-001"},
		{"timeout", context.DeadlineExceeded, "NET-002"},
		{"canceled", context.Canceled, "NET-003"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls int
			client, err := New(Options{Credentials: config.Credentials{AuthToken: "SECRET"}, HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { calls++; return nil, test.err })}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Call(context.Background(), "DeleteMoneyRequest", nil)
			errorCode(t, err, test.code)
			if calls != 1 || strings.Contains(err.Error(), "SECRET") {
				t.Errorf("calls = %d, error = %v", calls, err)
			}
		})
	}
}

func TestResponseSizeLimit(t *testing.T) {
	t.Parallel()
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, strings.Repeat("x", maxResponseBytes+1))
	}, config.Credentials{AuthToken: "token"}, nil)
	_, err := client.Call(context.Background(), "OpenApp", nil)
	errorCode(t, err, "API-003")
}

func TestBaseURLValidation(t *testing.T) {
	t.Parallel()
	for _, base := range []string{"http://example.com/api", "ftp://example.com", "https://user:SECRET@example.com/api", "https://example.com/api?token=SECRET", "https://example.com/#SECRET", "relative/api", "http://localhost.example.com", "https://example.com/%2fapi", "https://"} {
		_, err := New(Options{BaseURL: base})
		errorCode(t, err, "CONFIG-003")
		if strings.Contains(err.Error(), "SECRET") {
			t.Error("base URL leaked")
		}
	}
	for _, base := range []string{"", "https://example.com/api", "http://localhost:8080/api", "http://127.0.0.1/api", "http://[::1]/api"} {
		if _, err := New(Options{BaseURL: base}); err != nil {
			t.Errorf("base %q: %v", base, err)
		}
	}
}

func TestTransportAndTimeoutConfiguration(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name          string
		timeout, want time.Duration
	}{
		{"default", 0, 30 * time.Second},
		{"long override", 2 * time.Minute, 2 * time.Minute},
		{"short override", time.Second, time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			provided := &http.Client{Timeout: test.timeout}
			client, err := New(Options{HTTPClient: provided})
			if err != nil {
				t.Fatal(err)
			}
			if client.httpClient.Timeout != test.want {
				t.Errorf("timeout = %s, want %s", client.httpClient.Timeout, test.want)
			}
			transport, ok := client.httpClient.Transport.(*http.Transport)
			if !ok || transport == http.DefaultTransport || transport.DialContext == nil || transport.TLSHandshakeTimeout != 5*time.Second {
				t.Error("safe transport defaults were not configured")
			}
			if provided.Timeout != test.timeout || provided.Transport != nil || provided.CheckRedirect != nil {
				t.Error("provided client was mutated")
			}
		})
	}
}

func TestCustomDefaultTransportIsPreserved(t *testing.T) {
	original := http.DefaultTransport
	defer func() { http.DefaultTransport = original }()
	var calls int
	http.DefaultTransport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"jsonCode":200}`)), Header: make(http.Header)}, nil
	})
	client, err := New(Options{Credentials: config.Credentials{AuthToken: "token"}, HTTPClient: &http.Client{Timeout: 2 * time.Minute}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Call(context.Background(), "OpenApp", nil); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("custom transport calls = %d", calls)
	}
}

func TestExitCodes(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ code, exit int }{{401, 3}, {407, 3}, {403, 5}, {404, 4}, {429, 10}, {500, 1}} {
		failure := apiError(test.code).(*fault.Error)
		if failure.Exit != test.exit {
			t.Errorf("jsonCode %d: exit %d, want %d", test.code, failure.Exit, test.exit)
		}
	}
	if invalidResponse().(*fault.Error).Exit != 1 {
		t.Error("invalid response must use general failure exit 1")
	}
}

func TestRejectRedirect(t *testing.T) {
	t.Parallel()
	var leaked atomic.Int32
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Hostname() == "leak.example" {
			leaked.Add(1)
			io.WriteString(w, `{"jsonCode":200}`)
			return
		}
		http.Redirect(w, r, "https://leak.example/api/OpenApp", http.StatusTemporaryRedirect)
	}, config.Credentials{AuthToken: "SECRET"}, nil)
	_, err := client.Call(context.Background(), "OpenApp", nil)
	errorCode(t, err, "API-HTTP")
	if leaked.Load() != 0 {
		t.Fatal("redirect followed with credentials")
	}
}

func TestInvalidCommandDoesNotRequest(t *testing.T) {
	t.Parallel()
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected request") }, config.Credentials{AuthToken: "token"}, nil)
	for _, command := range []string{"", "../Authenticate", "Open/App", "123", "Open App", "Open_App", "OpenÄpp"} {
		_, err := client.Call(context.Background(), command, nil)
		errorCode(t, err, "CLI-001")
	}
}

func receiptFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestUploadMultipartAndSessionReplay(t *testing.T) {
	t.Parallel()
	for _, fileType := range []struct {
		name, contentType string
		data              []byte
	}{
		{"receipt.pdf", "application/pdf", []byte("%PDF-1.7\nreceipt bytes\n")},
		{"receipt.JPG", "image/jpeg", []byte{0xff, 0xd8, 0xff, 0xe0, 0, 1, 2, 3}},
		{"receipt.png", "image/png", []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 1, 2, 3}},
	} {
		t.Run(fileType.name, func(t *testing.T) {
			path := receiptFile(t, fileType.name, fileType.data)
			var uploads, auths int
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/Authenticate" {
					auths++
					io.WriteString(w, `{"jsonCode":200,"authToken":"fresh"}`)
					return
				}
				uploads++
				if r.URL.Path != "/api/ReplaceReceipt" {
					t.Errorf("path = %s", r.URL.Path)
				}
				if err := r.ParseMultipartForm(1 << 20); err != nil {
					t.Error(err)
					return
				}
				defer r.MultipartForm.RemoveAll()
				wantToken := "old"
				if uploads > 1 {
					wantToken = "fresh"
				}
				for key, want := range map[string]string{"authToken": wantToken, "transactionID": "123", "platform": "web", "doNotRetry": "true"} {
					if r.FormValue(key) != want {
						t.Errorf("%s = %q, want %q", key, r.FormValue(key), want)
					}
				}
				file, header, err := r.FormFile("receipt")
				if err != nil {
					t.Error(err)
					return
				}
				defer file.Close()
				data, _ := io.ReadAll(file)
				if string(data) != string(fileType.data) || header.Filename != fileType.name || header.Header.Get("Content-Type") != fileType.contentType {
					t.Errorf("unexpected multipart file: %s %s %x", header.Filename, header.Header.Get("Content-Type"), data)
				}
				if uploads == 1 {
					io.WriteString(w, `{"jsonCode":407}`)
					return
				}
				io.WriteString(w, `{"jsonCode":200}`)
			}, config.Credentials{AuthToken: "old", PartnerUserID: "device", PartnerUserSecret: "secret"}, nil)
			if _, err := client.Upload(context.Background(), "ReplaceReceipt", url.Values{"transactionID": {"123"}}, "receipt", path); err != nil {
				t.Fatal(err)
			}
			if uploads != 2 || auths != 1 {
				t.Errorf("uploads/auths = %d/%d", uploads, auths)
			}
		})
	}
}

func TestUploadValidationBeforeAuthentication(t *testing.T) {
	t.Parallel()
	tooLarge := receiptFile(t, "huge.pdf", []byte("%PDF-1.7\n"))
	if err := os.Truncate(tooLarge, maxUploadBytes+1); err != nil {
		t.Fatal(err)
	}
	paths := []string{
		filepath.Join(t.TempDir(), "missing.pdf"),
		t.TempDir(),
		receiptFile(t, "empty.pdf", nil),
		receiptFile(t, "text.pdf", []byte("not a receipt")),
		receiptFile(t, "mismatch.png", []byte("%PDF-1.7\n")),
		receiptFile(t, "receipt.svg", []byte("<svg/>")),
		tooLarge,
	}
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("invalid file triggered network request") }, config.Credentials{PartnerUserID: "device", PartnerUserSecret: "secret"}, nil)
	for _, path := range paths {
		_, err := client.Upload(context.Background(), "ReplaceReceipt", nil, "receipt", path)
		errorCode(t, err, "FILE-001")
	}
}

func TestUploadRejectsChangedFileBeforeReplay(t *testing.T) {
	t.Parallel()
	path := receiptFile(t, "receipt.pdf", []byte("%PDF-1.7\noriginal receipt"))
	var uploads int
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/Authenticate" {
			if err := os.WriteFile(path, []byte("%PDF-1.7\nreplacement receipt is longer"), 0600); err != nil {
				t.Error(err)
			}
			io.WriteString(w, `{"jsonCode":200,"authToken":"fresh"}`)
			return
		}
		uploads++
		io.Copy(io.Discard, r.Body)
		io.WriteString(w, `{"jsonCode":407}`)
	}, config.Credentials{AuthToken: "old", PartnerUserID: "device", PartnerUserSecret: "secret"}, nil)
	_, err := client.Upload(context.Background(), "ReplaceReceipt", nil, "receipt", path)
	errorCode(t, err, "FILE-002")
	if uploads != 1 {
		t.Errorf("changed file sent %d uploads", uploads)
	}
}

func TestConcurrentExpiryRenewsOnlyOnce(t *testing.T) {
	t.Parallel()
	var expired atomic.Int32
	var auths atomic.Int32
	allExpired := make(chan struct{})
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.URL.Path == "/api/Authenticate" {
			auths.Add(1)
			io.WriteString(w, `{"jsonCode":200,"authToken":"fresh"}`)
			return
		}
		if r.PostForm.Get("authToken") == "old" {
			if expired.Add(1) == 4 {
				close(allExpired)
			}
			select {
			case <-allExpired:
			case <-time.After(2 * time.Second):
				t.Error("concurrent requests did not arrive")
			}
			io.WriteString(w, `{"jsonCode":407}`)
			return
		}
		io.WriteString(w, `{"jsonCode":200}`)
	}, config.Credentials{AuthToken: "old", PartnerUserID: "device", PartnerUserSecret: "secret"}, nil)
	var group sync.WaitGroup
	for range 4 {
		group.Go(func() {
			if _, err := client.Call(context.Background(), "OpenApp", nil); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
	if auths.Load() != 1 {
		t.Errorf("renewal calls = %d", auths.Load())
	}
}
