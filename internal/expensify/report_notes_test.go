package expensify

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
)

func notesFieldFixture(id, value string) map[string]any {
	return map[string]any{
		"name": "Notes", "fieldID": id, "type": "text", "value": value,
		"defaultValue": "Default notes", "orderWeight": 2, "deletable": true,
		"values": []any{}, "keys": []any{}, "externalIDs": []any{},
		"disabledOptions": []any{}, "isTax": false,
	}
}

func notesReportFixture() map[string]any {
	return map[string]any{"reportID": "456", "type": "expense", "policyID": "ABC", "ownerAccountID": "10", "stateNum": "0", "statusNum": "0"}
}

func TestPrepareReportNotesUsesWorkspaceFieldMetadataWithoutChangingSnapshot(t *testing.T) {
	field := notesFieldFixture("text_notes", "Old")
	state := snapshot{"policy_ABC": map[string]any{"fieldList": map[string]any{"expensify_text_notes": field}}}
	got, err := prepareReportNotes(state, notesReportFixture(), "Health, Software & Internet")
	if err != nil {
		t.Fatal(err)
	}
	want := notesFieldFixture("text_notes", "Health, Software & Internet")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("field=%#v want %#v", got, want)
	}
	if field["value"] != "Old" {
		t.Fatal("preparing notes mutated the snapshot")
	}
}

func TestPrepareReportNotesPrefersAttachedReportFieldAndKeepsExplicitClear(t *testing.T) {
	state := snapshot{"policy_ABC": map[string]any{"fieldList": map[string]any{"text_notes": notesFieldFixture("text_notes", "Workspace")}}}
	raw := notesReportFixture()
	raw["fieldList"] = map[string]any{"expensify_text_notes": notesFieldFixture("text_notes", "Attached")}
	got, err := prepareReportNotes(state, raw, "")
	if err != nil || got["value"] != "" {
		t.Fatalf("field=%#v err=%v", got, err)
	}
	if value := reportNotes(state, "456", raw); value != "Attached" {
		t.Fatalf("notes=%q", value)
	}
}

func TestReportNotesSelectsFromEffectiveAttachedFieldMetadata(t *testing.T) {
	for _, name := range []string{"renamed attached field", "attached invoice field"} {
		t.Run(name, func(t *testing.T) {
			workspaceFields := map[string]any{"expensify_text_notes": notesFieldFixture("text_notes", "Workspace Notes")}
			state := snapshot{"policy_ABC": map[string]any{"fieldList": workspaceFields}}
			attached := notesFieldFixture("text_notes", "Attached value")
			if name == "renamed attached field" {
				attached["name"] = "Other"
			} else {
				attached["target"] = "invoice"
			}
			raw := notesReportFixture()
			raw["fieldList"] = map[string]any{"expensify_text_notes": attached}
			if got := reportNotes(state, "456", raw); got != "" {
				t.Fatalf("attached metadata was replaced with workspace Notes: %q", got)
			}
			if _, err := prepareReportNotes(state, raw, "New Notes"); err == nil {
				t.Fatal("write would replace the attached field's metadata")
			}
			workspaceFields["expensify_text_other_notes"] = notesFieldFixture("text_other_notes", "Other Notes")
			field, err := prepareReportNotes(state, raw, "New Notes")
			if err != nil || field["fieldID"] != "text_other_notes" {
				t.Fatalf("the unrelated workspace Notes field was not selected: field=%#v err=%v", field, err)
			}
		})
	}
}

func TestPrepareReportNotesRejectsMissingAmbiguousAndWrongTypeFields(t *testing.T) {
	for _, name := range []string{"missing", "ambiguous", "dropdown", "missing ID", "invalid ID type", "invoice"} {
		t.Run(name, func(t *testing.T) {
			fields := map[string]any{}
			switch name {
			case "ambiguous":
				fields["a"] = notesFieldFixture("a", "")
				fields["b"] = notesFieldFixture("b", "")
			case "dropdown":
				field := notesFieldFixture("a", "")
				field["type"] = "dropdown"
				fields["a"] = field
			case "missing ID":
				fields["a"] = notesFieldFixture("", "")
			case "invalid ID type":
				field := notesFieldFixture("a", "")
				field["fieldID"] = 123
				fields["a"] = field
			case "invoice":
				field := notesFieldFixture("a", "")
				field["target"] = "invoice"
				fields["a"] = field
			}
			state := snapshot{"policy_ABC": map[string]any{"fieldList": fields}}
			if _, err := prepareReportNotes(state, notesReportFixture(), "New"); err == nil {
				t.Fatal("invalid Notes definition accepted")
			}
			if value := reportNotes(state, "456", notesReportFixture()); value != "" {
				t.Fatalf("unexpected readable notes %q", value)
			}
		})
	}
}

func TestPrepareReportNotesIgnoresInvalidUnrelatedFields(t *testing.T) {
	raw := notesReportFixture()
	raw["fieldList"] = map[string]any{
		"invalid": map[string]any{"name": "Other", "fieldID": 123, "type": "text"},
		"notes":   notesFieldFixture("text_notes", "Notes"),
	}
	field, err := prepareReportNotes(nil, raw, "Updated Notes")
	if err != nil || field["fieldID"] != "text_notes" {
		t.Fatalf("unrelated metadata affected Notes: field=%#v err=%v", field, err)
	}
}

func TestReportNotesUsesDefaultWithoutTreatingExplicitEmptyAsMissing(t *testing.T) {
	field := notesFieldFixture("text_notes", "")
	raw := notesReportFixture()
	raw["fieldList"] = map[string]any{"expensify_text_notes": field}
	if got := reportNotes(nil, "456", raw); got != "" {
		t.Fatalf("cleared notes=%q", got)
	}
	delete(field, "value")
	if got := reportNotes(nil, "456", raw); got != "Default notes" {
		t.Fatalf("default notes=%q", got)
	}
	field["value"] = nil
	if got := reportNotes(nil, "456", raw); got != "Default notes" {
		t.Fatalf("null notes=%q", got)
	}
}

func TestExpenseReportNotesUseAttachedValueRatherThanNameValuePairs(t *testing.T) {
	raw := notesReportFixture()
	raw["fieldList"] = map[string]any{"expensify_field_id_NOTES": notesFieldFixture("field_id_NOTES", "Attached Notes")}
	state := snapshot{"reportNameValuePairs_456": map[string]any{"expensify_field_id_NOTES": notesFieldFixture("field_id_NOTES", "Different NVP Notes")}}
	if got := reportNotes(state, "456", raw); got != "Attached Notes" {
		t.Fatalf("expense Notes=%q", got)
	}
}

func TestPrepareReportNotesRejectsClearingRequiredField(t *testing.T) {
	field := notesFieldFixture("text_notes", "Required")
	field["deletable"] = false
	raw := notesReportFixture()
	raw["fieldList"] = map[string]any{"expensify_text_notes": field}
	for _, notes := range []string{"", " \t\n"} {
		if _, err := prepareReportNotes(nil, raw, notes); err == nil {
			t.Fatalf("required field allowed %q", notes)
		}
	}
	if _, err := prepareReportNotes(nil, raw, "Health"); err != nil {
		t.Fatal(err)
	}
}

func TestReportNotesIgnoresNewWorkspaceDefaultsOnSettledReports(t *testing.T) {
	state := snapshot{"policy_ABC": map[string]any{"fieldList": map[string]any{"expensify_text_notes": notesFieldFixture("text_notes", "New workspace default")}}}
	raw := notesReportFixture()
	raw["statusNum"] = "4"
	if got := reportNotes(state, "456", raw); got != "" {
		t.Fatalf("settled report acquired notes %q", got)
	}
	raw["fieldList"] = map[string]any{"expensify_text_notes": notesFieldFixture("text_notes", "Original Notes")}
	if got := reportNotes(state, "456", raw); got != "Original Notes" {
		t.Fatalf("settled notes=%q", got)
	}
}

func TestSetReportNotesUsesSetReportFieldWithMetadataAndActionID(t *testing.T) {
	for _, notes := range []string{"Health, Software & Internet", ""} {
		t.Run(notes, func(t *testing.T) {
			calls := []string{}
			service := testService(t, func(r *http.Request) map[string]any {
				v := form(t, r)
				calls = append(calls, r.URL.Path)
				switch r.URL.Path {
				case "/api/OpenReport":
					if v.Get("reportID") != "456" {
						t.Fatal("wrong report opened")
					}
					return ok(patch("report_456", notesReportFixture()))
				case "/api/OpenApp":
					return ok(patch("session", map[string]any{"accountID": "10"}), patch("policy_ABC", map[string]any{"fieldList": map[string]any{"expensify_text_notes": notesFieldFixture("text_notes", "Old")}}))
				case "/api/SetReportField":
					if v.Get("reportID") != "456" || v.Has("description") {
						t.Fatal("wrong Notes contract")
					}
					var fields map[string]map[string]any
					var actions map[string]string
					if err := json.Unmarshal([]byte(v.Get("reportFields")), &fields); err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal([]byte(v.Get("reportFieldsActionIDs")), &actions); err != nil {
						t.Fatal(err)
					}
					field := fields["expensify_text_notes"]
					if len(fields) != 1 || field["value"] != notes || field["fieldID"] != "text_notes" || field["name"] != "Notes" || field["defaultValue"] != "Default notes" {
						t.Fatalf("bad notes fields %#v", fields)
					}
					if len(actions) != 1 || !decimalIDPattern.MatchString(actions["expensify_text_notes"]) {
						t.Fatal("missing report field action ID")
					}
					return ok()
				default:
					t.Fatalf("unexpected request %s", r.URL.Path)
				}
				return nil
			})
			result, err := service.Execute(context.Background(), "report", "edit", map[string]string{"report-id": "456", "notes": notes})
			got, _ := result.(Mutation)
			if err != nil || got.ID != "456" || got.Action != "updated" || !reflect.DeepEqual(calls, []string{"/api/OpenReport", "/api/OpenApp", "/api/SetReportField"}) {
				t.Fatalf("mutation=%#v calls=%v err=%v", got, calls, err)
			}
		})
	}
}

func TestSetReportNotesRejectsNonDraftOrUnownedReportWithoutWrite(t *testing.T) {
	for _, name := range []string{"submitted", "foreign owner", "chat"} {
		t.Run(name, func(t *testing.T) {
			raw := notesReportFixture()
			switch name {
			case "submitted":
				raw["stateNum"] = "1"
			case "foreign owner":
				raw["ownerAccountID"] = "11"
			case "chat":
				raw["type"] = "chat"
			}
			service := testService(t, func(r *http.Request) map[string]any {
				form(t, r)
				if r.URL.Path != "/api/OpenReport" {
					t.Fatalf("unsafe report sent %s", r.URL.Path)
				}
				return ok(patch("session", map[string]any{"accountID": "10"}), patch("policy_ABC", map[string]any{}), patch("report_456", raw))
			})
			if _, err := service.Execute(context.Background(), "report", "edit", map[string]string{"report-id": "456", "notes": "Notes"}); err == nil {
				t.Fatal("unsafe report allowed")
			}
		})
	}
}

func TestViewReportReadsAttachedNotesWithoutLoadingWorkspace(t *testing.T) {
	service := testService(t, func(r *http.Request) map[string]any {
		form(t, r)
		if r.URL.Path != "/api/OpenReport" {
			t.Fatalf("unnecessary request %s", r.URL.Path)
		}
		raw := notesReportFixture()
		raw["fieldList"] = map[string]any{"expensify_text_notes": notesFieldFixture("text_notes", "Health, Software & Internet")}
		return ok(patch("report_456", raw))
	})
	got, err := service.Execute(context.Background(), "report", "view", map[string]string{"report-id": "456"})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(encoded, &result); err != nil || result["notes"] != "Health, Software & Internet" {
		t.Fatalf("report=%s err=%v", encoded, err)
	}
}

func TestViewReportLoadsMissingNotesDefinitionAndReturnsExplicitEmptyWhenAbsent(t *testing.T) {
	for _, exists := range []bool{true, false} {
		t.Run(map[bool]string{true: "workspace default", false: "no Notes field"}[exists], func(t *testing.T) {
			calls := []string{}
			service := testService(t, func(r *http.Request) map[string]any {
				form(t, r)
				calls = append(calls, r.URL.Path)
				switch r.URL.Path {
				case "/api/OpenReport":
					return ok(patch("report_456", notesReportFixture()))
				case "/api/OpenApp":
					fields := map[string]any{}
					if exists {
						field := notesFieldFixture("text_notes", "")
						delete(field, "value")
						fields["expensify_text_notes"] = field
					}
					return ok(patch("policy_ABC", map[string]any{"fieldList": fields}))
				default:
					t.Fatalf("unexpected request %s", r.URL.Path)
				}
				return nil
			})
			got, err := service.Execute(context.Background(), "report", "view", map[string]string{"report-id": "456"})
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			var result map[string]any
			if err := json.Unmarshal(encoded, &result); err != nil {
				t.Fatal(err)
			}
			want := ""
			if exists {
				want = "Default notes"
			}
			if result["notes"] != want || !reflect.DeepEqual(calls, []string{"/api/OpenReport", "/api/OpenApp"}) {
				t.Fatalf("report=%s calls=%v", encoded, calls)
			}
		})
	}
}

func TestViewReportDoesNotInterpretRoomDescriptionAsExpenseNotes(t *testing.T) {
	for _, reportType := range []string{"chat", "invoice"} {
		t.Run(reportType, func(t *testing.T) {
			service := testService(t, func(r *http.Request) map[string]any {
				form(t, r)
				if r.URL.Path != "/api/OpenReport" {
					t.Fatalf("unnecessary workspace Notes request %s", r.URL.Path)
				}
				raw := notesReportFixture()
				raw["type"] = reportType
				raw["description"] = "Room description"
				return ok(patch("report_456", raw), patch("policy_ABC", map[string]any{"fieldList": map[string]any{"expensify_text_notes": notesFieldFixture("text_notes", "Workspace Notes")}}))
			})
			result, err := service.Execute(context.Background(), "report", "view", map[string]string{"report-id": "456"})
			if err != nil {
				t.Fatal(err)
			}
			got := result.(Report)
			if got.Notes == nil || *got.Notes != "" {
				t.Fatalf("non-expense notes=%#v", got.Notes)
			}
		})
	}
}

func TestViewReportDoesNotLoadWorkspaceExpenseNotesForChat(t *testing.T) {
	service := testService(t, func(r *http.Request) map[string]any {
		form(t, r)
		if r.URL.Path != "/api/OpenReport" {
			t.Fatalf("unnecessary workspace Notes request %s", r.URL.Path)
		}
		raw := notesReportFixture()
		raw["type"] = "chat"
		return ok(patch("report_456", raw))
	})
	if _, err := service.Execute(context.Background(), "report", "view", map[string]string{"report-id": "456"}); err != nil {
		t.Fatal(err)
	}
}

func TestReportNotesLoadsFieldsFromPartialWorkspaceSnapshot(t *testing.T) {
	for _, verb := range []string{"view", "edit"} {
		t.Run(verb, func(t *testing.T) {
			writes := 0
			service := testService(t, func(r *http.Request) map[string]any {
				v := form(t, r)
				switch r.URL.Path {
				case "/api/OpenReport":
					return ok(patch("report_456", notesReportFixture()), patch("session", map[string]any{"accountID": "10"}), patch("policy_ABC", map[string]any{"id": "ABC"}))
				case "/api/OpenApp":
					return ok(
						patch("report_456", map[string]any{"ownerAccountID": "11", "stateNum": "6", "statusNum": "4"}),
						patch("session", map[string]any{"accountID": "11"}),
						patch("policy_ABC", map[string]any{"id": "ABC", "fieldList": map[string]any{"expensify_text_notes": notesFieldFixture("text_notes", "Workspace Notes")}}),
					)
				case "/api/SetReportField":
					writes++
					if v.Get("reportID") != "456" {
						t.Fatal("wrong report edited")
					}
					return ok()
				default:
					t.Fatalf("unexpected request %s", r.URL.Path)
				}
				return nil
			})
			values := map[string]string{"report-id": "456"}
			if verb == "edit" {
				values["notes"] = "Updated Notes"
			}
			result, err := service.Execute(context.Background(), "report", verb, values)
			if err != nil {
				t.Fatal(err)
			}
			if verb == "view" {
				got := result.(Report)
				if got.Notes == nil || *got.Notes != "Workspace Notes" || got.OwnerAccountID != "10" || writes != 0 {
					t.Fatalf("partial policy report=%#v writes=%d", got, writes)
				}
			} else if writes != 1 {
				t.Fatalf("writes=%d", writes)
			}
		})
	}
}

func TestSetReportNotesLoadsMissingAccountIDFromPartialSession(t *testing.T) {
	writes := 0
	service := testService(t, func(r *http.Request) map[string]any {
		v := form(t, r)
		switch r.URL.Path {
		case "/api/OpenReport":
			return ok(
				patch("report_456", notesReportFixture()),
				patch("session", map[string]any{"email": "owner@example.test"}),
				patch("policy_ABC", map[string]any{"fieldList": map[string]any{"expensify_text_notes": notesFieldFixture("text_notes", "Notes")}}),
			)
		case "/api/OpenApp":
			return ok(patch("session", map[string]any{"accountID": "10", "email": "owner@example.test"}))
		case "/api/SetReportField":
			if v.Get("reportID") != "456" {
				t.Fatal("wrong report edited")
			}
			writes++
			return ok()
		default:
			t.Fatalf("unexpected request %s", r.URL.Path)
		}
		return nil
	})
	result, err := service.Execute(context.Background(), "report", "edit", map[string]string{"report-id": "456", "notes": "Updated Notes"})
	if err != nil || result.(Mutation).ID != "456" || writes != 1 {
		t.Fatalf("mutation=%#v writes=%d err=%v", result, writes, err)
	}
}

func TestReportNotesStatePreservesProvidedSessionIdentity(t *testing.T) {
	state := snapshot{"session": map[string]any{"accountID": "10", "email": "fresh@example.test"}}
	app := snapshot{"session": map[string]any{"accountID": "11", "email": "older@example.test"}}
	enrichReportNotesState(state, app, "ABC")
	session := object(state["session"])
	if session["accountID"] != "10" || session["email"] != "fresh@example.test" {
		t.Fatalf("provided identity was overwritten: %#v", session)
	}
	delete(session, "accountID")
	enrichReportNotesState(state, app, "ABC")
	if session["accountID"] != "11" || session["email"] != "fresh@example.test" {
		t.Fatalf("partial identity was not filled safely: %#v", session)
	}
	delete(session, "email")
	enrichReportNotesState(state, app, "ABC")
	if session["email"] != "older@example.test" {
		t.Fatalf("missing email was not filled: %#v", session)
	}
}
