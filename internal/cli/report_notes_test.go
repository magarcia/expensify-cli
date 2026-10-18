package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReportNotesDryRunsPreserveExplicitValues(t *testing.T) {
	clearAuthenticationEnvironment(t)
	t.Setenv("EXPENSIFY_CONFIG_DIR", privateTempDir(t))
	const notes = "Health, Software License, Books, Courses & Internet\nOctober — invoices"
	for _, tc := range []struct {
		name, text string
		args       []string
	}{
		{"create", notes, []string{"report", "create", "--name", "October", "--policy-id", "ABC", "--notes", notes}},
		{"edit", notes, []string{"report", "edit", "--report-id", "456", "--notes", notes}},
		{"clear", "", []string{"report", "edit", "--report-id", "456", "--notes="}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out, stderr := invoke(t, append(tc.args, "--dry-run", "--no-input", "--output", "json")...)
			if code != 0 || stderr != "" {
				t.Fatalf("code=%d stderr=%q", code, stderr)
			}
			var preview struct {
				DryRun     bool              `json:"dryRun"`
				Parameters map[string]string `json:"parameters"`
			}
			if err := json.Unmarshal([]byte(out), &preview); err != nil {
				t.Fatal(err)
			}
			if got, present := preview.Parameters["notes"]; !preview.DryRun || !present || got != tc.text {
				t.Fatalf("notes changed in preview: %q", out)
			}
			code, out, stderr = invoke(t, append(tc.args, "--dry-run", "--no-input", "--output", "llm")...)
			if code != 0 || stderr != "" || !strings.Contains(out, "dryRun: true") {
				t.Fatalf("agent preview: code=%d out=%q stderr=%q", code, out, stderr)
			}
		})
	}
}

func TestReportNotesEditRequiresNotesAndID(t *testing.T) {
	t.Setenv("EXPENSIFY_CONFIG_DIR", privateTempDir(t))
	t.Setenv("EXPENSIFY_POLICY_ID", "")
	for _, args := range [][]string{{"report", "edit", "--report-id", "456"}, {"report", "edit", "--notes", "October"}, {"report", "create", "--name", "October", "--notes", "Health"}} {
		code, out, _ := invoke(t, append(args, "--dry-run", "--no-input")...)
		if code != 2 || out != "" {
			t.Fatalf("args=%v code=%d out=%q", args, code, out)
		}
	}
}

func TestReportNotesHelpAndCompletion(t *testing.T) {
	for _, verb := range []string{"create", "edit"} {
		for _, args := range [][]string{{"report", verb, "--help"}, {"__complete", "report", verb, "--notes"}} {
			code, out, stderr := invoke(t, args...)
			if code != 0 || stderr != "" || !strings.Contains(out, "--notes") {
				t.Fatalf("args=%v code=%d out=%q stderr=%q", args, code, out, stderr)
			}
		}
	}
}
