package expensify

import (
	"context"
	"net/http"
	"testing"
)

func TestReportCreationRejectsInstantSubmissionBeforeWriting(t *testing.T) {
	service := testService(t, func(r *http.Request) map[string]any {
		if r.URL.Path != "/api/OpenApp" {
			t.Fatalf("unsafe report creation sent %s", r.URL.Path)
		}
		return ok(patch("policy_ABC", map[string]any{"autoReporting": true, "autoReportingFrequency": "instant", "approvalMode": "basic"}))
	})
	if _, err := service.Execute(context.Background(), "report", "create", map[string]string{"name": "Monthly draft", "policy-id": "ABC"}); err == nil {
		t.Fatal("instant submission allowed")
	}
}

func TestReportCreationAllowsManualOrScheduledWorkspaces(t *testing.T) {
	for _, frequency := range []string{"manual", "monthly"} {
		t.Run(frequency, func(t *testing.T) {
			writes := 0
			service := testService(t, func(r *http.Request) map[string]any {
				v := form(t, r)
				if r.URL.Path == "/api/OpenApp" {
					return ok(patch("policy_ABC", map[string]any{"autoReporting": true, "autoReportingFrequency": frequency, "approvalMode": "basic"}))
				}
				if r.URL.Path != "/api/CreateAppReport" || v.Get("policyID") != "ABC" || v.Get("reportName") != "Monthly draft" {
					t.Fatalf("unexpected request: %s", r.URL.Path)
				}
				writes++
				return ok()
			})
			if _, err := service.Execute(context.Background(), "report", "create", map[string]string{"name": "Monthly draft", "policy-id": "ABC"}); err != nil || writes != 1 {
				t.Fatalf("writes=%d err=%v", writes, err)
			}
		})
	}
}
