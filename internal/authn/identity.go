package authn

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"unicode"
	"unicode/utf8"
)

type Identity struct {
	Provider    string
	Issuer      string
	Subject     string
	DisplayName string
	Groups      []string
}

type ScopeMapper struct {
	InstallationID int64
	RepositoryIDs  []int64
	AllowedGroups  []string
}

func (m ScopeMapper) Map(identity Identity) (Principal, error) {
	if !validIdentity(identity) {
		return Principal{}, ErrInvalidIdentity
	}
	allowed := uniqueStrings(m.AllowedGroups)
	if len(allowed) > 0 && !hasAllowedGroup(allowed, identity.Groups) {
		return Principal{}, ErrIdentityForbidden
	}
	return Principal{
		Subject:        identitySubject(identity),
		Method:         identity.Provider,
		DisplayName:    identity.DisplayName,
		InstallationID: m.InstallationID,
		RepositoryIDs:  uniqueInt64s(m.RepositoryIDs),
	}, nil
}

func validIdentity(identity Identity) bool {
	if !validIdentityField(identity.Provider, 256) || !validIdentityField(identity.Issuer, 2<<10) || !validIdentityField(identity.Subject, 1<<10) || !utf8.ValidString(identity.DisplayName) || len(identity.DisplayName) > 256 || len(identity.Groups) > 256 {
		return false
	}
	total := 0
	for _, group := range identity.Groups {
		if group == "" || !utf8.ValidString(group) || len(group) > 256 {
			return false
		}
		total += len(group)
		if total > 32<<10 {
			return false
		}
		for _, r := range group {
			if unicode.IsControl(r) {
				return false
			}
		}
	}
	return true
}

func validIdentityField(value string, limit int) bool {
	if value == "" || !utf8.ValidString(value) || len(value) > limit {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func identitySubject(identity Identity) string {
	sum := sha256.Sum256([]byte(identity.Issuer + "\x00" + identity.Subject))
	return identity.Provider + ":" + hex.EncodeToString(sum[:])
}

func hasAllowedGroup(allowed, groups []string) bool {
	for _, group := range groups {
		index := sort.SearchStrings(allowed, group)
		if index < len(allowed) && allowed[index] == group {
			return true
		}
	}
	return false
}

func uniqueStrings(values []string) []string {
	values = append([]string(nil), values...)
	sort.Strings(values)
	return compactStrings(values)
}

func compactStrings(values []string) []string {
	if len(values) == 0 {
		return values
	}
	n := 1
	for _, value := range values[1:] {
		if value != values[n-1] {
			values[n] = value
			n++
		}
	}
	return values[:n]
}

func uniqueInt64s(values []int64) []int64 {
	values = append([]int64(nil), values...)
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	return compactInt64s(values)
}

func compactInt64s(values []int64) []int64 {
	if len(values) == 0 {
		return values
	}
	n := 1
	for _, value := range values[1:] {
		if value != values[n-1] {
			values[n] = value
			n++
		}
	}
	return values[:n]
}
