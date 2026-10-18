package expensify

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestOpenAppReadsAcceptSetCollectionResponses(t *testing.T) {
	for _, tc := range []struct {
		noun, verb string
		values     map[string]string
		want       string
	}{
		{noun: "user", verb: "view", want: `"email":"reader@example.test"`},
		{noun: "workspace", verb: "list", want: `"id":"ABC123"`},
		{noun: "expense", verb: "view", values: map[string]string{"expense-id": "123"}, want: `"amount":1250`},
	} {
		t.Run(tc.noun+" "+tc.verb, func(t *testing.T) {
			service := testService(t, func(request *http.Request) map[string]any {
				if request.URL.Path != "/api/OpenApp" {
					t.Fatalf("unexpected request: %s", request.URL.Path)
				}
				return ok(
					patch("session", map[string]any{"accountID": 123, "email": "reader@example.test"}),
					map[string]any{"key": "policy_", "onyxMethod": "setcollection", "value": map[string]any{"policy_ABC123": map[string]any{"id": "ABC123", "name": "Test workspace"}}},
					map[string]any{"key": "transactions_", "onyxMethod": "setcollection", "value": map[string]any{"transactions_123": map[string]any{"transactionID": "123", "amount": -1250, "currency": "USD"}}},
				)
			})
			result, err := service.Execute(context.Background(), tc.noun, tc.verb, tc.values)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(result)
			if err != nil || !strings.Contains(string(encoded), tc.want) {
				t.Fatalf("result = %s, error = %v; want %s", encoded, err, tc.want)
			}
		})
	}
}

func TestSnapshotSetCollectionReplacesMembers(t *testing.T) {
	state, err := decodeSnapshot(ok(
		patch("policy_old", map[string]any{"name": "Removed"}),
		patch("policy_keep", map[string]any{"name": "Old name", "obsolete": true}),
		patch("report_123", map[string]any{"reportName": "Unrelated"}),
		map[string]any{"key": "policy_", "onyxMethod": "setcollection", "value": map[string]any{
			"policy_keep": map[string]any{"name": "New name"},
			"policy_new":  map[string]any{"name": "New workspace"},
		}},
	))
	if err != nil {
		t.Fatal(err)
	}
	want := snapshot{
		"policy_keep": map[string]any{"name": "New name"},
		"policy_new":  map[string]any{"name": "New workspace"},
		"report_123":  map[string]any{"reportName": "Unrelated"},
	}
	if !reflect.DeepEqual(state, want) {
		t.Fatalf("state = %#v; want %#v", state, want)
	}
}

func TestSnapshotEmptySetCollectionRemovesOnlyItsMembers(t *testing.T) {
	for _, empty := range []any{map[string]any{}, []any{}} {
		state, err := decodeSnapshot(ok(
			patch("policy_123", map[string]any{"name": "Removed"}),
			patch("report_123", map[string]any{"reportName": "Preserved"}),
			map[string]any{"key": "policy_", "onyxMethod": "setcollection", "value": empty},
		))
		if err != nil {
			t.Fatal(err)
		}
		if len(state) != 1 || object(state["report_123"])["reportName"] != "Preserved" {
			t.Fatalf("unexpected state after empty collection replacement: %#v", state)
		}
	}
}

func TestSnapshotMergeCollectionPreservesMembersAndAppliesDeletions(t *testing.T) {
	state, err := decodeSnapshot(ok(
		patch("policy_keep", map[string]any{"name": "Preserved"}),
		patch("policy_remove", map[string]any{"name": "Removed"}),
		patch("policy_update", map[string]any{"name": "Old", "currency": "USD", "obsolete": true}),
		map[string]any{"key": "policy_", "onyxMethod": "mergecollection", "value": map[string]any{
			"policy_remove": nil,
			"policy_update": map[string]any{"name": "New", "obsolete": nil},
		}},
	))
	if err != nil {
		t.Fatal(err)
	}
	want := snapshot{
		"policy_keep":   map[string]any{"name": "Preserved"},
		"policy_update": map[string]any{"name": "New", "currency": "USD"},
	}
	if !reflect.DeepEqual(state, want) {
		t.Fatalf("state = %#v; want %#v", state, want)
	}
}

func TestSnapshotMultiSetAcceptsMissingKeyAndReplacesValues(t *testing.T) {
	state, err := decodeSnapshot(ok(
		patch("session", map[string]any{"email": "old@example.test", "obsolete": true}),
		patch("report_123", map[string]any{"reportName": "Removed"}),
		map[string]any{"onyxMethod": "multiset", "value": map[string]any{
			"session":        map[string]any{"email": "new@example.test"},
			"report_123":     nil,
			"selfDMReportID": "456",
		}},
	))
	if err != nil {
		t.Fatal(err)
	}
	want := snapshot{"session": map[string]any{"email": "new@example.test"}, "selfDMReportID": "456"}
	if !reflect.DeepEqual(state, want) {
		t.Fatalf("state = %#v; want %#v", state, want)
	}
}

func TestSnapshotClearAcceptsMissingKeyAndValue(t *testing.T) {
	// Onyx.update clears persisted state before applying the response's queued writes.
	state, err := decodeSnapshot(ok(
		patch("session", map[string]any{"email": "reader@example.test"}),
		map[string]any{"onyxMethod": "clear"},
		patch("selfDMReportID", "123"),
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(state) != 2 || text(object(state["session"])["email"]) != "reader@example.test" || state["selfDMReportID"] != "123" {
		t.Fatalf("clear removed response data: %#v", state)
	}
}

func TestReportSearchAcceptsEmptyPHPObjectArray(t *testing.T) {
	service := testService(t, func(request *http.Request) map[string]any {
		values := form(t, request)
		return ok(patch("snapshot_"+values.Get("hash"), map[string]any{
			"search": map[string]any{"offset": 0, "hasMoreResults": false, "hasResults": false, "count": 0},
			"data":   []any{},
		}))
	})
	result, err := service.Execute(context.Background(), "report", "list", map[string]string{"status": "drafts", "limit": "3"})
	if err != nil {
		t.Fatal(err)
	}
	page := result.(Page[Report])
	if len(page.Items) != 0 || page.HasMore || page.Offset != 0 || page.Limit != 3 {
		t.Fatalf("unexpected empty page: %#v", page)
	}
}

func TestSearchRejectsNonemptyArraysAndMissingCountedResults(t *testing.T) {
	for _, tc := range []struct {
		name   string
		data   []any
		count  int
		reason string
	}{
		{name: "nonempty array", data: []any{map[string]any{"reportID": "123"}}, reason: "Search data is not an object"},
		{name: "missing counted results", data: []any{}, count: 1, reason: "Search count is positive but expected resources are missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := testService(t, func(request *http.Request) map[string]any {
				values := form(t, request)
				return ok(patch("snapshot_"+values.Get("hash"), map[string]any{
					"search": map[string]any{"offset": 0, "hasMoreResults": false, "count": tc.count},
					"data":   tc.data,
				}))
			})
			_, err := service.Execute(context.Background(), "report", "list", nil)
			if err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("error = %v; want %s", err, tc.reason)
			}
		})
	}
}

func TestSnapshotRejectsMalformedCollectionOperations(t *testing.T) {
	for _, tc := range []struct {
		name   string
		patch  map[string]any
		reason string
	}{
		{"setcollection nonobject", map[string]any{"onyxMethod": "setcollection", "key": "policy_", "value": "private"}, "Onyx collection value is not an object"},
		{"setcollection null", map[string]any{"onyxMethod": "setcollection", "key": "policy_", "value": nil}, "Onyx collection value is not an object"},
		{"setcollection invalid key", map[string]any{"onyxMethod": "setcollection", "key": "policy", "value": map[string]any{}}, "Onyx collection key is invalid"},
		{"setcollection wrong parent", map[string]any{"onyxMethod": "setcollection", "key": "policy_", "value": map[string]any{"report_123": map[string]any{}}}, "Onyx collection member does not match its key"},
		{"mergecollection wrong parent", map[string]any{"onyxMethod": "mergecollection", "key": "policy_", "value": map[string]any{"report_123": map[string]any{}}}, "Onyx collection member does not match its key"},
		{"multiset nonobject", map[string]any{"onyxMethod": "multiset", "value": []any{}}, "Onyx multiSet value is not an object"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeSnapshot(ok(tc.patch))
			if err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("error = %v; want %s", err, tc.reason)
			}
		})
	}
}
