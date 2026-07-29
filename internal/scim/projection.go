package scim

import (
	"net/url"
	"strings"
)

type Projection struct{ Include, Exclude map[string]bool }

func ParseProjection(values url.Values, resource ResourceType) (Projection, error) {
	include, err := projectionValues(values, "attributes", resource)
	if err != nil {
		return Projection{}, err
	}
	exclude, err := projectionValues(values, "excludedAttributes", resource)
	if err != nil {
		return Projection{}, err
	}
	for _, attribute := range []string{"schemas", "id", "meta"} {
		include[attribute] = true
		delete(exclude, attribute)
	}
	return Projection{Include: include, Exclude: exclude}, nil
}

func projectionValues(values url.Values, name string, resource ResourceType) (map[string]bool, error) {
	result := map[string]bool{}
	raw, ok := values[name]
	if !ok {
		return result, nil
	}
	if len(raw) != 1 || raw[0] == "" {
		return nil, parseError("invalidValue")
	}
	for _, attribute := range strings.Split(raw[0], ",") {
		canonical, ok := filterAttribute(resource, strings.TrimSpace(attribute))
		if !ok {
			return nil, parseError("invalidPath")
		}
		result[canonical] = true
	}
	return result, nil
}
