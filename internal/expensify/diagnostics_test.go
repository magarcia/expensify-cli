package expensify

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/magarcia/expensify-cli/internal/fault"
)

func TestExpenseListResponseErrorsIdentifyTheFailedContract(t *testing.T) {
	const privateValue = "private-response-value"
	for _, tc := range []struct {
		name, reason string
		response     func(string) map[string]any
	}{
		{"missing Onyx data", "onyxData is not an array", func(string) map[string]any {
			return map[string]any{"jsonCode": 200, "onyxData": privateValue}
		}},
		{"unsupported update", "unsupported Onyx update method", func(string) map[string]any {
			return ok(map[string]any{"key": privateValue, "onyxMethod": privateValue, "value": privateValue})
		}},
		{"missing snapshot", "Search snapshot is missing", func(string) map[string]any {
			return ok()
		}},
		{"missing metadata", "Search metadata is not an object", func(hash string) map[string]any {
			return ok(patch("snapshot_"+hash, map[string]any{"data": map[string]any{}}))
		}},
		{"missing data", "Search data is not an object", func(hash string) map[string]any {
			return ok(patch("snapshot_"+hash, map[string]any{"search": map[string]any{"offset": 0, "hasMoreResults": false}}))
		}},
		{"invalid has more", "Search hasMoreResults is not a boolean", func(hash string) map[string]any {
			return ok(patch("snapshot_"+hash, map[string]any{"search": map[string]any{"offset": 0, "hasMoreResults": privateValue}, "data": map[string]any{}}))
		}},
		{"invalid offset", "Search offset does not match the requested page", func(hash string) map[string]any {
			return ok(patch("snapshot_"+hash, map[string]any{"search": map[string]any{"offset": privateValue, "hasMoreResults": false}, "data": map[string]any{}}))
		}},
		{"missing resources", "Search count is positive but expected resources are missing", func(hash string) map[string]any {
			return ok(patch("snapshot_"+hash, map[string]any{"search": map[string]any{"offset": 0, "hasMoreResults": false, "count": 1}, "data": map[string]any{}}))
		}},
		{"invalid amount", "expense amount is not a signed 64-bit integer", func(hash string) map[string]any {
			return ok(patch("snapshot_"+hash, map[string]any{"search": map[string]any{"offset": 0, "hasMoreResults": false}, "data": map[string]any{"transactions_123": map[string]any{"amount": privateValue}}}))
		}},
		{"invalid modified amount", "expense modifiedAmount is not a signed 64-bit integer", func(hash string) map[string]any {
			return ok(patch("snapshot_"+hash, map[string]any{"search": map[string]any{"offset": 0, "hasMoreResults": false}, "data": map[string]any{"transactions_123": map[string]any{"amount": -100, "modifiedAmount": privateValue}}}))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := testService(t, func(request *http.Request) map[string]any {
				return tc.response(form(t, request).Get("hash"))
			})
			_, err := service.Execute(context.Background(), "expense", "list", nil)
			var failure *fault.Error
			if !errors.As(err, &failure) || failure.Code != "API-004" || !strings.Contains(failure.Message, tc.reason) {
				t.Fatalf("expected API-004 identifying %q; got %v", tc.reason, err)
			}
			if strings.Contains(failure.Message+failure.Hint, privateValue) {
				t.Fatal("response error exposed private data")
			}
		})
	}
}
