package expensify

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/magarcia/expensify-cli/internal/fault"
)

func TestMemberPolicyItemsUseAuthoritativeReadContract(t *testing.T) {
	for _, tc := range []struct {
		noun, command, key string
		collection         map[string]any
		want               []NamedItem
	}{
		{
			"category", "GetPolicyCategories", "policyCategories_WORKSPACE",
			map[string]any{
				"Books":    map[string]any{"name": "Books", "enabled": true},
				"Software": map[string]any{"name": "Software", "enabled": true},
			},
			[]NamedItem{{Name: "Books", Enabled: true}, {Name: "Software", Enabled: true}},
		},
		{
			"tag", "OpenPolicyTagsPage", "policyTags_WORKSPACE",
			map[string]any{
				"Department": map[string]any{"name": "Department", "tags": map[string]any{
					"SoftwareTag": map[string]any{"name": "SoftwareTag", "enabled": true},
				}},
			},
			[]NamedItem{{Name: "SoftwareTag", Enabled: true, Group: "Department"}},
		},
	} {
		t.Run(tc.noun, func(t *testing.T) {
			service := testService(t, func(r *http.Request) map[string]any {
				values := form(t, r)
				if r.URL.Path != "/api/"+tc.command || values.Get("policyID") != "WORKSPACE" {
					t.Fatalf("unexpected request %s", r.URL.Path)
				}
				if values.Get("apiRequestType") != "read" {
					return map[string]any{"jsonCode": 666}
				}
				return ok(patch(tc.key, tc.collection))
			})
			result, err := service.Execute(context.Background(), tc.noun, "list", map[string]string{"policy-id": "WORKSPACE"})
			if err != nil {
				t.Fatalf("member-visible %s list failed: %v", tc.noun, err)
			}
			if !reflect.DeepEqual(result, tc.want) {
				t.Fatalf("items = %#v; want %#v", result, tc.want)
			}
		})
	}
}

func TestPolicyItemReadFailureDoesNotInferValuesFromAppOrExpenses(t *testing.T) {
	for _, noun := range []string{"category", "tag"} {
		t.Run(noun, func(t *testing.T) {
			calls := 0
			service := testService(t, func(r *http.Request) map[string]any {
				calls++
				form(t, r)
				if calls > 2 || (calls == 2 && r.URL.Path != "/api/Get") {
					t.Fatalf("unexpected request after rejected coding reads: %s", r.URL.Path)
				}
				return map[string]any{"jsonCode": 666}
			})
			_, err := service.Execute(context.Background(), noun, "list", map[string]string{"policy-id": "WORKSPACE"})
			var apiErr *fault.Error
			if !errors.As(err, &apiErr) || apiErr.Code != "API-001" || !strings.Contains(apiErr.Message, "jsonCode 666") {
				t.Fatalf("rejected member read = %v; want preserved API-001 jsonCode666", err)
			}
			if calls != 2 {
				t.Fatalf("rejected authoritative reads made %d calls; must not infer a partial list", calls)
			}
		})
	}
}

func TestPolicyItemReadRequiresRequestedCollection(t *testing.T) {
	for _, tc := range []struct{ noun, key string }{
		{"category", "policyCategories_OTHER"},
		{"tag", "policyTags_OTHER"},
	} {
		t.Run(tc.noun, func(t *testing.T) {
			calls := 0
			service := testService(t, func(r *http.Request) map[string]any {
				calls++
				form(t, r)
				return ok(
					patch(tc.key, map[string]any{"Selected elsewhere": map[string]any{"enabled": true}}),
					patch("transactions_123", map[string]any{"category": "Selected only", "tag": "Observed only"}),
				)
			})
			_, err := service.Execute(context.Background(), tc.noun, "list", map[string]string{"policy-id": "WORKSPACE"})
			var apiErr *fault.Error
			if !errors.As(err, &apiErr) || apiErr.Code != "API-004" {
				t.Fatalf("missing requested collection = %v; want API-004", err)
			}
			if calls != 1 {
				t.Fatalf("successful response missing its collection made %d requests; want no fallback", calls)
			}
		})
	}
}

func fullPolicyResponse(value map[string]any) map[string]any {
	return map[string]any{"jsonCode": 200, "policyList": []any{
		map[string]any{"id": "WORKSPACE", "role": "user", "value": value},
	}}
}

func TestMemberPolicyItemsFallBackToCompleteFullPolicy(t *testing.T) {
	for _, tc := range []struct {
		noun string
		want []NamedItem
	}{
		{"category", []NamedItem{{Name: "Books", Enabled: false}, {Name: "Software", Enabled: true}}},
		{"tag", []NamedItem{
			{Name: "Engineering", Enabled: true, Group: "Department"},
			{Name: "Operations", Enabled: false, Group: "Department"},
			{Name: "Software", Enabled: true, Group: "Project"},
		}},
	} {
		t.Run(tc.noun, func(t *testing.T) {
			calls := 0
			service := testService(t, func(r *http.Request) map[string]any {
				calls++
				values := form(t, r)
				if calls == 1 {
					return map[string]any{"jsonCode": 666}
				}
				if calls != 2 || r.URL.Path != "/api/Get" || values.Get("returnValueList") != "policyList" || values.Get("policyIDList") != "WORKSPACE" {
					t.Fatalf("unexpected full policy request %s %v", r.URL.Path, values)
				}
				return fullPolicyResponse(map[string]any{
					"categoryObjects": map[string]any{
						"Books":    map[string]any{"name": "Books", "enabled": false},
						"Software": map[string]any{"name": "Software", "enabled": true},
					},
					"tagObjects": []any{},
					"tagLists": []any{
						map[string]any{"tagListData": map[string]any{"name": "Department", "tags": []any{
							map[string]any{"name": "Operations", "enabled": false},
							map[string]any{"name": "Engineering", "enabled": true},
						}}},
						map[string]any{"tagListData": map[string]any{"name": "Project", "tags": []any{
							map[string]any{"name": "Software", "enabled": true},
						}}},
					},
				})
			})
			got, err := service.Execute(context.Background(), tc.noun, "list", map[string]string{"policy-id": "WORKSPACE"})
			if err != nil {
				t.Fatalf("member full policy fallback failed: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) || calls != 2 {
				t.Fatalf("items = %#v; want %#v, calls=%d", got, tc.want, calls)
			}
		})
	}
}

func TestFullPolicyItemsSelectRequestedWorkspace(t *testing.T) {
	service := testService(t, func(r *http.Request) map[string]any {
		form(t, r)
		response := fullPolicyResponse(map[string]any{"categoryObjects": map[string]any{
			"Books": map[string]any{"name": "Books", "enabled": false},
		}})
		response["policyList"] = append([]any{map[string]any{"id": "OTHER", "value": map[string]any{
			"categoryObjects": map[string]any{"Other": map[string]any{"name": "Other", "enabled": true}},
		}}}, response["policyList"].([]any)...)
		return response
	})
	got, err := service.fullPolicyItems(context.Background(), "category", "WORKSPACE")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || object(got["Books"])["enabled"] != false {
		t.Fatalf("selected wrong workspace categories: %#v", got)
	}
}

func TestFullPolicyEmptyCollectionsAndFlatTags(t *testing.T) {
	for _, tc := range []struct {
		name, noun string
		value      map[string]any
		want       map[string]any
	}{
		{"empty categories object", "category", map[string]any{"categoryObjects": map[string]any{}}, map[string]any{}},
		{"empty categories PHP array", "category", map[string]any{"categoryObjects": []any{}}, map[string]any{}},
		{"empty tags PHP array", "tag", map[string]any{"tagObjects": []any{}}, map[string]any{}},
		{"empty lists with flat tags", "tag", map[string]any{"tagLists": []any{}, "tagObjects": map[string]any{}}, map[string]any{}},
		{"flat tags", "tag", map[string]any{"tagObjects": map[string]any{
			"Software": map[string]any{"name": "Software", "enabled": false},
		}}, map[string]any{"Software": map[string]any{"name": "Software", "enabled": false}}},
		{"empty group", "tag", map[string]any{"tagLists": []any{
			map[string]any{"tagListData": map[string]any{"name": "Department", "tags": []any{}}},
		}}, map[string]any{"Department": map[string]any{"name": "Department", "tags": map[string]any{}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := testService(t, func(r *http.Request) map[string]any { form(t, r); return fullPolicyResponse(tc.value) })
			got, err := service.fullPolicyItems(context.Background(), tc.noun, "WORKSPACE")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("coding items = %#v; want %#v", got, tc.want)
			}
		})
	}
}

func TestFullPolicyItemsRejectIncompleteOrAmbiguousData(t *testing.T) {
	category := func(raw any) map[string]any { return fullPolicyResponse(map[string]any{"categoryObjects": raw}) }
	tags := func(raw any) map[string]any { return fullPolicyResponse(map[string]any{"tagLists": raw}) }
	group := func(name string, rows any) map[string]any {
		return map[string]any{"tagListData": map[string]any{"name": name, "tags": rows}}
	}
	for _, tc := range []struct {
		name, noun string
		response   map[string]any
		code       string
	}{
		{"missing policyList", "category", map[string]any{"jsonCode": 200}, "API-004"},
		{"policyList object", "category", map[string]any{"jsonCode": 200, "policyList": map[string]any{}}, "API-004"},
		{"missing matching workspace", "category", map[string]any{"jsonCode": 200, "policyList": []any{map[string]any{"id": "OTHER"}}}, "API-404"},
		{"invalid policy row", "category", map[string]any{"jsonCode": 200, "policyList": []any{"WORKSPACE"}}, "API-004"},
		{"duplicated workspace", "category", map[string]any{"jsonCode": 200, "policyList": []any{map[string]any{"id": "WORKSPACE"}, map[string]any{"id": "WORKSPACE"}}}, "API-004"},
		{"missing value", "category", map[string]any{"jsonCode": 200, "policyList": []any{map[string]any{"id": "WORKSPACE"}}}, "API-004"},
		{"missing categories", "category", fullPolicyResponse(map[string]any{}), "API-004"},
		{"category array", "category", category([]any{"Books"}), "API-004"},
		{"category scalar", "category", category(map[string]any{"Books": true}), "API-004"},
		{"category missing name", "category", category(map[string]any{"Books": map[string]any{"enabled": true}}), "API-004"},
		{"category invalid enabled", "category", category(map[string]any{"Books": map[string]any{"name": "Books", "enabled": "false"}}), "API-004"},
		{"category duplicated name", "category", category(map[string]any{"A": map[string]any{"name": "Books"}, "B": map[string]any{"name": "Books"}}), "API-004"},
		{"missing tags", "tag", fullPolicyResponse(map[string]any{}), "API-004"},
		{"nonempty flat tag array", "tag", fullPolicyResponse(map[string]any{"tagObjects": []any{"Software"}}), "API-004"},
		{"tagLists object", "tag", tags(map[string]any{}), "API-004"},
		{"missing tagListData", "tag", tags([]any{map[string]any{"name": "Department"}}), "API-004"},
		{"group missing name", "tag", tags([]any{group("", []any{})}), "API-004"},
		{"group missing tags", "tag", tags([]any{map[string]any{"tagListData": map[string]any{"name": "Department"}}}), "API-004"},
		{"group tags object", "tag", tags([]any{group("Department", map[string]any{})}), "API-004"},
		{"malformed tag row", "tag", tags([]any{group("Department", []any{"Software"})}), "API-004"},
		{"tag invalid enabled", "tag", tags([]any{group("Department", []any{map[string]any{"name": "Software", "enabled": "false"}})}), "API-004"},
		{"duplicated group", "tag", tags([]any{group("Department", []any{}), group("Department", []any{})}), "API-004"},
		{"duplicated tag", "tag", tags([]any{group("Department", []any{map[string]any{"name": "Software"}, map[string]any{"name": "Software"}})}), "API-004"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := testService(t, func(r *http.Request) map[string]any { form(t, r); return tc.response })
			_, err := service.fullPolicyItems(context.Background(), tc.noun, "WORKSPACE")
			var apiErr *fault.Error
			if !errors.As(err, &apiErr) || apiErr.Code != tc.code {
				t.Fatalf("response accepted or wrong error: %v; want %s", err, tc.code)
			}
		})
	}
}

func TestPolicyItemsDoNotFallBackAfterAuthOrRateLimitFailure(t *testing.T) {
	for _, code := range []int{403, 404, 429} {
		calls := 0
		service := testService(t, func(r *http.Request) map[string]any {
			calls++
			form(t, r)
			return map[string]any{"jsonCode": code}
		})
		if _, err := service.policyItems(context.Background(), "category", "WORKSPACE"); err == nil || calls != 1 {
			t.Fatalf("jsonCode%d: err=%v, calls=%d; want one failed request", code, err, calls)
		}
	}
}
