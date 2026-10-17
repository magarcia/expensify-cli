package expensify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/magarcia/expensify-cli/internal/api"
	"github.com/magarcia/expensify-cli/internal/config"
	"github.com/magarcia/expensify-cli/internal/fault"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testService(t *testing.T, handler func(*http.Request) map[string]any) *Service {
	t.Helper()
	client, err := api.New(api.Options{BaseURL: "https://expensify.test/api", Credentials: config.Credentials{AuthToken: "test-token"}, HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, err := json.Marshal(handler(request))
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})}})
	if err != nil {
		t.Fatal(err)
	}
	return New(client)
}

func form(t *testing.T, r *http.Request) url.Values {
	t.Helper()
	if err := r.ParseForm(); err != nil {
		t.Fatal(err)
	}
	if r.Form.Get("authToken") != "test-token" {
		t.Fatal("missing authentication")
	}
	return r.Form
}

func patch(key string, value any) map[string]any {
	return map[string]any{"key": key, "onyxMethod": "set", "value": value}
}
func ok(patches ...map[string]any) map[string]any {
	values := make([]any, len(patches))
	for i, p := range patches {
		values[i] = p
	}
	return map[string]any{"jsonCode": 200, "onyxData": values}
}

func TestCreateExpenseUsesPersonalTrackExpenseContract(t *testing.T) {
	calls := 0
	service := testService(t, func(r *http.Request) map[string]any {
		calls++
		values := form(t, r)
		switch calls {
		case 1:
			if r.URL.Path != "/api/OpenApp" || values.Get("policyIDList") != "[]" {
				t.Fatalf("bad preflight %s %v", r.URL.Path, values)
			}
			return ok(patch("selfDMReportID", "9007199254740000"))
		case 2:
			if r.URL.Path != "/api/TrackExpense" {
				t.Fatalf("bad command %s", r.URL.Path)
			}
			for key, want := range map[string]string{"amount": "1250", "currency": "EUR", "merchant": "Cafe", "created": "2026-10-17", "chatReportID": "9007199254740000", "billable": "false", "taxAmount": "0"} {
				if values.Get(key) != want {
					t.Errorf("%s=%q want %q", key, values.Get(key), want)
				}
			}
			for _, key := range []string{"transactionID", "reportActionID", "transactionThreadReportID", "createdReportActionIDForThread"} {
				if !decimalIDPattern.MatchString(values.Get(key)) {
					t.Errorf("missing ID %s", key)
				}
			}
			if values.Has("policyID") || values.Has("iouReportID") {
				t.Fatal("unsupported personal tracking fields")
			}
		default:
			t.Fatal("unexpected request")
		}
		return ok()
	})
	result, err := service.Execute(context.Background(), "expense", "create", map[string]string{"amount": "1250", "currency": "EUR", "merchant": "Cafe", "date": "2026-10-17", "billable": "false"})
	if err != nil {
		t.Fatal(err)
	}
	if result.(Mutation).ID == "" || calls != 2 {
		t.Fatalf("bad result %#v calls%d", result, calls)
	}
}

func TestCreateExpenseCreatesMissingSelfDM(t *testing.T) {
	calls := 0
	service := testService(t, func(r *http.Request) map[string]any {
		calls++
		v := form(t, r)
		if calls == 1 {
			return ok()
		}
		if v.Get("chatReportID") != "" || v.Get("optimisticReportID") == "" || v.Get("optimisticReportActionID") == "" {
			t.Fatal("missing optimistic self DM creation")
		}
		return ok()
	})
	_, err := service.Execute(context.Background(), "expense", "create", map[string]string{"amount": "1", "currency": "USD", "merchant": "A", "date": "2026-01-01"})
	if err != nil {
		t.Fatal(err)
	}
}

func TestEditUsesUpdatesJSONAndExplicitFalseAndClear(t *testing.T) {
	service := testService(t, func(r *http.Request) map[string]any {
		v := form(t, r)
		if r.URL.Path != "/api/UpdateMoneyRequest" || v.Get("transactionID") != "123" || v.Has("merchant") {
			t.Fatalf("bad update %s %v", r.URL.Path, v)
		}
		var updates map[string]any
		if err := json.Unmarshal([]byte(v.Get("updates")), &updates); err != nil {
			t.Fatal(err)
		}
		if updates["merchant"] != "Cafe" || updates["category"] != "" || updates["billable"] != false {
			t.Fatalf("bad updates %#v", updates)
		}
		return ok()
	})
	_, err := service.Execute(context.Background(), "expense", "edit", map[string]string{"expense-id": "123", "merchant": "Cafe", "category": "", "billable": "false"})
	if err != nil {
		t.Fatal(err)
	}
}

func TestEditChangesOnlyProvidedField(t *testing.T) {
	for _, field := range []string{"merchant", "comment"} {
		t.Run(field, func(t *testing.T) {
			value := "Cafe"
			if field == "comment" {
				value = ""
			}
			service := testService(t, func(r *http.Request) map[string]any {
				v := form(t, r)
				var updates map[string]any
				if err := json.Unmarshal([]byte(v.Get("updates")), &updates); err != nil {
					t.Fatal(err)
				}
				if len(updates) != 1 || updates[field] != value {
					t.Fatalf("unexpected changes %#v", updates)
				}
				return ok()
			})
			if _, err := service.Execute(context.Background(), "expense", "edit", map[string]string{"expense-id": "123", field: value}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCreateDateDefaultsWithoutMutatingInput(t *testing.T) {
	values := map[string]string{"amount": "1", "currency": "USD", "merchant": "Cafe"}
	calls := 0
	service := testService(t, func(r *http.Request) map[string]any {
		calls++
		v := form(t, r)
		if calls == 1 {
			return ok(patch("selfDMReportID", "123"))
		}
		if v.Get("created") != time.Now().Format("2006-01-02") {
			t.Fatalf("created=%q", v.Get("created"))
		}
		return ok()
	})
	if _, err := service.Execute(context.Background(), "expense", "create", values); err != nil {
		t.Fatal(err)
	}
	if _, ok := values["date"]; ok {
		t.Fatal("caller values mutated")
	}
}

func TestSearchDefaultHashesMatchOfficialApp(t *testing.T) {
	for kind, want := range map[string]string{"expense": "959171759", "expense-report": "1657503970"} {
		if got := searchHash(kind, nil); got != want {
			t.Fatalf("%s hash=%s want%s", kind, got, want)
		}
	}
}

func TestSearchHumanFilters(t *testing.T) {
	service := testService(t, func(r *http.Request) map[string]any {
		v := form(t, r)
		query := v.Get("jsonQuery")
		for _, fragment := range []string{`"left":"status"`, `"right":"unreported"`, `"left":"date"`, `"operator":"gte"`, `"operator":"lte"`, `"left":"merchant"`, `"operator":"contains"`} {
			if !strings.Contains(query, fragment) {
				t.Errorf("missing %s in %s", fragment, query)
			}
		}
		return ok(patch("snapshot_"+v.Get("hash"), map[string]any{"search": map[string]any{"offset": 0, "hasMoreResults": false}, "data": map[string]any{}}))
	})
	if _, err := service.Execute(context.Background(), "expense", "list", map[string]string{"status": "unreported", "since": "2026-10-01", "until": "2026-10-17", "merchant": "Cafe"}); err != nil {
		t.Fatal(err)
	}
	if err := Validate("expense", "list", map[string]string{"since": "2026-10-17", "until": "2026-10-01"}); err == nil {
		t.Fatal("inverted date range accepted")
	}
}

func TestDeleteResolvesOriginalAction(t *testing.T) {
	calls := 0
	service := testService(t, func(r *http.Request) map[string]any {
		calls++
		v := form(t, r)
		if calls == 1 {
			return ok(patch("reportActions_456", map[string]any{"789": map[string]any{"reportActionID": "789", "originalMessage": map[string]any{"IOUTransactionID": "123"}}}))
		}
		if r.URL.Path != "/api/DeleteMoneyRequest" || v.Get("reportActionID") != "789" {
			t.Fatalf("unsafe delete %s %v", r.URL.Path, v)
		}
		return ok()
	})
	_, err := service.Execute(context.Background(), "expense", "delete", map[string]string{"expense-id": "123", "report-id": "456"})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
}

func TestDeleteWithoutOriginalActionDoesNotWrite(t *testing.T) {
	calls := 0
	service := testService(t, func(r *http.Request) map[string]any { calls++; return ok() })
	_, err := service.Execute(context.Background(), "expense", "delete", map[string]string{"expense-id": "123", "report-id": "456"})
	if err == nil || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestReportWriteContracts(t *testing.T) {
	for _, tc := range []struct {
		verb, endpoint, idKey string
		values                map[string]string
	}{
		{"create", "CreateAppReport", "reportActionID", map[string]string{"name": "October", "policy-id": "ABC"}},
		{"submit", "SubmitReport", "reportActionID", map[string]string{"report-id": "456", "manager-email": "manager@example.com"}},
		{"approve", "ApproveMoneyRequest", "approvedReportActionID", map[string]string{"report-id": "456"}},
		{"comment", "AddComment", "reportActionID", map[string]string{"report-id": "456", "text": "Ready"}},
	} {
		t.Run(tc.verb, func(t *testing.T) {
			service := testService(t, func(r *http.Request) map[string]any {
				v := form(t, r)
				if r.URL.Path != "/api/"+tc.endpoint || !decimalIDPattern.MatchString(v.Get(tc.idKey)) {
					t.Fatalf("bad request %s %v", r.URL.Path, v)
				}
				if tc.verb == "create" && (v.Get("type") != "expense" || v.Get("reportPreviewReportActionID") == "" || v.Get("reportName") != "October") {
					t.Fatal("bad report creation")
				}
				if tc.verb == "comment" && v.Get("reportComment") != "Ready" {
					t.Fatal("missing comment")
				}
				return ok()
			})
			if _, err := service.Execute(context.Background(), "report", tc.verb, tc.values); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSearchPaginationAndLargeIDs(t *testing.T) {
	calls := 0
	service := testService(t, func(r *http.Request) map[string]any {
		calls++
		v := form(t, r)
		if r.URL.Path != "/api/Search" {
			t.Fatal(r.URL.Path)
		}
		var q map[string]any
		if err := json.Unmarshal([]byte(v.Get("jsonQuery")), &q); err != nil {
			t.Fatal(err)
		}
		offset := int(q["offset"].(float64))
		if offset != (calls-1)*50 {
			t.Fatalf("offset=%d", offset)
		}
		data := map[string]any{}
		for i := 0; i < 50; i++ {
			id := fmt.Sprintf("922337203685477%04d", offset+i)
			data["transactions_"+id] = map[string]any{"transactionID": id, "amount": -1234, "currency": "USD", "merchant": "Cafe", "created": "2026-10-17"}
		}
		return ok(patch("snapshot_"+v.Get("hash"), map[string]any{"search": map[string]any{"offset": offset, "hasMoreResults": calls < 2}, "data": data}))
	})
	result, err := service.Execute(context.Background(), "expense", "list", map[string]string{"limit": "70"})
	if err != nil {
		t.Fatal(err)
	}
	page := result.(Page[Expense])
	if len(page.Items) != 70 || !page.HasMore || calls != 2 {
		t.Fatalf("items%d more%v calls%d", len(page.Items), page.HasMore, calls)
	}
	if len(page.Items[0].ID) != 19 || page.Items[0].Amount != 1234 {
		t.Fatalf("bad normalization %#v", page.Items[0])
	}
}

func TestSearchRejectsMissingSnapshot(t *testing.T) {
	service := testService(t, func(r *http.Request) map[string]any { return ok() })
	if _, err := service.Execute(context.Background(), "expense", "list", nil); err == nil {
		t.Fatal("unexpected response accepted as empty list")
	}
}

func TestResourceReadsValidateMonetaryValues(t *testing.T) {
	for _, tc := range []struct {
		name, noun string
		fields     map[string]any
		iou        bool
		want       int64
		wantError  bool
	}{
		{name: "malformed amount", noun: "expense", fields: map[string]any{"amount": "invalid"}, wantError: true},
		{name: "fractional amount", noun: "expense", fields: map[string]any{"amount": json.Number("1.5")}, wantError: true},
		{name: "overflow amount", noun: "expense", fields: map[string]any{"amount": "9223372036854775808"}, wantError: true},
		{name: "sign overflow", noun: "expense", fields: map[string]any{"amount": json.Number("-9223372036854775808")}, wantError: true},
		{name: "iou sign overflow", noun: "expense", fields: map[string]any{"amount": json.Number("-9223372036854775808")}, iou: true, wantError: true},
		{name: "malformed modified amount", noun: "expense", fields: map[string]any{"amount": -100, "modifiedAmount": "invalid"}, wantError: true},
		{name: "overflow modified amount", noun: "expense", fields: map[string]any{"amount": -100, "modifiedAmount": "9223372036854775808"}, wantError: true},
		{name: "modified sign overflow", noun: "expense", fields: map[string]any{"amount": -100, "modifiedAmount": json.Number("-9223372036854775808")}, wantError: true},
		{name: "zero amount", noun: "expense", fields: map[string]any{"amount": 0}},
		{name: "missing amount", noun: "expense"},
		{name: "null amount", noun: "expense", fields: map[string]any{"amount": nil}},
		{name: "null modified amount", noun: "expense", fields: map[string]any{"amount": -100, "modifiedAmount": nil}, want: 100},
		{name: "empty modified amount", noun: "expense", fields: map[string]any{"amount": -100, "modifiedAmount": ""}, want: 100},
		{name: "empty modified amount with zero base", noun: "expense", fields: map[string]any{"amount": 0, "modifiedAmount": ""}},
		{name: "empty modified amount with missing base", noun: "expense", fields: map[string]any{"modifiedAmount": ""}},
		{name: "modified zero", noun: "expense", fields: map[string]any{"amount": -100, "modifiedAmount": 0}},
		{name: "modified overrides malformed base", noun: "expense", fields: map[string]any{"amount": "invalid", "modifiedAmount": -75}, want: 75},
		{name: "credit", noun: "expense", fields: map[string]any{"amount": 100}, want: -100},
		{name: "iou spending", noun: "expense", fields: map[string]any{"amount": 100}, iou: true, want: 100},
		{name: "malformed total", noun: "report", fields: map[string]any{"total": "invalid"}, wantError: true},
		{name: "overflow total", noun: "report", fields: map[string]any{"total": "9223372036854775808"}, wantError: true},
		{name: "zero total", noun: "report", fields: map[string]any{"total": 0}},
		{name: "missing total", noun: "report"},
		{name: "null total", noun: "report", fields: map[string]any{"total": nil}},
		{name: "minimum total", noun: "report", fields: map[string]any{"total": json.Number("-9223372036854775808")}, want: -1 << 63},
	} {
		for _, verb := range []string{"view", "list"} {
			t.Run(tc.name+"/"+verb, func(t *testing.T) {
				service := testService(t, func(r *http.Request) map[string]any {
					raw := map[string]any{"type": "expense", "reportID": "456"}
					for key, value := range tc.fields {
						raw[key] = value
					}
					key := "report_456"
					if tc.noun == "expense" {
						key = "transactions_123"
					}
					data := map[string]any{key: raw}
					if tc.iou {
						data["report_456"] = map[string]any{"type": "iou"}
					}
					if verb == "list" {
						values := form(t, r)
						return ok(patch("snapshot_"+values.Get("hash"), map[string]any{"search": map[string]any{"offset": 0, "hasMoreResults": false}, "data": data}))
					}
					patches := []map[string]any{}
					for key, value := range data {
						patches = append(patches, patch(key, value))
					}
					return ok(patches...)
				})
				result, err := service.Execute(context.Background(), tc.noun, verb, map[string]string{"expense-id": "123", "report-id": "456"})
				if tc.wantError {
					var failure *fault.Error
					if !errors.As(err, &failure) || failure.Code != "API-004" {
						t.Fatalf("got %#v, %v; want API-004", result, err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				var amount int64
				switch value := result.(type) {
				case Expense:
					amount = value.Amount
				case Report:
					amount = value.Total
				case Page[Expense]:
					if len(value.Items) != 1 {
						t.Fatalf("unexpected expenses: %#v", value)
					}
					amount = value.Items[0].Amount
				case Page[Report]:
					if len(value.Items) != 1 {
						t.Fatalf("unexpected reports: %#v", value)
					}
					amount = value.Items[0].Total
				default:
					t.Fatalf("unexpected result: %#v", result)
				}
				if amount != tc.want {
					t.Fatalf("amount = %d; want %d", amount, tc.want)
				}
			})
		}
	}
}

func TestSnapshotPatchOrderAndModifiedAmounts(t *testing.T) {
	response := ok(patch("transactions_123", map[string]any{"transactionID": "123", "amount": json.Number("-100"), "currency": "USD", "comment": map[string]any{"comment": "old"}}), map[string]any{"key": "transactions_123", "onyxMethod": "merge", "value": map[string]any{"modifiedAmount": json.Number("75"), "modifiedCurrency": "EUR", "comment": map[string]any{"comment": "credit"}}}, map[string]any{"key": "transactions_", "onyxMethod": "mergecollection", "value": map[string]any{"transactions_124": map[string]any{"amount": json.Number("-20")}}})
	state, err := decodeSnapshot(response)
	if err != nil {
		t.Fatal(err)
	}
	e, err := expense("123", object(state["transactions_123"]))
	if err != nil {
		t.Fatal(err)
	}
	if e.Amount != -75 || e.Currency != "EUR" || e.Comment != "credit" {
		t.Fatalf("bad merged transaction %#v", e)
	}
	if len(keys(state, "transactions_")) != 2 {
		t.Fatal("collection merge missing")
	}
}

func TestInvalidWritesFailBeforeNetwork(t *testing.T) {
	for _, values := range []map[string]string{{"amount": "0", "currency": "USD", "merchant": "Cafe", "date": "2026-10-17"}, {"amount": "1.25", "currency": "USD", "merchant": "Cafe", "date": "2026-10-17"}, {"amount": "1", "currency": "usd", "merchant": "Cafe", "date": "2026-10-17"}, {"amount": "1", "currency": "USD", "merchant": "Cafe", "date": "2026-02-30"}} {
		service := testService(t, func(*http.Request) map[string]any { t.Fatal("invalid input reached network"); return nil })
		if _, err := service.Execute(context.Background(), "expense", "create", values); err == nil {
			t.Errorf("accepted %#v", values)
		}
	}
}

func TestReceiptValidationAndMultipartUpload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receipt.pdf")
	if err := os.WriteFile(path, []byte("%PDF-1.7\nexample receipt"), 0600); err != nil {
		t.Fatal(err)
	}
	service := testService(t, func(r *http.Request) map[string]any {
		if r.URL.Path != "/api/ReplaceReceipt" {
			t.Fatal(r.URL.Path)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		if r.FormValue("transactionID") != "123" || r.FormValue("receiptState") != "SCANREADY" {
			t.Fatal("bad receipt fields")
		}
		file, header, err := r.FormFile("receipt")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		if header.Filename != "receipt.pdf" {
			t.Fatal(header.Filename)
		}
		return ok()
	})
	if _, err := service.Execute(context.Background(), "expense", "attach", map[string]string{"expense-id": "123", "file": path}); err != nil {
		t.Fatal(err)
	}
	badPath := filepath.Join(t.TempDir(), "receipt.png")
	if err := os.WriteFile(badPath, []byte("not an image"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Validate("expense", "attach", map[string]string{"expense-id": "123", "file": badPath}); err == nil {
		t.Fatal("invalid receipt accepted")
	}
}

func TestQueryValidation(t *testing.T) {
	for _, query := range []string{`{"operator":"eq","left":"merchant","right":"Cafe"}`, `{"operator":"and","left":{"operator":"eq","left":"currency","right":"EUR"},"right":{"operator":"gte","left":"date","right":"2026-10-01"}}`} {
		if err := Validate("expense", "list", map[string]string{"query": query}); err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range []string{"merchant:Cafe", `{"operator":"eq","left":"merchant"}`, `{"operator":"delete","left":"merchant","right":"Cafe"}`} {
		if err := Validate("expense", "list", map[string]string{"query": query}); err == nil {
			t.Errorf("accepted %s", query)
		}
	}
}

func TestRandomIDsStayInsideOfficialBounds(t *testing.T) {
	for _, bits := range []uint{53, 63} {
		id, err := randomID(bits)
		if err != nil {
			t.Fatal(err)
		}
		n, err := strconv.ParseUint(id, 10, 64)
		if err != nil || n == 0 || n > 1<<bits-1 {
			t.Fatalf("bad %d-bit ID %s", bits, id)
		}
	}
}
