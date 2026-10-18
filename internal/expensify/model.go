package expensify

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/magarcia/expensify-cli/internal/fault"
)

type Expense struct {
	ID           string `json:"id"`
	ReportID     string `json:"report_id,omitempty"`
	Amount       int64  `json:"amount"`
	Currency     string `json:"currency"`
	Merchant     string `json:"merchant"`
	Date         string `json:"date"`
	Comment      string `json:"comment,omitempty"`
	Category     string `json:"category,omitempty"`
	Tag          string `json:"tag,omitempty"`
	Billable     bool   `json:"billable"`
	Reimbursable bool   `json:"reimbursable"`
	Receipt      any    `json:"receipt,omitempty"`
}

type Report struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	PolicyID       string `json:"policy_id,omitempty"`
	Type           string `json:"type,omitempty"`
	Total          int64  `json:"total"`
	Currency       string `json:"currency,omitempty"`
	State          string `json:"state,omitempty"`
	Status         string `json:"status,omitempty"`
	OwnerAccountID string `json:"owner_account_id,omitempty"`
	ManagerID      string `json:"manager_id,omitempty"`
}

type Workspace struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type,omitempty"`
	Currency string `json:"currency,omitempty"`
	Role     string `json:"role,omitempty"`
	Owner    string `json:"owner,omitempty"`
}

type NamedItem struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Group   string `json:"group,omitempty"`
}

type Page[T any] struct {
	Items   []T  `json:"items"`
	Offset  int  `json:"offset"`
	Limit   int  `json:"limit"`
	HasMore bool `json:"has_more"`
}

type Mutation struct {
	ID       string `json:"id"`
	Action   string `json:"action"`
	ReportID string `json:"report_id,omitempty"`
}

type snapshot map[string]any

func unexpected(reason string) error {
	return fault.New("API-004", "Expensify returned an unexpected resource response: "+reason+".", "Try again later or update the CLI if the New Expensify API has changed.", 1)
}
func notFound(resource, id string) error {
	return fault.New("API-404", fmt.Sprintf("%s %s was not found or is inaccessible.", resource, id), "Check the ID and your account's access.", 4)
}

// decodeSnapshot applies server patches in order. Collection patches and nested
// search snapshots are expanded so callers see the same effective Onyx state.
func decodeSnapshot(response map[string]any) (snapshot, error) {
	patches, ok := response["onyxData"].([]any)
	if !ok {
		return nil, unexpected("onyxData is not an array")
	}
	state := snapshot{}
	for _, entry := range patches {
		patch, ok := entry.(map[string]any)
		if !ok {
			return nil, unexpected("Onyx update is not an object")
		}
		key, keyOK := patch["key"].(string)
		method, methodOK := patch["onyxMethod"].(string)
		value, valueOK := patch["value"]
		if !methodOK || method == "" {
			return nil, unexpected("Onyx update is missing its key, method, or value")
		}
		method = strings.ToLower(method)
		if method != "clear" && method != "multiset" && (!keyOK || key == "" || !valueOK) {
			return nil, unexpected("Onyx update is missing its key, method, or value")
		}
		switch method {
		case "set":
			setSnapshotValue(state, key, value)
		case "merge":
			setSnapshotValue(state, key, merge(state[key], value))
		case "mergecollection", "setcollection":
			collection := onyxObject(value)
			if collection == nil {
				return nil, unexpected("Onyx collection value is not an object")
			}
			if !strings.HasSuffix(key, "_") {
				return nil, unexpected("Onyx collection key is invalid")
			}
			for member := range collection {
				if !strings.HasPrefix(member, key) || member == key {
					return nil, unexpected("Onyx collection member does not match its key")
				}
			}
			if method == "setcollection" {
				for member := range state {
					if strings.HasPrefix(member, key) {
						delete(state, member)
					}
				}
			}
			for k, v := range collection {
				if method == "mergecollection" {
					v = merge(state[k], v)
				}
				setSnapshotValue(state, k, v)
			}
		case "multiset":
			values := object(value)
			if values == nil {
				return nil, unexpected("Onyx multiSet value is not an object")
			}
			for key, value := range values {
				if key == "" {
					return nil, unexpected("Onyx multiSet member has no key")
				}
				setSnapshotValue(state, key, value)
			}
		case "clear":
			// Onyx clears persisted data before queued writes. Each response here
			// starts with empty state, so its writes already have that behavior.
		default:
			return nil, unexpected("unsupported Onyx update method")
		}
	}
	for key, value := range state {
		if !strings.HasPrefix(key, "snapshot_") {
			continue
		}
		if data := onyxObject(object(value)["data"]); data != nil {
			object(value)["data"] = data
			for k, v := range data {
				state[k] = v
			}
		}
	}
	return state, nil
}

func setSnapshotValue(state snapshot, key string, value any) {
	if value == nil {
		delete(state, key)
	} else {
		state[key] = value
	}
}

func onyxObject(value any) map[string]any {
	if result := object(value); result != nil {
		return result
	}
	// PHP can encode an empty object as []; populated arrays are not objects.
	if values, ok := value.([]any); ok && len(values) == 0 {
		return map[string]any{}
	}
	return nil
}

func merge(current, patch any) any {
	if patch == nil {
		return nil
	}
	updates, ok := patch.(map[string]any)
	if !ok {
		return patch
	}
	result := make(map[string]any)
	for k, v := range object(current) {
		result[k] = v
	}
	for k, v := range updates {
		if v == nil {
			delete(result, k)
		} else {
			result[k] = merge(result[k], v)
		}
	}
	return result
}

func object(v any) map[string]any { result, _ := v.(map[string]any); return result }
func text(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	case int64:
		return strconv.FormatInt(v, 10)
	case int:
		return strconv.Itoa(v)
	}
	return ""
}
func integer(v any) int64 { n, _ := strconv.ParseInt(text(v), 10, 64); return n }
func boolean(v any) bool  { b, _ := v.(bool); return b }

func parseMoney(value any, field string) (int64, error) {
	if value == nil {
		return 0, nil
	}
	amount, err := strconv.ParseInt(text(value), 10, 64)
	if err != nil {
		return 0, unexpected(field + " is not a signed 64-bit integer")
	}
	return amount, nil
}

func expense(id string, raw map[string]any) (Expense, error) {
	if value := text(raw["transactionID"]); value != "" {
		id = value
	}
	value := raw["amount"]
	field := "expense amount"
	if modified := raw["modifiedAmount"]; modified != nil && modified != "" {
		value = modified
		field = "expense modifiedAmount"
	}
	amount, err := parseMoney(value, field)
	if err != nil {
		return Expense{}, err
	}
	if amount == math.MinInt64 {
		return Expense{}, unexpected("expense amount cannot be negated without overflow")
	}
	// Expense storage uses negative amounts for spending and positive for credits.
	amount = -amount
	currency := text(raw["currency"])
	if v := text(raw["modifiedCurrency"]); v != "" {
		currency = v
	}
	merchant := text(raw["merchant"])
	if v := text(raw["modifiedMerchant"]); v != "" {
		merchant = v
	}
	date := text(raw["created"])
	if v := text(raw["modifiedCreated"]); v != "" {
		date = v
	}
	comment := text(raw["comment"])
	if c := object(raw["comment"]); c != nil {
		comment = text(c["comment"])
	}
	return Expense{ID: id, ReportID: text(raw["reportID"]), Amount: amount, Currency: currency, Merchant: merchant, Date: date, Comment: comment, Category: text(raw["category"]), Tag: text(raw["tag"]), Billable: boolean(raw["billable"]), Reimbursable: boolean(raw["reimbursable"]), Receipt: raw["receipt"]}, nil
}

func expenseInSnapshot(state snapshot, id string, raw map[string]any) (Expense, error) {
	result, err := expense(id, raw)
	if err != nil {
		return Expense{}, err
	}
	if related := object(state["report_"+result.ReportID]); text(related["type"]) == "iou" && result.Amount < 0 {
		result.Amount = -result.Amount
	}
	return result, nil
}
func report(id string, raw map[string]any) (Report, error) {
	if v := text(raw["reportID"]); v != "" {
		id = v
	}
	total, err := parseMoney(raw["total"], "report total")
	if err != nil {
		return Report{}, err
	}
	return Report{ID: id, Name: text(raw["reportName"]), PolicyID: text(raw["policyID"]), Type: text(raw["type"]), Total: total, Currency: text(raw["currency"]), State: text(raw["stateNum"]), Status: text(raw["statusNum"]), OwnerAccountID: text(raw["ownerAccountID"]), ManagerID: text(raw["managerID"])}, nil
}
func workspace(id string, raw map[string]any) Workspace {
	if v := text(raw["id"]); v != "" {
		id = v
	}
	return Workspace{ID: id, Name: text(raw["name"]), Type: text(raw["type"]), Currency: text(raw["outputCurrency"]), Role: text(raw["role"]), Owner: text(raw["owner"])}
}
func keys(state snapshot, prefix string) []string {
	result := []string{}
	for key, value := range state {
		if strings.HasPrefix(key, prefix) && len(key) > len(prefix) && object(value) != nil {
			result = append(result, key)
		}
	}
	sort.Strings(result)
	return result
}

func paginate[T any](items []T, values map[string]string) Page[T] {
	limit := 100
	if v := values["limit"]; v != "" {
		limit, _ = strconv.Atoi(v)
	}
	offset, _ := strconv.Atoi(values["offset"])
	start := min(offset, len(items))
	end := min(start+limit, len(items))
	return Page[T]{Items: items[start:end], Offset: offset, Limit: limit, HasMore: end < len(items)}
}
