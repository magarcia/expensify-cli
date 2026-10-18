package expensify

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/magarcia/expensify-cli/internal/api"
	"github.com/magarcia/expensify-cli/internal/fault"
)

type Service struct{ client *api.Client }

func New(client *api.Client) *Service { return &Service{client: client} }

func (s *Service) Execute(ctx context.Context, noun, verb string, values map[string]string) (any, error) {
	if noun == "expense" && verb == "create" && values["date"] == "" {
		copy := make(map[string]string, len(values)+1)
		for k, v := range values {
			copy[k] = v
		}
		copy["date"] = time.Now().Format("2006-01-02")
		values = copy
	}
	if err := Validate(noun, verb, values); err != nil {
		return nil, err
	}
	switch noun + " " + verb {
	case "expense list":
		return s.listExpenses(ctx, values)
	case "report list":
		return s.listReports(ctx, values)
	case "expense view":
		return s.viewExpense(ctx, values)
	case "report view":
		return s.viewReport(ctx, values)
	case "workspace list", "workspace view":
		return s.workspaces(ctx, verb, values)
	case "category list", "tag list":
		return s.namedItems(ctx, noun, values)
	case "user view":
		state, err := s.openApp(ctx)
		if err != nil {
			return nil, err
		}
		account := object(state["session"])
		accountID, email := text(account["accountID"]), text(account["email"])
		credentials := s.client.Credentials()
		if accountID == "" && credentials.AccountID > 0 {
			accountID = strconv.FormatInt(credentials.AccountID, 10)
		}
		if email == "" {
			email = credentials.Email
		}
		if accountID == "" && email == "" {
			return nil, unexpected("account identity is missing")
		}
		return struct {
			AccountID string `json:"account_id"`
			Email     string `json:"email"`
		}{accountID, email}, nil
	case "expense create":
		return s.createExpense(ctx, values)
	case "expense move":
		return s.moveExpense(ctx, values)
	case "expense edit":
		return s.editExpense(ctx, values)
	case "expense delete":
		return s.deleteExpense(ctx, values)
	case "expense attach":
		id, err := randomID(63)
		if err != nil {
			return nil, err
		}
		_, err = s.client.Upload(ctx, "ReplaceReceipt", url.Values{"transactionID": {values["expense-id"]}, "receiptState": {"SCANREADY"}, "reportActionID": {id}}, "receipt", values["file"])
		if err != nil {
			return nil, err
		}
		return Mutation{ID: values["expense-id"], Action: "receipt_attached"}, nil
	case "report create":
		return s.createReport(ctx, values)
	case "report edit":
		return s.setReportNotes(ctx, values["report-id"], values["notes"])
	case "report submit", "report approve", "report comment":
		return s.reportAction(ctx, verb, values)
	}
	return nil, fault.Usage("Unknown resource command.")
}

func (s *Service) snapshot(ctx context.Context, command string, params url.Values) (snapshot, error) {
	response, err := s.client.Call(ctx, command, params)
	if err != nil {
		return nil, err
	}
	return decodeSnapshot(response)
}
func (s *Service) openApp(ctx context.Context) (snapshot, error) {
	return s.snapshot(ctx, "OpenApp", url.Values{"policyIDList": {"[]"}, "enablePriorityModeFilter": {"false"}})
}
func (s *Service) openReport(ctx context.Context, id string) (snapshot, error) {
	return s.snapshot(ctx, "OpenReport", url.Values{"reportID": {id}})
}

func (s *Service) viewExpense(ctx context.Context, values map[string]string) (Expense, error) {
	var state snapshot
	var err error
	if values["report-id"] != "" {
		state, err = s.openReport(ctx, values["report-id"])
	} else {
		state, err = s.openApp(ctx)
	}
	if err != nil {
		return Expense{}, err
	}
	id := values["expense-id"]
	raw := object(state["transactions_"+id])
	if raw == nil {
		if values["report-id"] == "" {
			return s.findExpense(ctx, id)
		}
		return Expense{}, notFound("Expense", id)
	}
	return expenseInSnapshot(state, id, raw)
}

func (s *Service) workspaces(ctx context.Context, verb string, values map[string]string) (any, error) {
	state, err := s.openApp(ctx)
	if err != nil {
		return nil, err
	}
	if verb == "view" {
		id := values["policy-id"]
		raw := object(state["policy_"+id])
		if raw == nil {
			return nil, notFound("Workspace", id)
		}
		return workspace(id, raw), nil
	}
	items := []Workspace{}
	for _, key := range keys(state, "policy_") {
		items = append(items, workspace(strings.TrimPrefix(key, "policy_"), object(state[key])))
	}
	return items, nil
}

func (s *Service) namedItems(ctx context.Context, noun string, values map[string]string) ([]NamedItem, error) {
	collection, err := s.policyItems(ctx, noun, values["policy-id"])
	if err != nil {
		return nil, err
	}
	items := []NamedItem{}
	for key, value := range collection {
		item := object(value)
		if item == nil {
			continue
		}
		if noun == "tag" {
			if tags := object(item["tags"]); tags != nil {
				for name, value := range tags {
					tag := object(value)
					if tag == nil {
						continue
					}
					enabled := true
					if b, ok := tag["enabled"].(bool); ok {
						enabled = b
					}
					items = append(items, NamedItem{Name: name, Enabled: enabled, Group: text(item["name"])})
				}
				continue
			}
		}
		name := text(item["name"])
		if name == "" {
			name = key
		}
		enabled := true
		if b, ok := item["enabled"].(bool); ok {
			enabled = b
		}
		items = append(items, NamedItem{Name: name, Enabled: enabled})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Group == items[j].Group {
			return items[i].Name < items[j].Name
		}
		return items[i].Group < items[j].Group
	})
	return items, nil
}

func randomID(bits uint) (string, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), bits)
	limit.Sub(limit, big.NewInt(1))
	n, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return "", fault.New("CLI-004", "A secure request ID could not be generated.", "Try again after the operating system's random source is available.", 1)
	}
	n.Add(n, big.NewInt(1))
	return n.String(), nil
}

func newIDs(bits ...uint) ([]string, error) {
	ids := make([]string, len(bits))
	for i, b := range bits {
		id, err := randomID(b)
		if err != nil {
			return nil, err
		}
		ids[i] = id
	}
	return ids, nil
}

func (s *Service) createExpense(ctx context.Context, values map[string]string) (Mutation, error) {
	if values["policy-id"] != "" || values["report-id"] != "" {
		return s.createWorkspaceExpense(ctx, values)
	}
	state, err := s.openApp(ctx)
	if err != nil {
		return Mutation{}, err
	}
	ids, err := newIDs(63, 63, 53, 63)
	if err != nil {
		return Mutation{}, err
	}
	params := url.Values{"amount": {values["amount"]}, "currency": {values["currency"]}, "merchant": {values["merchant"]}, "created": {values["date"]}, "comment": {values["comment"]}, "transactionID": {ids[0]}, "reportActionID": {ids[1]}, "transactionThreadReportID": {ids[2]}, "createdReportActionIDForThread": {ids[3]}, "taxCode": {""}, "taxAmount": {"0"}}
	chatID := text(state["selfDMReportID"])
	if chatID == "" {
		for _, key := range keys(state, "report_") {
			r := object(state[key])
			if text(r["chatType"]) == "selfDM" {
				chatID = text(r["reportID"])
				if chatID == "" {
					chatID = strings.TrimPrefix(key, "report_")
				}
				break
			}
		}
	}
	if chatID != "" {
		params.Set("chatReportID", chatID)
	} else {
		ids, err := newIDs(53, 63)
		if err != nil {
			return Mutation{}, err
		}
		params.Set("optimisticReportID", ids[0])
		params.Set("optimisticReportActionID", ids[1])
	}
	for _, key := range []string{"category", "tag", "billable", "reimbursable"} {
		if value, ok := values[key]; ok && value != "" {
			params.Set(key, value)
		}
	}
	if err := s.sendExpense(ctx, "TrackExpense", params, values["receipt"]); err != nil {
		return Mutation{}, err
	}
	return Mutation{ID: ids[0], Action: "created"}, nil
}

func (s *Service) editExpense(ctx context.Context, values map[string]string) (Mutation, error) {
	id, err := randomID(63)
	if err != nil {
		return Mutation{}, err
	}
	updates := map[string]any{}
	for _, key := range []string{"amount", "currency", "merchant", "date", "comment", "category", "tag", "billable", "reimbursable"} {
		if value, ok := values[key]; ok {
			name := key
			if key == "date" {
				name = "created"
			}
			var v any = value
			if key == "amount" {
				v, _ = strconv.ParseInt(value, 10, 64)
			}
			if key == "billable" || key == "reimbursable" {
				v = value == "true"
			}
			updates[name] = v
		}
	}
	encoded, _ := json.Marshal(updates)
	params := url.Values{"transactionID": {values["expense-id"]}, "reportActionID": {id}, "updates": {string(encoded)}}
	if _, err := s.client.Call(ctx, "UpdateMoneyRequest", params); err != nil {
		return Mutation{}, err
	}
	return Mutation{ID: values["expense-id"], Action: "updated"}, nil
}

func (s *Service) deleteExpense(ctx context.Context, values map[string]string) (Mutation, error) {
	state, err := s.openReport(ctx, values["report-id"])
	if err != nil {
		return Mutation{}, err
	}
	var actionID string
	for key, value := range object(state["reportActions_"+values["report-id"]]) {
		action := object(value)
		original := object(action["originalMessage"])
		if text(original["IOUTransactionID"]) != values["expense-id"] && text(original["transactionID"]) != values["expense-id"] {
			continue
		}
		actionID = text(action["reportActionID"])
		if actionID == "" {
			actionID = key
		}
		break
	}
	if actionID == "" {
		return Mutation{}, fault.New("API-405", "The expense's original money-request action could not be found.", "Check --report-id. Deletion requires the original action, so no delete request was sent.", 4)
	}
	if _, err := s.client.Call(ctx, "DeleteMoneyRequest", url.Values{"transactionID": {values["expense-id"]}, "reportActionID": {actionID}}); err != nil {
		return Mutation{}, err
	}
	return Mutation{ID: values["expense-id"], Action: "deleted"}, nil
}

func (s *Service) createReport(ctx context.Context, values map[string]string) (Mutation, error) {
	_, hasNotes := values["notes"]
	if policyID := values["policy-id"]; policyID != "" {
		state, err := s.openApp(ctx)
		if err != nil {
			return Mutation{}, err
		}
		policy := object(state["policy_"+policyID])
		if policy == nil {
			return Mutation{}, notFound("Workspace", policyID)
		}
		// CreateAppReport has no option to defer an instant-submit policy.
		if boolean(policy["autoReporting"]) && text(policy["autoReportingFrequency"]) == "instant" && text(policy["approvalMode"]) != "optional" {
			return Mutation{}, fault.New("API-405", "This workspace can immediately submit newly created reports.", "Choose an existing draft report or change the workspace's submission schedule. No report was created.", 4)
		}
		if hasNotes {
			if _, err := prepareReportNotes(state, map[string]any{"type": "expense", "policyID": policyID}, values["notes"]); err != nil {
				return Mutation{}, err
			}
		}
	}
	ids, err := newIDs(53, 63, 63)
	if err != nil {
		return Mutation{}, err
	}
	params := url.Values{"type": {"expense"}, "reportID": {ids[0]}, "reportActionID": {ids[1]}, "reportPreviewReportActionID": {ids[2]}, "reportName": {values["name"]}}
	for key, flag := range map[string]string{"policyID": "policy-id", "ownerEmail": "owner-email"} {
		if values[flag] != "" {
			params.Set(key, values[flag])
		}
	}
	if _, err := s.client.Call(ctx, "CreateAppReport", params); err != nil {
		return Mutation{}, err
	}
	if hasNotes {
		if _, err := s.setReportNotes(ctx, ids[0], values["notes"]); err != nil {
			message := fmt.Sprintf("Report %s was created, but its Notes could not be saved.", ids[0])
			exit := 1
			var failure *fault.Error
			if errors.As(err, &failure) {
				message += " " + failure.Message
				exit = failure.Exit
			}
			return Mutation{}, fault.New("API-006", message, fmt.Sprintf("Do not create another report. Inspect expensify report view --report-id %s, then retry expensify report edit --report-id %s --notes '<text>'.", ids[0], ids[0]), exit)
		}
	}
	return Mutation{ID: ids[0], Action: "created"}, nil
}

func (s *Service) reportAction(ctx context.Context, verb string, values map[string]string) (Mutation, error) {
	id, err := randomID(63)
	if err != nil {
		return Mutation{}, err
	}
	command := "SubmitReport"
	params := url.Values{"reportID": {values["report-id"]}}
	switch verb {
	case "submit":
		params.Set("reportActionID", id)
		if values["manager-email"] != "" {
			params.Set("managerEmail", values["manager-email"])
		}
	case "approve":
		command = "ApproveMoneyRequest"
		params.Set("approvedReportActionID", id)
	case "comment":
		command = "AddComment"
		params.Set("reportActionID", id)
		params.Set("reportComment", values["text"])
	}
	if _, err := s.client.Call(ctx, command, params); err != nil {
		return Mutation{}, err
	}
	return Mutation{ID: values["report-id"], Action: verb}, nil
}

func (s *Service) search(ctx context.Context, kind string, values map[string]string) (snapshot, int, bool, error) {
	limit := 100
	if values["limit"] != "" {
		limit, _ = strconv.Atoi(values["limit"])
	}
	offset, _ := strconv.Atoi(values["offset"])
	var filter any
	if values["query"] != "" {
		_ = json.Unmarshal([]byte(values["query"]), &filter)
	}
	for _, entry := range [][3]string{{"policyID", "policy-id", "eq"}, {"reportID", "report-id", "eq"}, {"status", "status", "eq"}, {"date", "since", "gte"}, {"date", "until", "lte"}, {"merchant", "merchant", "contains"}} {
		if values[entry[1]] != "" {
			next := map[string]any{"operator": entry[2], "left": entry[0], "right": values[entry[1]]}
			if filter == nil {
				filter = next
			} else {
				filter = map[string]any{"operator": "and", "left": filter, "right": next}
			}
		}
	}
	all := snapshot{}
	hasMore := false
	for fetched := 0; fetched < limit; fetched += 50 {
		hash := searchHash(kind, filter)
		hashNumber, _ := strconv.ParseInt(hash, 10, 64)
		query := map[string]any{"type": kind, "sortBy": "date", "sortOrder": "desc", "view": "table", "filters": filter, "offset": offset + fetched, "shouldCalculateTotals": true, "shouldSaveRecentSearch": false, "hash": hashNumber, "inputQuery": "type:" + kind}
		body, _ := json.Marshal(query)
		response, err := s.client.Call(ctx, "Search", url.Values{"hash": {hash}, "jsonQuery": {string(body)}})
		if err != nil {
			return nil, 0, false, err
		}
		state, err := decodeSnapshot(response)
		if err != nil {
			return nil, 0, false, err
		}
		snap := object(state["snapshot_"+hash])
		if snap == nil {
			return nil, 0, false, unexpected("Search snapshot is missing")
		}
		metadata := object(snap["search"])
		data := object(snap["data"])
		if metadata == nil {
			return nil, 0, false, unexpected("Search metadata is not an object")
		}
		if data == nil {
			return nil, 0, false, unexpected("Search data is not an object")
		}
		hasMoreValue, ok := metadata["hasMoreResults"].(bool)
		if !ok {
			return nil, 0, false, unexpected("Search hasMoreResults is not a boolean")
		}
		if actual, err := strconv.Atoi(text(metadata["offset"])); err != nil || actual != offset+fetched {
			return nil, 0, false, unexpected("Search offset does not match the requested page")
		}
		prefix := "transactions_"
		if kind == "expense-report" {
			prefix = "report_"
		}
		if integer(metadata["count"]) > 0 && len(keys(snapshot(data), prefix)) == 0 {
			return nil, 0, false, unexpected("Search count is positive but expected resources are missing")
		}
		for k, v := range data {
			all[k] = v
		}
		hasMore = hasMoreValue
		if !hasMore {
			break
		}
	}
	return all, offset, hasMore, nil
}

func searchHash(kind string, filter any) string {
	canonical := "type:" + kind + " status: groupBy:undefined sortBy:date sortOrder:desc columns:undefined"
	if filter != nil {
		encoded, _ := json.Marshal(filter)
		canonical += " " + string(encoded)
	}
	var hash int32
	for _, r := range utf16.Encode([]rune(strings.ToLower(canonical))) {
		hash = hash*31 + int32(r)
	}
	n := int64(hash)
	if n < 0 {
		n = -n
	}
	return strconv.FormatInt(n, 10)
}

func (s *Service) listExpenses(ctx context.Context, values map[string]string) (Page[Expense], error) {
	state, offset, more, err := s.search(ctx, "expense", values)
	if err != nil {
		return Page[Expense]{}, err
	}
	items := []Expense{}
	for _, key := range keys(state, "transactions_") {
		item, err := expenseInSnapshot(state, strings.TrimPrefix(key, "transactions_"), object(state[key]))
		if err != nil {
			return Page[Expense]{}, err
		}
		items = append(items, item)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Date == items[j].Date {
			return items[i].ID > items[j].ID
		}
		return items[i].Date > items[j].Date
	})
	localValues := map[string]string{"limit": values["limit"]}
	page := paginate(items, localValues)
	page.Offset = offset
	page.HasMore = page.HasMore || more
	return page, nil
}

func (s *Service) listReports(ctx context.Context, values map[string]string) (Page[Report], error) {
	state, offset, more, err := s.search(ctx, "expense-report", values)
	if err != nil {
		return Page[Report]{}, err
	}
	items := []Report{}
	for _, key := range keys(state, "report_") {
		raw := object(state[key])
		if text(raw["type"]) != "expense" {
			continue
		}
		item, err := report(strings.TrimPrefix(key, "report_"), raw)
		if err != nil {
			return Page[Report]{}, err
		}
		items = append(items, item)
	}
	localValues := map[string]string{"limit": values["limit"]}
	page := paginate(items, localValues)
	page.Offset = offset
	page.HasMore = page.HasMore || more
	return page, nil
}
