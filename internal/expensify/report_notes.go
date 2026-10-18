package expensify

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/magarcia/expensify-cli/internal/fault"
)

func (s *Service) viewReport(ctx context.Context, values map[string]string) (Report, error) {
	id := values["report-id"]
	state, err := s.openReport(ctx, id)
	if err != nil {
		return Report{}, err
	}
	raw := object(state["report_"+id])
	if raw == nil {
		return Report{}, notFound("Report", id)
	}
	result, err := report(id, raw)
	if err != nil {
		return Report{}, err
	}
	field, fieldErr := findNotesField(state, raw)
	policyID := text(raw["policyID"])
	needsPolicyFields := fieldErr != nil || field["value"] == nil && field["defaultValue"] == nil
	if text(raw["type"]) == "expense" && text(raw["statusNum"]) != "4" && policyID != "" && !reportNotesPolicyLoaded(state, policyID) && needsPolicyFields {
		app, err := s.openApp(ctx)
		if err != nil {
			return Report{}, err
		}
		enrichReportNotesState(state, app, policyID)
	}
	notes := reportNotes(state, id, raw)
	result.Notes = &notes
	return result, nil
}

func prepareReportNotes(state snapshot, raw map[string]any, notes string) (map[string]any, error) {
	field, err := findNotesField(state, raw)
	if err != nil {
		return nil, err
	}
	if deletable, ok := field["deletable"].(bool); ok && !deletable && strings.TrimSpace(notes) == "" {
		return nil, fault.New("API-405", "The workspace requires a value for the Notes report field.", "Provide nonempty --notes text. No report notes were changed.", 4)
	}
	updated := make(map[string]any, len(field)+1)
	for key, value := range field {
		updated[key] = value
	}
	updated["value"] = notes
	return updated, nil
}

func reportNotes(state snapshot, id string, raw map[string]any) string {
	if raw == nil {
		raw = object(state["report_"+id])
	}
	field, err := findNotesField(state, raw)
	if err != nil {
		return ""
	}
	if field["value"] != nil {
		return text(field["value"])
	}
	return text(field["defaultValue"])
}

func findNotesField(state snapshot, raw map[string]any) (map[string]any, error) {
	if text(raw["type"]) != "expense" {
		return nil, fault.New("API-405", "Report Notes are only supported on expense reports.", "Choose an expense report. No report notes were changed.", 4)
	}
	effective := map[string]map[string]any{}
	policy := object(state["policy_"+text(raw["policyID"])])
	if text(raw["statusNum"]) == "4" {
		// Settled reports retain their attached fields rather than acquiring
		// defaults from fields the workspace added after reimbursement.
		policy = nil
	}
	// Attached fields preserve the report's metadata and values even when the
	// workspace changes its report fields.
	for _, source := range []map[string]any{object(policy["fieldList"]), object(raw["fieldList"])} {
		for _, value := range source {
			field := object(value)
			if field == nil {
				continue
			}
			id, validID := field["fieldID"].(string)
			if !validID || id == "" {
				target := text(field["target"])
				if strings.EqualFold(strings.TrimSpace(text(field["name"])), "Notes") && (target == "" || target == "expense") {
					return nil, unexpected("the Notes report field has an invalid field ID")
				}
				continue
			}
			effective[id] = field
		}
	}
	fields := map[string]map[string]any{}
	for id, field := range effective {
		if !strings.EqualFold(strings.TrimSpace(text(field["name"])), "Notes") {
			continue
		}
		if target := text(field["target"]); target != "" && target != "expense" {
			continue
		}
		if id == "text_title" {
			return nil, unexpected("the Notes report field has an invalid field ID")
		}
		fields[id] = field
	}
	if len(fields) == 0 {
		return nil, fault.New("API-405", "The workspace has no Notes report field.", "Ask a workspace admin to add a text report field named Notes. No report notes were changed.", 4)
	}
	if len(fields) > 1 {
		return nil, fault.New("API-405", "The report has more than one Notes field.", "Give the workspace report fields unique names before editing Notes. No report notes were changed.", 4)
	}
	for _, field := range fields {
		if text(field["type"]) != "text" {
			return nil, fault.New("API-405", "The Notes report field is not a text field.", "Use a workspace text report field named Notes. No report notes were changed.", 4)
		}
		return field, nil
	}
	return nil, unexpected("the Notes report field is missing")
}

func (s *Service) setReportNotes(ctx context.Context, reportID, notes string) (Mutation, error) {
	state, err := s.openReport(ctx, reportID)
	if err != nil {
		return Mutation{}, err
	}
	raw := object(state["report_"+reportID])
	if raw == nil {
		return Mutation{}, notFound("Report", reportID)
	}
	accountID, _ := s.expenseAccount(state)
	unsafeTarget := fault.New("API-405", "Report notes can only be edited on your own draft expense reports.", "Choose an unsubmitted expense report owned by the authenticated account. No report notes were changed.", 4)
	if accountID != "" && !ownDraftExpenseReport(raw, accountID) {
		return Mutation{}, unsafeTarget
	}
	policyID := text(raw["policyID"])
	if accountID == "" || policyID != "" && !reportNotesPolicyLoaded(state, policyID) {
		app, err := s.openApp(ctx)
		if err != nil {
			return Mutation{}, err
		}
		enrichReportNotesState(state, app, policyID)
		accountID, _ = s.expenseAccount(state)
	}
	if !ownDraftExpenseReport(raw, accountID) {
		return Mutation{}, unsafeTarget
	}
	field, err := prepareReportNotes(state, raw, notes)
	if err != nil {
		return Mutation{}, err
	}
	actionID, err := randomID(63)
	if err != nil {
		return Mutation{}, err
	}
	key := "expensify_" + text(field["fieldID"])
	fields, err := json.Marshal(map[string]any{key: field})
	if err != nil {
		return Mutation{}, unexpected("the Notes report field cannot be encoded")
	}
	actions, _ := json.Marshal(map[string]string{key: actionID})
	params := url.Values{"reportID": {reportID}, "reportFields": {string(fields)}, "reportFieldsActionIDs": {string(actions)}}
	if _, err := s.client.Call(ctx, "SetReportField", params); err != nil {
		return Mutation{}, err
	}
	return Mutation{ID: reportID, Action: "updated"}, nil
}

func reportNotesPolicyLoaded(state snapshot, policyID string) bool {
	return onyxObject(object(state["policy_"+policyID])["fieldList"]) != nil
}

func enrichReportNotesState(state, app snapshot, policyID string) {
	for key, value := range app {
		if _, exists := state[key]; !exists {
			state[key] = value
		}
	}
	session := object(state["session"])
	fullSession := object(app["session"])
	if session == nil {
		if fullSession != nil {
			state["session"] = fullSession
		}
	} else {
		if !decimalIDPattern.MatchString(text(session["accountID"])) && decimalIDPattern.MatchString(text(fullSession["accountID"])) {
			session["accountID"] = fullSession["accountID"]
		}
		if strings.TrimSpace(text(session["email"])) == "" && strings.TrimSpace(text(fullSession["email"])) != "" {
			session["email"] = fullSession["email"]
		}
	}
	key := "policy_" + policyID
	policy := object(state[key])
	fullPolicy := object(app[key])
	if policy == nil {
		if fullPolicy != nil {
			state[key] = fullPolicy
		}
		return
	}
	for field, value := range fullPolicy {
		if _, exists := policy[field]; !exists || field == "fieldList" && onyxObject(policy[field]) == nil {
			policy[field] = value
		}
	}
}
