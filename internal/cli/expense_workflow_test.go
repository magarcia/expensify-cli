package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExpenseWorkflowDryRunsAreOffline(t *testing.T) {
	clearAuthenticationEnvironment(t)
	t.Setenv("EXPENSIFY_CONFIG_DIR", privateTempDir(t))
	receipt := filepath.Join(t.TempDir(), "receipt.pdf")
	if err := os.WriteFile(receipt, []byte("%PDF-1.7\nreceipt"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"expense", "create", "--amount", "1250", "--currency", "USD", "--merchant", "Software", "--policy-id", "ABC", "--receipt", receipt},
		{"expense", "create", "--amount", "1250", "--merchant", "Software", "--report-id", "456", "--category", "Software License Expense", "--tag", "Engineering:Software Stipend"},
		{"expense", "move", "--expense-id", "123", "--report-id", "456"},
	} {
		code, out, stderr := invoke(t, append(args, "--dry-run", "--no-input", "--output", "json")...)
		if code != 0 || stderr != "" {
			t.Fatalf("%v: code=%d stderr=%q", args, code, stderr)
		}
		var preview struct {
			DryRun     bool              `json:"dryRun"`
			Parameters map[string]string `json:"parameters"`
		}
		if err := json.Unmarshal([]byte(out), &preview); err != nil || !preview.DryRun {
			t.Fatalf("invalid preview %q: %v", out, err)
		}
		if args[1] == "move" && preview.Parameters["report-id"] != "456" {
			t.Fatalf("destination missing: %s", out)
		}
		code, out, stderr = invoke(t, append(args, "--dry-run", "--no-input", "--output", "llm")...)
		if code != 0 || stderr != "" || !strings.Contains(out, "dryRun: true") {
			t.Fatalf("invalid agent preview: exit=%d out=%q stderr=%q", code, out, stderr)
		}
	}
}

func TestExpenseWorkflowRejectsInvalidDryRuns(t *testing.T) {
	t.Setenv("EXPENSIFY_CONFIG_DIR", privateTempDir(t))
	for _, args := range [][]string{
		{"expense", "move", "--expense-id", "123"},
		{"expense", "move", "--expense-id", "123", "--report-id", "invalid"},
		{"expense", "create", "--amount", "1", "--merchant", "Software", "--receipt", "missing.pdf"},
	} {
		code, out, _ := invoke(t, append(args, "--dry-run", "--no-input")...)
		if code != 2 || out != "" {
			t.Fatalf("%v: code=%d out=%q", args, code, out)
		}
	}
}

func TestExpenseWorkflowHelpAndCompletion(t *testing.T) {
	for _, args := range [][]string{{"expense", "create", "--help"}, {"__complete", "expense", "create", "--"}} {
		code, out, stderr := invoke(t, args...)
		for _, flag := range []string{"--policy-id", "--report-id", "--receipt"} {
			if code != 0 || stderr != "" || !strings.Contains(out, flag) {
				t.Fatalf("%v missing %s: code=%d out=%q stderr=%q", args, flag, code, out, stderr)
			}
		}
	}
	code, out, stderr := invoke(t, "expense", "move", "--help")
	if code != 0 || stderr != "" || !strings.Contains(out, "--report-id") {
		t.Fatalf("move help: code=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestReportTargetOverridesDefaultWorkspace(t *testing.T) {
	t.Setenv("EXPENSIFY_CONFIG_DIR", privateTempDir(t))
	t.Setenv("EXPENSIFY_POLICY_ID", "DEFAULT")
	for _, tc := range []struct {
		name, policy string
		flags        []string
	}{
		{"default workspace", "DEFAULT", nil},
		{"report infers workspace", "", []string{"--report-id", "456"}},
		{"explicit workspace preserved", "EXPLICIT", []string{"--report-id", "456", "--policy-id", "EXPLICIT"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"expense", "create", "--amount", "1", "--merchant", "Software", "--dry-run", "--json"}
			code, out, stderr := invoke(t, append(args, tc.flags...)...)
			if code != 0 || stderr != "" {
				t.Fatalf("exit=%d stderr=%q", code, stderr)
			}
			var preview struct {
				Parameters map[string]string `json:"parameters"`
			}
			if err := json.Unmarshal([]byte(out), &preview); err != nil {
				t.Fatal(err)
			}
			if got := preview.Parameters["policy-id"]; got != tc.policy {
				t.Fatalf("policy-id=%q want %q", got, tc.policy)
			}
		})
	}
}
