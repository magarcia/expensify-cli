package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/magarcia/expensify-cli/internal/cli"
)

var executable string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "expensify-integration-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	executable = filepath.Join(dir, "expensify")
	if runtime.GOOS == "windows" {
		executable += ".exe"
	}
	build := exec.Command("go", "build", "-o", executable, "./cmd/expensify")
	build.Dir = ".."
	if output, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build CLI: %v\n%s", err, output)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type result struct {
	stdout, stderr string
	code           int
}

type sandbox struct {
	t         *testing.T
	dir       string
	configDir string
	env       []string
}

func newSandbox(t *testing.T, apiURL string, overrides map[string]string) *sandbox {
	t.Helper()
	dir := t.TempDir()
	values := map[string]string{
		"HOME":                       filepath.Join(dir, "home"),
		"XDG_CONFIG_HOME":            filepath.Join(dir, "xdg"),
		"EXPENSIFY_CONFIG_DIR":       filepath.Join(dir, "private"),
		"EXPENSIFY_CREDENTIAL_STORE": "file",
		"EXPENSIFY_API_URL":          apiURL,
		"NO_COLOR":                   "1",
		"TERM":                       "dumb",
	}
	for key, value := range overrides {
		values[key] = value
	}
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "EXPENSIFY_") || key == "HOME" || strings.HasPrefix(key, "XDG_") || key == "NO_COLOR" || key == "TERM" {
			continue
		}
		env = append(env, entry)
	}
	for key, value := range values {
		env = append(env, key+"="+value)
	}
	return &sandbox{t: t, dir: dir, configDir: values["EXPENSIFY_CONFIG_DIR"], env: env}
}

func (s *sandbox) command(ctx context.Context, input string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Env = s.env
	cmd.Dir = s.dir
	cmd.Stdin = strings.NewReader(input)
	return cmd
}

func (s *sandbox) run(input string, args ...string) result {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := s.command(ctx, input, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		s.t.Fatalf("CLI timed out: %v", args)
	}
	code := 0
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		} else {
			s.t.Fatalf("run CLI: %v", err)
		}
	}
	return result{stdout: stdout.String(), stderr: stderr.String(), code: code}
}

func (s *sandbox) runInProcess(input string, args ...string) result {
	s.t.Helper()
	for _, entry := range s.env {
		key, value, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "EXPENSIFY_") || key == "HOME" || strings.HasPrefix(key, "XDG_") || key == "NO_COLOR" || key == "TERM" {
			s.t.Setenv(key, value)
		}
	}
	for _, key := range []string{"EXPENSIFY_AUTH_TOKEN", "EXPENSIFY_PARTNER_USER_ID", "EXPENSIFY_PARTNER_USER_SECRET"} {
		present := false
		for _, entry := range s.env {
			present = present || strings.HasPrefix(entry, key+"=")
		}
		if !present {
			s.t.Setenv(key, "")
		}
	}
	var stdout, stderr bytes.Buffer
	code := cli.Run(context.Background(), args, strings.NewReader(input), &stdout, &stderr, "test", "test", "test")
	return result{stdout: stdout.String(), stderr: stderr.String(), code: code}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Exercise CLI, API, and storage integration without requiring a listening socket.
func mockAPI(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	previous := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "fixture.expensify.test" {
			t.Fatalf("unexpected destination: %s", r.URL.Host)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, r)
		return recorder.Result(), nil
	})
	t.Cleanup(func() { http.DefaultTransport = previous })
	return "https://fixture.expensify.test"
}

func requireSuccess(t *testing.T, got result) {
	t.Helper()
	if got.code != 0 || got.stderr != "" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", got.code, got.stdout, got.stderr)
	}
}

func jsonObject(t *testing.T, value string) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.UseNumber()
	var decoded map[string]any
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatalf("invalid JSON output %q: %v", value, err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		t.Fatalf("trailing non-JSON output %q: %v", value, err)
	}
	return decoded
}

func respond(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func snapshot(r *http.Request) map[string]any {
	response := map[string]any{
		"jsonCode": 200,
		"onyxData": []any{
			map[string]any{"onyxMethod": "set", "key": "transactions_101", "value": map[string]any{"transactionID": "101", "reportID": "201", "amount": -1234, "currency": "EUR", "merchant": "Cafe", "created": "2026-10-17"}},
			map[string]any{"onyxMethod": "set", "key": "report_201", "value": map[string]any{"reportID": "201", "reportName": "October", "type": "expense", "total": 1234, "currency": "EUR"}},
			map[string]any{"onyxMethod": "set", "key": "selfDMReportID", "value": "301"},
			map[string]any{"onyxMethod": "set", "key": "policy_ABC", "value": map[string]any{"id": "ABC", "name": "Team", "autoReporting": false}},
			map[string]any{"onyxMethod": "set", "key": "reportActions_201", "value": map[string]any{"401": map[string]any{"reportActionID": "401", "originalMessage": map[string]any{"IOUTransactionID": "101"}}}},
		},
	}
	if r.URL.Path == "/Search" {
		query := jsonObjectValue(r.Form.Get("jsonQuery"))
		data := map[string]any{}
		for _, raw := range response["onyxData"].([]any) {
			patch := raw.(map[string]any)
			data[patch["key"].(string)] = patch["value"]
		}
		response["onyxData"] = []any{map[string]any{"onyxMethod": "set", "key": "snapshot_" + r.Form.Get("hash"), "value": map[string]any{"data": data, "search": map[string]any{"hasMoreResults": false, "offset": query["offset"]}}}}
	}
	return response
}

func jsonObjectValue(value string) map[string]any {
	var result map[string]any
	_ = json.Unmarshal([]byte(value), &result)
	return result
}

func TestLoginRenewsAndPersistsSessionAcrossInvocations(t *testing.T) {
	var mu sync.Mutex
	var tokens []string
	var calls []string
	var authCount int
	apiURL := mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse request: %v", err)
		}
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, r.URL.Path)
		if r.URL.Path == "/Authenticate" {
			if r.Form.Get("partnerUserID") != "device-user" || r.Form.Get("partnerUserSecret") != "device-secret" || r.Form.Get("useExpensifyLogin") != "false" {
				t.Error("authentication did not use the supplied device credentials")
			}
			authCount++
			respond(w, map[string]any{"jsonCode": 200, "authToken": fmt.Sprintf("session-%d", authCount), "email": "test@example.test", "accountID": 42})
			return
		}
		token := r.Form.Get("authToken")
		tokens = append(tokens, token)
		if token == "session-1" {
			respond(w, map[string]any{"jsonCode": 407})
			return
		}
		respond(w, snapshot(r))
	})
	s := newSandbox(t, apiURL, nil)
	login := s.runInProcess(`{"partnerUserID":"device-user","partnerUserSecret":"device-secret"}`, "auth", "login", "--credentials-stdin", "--output", "json", "--no-input")
	requireSuccess(t, login)
	loginResult := jsonObject(t, login.stdout)
	if loginResult["source"] != "stored" || loginResult["storage"] != "file" || loginResult["path"] != filepath.Join(s.configDir, "credentials.json") {
		t.Fatalf("login omitted storage location: %s", login.stdout)
	}
	for _, secret := range []string{"device-user", "device-secret", "session-1"} {
		if strings.Contains(login.stdout+login.stderr, secret) {
			t.Fatalf("login output revealed credential %q", secret)
		}
	}
	for path, perm := range map[string]os.FileMode{s.configDir: 0o700, filepath.Join(s.configDir, "credentials.json"): 0o600} {
		info, err := os.Stat(path)
		if err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != perm {
			t.Fatalf("private storage %s: info=%v err=%v", path, info, err)
		}
	}
	mu.Lock()
	before := len(calls)
	mu.Unlock()
	status := s.runInProcess("", "auth", "status", "--output", "json")
	requireSuccess(t, status)
	if value := jsonObject(t, status.stdout); value["storage"] != "file" || value["source"] != "stored" || value["path"] != filepath.Join(s.configDir, "credentials.json") {
		t.Fatalf("status omitted storage location: %s", status.stdout)
	}
	mu.Lock()
	if len(calls) != before {
		t.Error("offline auth status made a network request")
	}
	mu.Unlock()
	for i := 0; i < 2; i++ {
		got := s.runInProcess("", "expense", "list", "--limit", "1", "--output", "json", "--no-input")
		requireSuccess(t, got)
		page := jsonObject(t, got.stdout)
		items, ok := page["items"].([]any)
		if !ok || len(items) != 1 || page["limit"] != json.Number("1") || page["offset"] != json.Number("0") || page["has_more"] != false {
			t.Fatalf("unexpected expense page: %s", got.stdout)
		}
		expense := items[0].(map[string]any)
		if expense["id"] != "101" || expense["amount"] != json.Number("1234") || expense["merchant"] != "Cafe" {
			t.Fatalf("unexpected expense: %#v", expense)
		}
	}
	mu.Lock()
	if authCount != 2 || strings.Join(tokens, ",") != "session-1,session-2,session-2" {
		t.Errorf("refresh/replay was not persisted: auth=%d tokens=%v", authCount, tokens)
	}
	mu.Unlock()
	stored, err := os.ReadFile(filepath.Join(s.configDir, "credentials.json"))
	if err != nil || jsonObject(t, string(stored))["authToken"] != "session-2" {
		t.Fatalf("refreshed token not saved: %v", err)
	}
	requireSuccess(t, s.runInProcess("", "auth", "status", "--check", "--output", "json"))
	requireSuccess(t, s.runInProcess("", "auth", "logout", "--output", "json"))
	if _, err := os.Stat(filepath.Join(s.configDir, "credentials.json")); !os.IsNotExist(err) {
		t.Fatalf("logout left credentials: %v", err)
	}
}

func TestEnvironmentCredentialsAreNeverPersisted(t *testing.T) {
	var tokens []string
	var mu sync.Mutex
	apiURL := mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.URL.Path == "/Authenticate" {
			respond(w, map[string]any{"jsonCode": 200, "authToken": "environment-renewed"})
			return
		}
		mu.Lock()
		tokens = append(tokens, r.Form.Get("authToken"))
		mu.Unlock()
		if r.Form.Get("authToken") == "environment-expired" {
			respond(w, map[string]any{"jsonCode": 407})
			return
		}
		respond(w, snapshot(r))
	})
	s := newSandbox(t, apiURL, map[string]string{"EXPENSIFY_AUTH_TOKEN": "environment-expired", "EXPENSIFY_PARTNER_USER_ID": "environment-user", "EXPENSIFY_PARTNER_USER_SECRET": "environment-secret"})
	got := s.runInProcess("", "expense", "list", "--output", "json")
	requireSuccess(t, got)
	if _, err := os.Stat(s.configDir); !os.IsNotExist(err) {
		t.Fatalf("environment credentials created persistent configuration: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(tokens, ",") != "environment-expired,environment-renewed" {
		t.Fatalf("environment session did not renew: %v", tokens)
	}
}

func TestResourceCommandsPreserveInputs(t *testing.T) {
	var commands []string
	var updates []map[string]any
	var receipt []byte
	apiURL := mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		commands = append(commands, r.URL.Path)
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Fatalf("parse receipt: %v", err)
			}
			defer r.MultipartForm.RemoveAll()
			file, _, err := r.FormFile("receipt")
			if err != nil {
				t.Fatalf("read receipt: %v", err)
			}
			defer file.Close()
			var data bytes.Buffer
			_, _ = data.ReadFrom(file)
			receipt = data.Bytes()
		} else if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		if r.Form.Get("authToken") != "session-token" {
			t.Error("resource command omitted authentication")
		}
		switch r.URL.Path {
		case "/TrackExpense":
			if r.Form.Get("amount") != "1234" || r.Form.Get("currency") != "EUR" || r.Form.Get("merchant") != "Cafe" || r.Form.Get("created") != "2026-10-17" {
				t.Error("expense create changed input units or fields")
			}
		case "/UpdateMoneyRequest":
			updates = append(updates, jsonObject(t, r.Form.Get("updates")))
		case "/DeleteMoneyRequest":
			if r.Form.Get("transactionID") != "101" || r.Form.Get("reportActionID") != "401" {
				t.Error("delete did not resolve original action")
			}
		case "/AddComment":
			if r.Form.Get("reportComment") != "Ready for review" {
				t.Error("comment text changed")
			}
		}
		respond(w, snapshot(r))
	})
	s := newSandbox(t, apiURL, map[string]string{"EXPENSIFY_AUTH_TOKEN": "session-token"})
	receiptPath := filepath.Join(s.dir, "receipt.pdf")
	const receiptData = "%PDF-1.7\nfixture receipt\n"
	if err := os.WriteFile(receiptPath, []byte(receiptData), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args       []string
		action, id string
	}{
		{[]string{"expense", "create", "--amount", "1234", "--currency", "EUR", "--merchant", "Cafe", "--date", "2026-10-17"}, "created", ""},
		{[]string{"expense", "edit", "--expense-id", "101", "--merchant", "New Cafe"}, "updated", "101"},
		{[]string{"expense", "edit", "--expense-id", "101", "--comment="}, "updated", "101"},
		{[]string{"expense", "attach", "--expense-id", "101", "--file", receiptPath}, "receipt_attached", "101"},
		{[]string{"expense", "delete", "--expense-id", "101", "--report-id", "201", "--yes"}, "deleted", "101"},
		{[]string{"report", "create", "--name", "October", "--policy-id", "ABC"}, "created", ""},
		{[]string{"report", "submit", "--report-id", "201", "--yes"}, "submit", "201"},
		{[]string{"report", "approve", "--report-id", "201", "--yes"}, "approve", "201"},
		{[]string{"report", "comment", "--report-id", "201", "--text", "Ready for review"}, "comment", "201"},
	} {
		got := s.runInProcess("", append(tc.args, "--output", "json", "--no-input")...)
		requireSuccess(t, got)
		mutation := jsonObject(t, got.stdout)
		if mutation["action"] != tc.action || mutation["id"] == "" || tc.id != "" && mutation["id"] != tc.id {
			t.Errorf("%v: %s", tc.args, got.stdout)
		}
	}
	if len(updates) != 2 || len(updates[0]) != 1 || updates[0]["merchant"] != "New Cafe" || len(updates[1]) != 1 || updates[1]["comment"] != "" {
		t.Errorf("edits changed unspecified fields or ignored an explicit clear: %#v", updates)
	}
	if string(receipt) != receiptData {
		t.Errorf("receipt upload corrupted content: %q", receipt)
	}
	for _, args := range [][]string{{"expense", "view", "--expense-id", "101", "--report-id", "201"}, {"report", "view", "--report-id", "201"}, {"report", "list", "--limit", "1"}} {
		got := s.runInProcess("", append(args, "--output", "json")...)
		requireSuccess(t, got)
		value := jsonObject(t, got.stdout)
		if args[1] == "view" && value["id"] == "" {
			t.Errorf("view missing id: %s", got.stdout)
		}
		if args[1] == "list" {
			if items, ok := value["items"].([]any); !ok || len(items) != 1 {
				t.Errorf("report list missing records: %s", got.stdout)
			}
		}
	}
	if strings.Contains(strings.Join(commands, ","), "/Authenticate") {
		t.Error("valid environment session was unnecessarily authenticated")
	}
}

func TestDraftWorkspaceExpenseWorkflow(t *testing.T) {
	const policyID = "ABC"
	const reportName = "Test Person 2026-10"
	const category = "Software License Expense"
	const tag = "Engineering:Software Stipend"
	const receiptData = "%PDF-1.7\nfixture invoice\n"
	reports := map[string]any{
		"report_100": map[string]any{"reportID": "100", "type": "chat", "chatType": "policyExpenseChat", "policyID": policyID, "ownerAccountID": 42},
		"report_301": map[string]any{"reportID": "301", "type": "chat", "chatType": "selfDM", "ownerAccountID": 42},
	}
	transactions := map[string]any{}
	actions := map[string]map[string]any{"100": {}, "301": {}}
	var reportID, previewActionID string
	var writes []string
	apiURL := mockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Fatalf("parse create receipt: %v", err)
			}
			defer r.MultipartForm.RemoveAll()
		} else if err := r.ParseForm(); err != nil {
			t.Fatalf("parse workflow request: %v", err)
		}
		if r.Form.Get("authToken") != "session-token" {
			t.Fatal("workflow omitted authentication")
		}
		onyxResponse := func(patches ...any) {
			respond(w, map[string]any{"jsonCode": 200, "onyxData": patches})
		}
		switch r.URL.Path {
		case "/OpenApp":
			unreported := map[string]any{}
			for key, value := range transactions {
				if value.(map[string]any)["reportID"] == "0" {
					unreported[key] = value
				}
			}
			onyxResponse(
				map[string]any{"onyxMethod": "multiSet", "value": map[string]any{"session": map[string]any{"accountID": 42, "email": "person@example.test"}, "selfDMReportID": "301"}},
				map[string]any{"onyxMethod": "setCollection", "key": "policy_", "value": map[string]any{"policy_ABC": map[string]any{"id": policyID, "name": "Team", "autoReporting": false}}},
				map[string]any{"onyxMethod": "setCollection", "key": "report_", "value": reports},
				map[string]any{"onyxMethod": "setCollection", "key": "transactions_", "value": unreported},
			)
		case "/OpenReport":
			id := r.Form.Get("reportID")
			if reports["report_"+id] == nil {
				t.Fatalf("read unknown report %q", id)
			}
			onyxResponse(
				map[string]any{"onyxMethod": "set", "key": "report_" + id, "value": reports["report_"+id]},
				map[string]any{"onyxMethod": "set", "key": "reportActions_" + id, "value": actions[id]},
			)
		case "/Search":
			query := jsonObject(t, r.Form.Get("jsonQuery"))
			data := map[string]any{}
			var results any = data
			count := len(transactions)
			if query["type"] == "expense-report" {
				filter, ok := query["filters"].(map[string]any)
				if !ok || filter["left"] != "status" || filter["right"] != "drafts" || filter["operator"] != "eq" {
					t.Fatalf("report status filter changed: %#v", query["filters"])
				}
				if reportID != "" {
					t.Fatal("empty draft search ran after report creation")
				}
				results, count = []any{}, 0
			} else if query["type"] == "expense" {
				for key, value := range transactions {
					data[key] = value
				}
				for key, value := range reports {
					data[key] = value
				}
			} else {
				t.Fatalf("unexpected search type: %#v", query["type"])
			}
			onyxResponse(map[string]any{"onyxMethod": "set", "key": "snapshot_" + r.Form.Get("hash"), "value": map[string]any{"data": results, "search": map[string]any{"hasMoreResults": false, "offset": query["offset"], "count": count}}})
		case "/CreateAppReport":
			writes = append(writes, r.URL.Path)
			if r.Form.Get("policyID") != policyID || r.Form.Get("reportName") != reportName || r.Form.Get("type") != "expense" {
				t.Fatal("report create lost workspace, name, or type")
			}
			reportID, previewActionID = r.Form.Get("reportID"), r.Form.Get("reportPreviewReportActionID")
			if reportID == "" || previewActionID == "" || r.Form.Get("reportActionID") == "" {
				t.Fatal("report create omitted generated IDs")
			}
			reports["report_"+reportID] = map[string]any{"reportID": reportID, "reportName": reportName, "type": "expense", "policyID": policyID, "chatReportID": "100", "ownerAccountID": 42, "stateNum": 0, "statusNum": 0, "currency": "EUR", "total": 0}
			reports["report_100"].(map[string]any)["iouReportID"] = reportID
			actions["100"][previewActionID] = map[string]any{"reportActionID": previewActionID, "actionName": "REPORTPREVIEW", "originalMessage": map[string]any{"linkedReportID": reportID}}
			respond(w, map[string]any{"jsonCode": 200})
		case "/RequestMoney":
			writes = append(writes, r.URL.Path)
			for key, want := range map[string]string{"amount": "1250", "currency": "EUR", "merchant": "Software", "created": "2026-10-18", "chatReportID": "100", "iouReportID": reportID, "reportPreviewReportActionID": previewActionID, "category": category, "tag": tag, "reimbursable": "true", "billable": "false", "shouldDeferAutoSubmit": "true", "receiptState": "SCANREADY"} {
				if r.Form.Get(key) != want {
					t.Errorf("create %s=%q, want %q", key, r.Form.Get(key), want)
				}
			}
			file, header, err := r.FormFile("receipt")
			if err != nil {
				t.Fatalf("receipt missing from expense create: %v", err)
			}
			defer file.Close()
			contents, err := io.ReadAll(file)
			if err != nil || string(contents) != receiptData || header.Filename != "invoice.pdf" {
				t.Fatalf("receipt changed: %q, %v", contents, err)
			}
			var attendees []map[string]string
			if err := json.Unmarshal([]byte(r.Form.Get("attendees")), &attendees); err != nil || len(attendees) != 1 || attendees[0]["email"] != "person@example.test" {
				t.Fatalf("expense attendees omitted authenticated user: %q", r.Form.Get("attendees"))
			}
			id := r.Form.Get("transactionID")
			if id == "" || r.Form.Get("reportActionID") == "" || r.Form.Get("transactionThreadReportID") == "" {
				t.Fatal("expense create omitted generated IDs")
			}
			transactions["transactions_"+id] = map[string]any{"transactionID": id, "reportID": reportID, "amount": -1250, "modifiedAmount": "", "currency": "EUR", "merchant": "Software", "created": "2026-10-18", "category": category, "tag": tag, "reimbursable": true, "billable": false, "receipt": map[string]any{"state": "SCANREADY"}}
			respond(w, map[string]any{"jsonCode": 200})
		case "/TrackExpense":
			writes = append(writes, r.URL.Path)
			if r.Form.Get("amount") != "500" || r.Form.Get("chatReportID") != "301" {
				t.Fatal("personal expense did not use self DM and positive minor units")
			}
			id := r.Form.Get("transactionID")
			transactions["transactions_"+id] = map[string]any{"transactionID": id, "reportID": "0", "amount": -500, "currency": "EUR", "merchant": "Unreported software", "created": "2026-10-18"}
			actions["301"][r.Form.Get("reportActionID")] = map[string]any{"actorAccountID": 42, "originalMessage": map[string]any{"IOUTransactionID": id}}
			respond(w, map[string]any{"jsonCode": 200})
		case "/ChangeTransactionsReport":
			writes = append(writes, r.URL.Path)
			id := r.Form.Get("transactionList")
			transaction, ok := transactions["transactions_"+id].(map[string]any)
			if !ok || transaction["reportID"] != "0" || r.Form.Get("reportID") != reportID || strings.HasPrefix(id, "[") {
				t.Fatalf("move did not use the unreported expense ID as CSV: %q", id)
			}
			mapping := jsonObject(t, r.Form.Get("transactionIDToReportActionAndThreadData"))
			action, ok := mapping[id].(map[string]any)
			if len(mapping) != 1 || !ok || action["moneyRequestPreviewReportActionID"] == "" || action["moneyRequestPreviewReportActionID"] == nil {
				t.Fatalf("move omitted preview action mapping: %#v", mapping)
			}
			transaction["reportID"] = reportID
			respond(w, map[string]any{"jsonCode": 200})
		default:
			t.Fatalf("unexpected workflow command: %s", r.URL.Path)
		}
	})
	s := newSandbox(t, apiURL, map[string]string{"EXPENSIFY_AUTH_TOKEN": "session-token"})
	run := func(args ...string) map[string]any {
		t.Helper()
		got := s.runInProcess("", append(args, "--no-input", "--output", "json")...)
		requireSuccess(t, got)
		return jsonObject(t, got.stdout)
	}
	page := run("report", "list", "--status", "drafts", "--limit", "3")
	if items, ok := page["items"].([]any); !ok || len(items) != 0 || page["has_more"] != false {
		t.Fatalf("empty draft search failed: %#v", page)
	}
	createdReport := run("report", "create", "--policy-id", policyID, "--name", reportName)
	if createdReport["id"] != reportID || createdReport["action"] != "created" {
		t.Fatalf("report create returned wrong ID: %#v", createdReport)
	}
	receiptPath := filepath.Join(s.dir, "invoice.pdf")
	if err := os.WriteFile(receiptPath, []byte(receiptData), 0o600); err != nil {
		t.Fatal(err)
	}
	created := run("expense", "create", "--policy-id", policyID, "--report-id", reportID, "--amount", "1250", "--currency", "EUR", "--merchant", "Software", "--date", "2026-10-18", "--category", category, "--tag", tag, "--receipt", receiptPath)
	expenseID, ok := created["id"].(string)
	if !ok || expenseID == "" || created["report_id"] != reportID || created["action"] != "created" {
		t.Fatalf("workspace create lost destination: %#v", created)
	}
	view := run("expense", "view", "--expense-id", expenseID)
	if view["id"] != expenseID || view["report_id"] != reportID || view["amount"] != json.Number("1250") || view["category"] != category || view["tag"] != tag || view["receipt"] == nil || view["reimbursable"] != true || view["billable"] != false {
		t.Fatalf("workspace expense readback changed fields: %#v", view)
	}
	personal := run("expense", "create", "--amount", "500", "--currency", "EUR", "--merchant", "Unreported software", "--date", "2026-10-18")
	personalID, ok := personal["id"].(string)
	if !ok || personalID == "" {
		t.Fatalf("personal expense ID missing: %#v", personal)
	}
	moved := run("expense", "move", "--expense-id", personalID, "--report-id", reportID)
	if moved["id"] != personalID || moved["report_id"] != reportID || moved["action"] != "moved" {
		t.Fatalf("move returned wrong destination: %#v", moved)
	}
	view = run("expense", "view", "--expense-id", personalID)
	if view["report_id"] != reportID || view["amount"] != json.Number("500") || len(transactions) != 2 {
		t.Fatalf("move readback failed or duplicated expenses: %#v", view)
	}
	report := run("report", "view", "--report-id", reportID)
	if report["name"] != reportName || report["policy_id"] != policyID || report["state"] != "0" || report["status"] != "0" {
		t.Fatalf("report no longer draft: %#v", report)
	}
	if got := strings.Join(writes, ","); got != "/CreateAppReport,/RequestMoney,/TrackExpense,/ChangeTransactionsReport" {
		t.Fatalf("unexpected writes: %s", got)
	}
}

func TestHelpVersionAndCompletionDoNotAccessConfiguration(t *testing.T) {
	s := newSandbox(t, "http://127.0.0.1:1", nil)
	if err := os.WriteFile(s.configDir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{nil, {"--help"}, {"--version"}, {"expense", "create", "--help"}, {"completion", "bash"}, {"completion", "zsh"}, {"completion", "fish"}, {"completion", "powershell"}} {
		got := s.run("", args...)
		requireSuccess(t, got)
		if strings.TrimSpace(got.stdout) == "" {
			t.Errorf("%v produced no output", args)
		}
	}
}

func TestValidationAndDryRunNeverContactAPI(t *testing.T) {
	s := newSandbox(t, "http://127.0.0.1:1", nil)
	for _, args := range [][]string{
		{"expense", "create", "--dry-run"},
		{"expense", "create", "--amount", "0", "--currency", "EUR", "--merchant", "Cafe", "--date", "2026-10-17", "--dry-run"},
		{"expense", "create", "--amount", "1250", "--currency", "EUR", "--merchant", "Cafe", "--date", "2026-02-30", "--dry-run"},
		{"expense", "delete", "--expense-id", "101", "--report-id", "201", "--no-input"},
		{"expense", "edit", "--expense-id", "101", "--dry-run"},
		{"auth", "login", "--auth-token", "do-not-print-this-secret"},
	} {
		args = append(args, "--output", "json")
		got := s.run("", args...)
		if got.code != 2 || got.stdout != "" {
			t.Errorf("%v: exit=%d stdout=%q stderr=%q", args, got.code, got.stdout, got.stderr)
		}
		failure := jsonObject(t, got.stderr)
		if _, ok := failure["error"].(map[string]any); !ok || strings.Contains(got.stderr, "do-not-print-this-secret") {
			t.Errorf("invalid or unsafe structured error: %s", got.stderr)
		}
	}
	got := s.run("", "expense", "create", "--amount", "1250", "--currency", "EUR", "--merchant", "Cafe", "--date", "2026-10-17", "--dry-run", "--output", "json")
	requireSuccess(t, got)
	preview := jsonObject(t, got.stdout)
	if preview["dryRun"] != true || preview["command"] != "expense create" {
		t.Fatalf("unexpected preview: %s", got.stdout)
	}
	if _, err := os.Stat(s.configDir); !os.IsNotExist(err) {
		t.Fatalf("dry-run created configuration: %v", err)
	}
}

func TestStructuredErrorsAndSecretRedaction(t *testing.T) {
	for _, tc := range []struct {
		name, response string
		code           int
		want           string
	}{
		{"rejected-session", `{"jsonCode":401,"message":"server-secret"}`, 3, "AUTH-002"},
		{"malformed-json", `server-secret is not json`, 1, "API-002"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			apiURL := mockAPI(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(tc.response)) })
			s := newSandbox(t, apiURL, map[string]string{"EXPENSIFY_AUTH_TOKEN": "local-secret"})
			got := s.runInProcess("", "expense", "list", "--output", "json", "--verbose")
			if got.code != tc.code || got.stdout != "" {
				t.Fatalf("exit=%d stdout=%q stderr=%q", got.code, got.stdout, got.stderr)
			}
			failure := jsonObject(t, got.stderr)["error"].(map[string]any)
			if failure["code"] != tc.want || strings.Contains(got.stderr, "server-secret") || strings.Contains(got.stderr, "local-secret") {
				t.Fatalf("unsafe or incorrect error: %s", got.stderr)
			}
		})
	}
	s := newSandbox(t, "http://127.0.0.1:1", nil)
	got := s.run("", "expense", "list", "--output", "json", "--no-input")
	if got.code != 3 || got.stdout != "" || !strings.Contains(got.stderr, "AUTH-001") {
		t.Fatalf("missing authentication: exit=%d stdout=%q stderr=%q", got.code, got.stdout, got.stderr)
	}
	s = newSandbox(t, "http://127.0.0.1:1", map[string]string{"EXPENSIFY_AUTH_TOKEN": "session-token"})
	got = s.run("", "expense", "list", "--output", "json", "--no-input", "--timeout", "100ms")
	if got.code != 10 || got.stdout != "" || !strings.Contains(got.stderr, "NET-001") {
		t.Fatalf("network failure: exit=%d stdout=%q stderr=%q", got.code, got.stdout, got.stderr)
	}
}
