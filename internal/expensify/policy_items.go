package expensify

import (
	"context"
	"errors"
	"net/url"

	"github.com/magarcia/expensify-cli/internal/fault"
)

func (s *Service) policyItems(ctx context.Context, noun, policyID string) (map[string]any, error) {
	command, prefix := "GetPolicyCategories", "policyCategories_"
	if noun == "tag" {
		command, prefix = "OpenPolicyTagsPage", "policyTags_"
	}
	response, err := s.client.Read(ctx, command, url.Values{"policyID": {policyID}})
	if err != nil {
		var rejected *fault.Error
		if errors.As(err, &rejected) && rejected.Code == "API-001" {
			return s.fullPolicyItems(ctx, noun, policyID)
		}
		return nil, err
	}
	state, err := decodeSnapshot(response)
	if err != nil {
		return nil, err
	}
	raw, ok := state[prefix+policyID]
	if !ok {
		return nil, unexpected("workspace categories or tags are missing")
	}
	collection := onyxObject(raw)
	if collection == nil {
		return nil, unexpected("workspace categories or tags are not an object")
	}
	return collection, nil
}

func (s *Service) fullPolicyItems(ctx context.Context, noun, policyID string) (map[string]any, error) {
	// Full-policy reads expose member-visible coding lists without relying on
	// the potentially incomplete collections returned by app initialization.
	response, err := s.client.Call(ctx, "Get", url.Values{"returnValueList": {"policyList"}, "policyIDList": {policyID}})
	if err != nil {
		return nil, err
	}
	policies, ok := response["policyList"].([]any)
	if !ok {
		return nil, unexpected("full workspace policyList is not an array")
	}
	var policy map[string]any
	for _, entry := range policies {
		candidate := object(entry)
		id, validID := candidate["id"].(string)
		if candidate == nil || !validID || id == "" {
			return nil, unexpected("full workspace policy has no valid ID")
		}
		if id != policyID {
			continue
		}
		if policy != nil {
			return nil, unexpected("full workspace policy ID is duplicated")
		}
		policy = candidate
	}
	if policy == nil {
		return nil, notFound("Workspace", policyID)
	}
	value := object(policy["value"])
	if value == nil {
		return nil, unexpected("full workspace policy value is not an object")
	}
	if noun == "category" {
		return fullPolicyNamedObjects(value["categoryObjects"])
	}
	if rawLists, exists := value["tagLists"]; exists {
		lists, ok := rawLists.([]any)
		if !ok {
			return nil, unexpected("full workspace tagLists is not an array")
		}
		if len(lists) > 0 {
			return fullPolicyTagLists(lists)
		}
	}
	return fullPolicyNamedObjects(value["tagObjects"])
}

func fullPolicyNamedObjects(raw any) (map[string]any, error) {
	items := onyxObject(raw)
	if items == nil {
		return nil, unexpected("full workspace coding items are not an object")
	}
	names := make(map[string]bool, len(items))
	for key, value := range items {
		name, err := fullPolicyItemName(value)
		if err != nil {
			return nil, err
		}
		if key == "" || names[name] {
			return nil, unexpected("full workspace coding item name is empty or duplicated")
		}
		names[name] = true
	}
	return items, nil
}

func fullPolicyItemName(value any) (string, error) {
	item := object(value)
	name, ok := item["name"].(string)
	if item == nil || !ok || name == "" {
		return "", unexpected("full workspace coding item has no valid name")
	}
	if enabled, exists := item["enabled"]; exists {
		if _, ok := enabled.(bool); !ok {
			return "", unexpected("full workspace coding item enabled is not a boolean")
		}
	}
	return name, nil
}

func fullPolicyTagLists(lists []any) (map[string]any, error) {
	groups := make(map[string]any, len(lists))
	for _, raw := range lists {
		group := object(object(raw)["tagListData"])
		name, ok := group["name"].(string)
		if group == nil || !ok || name == "" {
			return nil, unexpected("full workspace tag list has no valid name")
		}
		if _, exists := groups[name]; exists {
			return nil, unexpected("full workspace tag list name is duplicated")
		}
		rows, ok := group["tags"].([]any)
		if !ok {
			return nil, unexpected("full workspace tag list tags is not an array")
		}
		tags := make(map[string]any, len(rows))
		for _, row := range rows {
			tagName, err := fullPolicyItemName(row)
			if err != nil {
				return nil, err
			}
			if _, exists := tags[tagName]; exists {
				return nil, unexpected("full workspace tag name is duplicated within a list")
			}
			tags[tagName] = row
		}
		groups[name] = map[string]any{"name": name, "tags": tags}
	}
	return groups, nil
}
