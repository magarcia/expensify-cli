package expensify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const targetPolicy = "WORKSPACE"

func targetDraft(id, policy string) map[string]any {
	return map[string]any{"reportID": id, "type": "expense", "policyID": policy, "chatReportID": "100", "ownerAccountID": 42, "stateNum": 0, "statusNum": 0}
}

func targetChat() map[string]any {
	return map[string]any{"reportID": "100", "type": "chat", "chatType": "policyExpenseChat", "policyID": targetPolicy, "ownerAccountID": 42, "iouReportID": "200"}
}

func targetApp(extra ...map[string]any) map[string]any {
	patches := []map[string]any{patch("session", map[string]any{"accountID": 42, "email": "person@example.com"}), patch("report_100", targetChat())}
	return ok(append(patches, extra...)...)
}

func targetRead(t *testing.T, r *http.Request, v url.Values) map[string]any {
	t.Helper()
	switch r.URL.Path {
	case "/api/OpenApp":
		return targetApp()
	case "/api/OpenReport":
		switch v.Get("reportID") {
		case "100":
			return ok(patch("report_100", targetChat()), patch("reportActions_100", map[string]any{"300": map[string]any{"reportActionID": "300", "actionName": "REPORTPREVIEW", "originalMessage": map[string]any{"linkedReportID": "200"}}}))
		case "200":
			return ok(patch("report_200", targetDraft("200", targetPolicy)))
		}
	}
	t.Fatalf("unexpected read %s %v", r.URL.Path, v)
	return nil
}

func TestWorkspaceExpenseUsesRequestMoneyAndRetainsDraft(t *testing.T) {
	for _, values := range []map[string]string{{"policy-id": targetPolicy}, {"report-id": "200"}, {"policy-id": targetPolicy, "report-id": "200"}} {
		t.Run(values["policy-id"]+values["report-id"], func(t *testing.T) {
			values["amount"], values["currency"], values["merchant"], values["date"] = "1250", "EUR", "Cafe", "2026-10-18"
			values["category"], values["tag"] = "Software License Expense", "Engineering:Software Stipend"
			writes := 0
			service := testService(t, func(r *http.Request) map[string]any {
				v := form(t, r)
				if r.URL.Path != "/api/RequestMoney" {
					return targetRead(t, r, v)
				}
				writes++
				for key, want := range map[string]string{"debtorEmail": "", "debtorAccountID": "0", "amount": "1250", "currency": "EUR", "merchant": "Cafe", "created": "2026-10-18", "chatReportID": "100", "iouReportID": "200", "reportPreviewReportActionID": "300", "reimbursable": "true", "billable": "false", "shouldDeferAutoSubmit": "true", "category": values["category"], "tag": values["tag"], "taxAmount": "0"} {
					if !v.Has(key) || v.Get(key) != want {
						t.Errorf("%s=%q want %q", key, v.Get(key), want)
					}
				}
				for _, key := range []string{"transactionID", "reportActionID", "transactionThreadReportID", "createdReportActionIDForThread"} {
					if !decimalIDPattern.MatchString(v.Get(key)) {
						t.Errorf("missing %s", key)
					}
				}
				if v.Has("createdIOUReportActionID") {
					t.Fatal("recreated existing report")
				}
				var attendees []map[string]any
				if err := json.Unmarshal([]byte(v.Get("attendees")), &attendees); err != nil || len(attendees) != 1 || attendees[0]["email"] != "person@example.com" || attendees[0]["avatarUrl"] != "" || attendees[0]["displayName"] == "" {
					t.Fatalf("invalid attendees: %#v (%v)", attendees, err)
				}
				return ok()
			})
			result, err := executeTarget(service, "create", values)
			if err != nil || result.ID == "" || result.ReportID != "200" || writes != 1 {
				t.Fatalf("result=%#v err=%v writes=%d", result, err, writes)
			}
		})
	}
}

func TestWorkspaceExpenseCreatesNewDraftWhenLinkedReportIsSubmitted(t *testing.T) {
	writes := 0
	service := testService(t, func(r *http.Request) map[string]any {
		v := form(t, r)
		if r.URL.Path == "/api/OpenReport" && v.Get("reportID") == "200" {
			report := targetDraft("200", targetPolicy)
			report["stateNum"], report["statusNum"] = 1, 1
			return ok(patch("report_200", report))
		}
		if r.URL.Path != "/api/RequestMoney" {
			return targetRead(t, r, v)
		}
		writes++
		if v.Get("iouReportID") == "200" || !decimalIDPattern.MatchString(v.Get("iouReportID")) || !decimalIDPattern.MatchString(v.Get("createdIOUReportActionID")) {
			t.Fatalf("new draft IDs missing: %v", v)
		}
		if v.Get("reimbursable") != "false" || v.Get("billable") != "true" {
			t.Fatal("explicit booleans lost")
		}
		return ok()
	})
	result, err := executeTarget(service, "create", map[string]string{"policy-id": targetPolicy, "amount": "1", "currency": "USD", "merchant": "Cafe", "date": "2026-10-18", "reimbursable": "false", "billable": "true"})
	if err != nil || result.ReportID == "" || result.ReportID == "200" || writes != 1 {
		t.Fatalf("result=%#v err=%v writes=%d", result, err, writes)
	}
}

func TestWorkspaceExpenseRejectsUnsafeTargetsBeforeWriting(t *testing.T) {
	for _, tc := range []struct {
		name   string
		edit   func(map[string]any)
		policy string
	}{
		{"submitted", func(r map[string]any) { r["stateNum"] = 1 }, ""},
		{"closed", func(r map[string]any) { r["statusNum"] = 2 }, ""},
		{"another owner", func(r map[string]any) { r["ownerAccountID"] = 43 }, ""},
		{"missing owner", func(r map[string]any) { delete(r, "ownerAccountID") }, ""},
		{"missing state", func(r map[string]any) { delete(r, "stateNum") }, ""},
		{"chat", func(r map[string]any) { r["type"] = "chat" }, ""},
		{"policy mismatch", func(map[string]any) {}, "OTHER"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := testService(t, func(r *http.Request) map[string]any {
				v := form(t, r)
				if r.URL.Path == "/api/OpenReport" && v.Get("reportID") == "200" {
					report := targetDraft("200", targetPolicy)
					tc.edit(report)
					return ok(patch("report_200", report))
				}
				return targetRead(t, r, v)
			})
			_, err := executeTarget(service, "create", map[string]string{"report-id": "200", "policy-id": tc.policy, "amount": "1", "currency": "USD", "merchant": "Cafe", "date": "2026-10-18"})
			if err == nil {
				t.Fatal("unsafe target accepted")
			}
		})
	}
}

func TestWorkspaceExpenseUploadsReceiptInItsCreateRequest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receipt.pdf")
	const contents = "%PDF-1.4\nreceipt\n"
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	writes := 0
	service := testService(t, func(r *http.Request) map[string]any {
		if r.URL.Path != "/api/RequestMoney" {
			return targetRead(t, r, form(t, r))
		}
		writes++
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("receiptState") != "SCANREADY" || r.Form.Get("iouReportID") != "200" || r.Form.Get("authToken") != "test-token" {
			t.Fatal("missing multipart parameters")
		}
		file, header, err := r.FormFile("receipt")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		got, err := io.ReadAll(file)
		if err != nil || string(got) != contents || header.Filename != "receipt.pdf" {
			t.Fatalf("invalid receipt %q: %v", got, err)
		}
		return ok()
	})
	_, err := executeTarget(service, "create", map[string]string{"report-id": "200", "amount": "1", "currency": "USD", "merchant": "Cafe", "date": "2026-10-18", "receipt": path})
	if err != nil || writes != 1 {
		t.Fatalf("err=%v writes=%d", err, writes)
	}
}

func moveApp() map[string]any {
	return targetApp(patch("transactions_400", map[string]any{"transactionID": "400", "reportID": "0", "amount": -1250, "currency": "EUR", "cardID": 55}), patch("cardList", map[string]any{"55": map[string]any{"cardID": 55}}))
}

func TestMoveUnreportedExpenseUsesCSVAndActionMap(t *testing.T) {
	writes := 0
	service := testService(t, func(r *http.Request) map[string]any {
		v := form(t, r)
		if r.URL.Path == "/api/OpenApp" {
			return moveApp()
		}
		if r.URL.Path != "/api/ChangeTransactionsReport" {
			return targetRead(t, r, v)
		}
		writes++
		if v.Get("transactionList") != "400" || v.Get("reportID") != "200" {
			t.Fatalf("wrong move destination: %v", v)
		}
		var actions map[string]map[string]string
		if err := json.Unmarshal([]byte(v.Get("transactionIDToReportActionAndThreadData")), &actions); err != nil {
			t.Fatal(err)
		}
		if len(actions) != 1 || !decimalIDPattern.MatchString(actions["400"]["moneyRequestPreviewReportActionID"]) || actions["400"]["movedReportActionID"] != "" {
			t.Fatalf("invalid move action map: %#v", actions)
		}
		return ok()
	})
	result, err := executeTarget(service, "move", map[string]string{"expense-id": "400", "report-id": "200"})
	if err != nil || result.ID != "400" || result.ReportID != "200" || writes != 1 {
		t.Fatalf("result=%#v err=%v writes=%d", result, err, writes)
	}
}

func TestMoveRejectsSubmittedForeignOrCrossWorkspaceSource(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(map[string]any)
	}{
		{"submitted", func(r map[string]any) { r["stateNum"] = 1; r["statusNum"] = 1 }},
		{"another owner", func(r map[string]any) { r["ownerAccountID"] = 43 }},
		{"different workspace", func(r map[string]any) { r["policyID"] = "OTHER" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := testService(t, func(r *http.Request) map[string]any {
				v := form(t, r)
				if r.URL.Path == "/api/OpenApp" {
					return targetApp(patch("transactions_400", map[string]any{"transactionID": "400", "reportID": "500", "amount": -1250, "currency": "EUR"}))
				}
				if r.URL.Path == "/api/OpenReport" && v.Get("reportID") == "500" {
					source := targetDraft("500", targetPolicy)
					tc.edit(source)
					return ok(patch("report_500", source))
				}
				return targetRead(t, r, v)
			})
			if _, err := executeTarget(service, "move", map[string]string{"expense-id": "400", "report-id": "200"}); err == nil {
				t.Fatal("unsafe move accepted")
			}
		})
	}
}

func TestMoveRejectsUnverifiedUnreportedOwnership(t *testing.T) {
	service := testService(t, func(r *http.Request) map[string]any {
		v := form(t, r)
		if r.URL.Path == "/api/OpenApp" {
			return targetApp(patch("transactions_400", map[string]any{"transactionID": "400", "reportID": "0", "amount": -1250, "currency": "EUR"}))
		}
		return targetRead(t, r, v)
	})
	if _, err := executeTarget(service, "move", map[string]string{"expense-id": "400", "report-id": "200"}); err == nil {
		t.Fatal("unverified owner accepted")
	}
}

func TestSendExpenseReceiptFailureDoesNotWrite(t *testing.T) {
	service := testService(t, func(*http.Request) map[string]any { t.Fatal("request sent for unreadable receipt"); return nil })
	err := service.sendExpense(context.Background(), "RequestMoney", url.Values{"transactionID": {"400"}}, filepath.Join(t.TempDir(), "missing.pdf"))
	if err == nil || strings.Contains(err.Error(), "missing.pdf") {
		t.Fatalf("expected sanitized receipt error: %v", err)
	}
}

func TestWorkspaceExpenseRejectsAnotherMembersOrThreadChat(t *testing.T) {
	for _, fresh := range []bool{false, true} {
		for _, tc := range []struct {
			name string
			edit func(map[string]any)
		}{
			{"another member", func(chat map[string]any) { chat["ownerAccountID"] = 43 }},
			{"another policy", func(chat map[string]any) { chat["policyID"] = "OTHER" }},
			{"thread", func(chat map[string]any) { chat["parentReportID"], chat["parentReportActionID"] = "1", "2" }},
		} {
			t.Run(tc.name+strconv.FormatBool(fresh), func(t *testing.T) {
				service := testService(t, func(r *http.Request) map[string]any {
					v := form(t, r)
					chat := targetChat()
					tc.edit(chat)
					if !fresh && r.URL.Path == "/api/OpenApp" {
						return ok(patch("session", map[string]any{"accountID": 42}), patch("report_100", chat))
					}
					if fresh && r.URL.Path == "/api/OpenReport" && v.Get("reportID") == "100" {
						return ok(patch("report_100", chat))
					}
					return targetRead(t, r, v)
				})
				if _, err := executeTarget(service, "create", map[string]string{"policy-id": targetPolicy, "amount": "1", "currency": "USD", "merchant": "Cafe"}); err == nil {
					t.Fatal("unsafe workspace chat accepted")
				}
			})
		}
	}
}

func TestMoveRejectsKnownForeignCardholder(t *testing.T) {
	service := testService(t, func(r *http.Request) map[string]any {
		v := form(t, r)
		if r.URL.Path == "/api/OpenApp" {
			return targetApp(patch("transactions_400", map[string]any{"transactionID": "400", "reportID": "0", "amount": -1250, "currency": "EUR", "cardID": 55}), patch("cardList", map[string]any{"55": map[string]any{"cardID": 55, "accountID": 43}}))
		}
		return targetRead(t, r, v)
	})
	if _, err := executeTarget(service, "move", map[string]string{"expense-id": "400", "report-id": "200"}); err == nil {
		t.Fatal("another cardholder's expense accepted")
	}
}

func TestMoveSearchResultRetainsCardOwnership(t *testing.T) {
	writes, searches := 0, 0
	service := testService(t, func(r *http.Request) map[string]any {
		v := form(t, r)
		switch r.URL.Path {
		case "/api/OpenApp":
			return targetApp(patch("cardList", map[string]any{"55": map[string]any{"cardID": 55, "accountID": 42}}))
		case "/api/Search":
			searches++
			return ok(patch("snapshot_"+v.Get("hash"), map[string]any{"search": map[string]any{"offset": 0, "hasMoreResults": false}, "data": map[string]any{"transactions_400": map[string]any{"transactionID": "400", "reportID": "0", "amount": -1250, "currency": "EUR", "cardID": 55}}}))
		case "/api/ChangeTransactionsReport":
			writes++
			return ok()
		}
		return targetRead(t, r, v)
	})
	result, err := executeTarget(service, "move", map[string]string{"expense-id": "400", "report-id": "200"})
	if err != nil || result.Action != "moved" || writes != 1 || searches != 1 {
		t.Fatalf("result=%#v err=%v writes=%d searches=%d", result, err, writes, searches)
	}
}

func TestMoveOwnSelfDMExpenseRequiresOriginalActor(t *testing.T) {
	for _, sourceID := range []string{"0", "600"} {
		for _, actor := range []int{42, 43} {
			t.Run(sourceID+strconv.Itoa(actor), func(t *testing.T) {
				writes := 0
				service := testService(t, func(r *http.Request) map[string]any {
					v := form(t, r)
					if r.URL.Path == "/api/OpenApp" {
						return targetApp(patch("selfDMReportID", "600"), patch("transactions_400", map[string]any{"transactionID": "400", "reportID": sourceID, "amount": -1250, "currency": "EUR"}))
					}
					if r.URL.Path == "/api/OpenReport" && v.Get("reportID") == "600" {
						return ok(patch("report_600", map[string]any{"reportID": "600", "type": "chat", "chatType": "selfDM", "ownerAccountID": 42}), patch("reportActions_600", map[string]any{"700": map[string]any{"actorAccountID": actor, "originalMessage": map[string]any{"IOUTransactionID": "400"}}}))
					}
					if r.URL.Path == "/api/ChangeTransactionsReport" {
						writes++
						return ok()
					}
					return targetRead(t, r, v)
				})
				_, err := executeTarget(service, "move", map[string]string{"expense-id": "400", "report-id": "200"})
				if actor == 42 && (err != nil || writes != 1) || actor != 42 && (err == nil || writes != 0) {
					t.Fatalf("actor=%d err=%v writes=%d", actor, err, writes)
				}
			})
		}
	}
}

func TestMoveAlreadyInDestinationDoesNotWrite(t *testing.T) {
	service := testService(t, func(r *http.Request) map[string]any {
		v := form(t, r)
		if r.URL.Path == "/api/OpenApp" {
			return targetApp(patch("transactions_400", map[string]any{"transactionID": "400", "reportID": "200", "amount": -1250, "currency": "EUR"}))
		}
		return targetRead(t, r, v)
	})
	result, err := executeTarget(service, "move", map[string]string{"expense-id": "400", "report-id": "200"})
	if err != nil || result.Action != "unchanged" || result.ReportID != "200" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestMoveOwnDraftExpenseWithinWorkspace(t *testing.T) {
	writes := 0
	service := testService(t, func(r *http.Request) map[string]any {
		v := form(t, r)
		if r.URL.Path == "/api/OpenApp" {
			return targetApp(patch("transactions_400", map[string]any{"transactionID": "400", "reportID": "500", "amount": -1250, "currency": "EUR"}))
		}
		if r.URL.Path == "/api/OpenReport" && v.Get("reportID") == "500" {
			return ok(patch("report_500", targetDraft("500", targetPolicy)))
		}
		if r.URL.Path == "/api/ChangeTransactionsReport" {
			writes++
			return ok()
		}
		return targetRead(t, r, v)
	})
	if _, err := executeTarget(service, "move", map[string]string{"expense-id": "400", "report-id": "200"}); err != nil || writes != 1 {
		t.Fatalf("err=%v writes=%d", err, writes)
	}
}

func TestMoveRejectsUnsafeDestinationBeforeWriting(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(map[string]any)
	}{
		{"submitted", func(report map[string]any) { report["stateNum"] = 1 }},
		{"closed", func(report map[string]any) { report["statusNum"] = 2 }},
		{"another owner", func(report map[string]any) { report["ownerAccountID"] = 43 }},
		{"missing state", func(report map[string]any) { delete(report, "stateNum") }},
		{"missing policy", func(report map[string]any) { delete(report, "policyID") }},
		{"chat", func(report map[string]any) { report["type"] = "chat" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := testService(t, func(r *http.Request) map[string]any {
				v := form(t, r)
				if r.URL.Path == "/api/OpenApp" {
					return moveApp()
				}
				if r.URL.Path == "/api/OpenReport" && v.Get("reportID") == "200" {
					report := targetDraft("200", targetPolicy)
					tc.edit(report)
					return ok(patch("report_200", report))
				}
				return targetRead(t, r, v)
			})
			if _, err := executeTarget(service, "move", map[string]string{"expense-id": "400", "report-id": "200"}); err == nil {
				t.Fatal("unsafe destination accepted")
			}
		})
	}
}

func TestWorkspaceExpenseRejectsInvalidAccountBeforeWriting(t *testing.T) {
	for _, account := range []any{nil, 0, -1, "not-an-account"} {
		service := testService(t, func(r *http.Request) map[string]any {
			if r.URL.Path != "/api/OpenApp" {
				t.Fatalf("request made with invalid identity: %s", r.URL.Path)
			}
			return ok(patch("session", map[string]any{"accountID": account}))
		})
		if _, err := executeTarget(service, "create", map[string]string{"policy-id": targetPolicy, "amount": "1", "currency": "USD", "merchant": "Cafe"}); err == nil {
			t.Fatalf("invalid account accepted: %#v", account)
		}
	}
}

func executeTarget(service *Service, verb string, values map[string]string) (Mutation, error) {
	result, err := service.Execute(context.Background(), "expense", verb, values)
	if err != nil {
		return Mutation{}, err
	}
	return result.(Mutation), nil
}
