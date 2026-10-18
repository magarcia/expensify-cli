package expensify

import (
	"context"
	"net/http"
	"testing"
)

func TestCategoryListUsesMemberExpensePickerEndpoint(t *testing.T) {
	service := testService(t, func(r *http.Request) map[string]any {
		values := form(t, r)
		if r.URL.Path != "/api/GetPolicyCategories" || values.Get("policyID") != "ABC" {
			t.Fatalf("wrong category endpoint: %s", r.URL.Path)
		}
		return ok(patch("policyCategories_ABC", map[string]any{"Software License Expense": map[string]any{"name": "Software License Expense", "enabled": true}}))
	})
	result, err := service.Execute(context.Background(), "category", "list", map[string]string{"policy-id": "ABC"})
	if err != nil {
		t.Fatal(err)
	}
	items := result.([]NamedItem)
	if len(items) != 1 || items[0].Name != "Software License Expense" || !items[0].Enabled {
		t.Fatalf("bad categories: %#v", items)
	}
}

func TestNamedItemListsAcceptEmptyPHPObjects(t *testing.T) {
	for noun, key := range map[string]string{"category": "policyCategories_ABC", "tag": "policyTags_ABC"} {
		t.Run(noun, func(t *testing.T) {
			service := testService(t, func(r *http.Request) map[string]any { return ok(patch(key, []any{})) })
			result, err := service.Execute(context.Background(), noun, "list", map[string]string{"policy-id": "ABC"})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.([]NamedItem)) != 0 {
				t.Fatalf("unexpected items: %#v", result)
			}
		})
	}
}

func TestTagListUsesAuthoritativeCollection(t *testing.T) {
	service := testService(t, func(r *http.Request) map[string]any {
		if r.URL.Path != "/api/OpenPolicyTagsPage" || form(t, r).Get("policyID") != "ABC" {
			t.Fatalf("wrong tag endpoint: %s", r.URL.Path)
		}
		return ok(patch("policyTags_ABC", map[string]any{"0": map[string]any{"name": "Department", "tags": map[string]any{"Engineering:Software Stipend": map[string]any{"enabled": true}}}}))
	})
	result, err := service.Execute(context.Background(), "tag", "list", map[string]string{"policy-id": "ABC"})
	if err != nil {
		t.Fatal(err)
	}
	items := result.([]NamedItem)
	if len(items) != 1 || items[0].Name != "Engineering:Software Stipend" || items[0].Group != "Department" {
		t.Fatalf("bad tags: %#v", items)
	}
}
