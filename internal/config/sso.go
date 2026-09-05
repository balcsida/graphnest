package config

import (
	"net/url"
	"os"
	"strings"
	"time"
)

type SSO struct {
	PublicURL    *url.URL
	SessionIdle  time.Duration
	SessionTTL   time.Duration
	LoginFlowTTL time.Duration
	BreakGlass   bool
	OIDC         OIDC
	OAuth        OAuth
	MCPOAuth     MCPOAuth
}

// MCPOAuth turns GraphNest into an OAuth 2.1 authorization server for MCP
// clients: they sign in through the browser provider and receive short-lived
// access tokens for /mcp. KeyFile holds the 32-byte key that seals the GitHub
// token kept for refresh-time access sync; it is required only when GitHub
// access sync is enabled.
type MCPOAuth struct {
	Enabled bool
	KeyFile string
}

type OAuth struct{ GitHub GitHubOAuth }

type GitHubOAuth struct {
	Enabled          bool
	ClientID         string
	ClientSecretFile string
	// AccessSync provisions users on first sign-in and mirrors the repositories
	// they can reach through the configured GitHub App. It requires the OAuth
	// client to be that GitHub App's own OAuth credential.
	AccessSync bool
}

type OIDC struct {
	Enabled          bool
	IssuerURL        string
	ClientID         string
	ClientSecretFile string
	CAFile           string
	Scopes           []string
	LinkClaim        string
	DisplayNameClaim string
}

func loadSSO(databaseURL string) (SSO, error) {
	sso := SSO{}
	var err error
	if sso.SessionIdle, err = parseBoundedDuration("GRAPHNEST_SSO_SESSION_IDLE", os.Getenv("GRAPHNEST_SSO_SESSION_IDLE"), 30*time.Minute, 5*time.Minute, 24*time.Hour); err != nil {
		return SSO{}, err
	}
	if sso.SessionTTL, err = parseBoundedDuration("GRAPHNEST_SSO_SESSION_TTL", os.Getenv("GRAPHNEST_SSO_SESSION_TTL"), 8*time.Hour, 5*time.Minute, 24*time.Hour); err != nil {
		return SSO{}, err
	}
	if sso.SessionIdle > sso.SessionTTL {
		return SSO{}, invalid("GRAPHNEST_SSO_SESSION_IDLE must not exceed GRAPHNEST_SSO_SESSION_TTL")
	}
	if sso.LoginFlowTTL, err = parseBoundedDuration("GRAPHNEST_SSO_LOGIN_FLOW_TTL", os.Getenv("GRAPHNEST_SSO_LOGIN_FLOW_TTL"), 10*time.Minute, time.Minute, 15*time.Minute); err != nil {
		return SSO{}, err
	}

	issuerURL := os.Getenv("GRAPHNEST_OIDC_ISSUER_URL")
	clientID := os.Getenv("GRAPHNEST_OIDC_CLIENT_ID")
	clientSecretFile := os.Getenv("GRAPHNEST_OIDC_CLIENT_SECRET_FILE")
	githubClientID := os.Getenv("GRAPHNEST_OAUTH_GITHUB_CLIENT_ID")
	githubClientSecretFile := os.Getenv("GRAPHNEST_OAUTH_GITHUB_CLIENT_SECRET_FILE")
	oidcEnabled := issuerURL != "" || clientID != "" || clientSecretFile != ""
	githubEnabled := githubClientID != "" || githubClientSecretFile != ""
	if !oidcEnabled && !githubEnabled {
		if os.Getenv("GRAPHNEST_OAUTH_GITHUB_ACCESS_SYNC") != "" && os.Getenv("GRAPHNEST_OAUTH_GITHUB_ACCESS_SYNC") != "false" {
			return SSO{}, invalid("GRAPHNEST_OAUTH_GITHUB_ACCESS_SYNC requires GitHub OAuth")
		}
		if os.Getenv("GRAPHNEST_MCP_OAUTH") != "" && os.Getenv("GRAPHNEST_MCP_OAUTH") != "false" {
			return SSO{}, invalid("GRAPHNEST_MCP_OAUTH requires a browser sign-in provider")
		}
		switch os.Getenv("GRAPHNEST_BREAK_GLASS_ENABLED") {
		case "", "false":
		case "true":
			return SSO{}, invalid("GRAPHNEST_BREAK_GLASS_ENABLED requires an external provider")
		default:
			return SSO{}, invalid("GRAPHNEST_BREAK_GLASS_ENABLED must be true or false")
		}
		return sso, nil
	}
	if databaseURL == "" {
		return SSO{}, invalid("GRAPHNEST_DATABASE_URL is required for browser SSO")
	}
	if oidcEnabled {
		for _, setting := range []struct{ name, value string }{
			{"GRAPHNEST_OIDC_ISSUER_URL", issuerURL},
			{"GRAPHNEST_OIDC_CLIENT_ID", clientID},
			{"GRAPHNEST_OIDC_CLIENT_SECRET_FILE", clientSecretFile},
			{"GRAPHNEST_OIDC_LINK_CLAIM", os.Getenv("GRAPHNEST_OIDC_LINK_CLAIM")},
		} {
			if setting.value == "" {
				return SSO{}, invalid(setting.name + " is required for OIDC")
			}
		}
		info, err := os.Stat(clientSecretFile)
		if err != nil || !info.Mode().IsRegular() {
			return SSO{}, invalid("GRAPHNEST_OIDC_CLIENT_SECRET_FILE must be a regular file")
		}
	}
	if githubEnabled {
		for _, setting := range []struct{ name, value string }{
			{"GRAPHNEST_OAUTH_GITHUB_CLIENT_ID", githubClientID},
			{"GRAPHNEST_OAUTH_GITHUB_CLIENT_SECRET_FILE", githubClientSecretFile},
		} {
			if setting.value == "" {
				return SSO{}, invalid(setting.name + " is required for GitHub OAuth")
			}
		}
		info, err := os.Stat(githubClientSecretFile)
		if err != nil || !info.Mode().IsRegular() {
			return SSO{}, invalid("GRAPHNEST_OAUTH_GITHUB_CLIENT_SECRET_FILE must be a regular file")
		}
	}
	if sso.PublicURL, err = parseHTTPSOrigin("GRAPHNEST_PUBLIC_URL", os.Getenv("GRAPHNEST_PUBLIC_URL")); err != nil {
		return SSO{}, err
	}
	if oidcEnabled {
		issuer, err := url.ParseRequestURI(issuerURL)
		if err != nil || issuer.Scheme != "https" || issuer.Hostname() == "" || issuer.User != nil || issuer.ForceQuery || issuer.RawQuery != "" || issuer.Fragment != "" {
			return SSO{}, invalid("GRAPHNEST_OIDC_ISSUER_URL must be an HTTPS URL without userinfo, query, or fragment")
		}
		scopes, err := parseScopes(valueOr("GRAPHNEST_OIDC_SCOPES", "openid,profile,email"))
		if err != nil {
			return SSO{}, err
		}
		sso.OIDC = OIDC{
			Enabled:          true,
			IssuerURL:        issuerURL,
			ClientID:         clientID,
			ClientSecretFile: clientSecretFile,
			CAFile:           os.Getenv("GRAPHNEST_OIDC_CA_FILE"),
			Scopes:           scopes,
			LinkClaim:        os.Getenv("GRAPHNEST_OIDC_LINK_CLAIM"),
			DisplayNameClaim: valueOr("GRAPHNEST_OIDC_DISPLAY_NAME_CLAIM", "name"),
		}
	}
	if githubEnabled {
		sso.OAuth.GitHub = GitHubOAuth{
			Enabled:          true,
			ClientID:         githubClientID,
			ClientSecretFile: githubClientSecretFile,
		}
	}
	switch value := os.Getenv("GRAPHNEST_OAUTH_GITHUB_ACCESS_SYNC"); value {
	case "", "false":
	case "true":
		if !githubEnabled {
			return SSO{}, invalid("GRAPHNEST_OAUTH_GITHUB_ACCESS_SYNC requires GitHub OAuth")
		}
		sso.OAuth.GitHub.AccessSync = true
	default:
		return SSO{}, invalid("GRAPHNEST_OAUTH_GITHUB_ACCESS_SYNC must be true or false")
	}
	switch os.Getenv("GRAPHNEST_BREAK_GLASS_ENABLED") {
	case "", "false":
	case "true":
		sso.BreakGlass = true
	default:
		return SSO{}, invalid("GRAPHNEST_BREAK_GLASS_ENABLED must be true or false")
	}
	switch os.Getenv("GRAPHNEST_MCP_OAUTH") {
	case "", "false":
	case "true":
		sso.MCPOAuth.Enabled = true
		sso.MCPOAuth.KeyFile = os.Getenv("GRAPHNEST_MCP_OAUTH_KEY_FILE")
		if sso.MCPOAuth.KeyFile != "" {
			info, err := os.Stat(sso.MCPOAuth.KeyFile)
			if err != nil || !info.Mode().IsRegular() {
				return SSO{}, invalid("GRAPHNEST_MCP_OAUTH_KEY_FILE must be a regular file")
			}
		} else if sso.OAuth.GitHub.AccessSync {
			return SSO{}, invalid("GRAPHNEST_MCP_OAUTH_KEY_FILE is required when GitHub access sync is enabled")
		}
	default:
		return SSO{}, invalid("GRAPHNEST_MCP_OAUTH must be true or false")
	}
	return sso, nil
}

func parseHTTPSOrigin(name, value string) (*url.URL, error) {
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.ForceQuery || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return nil, invalid(name + " must be an HTTPS origin")
	}
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	return parsed, nil
}

func parseScopes(value string) ([]string, error) {
	seen := map[string]bool{}
	scopes := make([]string, 0, len(strings.Split(value, ",")))
	for _, scope := range strings.Split(value, ",") {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			return nil, invalid("GRAPHNEST_OIDC_SCOPES must not contain empty values")
		}
		if scope == "offline_access" {
			return nil, invalid("GRAPHNEST_OIDC_SCOPES must not contain offline_access")
		}
		if !seen[scope] {
			seen[scope] = true
			scopes = append(scopes, scope)
		}
	}
	if !seen["openid"] {
		return nil, invalid("GRAPHNEST_OIDC_SCOPES must contain openid")
	}
	return scopes, nil
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
