package expensify

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	"github.com/magarcia/expensify-cli/internal/fault"
)

func (s *Service) createWorkspaceExpense(ctx context.Context, values map[string]string) (Mutation, error) {
	state, err := s.openApp(ctx)
	if err != nil {
		return Mutation{}, err
	}
	accountID, email := s.expenseAccount(state)
	if accountID == "" {
		return Mutation{}, unexpected("account identity is missing")
	}

	policyID, reportID := values["policy-id"], values["report-id"]
	var destination map[string]any
	var chatID string
	if reportID != "" {
		destination, err = s.readExpenseReport(ctx, reportID)
		if err != nil {
			return Mutation{}, err
		}
		if !ownDraftExpenseReport(destination, accountID) {
			return Mutation{}, unsafeExpenseTarget()
		}
		if policyID != "" && policyID != text(destination["policyID"]) {
			return Mutation{}, fault.Usage("--policy-id does not match the destination report's workspace.")
		}
		policyID, chatID = text(destination["policyID"]), text(destination["chatReportID"])
	}
	if policyID == "" {
		return Mutation{}, unsafeExpenseTarget()
	}
	if chatID == "" {
		chatID = ownPolicyChatID(state, policyID, accountID)
	}
	if chatID == "" {
		return Mutation{}, fault.New("API-405", "Your workspace expense chat could not be found.", "Check --policy-id and your workspace membership. No expense was created.", 4)
	}
	chatState, err := s.openReport(ctx, chatID)
	if err != nil {
		return Mutation{}, err
	}
	chat := object(chatState["report_"+chatID])
	if !ownPolicyChat(chat, policyID, accountID) {
		return Mutation{}, unsafeExpenseTarget()
	}

	if reportID == "" {
		linkedID := text(chat["iouReportID"])
		if decimalIDPattern.MatchString(linkedID) {
			linked, err := s.openReport(ctx, linkedID)
			if err != nil {
				return Mutation{}, err
			}
			candidate := object(linked["report_"+linkedID])
			if ownDraftExpenseReport(candidate, accountID) && text(candidate["policyID"]) == policyID && text(candidate["chatReportID"]) == chatID {
				reportID, destination = linkedID, candidate
			}
		}
	}
	ids, err := newIDs(63, 63, 53, 63, 63)
	if err != nil {
		return Mutation{}, err
	}
	params := url.Values{
		"debtorEmail": {""}, "debtorAccountID": {"0"},
		"amount": {values["amount"]}, "currency": {values["currency"]}, "merchant": {values["merchant"]},
		"created": {values["date"]}, "comment": {values["comment"]}, "description": {values["comment"]},
		"chatReportID": {chatID}, "transactionID": {ids[0]}, "reportActionID": {ids[1]},
		"transactionThreadReportID": {ids[2]}, "createdReportActionIDForThread": {ids[3]},
		"reportPreviewReportActionID": {ids[4]}, "taxCode": {""}, "taxAmount": {"0"},
		"reimbursable": {"true"}, "billable": {"false"},
		// RequestMoney can otherwise immediately submit under workspace rules.
		"shouldDeferAutoSubmit": {"true"},
	}
	if destination == nil {
		newReportIDs, err := newIDs(53, 63)
		if err != nil {
			return Mutation{}, err
		}
		reportID = newReportIDs[0]
		params.Set("createdIOUReportActionID", newReportIDs[1])
	}
	params.Set("iouReportID", reportID)
	for key, value := range object(chatState["reportActions_"+chatID]) {
		action := object(value)
		if text(action["actionName"]) != "REPORTPREVIEW" || text(object(action["originalMessage"])["linkedReportID"]) != reportID {
			continue
		}
		id := text(action["reportActionID"])
		if id == "" {
			id = key
		}
		params.Set("reportPreviewReportActionID", id)
		break
	}
	if email != "" {
		displayName := text(object(object(state["personalDetailsList"])[accountID])["displayName"])
		if displayName == "" {
			displayName = email
		}
		attendees, _ := json.Marshal([]map[string]string{{"email": email, "displayName": displayName, "avatarUrl": ""}})
		params.Set("attendees", string(attendees))
	}
	for _, key := range []string{"category", "tag", "billable", "reimbursable"} {
		if value := values[key]; value != "" {
			params.Set(key, value)
		}
	}
	if err := s.sendExpense(ctx, "RequestMoney", params, values["receipt"]); err != nil {
		return Mutation{}, err
	}
	return Mutation{ID: ids[0], Action: "created", ReportID: reportID}, nil
}

func (s *Service) sendExpense(ctx context.Context, command string, params url.Values, receiptPath string) error {
	if receiptPath != "" {
		params.Set("receiptState", "SCANREADY")
		_, err := s.client.Upload(ctx, command, params, "receipt", receiptPath)
		return err
	}
	_, err := s.client.Call(ctx, command, params)
	return err
}

func (s *Service) moveExpense(ctx context.Context, values map[string]string) (Mutation, error) {
	state, err := s.openApp(ctx)
	if err != nil {
		return Mutation{}, err
	}
	accountID, _ := s.expenseAccount(state)
	if accountID == "" {
		return Mutation{}, unexpected("account identity is missing")
	}
	id, reportID := values["expense-id"], values["report-id"]
	destination, err := s.readExpenseReport(ctx, reportID)
	if err != nil {
		return Mutation{}, err
	}
	if !ownDraftExpenseReport(destination, accountID) || text(destination["policyID"]) == "" {
		return Mutation{}, unsafeExpenseTarget()
	}
	if object(state["transactions_"+id]) == nil {
		found, err := s.findExpenseSnapshot(ctx, id)
		if err != nil {
			return Mutation{}, err
		}
		for key, value := range found {
			if _, exists := state[key]; !exists {
				state[key] = value
			}
		}
	}
	source, err := expenseInSnapshot(state, id, object(state["transactions_"+id]))
	if err != nil {
		return Mutation{}, err
	}
	if source.ReportID == reportID {
		return Mutation{ID: id, Action: "unchanged", ReportID: reportID}, nil
	}
	if source.ReportID == "" || source.ReportID == "0" {
		owned, err := s.ownsUnreportedExpense(ctx, state, id, accountID)
		if err != nil {
			return Mutation{}, err
		}
		if !owned {
			return Mutation{}, fault.New("API-405", "The expense's ownership could not be verified.", "Only your own unreported expenses or draft reports can be moved. No expense was moved.", 4)
		}
	} else {
		sourceState, err := s.openReport(ctx, source.ReportID)
		if err != nil {
			return Mutation{}, err
		}
		sourceReport := object(sourceState["report_"+source.ReportID])
		if text(sourceReport["type"]) == "chat" && text(sourceReport["chatType"]) == "selfDM" && text(sourceReport["ownerAccountID"]) == accountID {
			if !ownExpenseAction(sourceState, source.ReportID, id, accountID) {
				return Mutation{}, unsafeExpenseTarget()
			}
		} else {
			if !ownDraftExpenseReport(sourceReport, accountID) {
				return Mutation{}, unsafeExpenseTarget()
			}
			if sourcePolicy := text(sourceReport["policyID"]); sourcePolicy != "" && sourcePolicy != text(destination["policyID"]) {
				return Mutation{}, fault.New("API-405", "Moving expenses between workspaces is not supported.", "Choose a draft report in the expense's current workspace. No expense was moved.", 4)
			}
		}
	}
	actionID, err := randomID(63)
	if err != nil {
		return Mutation{}, err
	}
	actions, _ := json.Marshal(map[string]map[string]string{id: {"moneyRequestPreviewReportActionID": actionID}})
	params := url.Values{"transactionList": {id}, "reportID": {reportID}, "transactionIDToReportActionAndThreadData": {string(actions)}}
	if _, err := s.client.Call(ctx, "ChangeTransactionsReport", params); err != nil {
		return Mutation{}, err
	}
	return Mutation{ID: id, Action: "moved", ReportID: reportID}, nil
}

func (s *Service) expenseAccount(state snapshot) (string, string) {
	session := object(state["session"])
	accountID, email := text(session["accountID"]), text(session["email"])
	credentials := s.client.Credentials()
	if accountID == "" && credentials.AccountID > 0 {
		accountID = strconv.FormatInt(credentials.AccountID, 10)
	}
	if email == "" {
		email = credentials.Email
	}
	if !decimalIDPattern.MatchString(accountID) {
		accountID = ""
	}
	return accountID, email
}

func (s *Service) readExpenseReport(ctx context.Context, id string) (map[string]any, error) {
	state, err := s.openReport(ctx, id)
	if err != nil {
		return nil, err
	}
	report := object(state["report_"+id])
	if report == nil {
		return nil, notFound("Report", id)
	}
	return report, nil
}

func ownDraftExpenseReport(report map[string]any, accountID string) bool {
	return accountID != "" && text(report["type"]) == "expense" && text(report["ownerAccountID"]) == accountID && text(report["stateNum"]) == "0" && text(report["statusNum"]) == "0"
}

func ownPolicyChat(report map[string]any, policyID, accountID string) bool {
	return text(report["chatType"]) == "policyExpenseChat" && text(report["policyID"]) == policyID && text(report["ownerAccountID"]) == accountID && !(text(report["parentReportID"]) != "" && text(report["parentReportActionID"]) != "")
}

func ownPolicyChatID(state snapshot, policyID, accountID string) string {
	for _, key := range keys(state, "report_") {
		if ownPolicyChat(object(state[key]), policyID, accountID) {
			return strings.TrimPrefix(key, "report_")
		}
	}
	return ""
}

func unsafeExpenseTarget() error {
	return fault.New("API-405", "The expense operation requires your own draft workspace report.", "Choose an unsubmitted expense report owned by the authenticated account. No expense was written or moved.", 4)
}

func (s *Service) ownsUnreportedExpense(ctx context.Context, state snapshot, id, accountID string) (bool, error) {
	raw := object(state["transactions_"+id])
	if cardID := text(raw["cardID"]); cardID != "" {
		if card := object(object(state["cardList"])[cardID]); card != nil {
			owner := text(card["accountID"])
			return owner == "" || owner == accountID, nil
		}
	}
	selfID := text(state["selfDMReportID"])
	if selfID == "" {
		for _, key := range keys(state, "report_") {
			report := object(state[key])
			if text(report["chatType"]) == "selfDM" && text(report["ownerAccountID"]) == accountID {
				selfID = strings.TrimPrefix(key, "report_")
				break
			}
		}
	}
	if selfID == "" {
		return false, nil
	}
	self, err := s.openReport(ctx, selfID)
	if err != nil {
		return false, err
	}
	return ownExpenseAction(self, selfID, id, accountID), nil
}

func ownExpenseAction(state snapshot, reportID, id, accountID string) bool {
	for _, value := range object(state["reportActions_"+reportID]) {
		action := object(value)
		original := object(action["originalMessage"])
		if (text(original["IOUTransactionID"]) == id || text(original["transactionID"]) == id) && text(action["actorAccountID"]) == accountID {
			return true
		}
	}
	return false
}
