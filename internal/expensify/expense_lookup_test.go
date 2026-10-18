package expensify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/magarcia/expensify-cli/internal/fault"
)

func TestFindExpenseSearchesLaterPagesByExactLargeID(t *testing.T) {
	const id = "7483136939580443951"
	calls := 0
	service := testService(t, func(request *http.Request) map[string]any {
		calls++
		if request.URL.Path != "/api/Search" {
			t.Fatalf("unexpected request: %s", request.URL.Path)
		}
		values := form(t, request)
		var query struct {
			Type    string `json:"type"`
			Offset  int    `json:"offset"`
			Filters any    `json:"filters"`
		}
		if err := json.Unmarshal([]byte(values.Get("jsonQuery")), &query); err != nil {
			t.Fatal(err)
		}
		if query.Type != "expense" || query.Offset != (calls-1)*50 || query.Filters != nil {
			t.Fatalf("unexpected Search query: %#v", query)
		}
		data := map[string]any{
			"transactions_" + id + "0": map[string]any{"transactionID": id + "0", "amount": -999},
		}
		if calls == 2 {
			data["transactions_"+id] = map[string]any{
				"transactionID": json.Number(id), "reportID": "123", "amount": 1250, "currency": "USD", "merchant": "Exact expense",
			}
			data["report_123"] = map[string]any{"type": "iou"}
		} else if calls > 2 {
			t.Fatal("continued searching after finding the expense")
		}
		return ok(patch("snapshot_"+values.Get("hash"), map[string]any{
			"search": map[string]any{"offset": query.Offset, "hasMoreResults": calls == 1},
			"data":   data,
		}))
	})

	result, err := service.findExpense(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || result.ID != id || result.ReportID != "123" || result.Amount != 1250 || result.Merchant != "Exact expense" {
		t.Fatalf("calls = %d, result = %#v", calls, result)
	}
}

func TestFindExpenseReturnsNotFoundAfterFinalSearchPage(t *testing.T) {
	calls := 0
	service := testService(t, func(request *http.Request) map[string]any {
		calls++
		values := form(t, request)
		return ok(patch("snapshot_"+values.Get("hash"), map[string]any{
			"search": map[string]any{"offset": (calls - 1) * 50, "hasMoreResults": calls == 1},
			"data":   map[string]any{},
		}))
	})
	_, err := service.findExpense(context.Background(), "123")
	var failure *fault.Error
	if calls != 2 || !errors.As(err, &failure) || failure.Code != "API-404" {
		t.Fatalf("calls = %d, error = %v; want API-404 after two pages", calls, err)
	}
}

func TestFindExpenseSnapshotPreservesOwnershipFieldsAndRelatedReport(t *testing.T) {
	service := testService(t, func(request *http.Request) map[string]any {
		values := form(t, request)
		return ok(patch("snapshot_"+values.Get("hash"), map[string]any{
			"search": map[string]any{"offset": 0, "hasMoreResults": false},
			"data": map[string]any{
				"transactions_123": map[string]any{"transactionID": "123", "reportID": "456", "cardID": json.Number("9007199254740993"), "amount": -100},
				"report_456":       map[string]any{"type": "expense", "ownerAccountID": 789},
			},
		}))
	})
	state, err := service.findExpenseSnapshot(context.Background(), "123")
	if err != nil {
		t.Fatal(err)
	}
	if cardID := text(object(state["transactions_123"])["cardID"]); cardID != "9007199254740993" {
		t.Fatalf("raw cardID = %q; want exact ownership ID", cardID)
	}
	if report := object(state["report_456"]); text(report["ownerAccountID"]) != "789" || report["type"] != "expense" {
		t.Fatalf("related report missing from found snapshot: %#v", state)
	}
}

func TestFindExpenseStopsCanceledSearch(t *testing.T) {
	for _, cancelBeforeRequest := range []bool{true, false} {
		t.Run(map[bool]string{true: "already canceled", false: "canceled between pages"}[cancelBeforeRequest], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if cancelBeforeRequest {
				cancel()
			}
			calls := 0
			service := testService(t, func(request *http.Request) map[string]any {
				calls++
				if calls > 1 {
					t.Fatal("canceled search requested another page")
				}
				cancel()
				values := form(t, request)
				return ok(patch("snapshot_"+values.Get("hash"), map[string]any{
					"search": map[string]any{"offset": 0, "hasMoreResults": true},
					"data":   map[string]any{},
				}))
			})
			_, err := service.findExpense(ctx, "123")
			wantCalls := 1
			if cancelBeforeRequest {
				wantCalls = 0
			}
			if calls != wantCalls || !errors.Is(err, context.Canceled) {
				t.Fatalf("calls = %d, error = %v; want calls %d and context.Canceled", calls, err, wantCalls)
			}
		})
	}
}
