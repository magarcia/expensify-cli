// Package expensify provides validated, typed resource operations for New Expensify.
package expensify

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/magarcia/expensify-cli/internal/fault"
)

type Flag struct {
	Name, Usage, Default string
	Required, Bool       bool
}

type Spec struct {
	Name, Summary, Example string
	Flags                  []Flag
	Mutates, Confirm       bool
}

func Specs() []Spec {
	id := func(name, usage string) Flag { return Flag{Name: name, Usage: usage, Required: true} }
	policy := Flag{Name: "policy-id", Usage: "Workspace policy ID"}
	notes := Flag{Name: "notes", Usage: "Report Notes text (use --notes='' to clear optional Notes)"}
	page := []Flag{{Name: "limit", Usage: "Maximum records (1–1000)", Default: "100"}, {Name: "offset", Usage: "Number of matching records to skip", Default: "0"}}
	query := Flag{Name: "query", Usage: `Search filter JSON AST, for example '{"operator":"eq","left":"merchant","right":"Cafe"}'`}
	listFilters := []Flag{query, policy, {Name: "status", Usage: "Expense or report status (for example drafts, outstanding, approved, paid)"}, {Name: "since", Usage: "Include expenses/reports dated on or after YYYY-MM-DD"}, {Name: "until", Usage: "Include expenses/reports dated on or before YYYY-MM-DD"}}
	fields := []Flag{{Name: "amount", Usage: "Positive integer amount in currency minor units (1234 = 12.34 USD)"}, {Name: "currency", Usage: "Three-letter currency code"}, {Name: "merchant", Usage: "Merchant name"}, {Name: "date", Usage: "Expense date (YYYY-MM-DD)"}, {Name: "comment", Usage: "Expense description"}, {Name: "category", Usage: "Workspace category"}, {Name: "tag", Usage: "Workspace tag"}, {Name: "billable", Usage: "Billable expense (true or false)"}, {Name: "reimbursable", Usage: "Reimbursable expense (true or false)"}}
	create := append([]Flag(nil), fields...)
	for i := range create {
		if create[i].Name == "amount" || create[i].Name == "currency" || create[i].Name == "merchant" {
			create[i].Required = true
		}
		if create[i].Name == "date" {
			create[i].Default = time.Now().Format("2006-01-02")
		}
	}
	create = append(create, policy, Flag{Name: "report-id", Usage: "Your draft expense report ID (infers workspace; must match --policy-id if both are set)"}, Flag{Name: "receipt", Usage: "JPEG, PNG, or PDF receipt to upload in the create request (up to 32 MiB)"})
	return []Spec{
		{Name: "expense list", Summary: "List expenses", Example: "expensify expense list --status unreported --limit 20", Flags: append(append(append([]Flag(nil), listFilters...), Flag{Name: "report-id", Usage: "Filter by report ID"}, Flag{Name: "merchant", Usage: "Filter by merchant name"}), page...)},
		{Name: "expense view", Summary: "View one expense", Example: "expensify expense view --expense-id 123", Flags: []Flag{id("expense-id", "Expense transaction ID"), {Name: "report-id", Usage: "Parent report ID (opens the report before looking up the expense)"}}},
		{Name: "expense create", Summary: "Create an expense in your workspace, draft report, or personal account", Example: "expensify expense create --amount 1250 --currency USD --merchant 'Software' --report-id 456 --receipt receipt.pdf", Flags: create, Mutates: true},
		{Name: "expense move", Summary: "Move your unreported or draft expense into your draft report", Example: "expensify expense move --expense-id 123 --report-id 456", Flags: []Flag{id("expense-id", "Expense transaction ID"), id("report-id", "Destination draft expense report ID")}, Mutates: true},
		{Name: "expense edit", Summary: "Update an expense", Example: "expensify expense edit --expense-id 123 --merchant 'Cafe'", Flags: append([]Flag{id("expense-id", "Expense transaction ID")}, fields...), Mutates: true},
		{Name: "expense delete", Summary: "Delete an expense", Example: "expensify expense delete --expense-id 123 --report-id 456 --yes", Flags: []Flag{id("expense-id", "Expense transaction ID"), id("report-id", "Parent report ID")}, Mutates: true, Confirm: true},
		{Name: "expense attach", Summary: "Attach a receipt to an expense", Example: "expensify expense attach --expense-id 123 --file receipt.jpg", Flags: []Flag{id("expense-id", "Expense transaction ID"), id("file", "Receipt file path")}, Mutates: true},
		{Name: "report list", Summary: "List expense reports", Example: "expensify report list --status drafts --limit 20", Flags: append(append([]Flag(nil), listFilters...), page...)},
		{Name: "report view", Summary: "View one report", Example: "expensify report view --report-id 456", Flags: []Flag{id("report-id", "Report ID")}},
		{Name: "report create", Summary: "Create an expense report", Example: "expensify report create --name 'October expenses' --policy-id ABC --notes 'Health, Software License, Books, Courses & Internet'", Flags: []Flag{id("name", "Report name"), policy, {Name: "owner-email", Usage: "Expense report owner email"}, notes}, Mutates: true},
		{Name: "report edit", Summary: "Set or clear an expense report's Notes", Example: "expensify report edit --report-id 456 --notes 'Health, Software License, Books, Courses & Internet'", Flags: []Flag{id("report-id", "Report ID"), notes}, Mutates: true},
		{Name: "report submit", Summary: "Submit an expense report", Example: "expensify report submit --report-id 456 --yes", Flags: []Flag{id("report-id", "Report ID"), {Name: "manager-email", Usage: "Approver email"}}, Mutates: true, Confirm: true},
		{Name: "report approve", Summary: "Approve an expense report", Example: "expensify report approve --report-id 456 --yes", Flags: []Flag{id("report-id", "Report ID")}, Mutates: true, Confirm: true},
		{Name: "report comment", Summary: "Add a plain-text comment to a report", Example: "expensify report comment --report-id 456 --text 'Ready for review'", Flags: []Flag{id("report-id", "Report ID"), id("text", "Comment text")}, Mutates: true},
		{Name: "workspace list", Summary: "List workspaces", Example: "expensify workspace list"},
		{Name: "workspace view", Summary: "View one workspace", Example: "expensify workspace view --policy-id ABC", Flags: []Flag{id("policy-id", "Workspace policy ID")}},
		{Name: "category list", Summary: "List workspace categories", Example: "expensify category list --policy-id ABC", Flags: []Flag{id("policy-id", "Workspace policy ID")}},
		{Name: "tag list", Summary: "List workspace tags", Example: "expensify tag list --policy-id ABC", Flags: []Flag{id("policy-id", "Workspace policy ID")}},
		{Name: "user view", Summary: "View the authenticated account", Example: "expensify user view"},
	}
}

var currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)
var decimalIDPattern = regexp.MustCompile(`^[1-9][0-9]*$`)

// Validate checks all user input without accessing the network. Both live and
// dry-run commands use this path, so invalid writes cannot appear executable.
func Validate(noun, verb string, values map[string]string) error {
	var spec *Spec
	for _, candidate := range Specs() {
		if candidate.Name == noun+" "+verb {
			copy := candidate
			spec = &copy
			break
		}
	}
	if spec == nil {
		return fault.Usage("Unknown resource command.")
	}
	for _, f := range spec.Flags {
		value := values[f.Name]
		if f.Required && strings.TrimSpace(value) == "" {
			return fault.Usage(fmt.Sprintf("--%s is required.", f.Name))
		}
		if strings.ContainsRune(value, '\x00') {
			return fault.Usage(fmt.Sprintf("--%s cannot contain a NUL character.", f.Name))
		}
	}
	for _, key := range []string{"expense-id", "report-id"} {
		if value := values[key]; value != "" && !decimalIDPattern.MatchString(value) {
			return fault.Usage(fmt.Sprintf("--%s must be a positive decimal ID.", key))
		}
	}
	if amount := values["amount"]; amount != "" {
		n, err := strconv.ParseInt(amount, 10, 64)
		if err != nil || n <= 0 {
			return fault.Usage("--amount must be a positive integer in currency minor units.")
		}
	}
	if currency := values["currency"]; currency != "" && !currencyPattern.MatchString(currency) {
		return fault.Usage("--currency must be a three-letter uppercase code, such as USD or EUR.")
	}
	if date := values["date"]; date != "" {
		if _, err := time.Parse("2006-01-02", date); err != nil {
			return fault.Usage("--date must be a valid date in YYYY-MM-DD format.")
		}
	}
	for _, key := range []string{"since", "until"} {
		if value := values[key]; value != "" {
			if _, err := time.Parse("2006-01-02", value); err != nil {
				return fault.Usage(fmt.Sprintf("--%s must be a valid date in YYYY-MM-DD format.", key))
			}
		}
	}
	if values["since"] != "" && values["until"] != "" && values["since"] > values["until"] {
		return fault.Usage("--since must be on or before --until.")
	}
	if status := values["status"]; status != "" {
		allowed := map[string]bool{"drafts": true, "outstanding": true, "approved": true, "done": true, "paid": true}
		if noun == "expense" {
			allowed["unreported"] = true
			allowed["deleted"] = true
		}
		if !allowed[status] {
			return fault.Usage("--status must use an official New Expensify status: drafts, outstanding, approved, done, paid (plus unreported or deleted for expenses).")
		}
	}
	for _, key := range []string{"billable", "reimbursable"} {
		if v := values[key]; v != "" && v != "true" && v != "false" {
			return fault.Usage(fmt.Sprintf("--%s must be true or false.", key))
		}
	}
	for _, key := range []string{"limit", "offset"} {
		if v := values[key]; v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 || key == "limit" && (n < 1 || n > 1000) {
				return fault.Usage(fmt.Sprintf("--%s is outside its allowed range.", key))
			}
		}
	}
	if noun == "expense" && verb == "edit" {
		changed := false
		for _, key := range []string{"amount", "currency", "merchant", "date", "comment", "category", "tag", "billable", "reimbursable"} {
			if value, ok := values[key]; ok {
				if value == "" && key != "comment" && key != "category" && key != "tag" {
					return fault.Usage(fmt.Sprintf("--%s cannot be empty.", key))
				}
				changed = true
			}
		}
		if !changed {
			return fault.Usage("Specify at least one expense field to change.")
		}
	}
	if noun == "report" && verb == "edit" {
		if _, present := values["notes"]; !present {
			return fault.Usage("--notes is required; use --notes='' to clear the report's Notes.")
		}
	}
	if noun == "report" && verb == "create" {
		if _, present := values["notes"]; present && values["policy-id"] == "" {
			return fault.Usage("--policy-id is required when creating a report with --notes.")
		}
	}
	if query := values["query"]; query != "" {
		var filter any
		if err := json.Unmarshal([]byte(query), &filter); err != nil || !validFilter(filter, 0) {
			return fault.Usage("--query must be a valid search filter JSON AST using eq, gte, lte, contains, and, or or.")
		}
	}
	for _, flag := range []string{"file", "receipt"} {
		if path := values[flag]; path != "" {
			if err := validateReceiptFile(flag, path); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateReceiptFile(flag, path string) error {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return fault.Usage(fmt.Sprintf("--%s must point to a readable regular receipt file.", flag))
	}
	if info.Size() == 0 {
		return fault.Usage("The receipt file is empty.")
	}
	if info.Size() > 32<<20 {
		return fault.Usage("The receipt file exceeds the 32 MiB upload limit.")
	}
	file, err := os.Open(path)
	if err != nil {
		return fault.Usage("The receipt file cannot be read.")
	}
	defer file.Close()
	header := make([]byte, 512)
	n, _ := file.Read(header)
	mime := http.DetectContentType(header[:n])
	ext := strings.ToLower(filepath.Ext(path))
	if !((ext == ".jpg" || ext == ".jpeg") && mime == "image/jpeg" || ext == ".png" && mime == "image/png" || ext == ".pdf" && mime == "application/pdf") {
		return fault.Usage("The receipt must be a JPEG, PNG, or PDF file with a matching extension.")
	}
	return nil
}

func validFilter(v any, depth int) bool {
	if depth > 20 {
		return false
	}
	f, ok := v.(map[string]any)
	if !ok || len(f) != 3 {
		return false
	}
	switch f["operator"] {
	case "and", "or":
		return validFilter(f["left"], depth+1) && validFilter(f["right"], depth+1)
	case "eq", "gte", "lte", "contains":
		left, ok := f["left"].(string)
		if !ok || left == "" {
			return false
		}
		switch right := f["right"].(type) {
		case string:
			return right != ""
		case []any:
			if len(right) == 0 {
				return false
			}
			for _, v := range right {
				if _, ok := v.(string); !ok {
					return false
				}
			}
			return true
		}
	}
	return false
}
