package expensify

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestReportNotesSavedStopsBeforeReadingCanceledContext(t *testing.T) {
	calls := 0
	service := testService(t, func(*http.Request) map[string]any {
		calls++
		return ok()
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if service.reportNotesSaved(ctx, "456", "Notes") || calls != 0 {
		t.Fatalf("canceled verification sent a request or confirmed Notes: calls=%d", calls)
	}
}

func TestReportNotesSavedCancellationStopsSecondRead(t *testing.T) {
	calls := 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service := testService(t, func(r *http.Request) map[string]any {
		form(t, r)
		if r.URL.Path != "/api/OpenReport" {
			t.Fatalf("verification sent a write or loaded unrelated data: %s", r.URL.Path)
		}
		calls++
		time.AfterFunc(25*time.Millisecond, cancel)
		return ok(patch("report_456", notesReportFixture()))
	})
	if service.reportNotesSaved(ctx, "456", "Notes") || calls != 1 || ctx.Err() == nil {
		t.Fatalf("cancellation did not stop the pending verification read: calls=%d err=%v", calls, ctx.Err())
	}
}

func TestReportNotesSavedPreservesEarlierParentDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	wantDeadline, _ := ctx.Deadline()
	service := testService(t, func(r *http.Request) map[string]any {
		deadline, hasDeadline := r.Context().Deadline()
		if !hasDeadline || !deadline.Equal(wantDeadline) {
			t.Fatalf("verification changed the earlier parent deadline: got %v want %v", deadline, wantDeadline)
		}
		raw := notesReportFixture()
		raw["fieldList"] = map[string]any{"expensify_text_notes": notesFieldFixture("text_notes", "Notes")}
		return ok(patch("report_456", raw))
	})
	if !service.reportNotesSaved(ctx, "456", "Notes") {
		t.Fatal("matching Notes were not confirmed under the parent deadline")
	}
}

func TestReportNotesSavedRequiresExplicitAttachedStringValue(t *testing.T) {
	for _, tc := range []struct {
		name    string
		value   any
		omit    bool
		missing bool
		want    string
		saved   bool
	}{
		{name: "explicit empty", value: "", want: "", saved: true},
		{name: "exact string", value: "Software\nInternet", want: "Software\nInternet", saved: true},
		{name: "matching default", omit: true, want: "Default notes"},
		{name: "missing attached field", missing: true, want: "Default notes"},
		{name: "null value", value: nil, want: "Default notes"},
		{name: "numeric value", value: 42, want: "42"},
		{name: "object value", value: map[string]any{"value": "Notes"}, want: "Notes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			service := testService(t, func(r *http.Request) map[string]any {
				deadline, hasDeadline := r.Context().Deadline()
				if !hasDeadline || time.Until(deadline) > 3*time.Second {
					t.Fatalf("verification read lacks its three-second budget: deadline=%v present=%v", deadline, hasDeadline)
				}
				values := form(t, r)
				if r.URL.Path != "/api/OpenReport" || values.Get("reportID") != "456" {
					t.Fatalf("verification sent an unexpected request: %s %v", r.URL.Path, values)
				}
				calls++
				raw := notesReportFixture()
				if !tc.missing {
					field := notesFieldFixture("text_notes", "")
					if tc.omit {
						delete(field, "value")
					} else {
						field["value"] = tc.value
					}
					raw["fieldList"] = map[string]any{"expensify_text_notes": field}
				}
				policy := map[string]any{"fieldList": map[string]any{"expensify_text_notes": notesFieldFixture("text_notes", tc.want)}}
				return ok(patch("report_456", raw), patch("policy_ABC", policy))
			})
			if got := service.reportNotesSaved(context.Background(), "456", tc.want); got != tc.saved {
				t.Fatalf("saved=%v want %v", got, tc.saved)
			}
			wantCalls := 2
			if tc.saved {
				wantCalls = 1
			}
			if calls != wantCalls {
				t.Fatalf("verification made %d reads; want %d", calls, wantCalls)
			}
		})
	}
}
