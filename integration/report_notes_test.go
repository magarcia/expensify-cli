package integration

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

type reportNotesAPI struct {
	t            *testing.T
	reports      map[string]map[string]any
	createdID    string
	writes       []string
	missingField bool
	rejectNotes  bool
}

func reportNotesMetadata(value string) map[string]any {
	return map[string]any{
		"name": "Notes", "fieldID": "text_notes", "type": "text", "value": value,
		"defaultValue": "Workspace default", "orderWeight": json.Number("2"), "deletable": true,
		"values": []any{}, "keys": []any{}, "externalIDs": []any{}, "disabledOptions": []any{},
		"isTax": false, "target": "expense",
	}
}

func newReportNotesAPI(t *testing.T) (*reportNotesAPI, *sandbox) {
	t.Helper()
	fixture := &reportNotesAPI{t: t, reports: map[string]map[string]any{}}
	apiURL := mockAPI(t, fixture.serveHTTP)
	return fixture, newSandbox(t, apiURL, map[string]string{"EXPENSIFY_AUTH_TOKEN": "notes-fixture-token"})
}

func (f *reportNotesAPI) serveHTTP(w http.ResponseWriter, r *http.Request) {
	f.t.Helper()
	if err := r.ParseForm(); err != nil {
		f.t.Fatalf("parse Notes request: %v", err)
	}
	if r.Form.Get("authToken") != "notes-fixture-token" {
		f.t.Fatal("Notes workflow omitted authentication")
	}
	patch := func(key string, value any) map[string]any {
		return map[string]any{"onyxMethod": "set", "key": key, "value": value}
	}
	onyx := func(patches ...any) {
		respond(w, map[string]any{"jsonCode": 200, "onyxData": patches})
	}
	switch r.URL.Path {
	case "/OpenApp":
		fields := map[string]any{}
		if !f.missingField {
			fields["expensify_text_notes"] = reportNotesMetadata("Workspace default")
		}
		onyx(
			patch("session", map[string]any{"accountID": "10", "email": "member@example.test"}),
			patch("policy_ABC", map[string]any{"id": "ABC", "role": "user", "autoReporting": false, "fieldList": fields}),
		)
	case "/OpenReport":
		id := r.Form.Get("reportID")
		if f.reports[id] == nil {
			f.t.Fatalf("read unknown fixture report %q", id)
		}
		onyx(patch("report_"+id, f.reports[id]))
	case "/CreateAppReport":
		f.writes = append(f.writes, r.URL.Path)
		if f.createdID != "" {
			f.t.Fatal("Notes workflow created a duplicate report")
		}
		if r.Form.Get("type") != "expense" || r.Form.Get("policyID") != "ABC" || r.Form.Get("reportName") != "Disposable Notes fixture" {
			f.t.Fatalf("wrong create destination: %v", r.Form)
		}
		for _, key := range []string{"reportID", "reportActionID", "reportPreviewReportActionID"} {
			if id, err := strconv.ParseUint(r.Form.Get(key), 10, 63); err != nil || id == 0 {
				f.t.Fatalf("create has invalid %s: %q", key, r.Form.Get(key))
			}
		}
		if r.Form.Has("description") || r.Form.Has("notes") {
			f.t.Fatal("create used a room-description field for report Notes")
		}
		f.createdID = r.Form.Get("reportID")
		f.reports[f.createdID] = map[string]any{
			"reportID": f.createdID, "reportName": "Disposable Notes fixture", "type": "expense",
			"policyID": "ABC", "ownerAccountID": "10", "stateNum": "0", "statusNum": "0",
			"currency": "EUR", "total": 0,
		}
		respond(w, map[string]any{"jsonCode": 200})
	case "/SetReportField":
		f.writes = append(f.writes, r.URL.Path)
		fields := jsonObject(f.t, r.Form.Get("reportFields"))
		actions := jsonObject(f.t, r.Form.Get("reportFieldsActionIDs"))
		field, ok := fields["expensify_text_notes"].(map[string]any)
		if !ok || len(fields) != 1 || len(actions) != 1 {
			f.t.Fatalf("Notes field or action mapping is incorrect: fields=%#v actions=%#v", fields, actions)
		}
		value, ok := field["value"].(string)
		if !ok || !reflect.DeepEqual(field, reportNotesMetadata(value)) {
			f.t.Fatalf("Notes update discarded report-field metadata: %#v", field)
		}
		actionID, ok := actions["expensify_text_notes"].(string)
		if id, err := strconv.ParseUint(actionID, 10, 63); !ok || err != nil || id == 0 {
			f.t.Fatalf("Notes action ID is invalid: %#v", actions)
		}
		for key := range r.Form {
			switch key {
			case "reportID", "reportFields", "reportFieldsActionIDs", "authToken", "platform", "referer", "api_setCookie", "doNotRetry":
			default:
				f.t.Fatalf("unexpected SetReportField parameter %q", key)
			}
		}
		report := f.reports[r.Form.Get("reportID")]
		if report == nil || report["ownerAccountID"] != "10" || report["stateNum"] != "0" || report["statusNum"] != "0" {
			f.t.Fatal("Notes workflow attempted to modify an unowned or submitted report")
		}
		if f.rejectNotes {
			respond(w, map[string]any{"jsonCode": 666})
			return
		}
		report["fieldList"] = fields
		respond(w, map[string]any{"jsonCode": 200})
	default:
		f.t.Fatalf("unexpected Notes workflow endpoint %s", r.URL.Path)
	}
}

func runNotesCommand(t *testing.T, s *sandbox, args ...string) map[string]any {
	t.Helper()
	got := s.runInProcess("", append(args, "--no-input", "--output", "json")...)
	requireSuccess(t, got)
	return jsonObject(t, got.stdout)
}

func TestReportNotesCreateEditClearAndReadBack(t *testing.T) {
	fixture, s := newReportNotesAPI(t)
	const initial = "Health, Software License, Books, Courses & Internet"
	created := runNotesCommand(t, s, "report", "create", "--policy-id", "ABC", "--name", "Disposable Notes fixture", "--notes", initial)
	if fixture.createdID == "" || created["id"] != fixture.createdID || created["action"] != "created" {
		t.Fatalf("Notes create lost the report ID: %#v", created)
	}
	checkNotes := func(want string) {
		t.Helper()
		view := runNotesCommand(t, s, "report", "view", "--report-id", fixture.createdID)
		if view["id"] != fixture.createdID || view["notes"] != want || view["state"] != "0" || view["status"] != "0" {
			t.Fatalf("Notes readback changed the value or draft state: %#v", view)
		}
	}
	checkNotes(initial)
	for _, notes := range []string{"Software & Internet\nOctober invoices", ""} {
		updated := runNotesCommand(t, s, "report", "edit", "--report-id", fixture.createdID, "--notes="+notes)
		if updated["id"] != fixture.createdID || updated["action"] != "updated" {
			t.Fatalf("Notes edit lost its report: %#v", updated)
		}
		checkNotes(notes)
	}
	if got := strings.Join(fixture.writes, ","); got != "/CreateAppReport,/SetReportField,/SetReportField,/SetReportField" {
		t.Fatalf("Notes workflow duplicated or submitted the report: %s", got)
	}
}

func TestReportNotesPartialCreateFailureRecoversWithoutDuplicate(t *testing.T) {
	fixture, s := newReportNotesAPI(t)
	fixture.rejectNotes = true
	got := s.runInProcess("", "report", "create", "--policy-id", "ABC", "--name", "Disposable Notes fixture", "--notes", "Health", "--no-input", "--output", "json")
	if got.code == 0 || got.stdout != "" || fixture.createdID == "" {
		t.Fatalf("partial failure was reported as success or lost its created report: %+v", got)
	}
	failure := jsonObject(t, got.stderr)["error"].(map[string]any)
	if failure["code"] != "API-006" || !strings.Contains(failure["message"].(string), fixture.createdID) || !strings.Contains(failure["message"].(string), "jsonCode 666") {
		t.Fatalf("partial failure omitted created ID or the backend rejection: %#v", failure)
	}
	hint := failure["hint"].(string)
	if !strings.Contains(hint, "Do not create another report") || !strings.Contains(hint, "expensify report edit --report-id "+fixture.createdID+" --notes") {
		t.Fatalf("partial failure did not explain how to recover: %q", hint)
	}
	if got := strings.Join(fixture.writes, ","); got != "/CreateAppReport,/SetReportField" {
		t.Fatalf("partial create failure was automatically retried: %s", got)
	}
	fixture.rejectNotes = false
	runNotesCommand(t, s, "report", "edit", "--report-id", fixture.createdID, "--notes", "Health")
	view := runNotesCommand(t, s, "report", "view", "--report-id", fixture.createdID)
	if view["notes"] != "Health" || len(fixture.reports) != 1 {
		t.Fatalf("partial failure recovery duplicated the report or lost Notes: %#v", view)
	}
}

func TestReportNotesMissingFieldPreventsCreate(t *testing.T) {
	fixture, s := newReportNotesAPI(t)
	fixture.missingField = true
	got := s.runInProcess("", "report", "create", "--policy-id", "ABC", "--name", "Disposable Notes fixture", "--notes", "Health", "--no-input", "--output", "json")
	if got.code == 0 || got.stdout != "" {
		t.Fatalf("missing Notes definition was accepted: %+v", got)
	}
	failure := jsonObject(t, got.stderr)["error"].(map[string]any)
	if failure["code"] != "API-405" || !strings.Contains(failure["message"].(string), "no Notes report field") || len(fixture.writes) != 0 || len(fixture.reports) != 0 {
		t.Fatalf("missing Notes field caused a partial report write: failure=%#v writes=%v", failure, fixture.writes)
	}
}

func TestReportNotesRejectsOtherOwnersAndSubmittedReports(t *testing.T) {
	for _, name := range []string{"other owner", "submitted"} {
		t.Run(name, func(t *testing.T) {
			fixture, s := newReportNotesAPI(t)
			report := map[string]any{"reportID": "456", "type": "expense", "policyID": "ABC", "ownerAccountID": "10", "stateNum": "0", "statusNum": "0"}
			if name == "other owner" {
				report["ownerAccountID"] = "11"
			} else {
				report["stateNum"], report["statusNum"] = "2", "1"
			}
			fixture.reports["456"] = report
			got := s.runInProcess("", "report", "edit", "--report-id", "456", "--notes", "Health", "--no-input", "--output", "json")
			if got.code == 0 || got.stdout != "" || len(fixture.writes) != 0 {
				t.Fatalf("unsafe Notes edit reached a write: result=%+v writes=%v", got, fixture.writes)
			}
			if failure := jsonObject(t, got.stderr)["error"].(map[string]any); failure["code"] != "API-405" {
				t.Fatalf("wrong edit guard failure: %#v", failure)
			}
		})
	}
}
