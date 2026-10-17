package integration

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestKeyringLoginThenListExpensesWithUnsetModifiedAmount(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(func() {
		keyring.MockInitWithError(errors.New("native keyring disabled in tests"))
	})
	var commands []string
	apiURL := mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse request: %v", err)
		}
		commands = append(commands, r.URL.Path)
		switch r.URL.Path {
		case "/Authenticate":
			if r.Form.Get("partnerUserID") != "keyring-device-user" || r.Form.Get("partnerUserSecret") != "keyring-device-secret" {
				t.Error("authentication did not use the supplied device credentials")
			}
			respond(w, map[string]any{"jsonCode": 200, "authToken": "keyring-session-token", "email": "test@example.test", "accountID": 42})
		case "/Search":
			if r.Form.Get("authToken") != "keyring-session-token" {
				t.Error("expense list did not load the session from the keyring")
			}
			respond(w, map[string]any{
				"jsonCode": 200,
				"onyxData": []any{map[string]any{
					"onyxMethod": "set",
					"key":        "snapshot_" + r.Form.Get("hash"),
					"value": map[string]any{
						"data": map[string]any{"transactions_101": map[string]any{
							"transactionID": "101", "amount": -1234, "modifiedAmount": "", "currency": "EUR", "merchant": "Cafe", "created": "2026-10-17",
						}},
						"search": map[string]any{"offset": 0, "count": 1, "hasMoreResults": false},
					},
				}},
			})
		default:
			t.Errorf("unexpected API command: %s", r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
	s := newSandbox(t, apiURL, map[string]string{"EXPENSIFY_CREDENTIAL_STORE": "keyring"})
	login := s.runInProcess(`{"partnerUserID":"keyring-device-user","partnerUserSecret":"keyring-device-secret"}`, "auth", "login", "--credentials-stdin", "--output", "json", "--no-input")
	requireSuccess(t, login)
	if value := jsonObject(t, login.stdout); value["storage"] != "keyring" || value["source"] != "stored" || value["path"] != "" || value["authenticated"] != true {
		t.Fatalf("login did not report keyring storage: %s", login.stdout)
	}
	listed := s.runInProcess("", "expense", "list", "--output", "json", "--no-input")
	requireSuccess(t, listed)
	page := jsonObject(t, listed.stdout)
	items, ok := page["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("unexpected expense page: %s", listed.stdout)
	}
	expense, ok := items[0].(map[string]any)
	if !ok || expense["id"] != "101" || expense["amount"] != json.Number("1234") {
		t.Fatalf("unset modified amount did not preserve the expense amount: %s", listed.stdout)
	}
	status := s.runInProcess("", "auth", "status", "--output", "json")
	requireSuccess(t, status)
	if value := jsonObject(t, status.stdout); value["storage"] != "keyring" || value["source"] != "stored" || value["path"] != "" {
		t.Fatalf("status did not retain keyring storage: %s", status.stdout)
	}
	if _, err := os.Stat(filepath.Join(s.configDir, "credentials.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("keyring flow created a plaintext credential file: %v", err)
	}
	for _, got := range []result{login, listed, status} {
		for _, private := range []string{"keyring-device-user", "keyring-device-secret", "keyring-session-token", s.configDir} {
			if strings.Contains(got.stdout+got.stderr, private) {
				t.Fatal("command output exposed credentials or a credential-file path")
			}
		}
	}
	if got := strings.Join(commands, ","); got != "/Authenticate,/Search" {
		t.Fatalf("unexpected API commands: %s", got)
	}
}
