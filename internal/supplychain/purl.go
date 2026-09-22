package supplychain

import (
	"errors"
	"net/url"
	"sort"
	"strings"
)

var errInvalidPURL = errors.New("invalid package URL")

// PURL is a decoded package URL (https://github.com/package-url/purl-spec).
// Only what the inventory records is decoded: type, namespace, name, version,
// and qualifiers. The subpath is preserved inside the original string.
type PURL struct {
	Type, Namespace, Name, Version string
	Qualifiers                     map[string]string
}

// ParsePURL decodes a package URL leniently enough for real producer output
// (percent-encoded namespaces, missing versions) while rejecting values that
// are not package URLs at all. It never invents a version.
func ParsePURL(value string) (PURL, error) {
	scheme, rest, ok := strings.Cut(value, ":")
	if !ok || !strings.EqualFold(scheme, "pkg") {
		return PURL{}, errInvalidPURL
	}
	rest = strings.TrimLeft(rest, "/")
	if hash := strings.IndexByte(rest, '#'); hash >= 0 {
		rest = rest[:hash]
	}
	var rawQualifiers string
	if question := strings.IndexByte(rest, '?'); question >= 0 {
		rest, rawQualifiers = rest[:question], rest[question+1:]
	}
	packageType, nameVersion, ok := strings.Cut(rest, "/")
	if !ok || packageType == "" {
		return PURL{}, errInvalidPURL
	}
	result := PURL{Type: strings.ToLower(packageType)}
	if at := strings.LastIndexByte(nameVersion, '@'); at >= 0 {
		version, err := url.PathUnescape(nameVersion[at+1:])
		if err != nil {
			return PURL{}, errInvalidPURL
		}
		result.Version, nameVersion = version, nameVersion[:at]
	}
	segments := strings.Split(strings.Trim(nameVersion, "/"), "/")
	if len(segments) == 0 || segments[len(segments)-1] == "" {
		return PURL{}, errInvalidPURL
	}
	name, err := url.PathUnescape(segments[len(segments)-1])
	if err != nil || name == "" {
		return PURL{}, errInvalidPURL
	}
	result.Name = name
	namespace := make([]string, 0, len(segments)-1)
	for _, segment := range segments[:len(segments)-1] {
		decoded, err := url.PathUnescape(segment)
		if err != nil || decoded == "" {
			return PURL{}, errInvalidPURL
		}
		namespace = append(namespace, decoded)
	}
	result.Namespace = strings.Join(namespace, "/")
	if rawQualifiers != "" {
		result.Qualifiers = map[string]string{}
		for _, pair := range strings.Split(rawQualifiers, "&") {
			key, rawValue, _ := strings.Cut(pair, "=")
			key = strings.ToLower(key)
			if key == "" {
				return PURL{}, errInvalidPURL
			}
			qualifierValue, err := url.QueryUnescape(rawValue)
			if err != nil {
				return PURL{}, errInvalidPURL
			}
			result.Qualifiers[key] = qualifierValue
		}
	}
	return result, nil
}

// QualifierKeys returns qualifier names in stable order for deterministic
// output.
func (purl PURL) QualifierKeys() []string {
	keys := make([]string, 0, len(purl.Qualifiers))
	for key := range purl.Qualifiers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
