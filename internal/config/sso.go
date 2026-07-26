package config

import (
	"net/url"
	"os"
	"strings"
	"time"
)

type SSO struct {
	PublicURL    *url.URL
	SessionTTL   time.Duration
	LoginFlowTTL time.Duration
	OIDC         OIDC
}

type OIDC struct {
	Enabled          bool
	IssuerURL        string
	ClientID         string
	ClientSecretFile string
	CAFile           string
	Scopes           []string
	GroupsClaim      string
	AllowedGroups    []string
	DisplayNameClaim string
}

func loadSSO(databaseURL string) (SSO, error) {
	sso := SSO{}
	var err error
	if sso.SessionTTL, err = parseBoundedDuration("GREPNEST_SSO_SESSION_TTL", os.Getenv("GREPNEST_SSO_SESSION_TTL"), 8*time.Hour, 5*time.Minute, 24*time.Hour); err != nil {
		return SSO{}, err
	}
	if sso.LoginFlowTTL, err = parseBoundedDuration("GREPNEST_SSO_LOGIN_FLOW_TTL", os.Getenv("GREPNEST_SSO_LOGIN_FLOW_TTL"), 10*time.Minute, time.Minute, 15*time.Minute); err != nil {
		return SSO{}, err
	}

	issuerURL := os.Getenv("GREPNEST_OIDC_ISSUER_URL")
	clientID := os.Getenv("GREPNEST_OIDC_CLIENT_ID")
	clientSecretFile := os.Getenv("GREPNEST_OIDC_CLIENT_SECRET_FILE")
	if issuerURL == "" && clientID == "" && clientSecretFile == "" {
		return sso, nil
	}
	if databaseURL == "" {
		return SSO{}, invalid("GREPNEST_DATABASE_URL is required for OIDC")
	}
	for _, setting := range []struct{ name, value string }{
		{"GREPNEST_OIDC_ISSUER_URL", issuerURL},
		{"GREPNEST_OIDC_CLIENT_ID", clientID},
		{"GREPNEST_OIDC_CLIENT_SECRET_FILE", clientSecretFile},
	} {
		if setting.value == "" {
			return SSO{}, invalid(setting.name + " is required for OIDC")
		}
	}
	if sso.PublicURL, err = parseHTTPSOrigin("GREPNEST_PUBLIC_URL", os.Getenv("GREPNEST_PUBLIC_URL")); err != nil {
		return SSO{}, err
	}
	issuer, err := url.ParseRequestURI(issuerURL)
	if err != nil || issuer.Scheme != "https" || issuer.Host == "" || issuer.User != nil || issuer.RawQuery != "" || issuer.Fragment != "" {
		return SSO{}, invalid("GREPNEST_OIDC_ISSUER_URL must be an HTTPS URL without userinfo, query, or fragment")
	}
	scopes, err := parseCSV("GREPNEST_OIDC_SCOPES", valueOr("GREPNEST_OIDC_SCOPES", "openid,profile,email"), "openid")
	if err != nil {
		return SSO{}, err
	}
	allowedGroups, err := parseCSV("GREPNEST_OIDC_ALLOWED_GROUPS", os.Getenv("GREPNEST_OIDC_ALLOWED_GROUPS"), "")
	if err != nil {
		return SSO{}, err
	}
	sso.OIDC = OIDC{
		Enabled:          true,
		IssuerURL:        issuerURL,
		ClientID:         clientID,
		ClientSecretFile: clientSecretFile,
		CAFile:           os.Getenv("GREPNEST_OIDC_CA_FILE"),
		Scopes:           scopes,
		GroupsClaim:      valueOr("GREPNEST_OIDC_GROUPS_CLAIM", "groups"),
		AllowedGroups:    allowedGroups,
		DisplayNameClaim: valueOr("GREPNEST_OIDC_DISPLAY_NAME_CLAIM", "name"),
	}
	return sso, nil
}

func parseHTTPSOrigin(name, value string) (*url.URL, error) {
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return nil, invalid(name + " must be an HTTPS origin")
	}
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	return parsed, nil
}

func parseCSV(name, value, required string) ([]string, error) {
	if value == "" {
		if required != "" {
			return nil, invalid(name + " must contain " + required)
		}
		return nil, nil
	}
	seen := make(map[string]bool)
	values := make([]string, 0, len(strings.Split(value, ",")))
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			return nil, invalid(name + " must not contain empty values")
		}
		if !seen[item] {
			seen[item] = true
			values = append(values, item)
		}
	}
	if required != "" && !seen[required] {
		return nil, invalid(name + " must contain " + required)
	}
	return values, nil
}

func parseBoundedDuration(name, value string, fallback, min, max time.Duration) (time.Duration, error) {
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed < min || parsed > max {
		return 0, invalid(name + " must be between " + min.String() + " and " + max.String())
	}
	return parsed, nil
}
