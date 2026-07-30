package scim

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

type ResourceType string

const (
	ResourceUsers  ResourceType = "Users"
	ResourceGroups ResourceType = "Groups"
)

type Filter struct{ Attribute, Value string }
type Page struct{ StartIndex, Count int }

type parseError string

func (e parseError) Error() string { return string(e) }

func ParseFilter(resource ResourceType, raw string) (Filter, error) {
	if len(raw) > 4096 {
		return Filter{}, parseError("invalidFilter")
	}
	attributeToken, rest, ok := strings.Cut(raw, " ")
	if !ok {
		return Filter{}, parseError("invalidFilter")
	}
	operator, valueToken, ok := strings.Cut(rest, " ")
	if !ok || attributeToken == "" || !strings.EqualFold(operator, "eq") {
		return Filter{}, parseError("invalidFilter")
	}
	attribute, ok := equalityFilterAttribute(resource, attributeToken)
	if !ok {
		return Filter{}, parseError("invalidFilter")
	}
	var value string
	if err := json.Unmarshal([]byte(valueToken), &value); err != nil {
		return Filter{}, parseError("invalidFilter")
	}
	return Filter{Attribute: attribute, Value: value}, nil
}

func ParsePage(values url.Values, max int) (Page, error) {
	startIndex, err := pageValue(values, "startIndex", 1)
	if err != nil || startIndex < 1 {
		return Page{}, parseError("invalidValue")
	}
	count, err := pageValue(values, "count", max)
	if err != nil || count < 0 {
		return Page{}, parseError("invalidValue")
	}
	if count > max {
		count = max
	}
	return Page{StartIndex: startIndex, Count: count}, nil
}

func pageValue(values url.Values, name string, defaultValue int) (int, error) {
	valuesForName, ok := values[name]
	if !ok {
		return defaultValue, nil
	}
	if len(valuesForName) != 1 || valuesForName[0] == "" {
		return 0, fmt.Errorf("invalid %s", name)
	}
	return strconv.Atoi(valuesForName[0])
}

func filterAttribute(resource ResourceType, attribute string) (string, bool) {
	for _, supported := range resourceAttributes(resource) {
		if strings.EqualFold(attribute, supported) {
			return supported, true
		}
	}
	return "", false
}

func equalityFilterAttribute(resource ResourceType, attribute string) (string, bool) {
	for _, supported := range map[ResourceType][]string{
		ResourceUsers:  {"id", "userName", "externalId"},
		ResourceGroups: {"id", "displayName", "externalId"},
	}[resource] {
		if strings.EqualFold(attribute, supported) {
			return supported, true
		}
	}
	return "", false
}

func resourceAttributes(resource ResourceType) []string {
	switch resource {
	case ResourceUsers:
		return []string{"id", "userName", "externalId", "displayName", "active", "name", "emails", "schemas", "meta"}
	case ResourceGroups:
		return []string{"id", "displayName", "externalId", "members", "schemas", "meta"}
	default:
		return nil
	}
}
