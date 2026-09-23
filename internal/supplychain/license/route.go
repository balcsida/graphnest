// Package license resolves exact-version license evidence from explicitly
// configured registry routes. Nothing here produces outbound traffic unless a
// route is configured, and a route never falls back from a private registry
// to a public one.
package license

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/net/http/httpproxy"
)

// Route is one configured registry destination for one ecosystem. All
// requests for the ecosystem go to this route (and only this route); a
// package that the route does not know is "not found" here, never retried
// against a public registry.
type Route struct {
	// Name is the operator label recorded on evidence rows.
	Name string
	// Ecosystem is the PURL type this route serves: npm, nuget, or maven.
	Ecosystem string
	// BaseURL is the registry API root, e.g. https://registry.npmjs.org/,
	// https://nuget.example/v3-flatcontainer/, https://repo1.maven.org/maven2/.
	BaseURL *url.URL
	// Bearer/Basic credentials loaded from secret files; never logged.
	BearerToken       string
	BasicUser         string
	BasicPassword     string
	AllowPrivateHosts bool
	CAPEM             []byte
	Timeout           time.Duration
	MaxResponseBytes  int64
	// AllowedNamespaces restricts npm scopes / maven groupIds this route may
	// answer for (prefix match, case-insensitive). Empty means any.
	AllowedNamespaces []string
}

var (
	ErrRouteMissing   = errors.New("no registry route is configured for this ecosystem")
	ErrRouteRejected  = errors.New("registry request rejected by route policy")
	ErrResponseLarge  = errors.New("registry response exceeds the configured limit")
	ErrUnavailable    = errors.New("registry unavailable")
	ErrNotFound       = errors.New("package version not found at the configured route")
	ErrMalformed      = errors.New("registry response is malformed")
	ErrNamespaceDeny  = errors.New("package namespace is not served by this route")
	errRedirectDenied = errors.New("registry redirect rejected")
)

// RoutesFromEnv loads routes from GRAPHNEST_SUPPLY_CHAIN_REGISTRY_<ECOSYSTEM>_URL
// and companion variables. With no variables set, no routes exist and
// enrichment produces no traffic.
//
//	GRAPHNEST_SUPPLY_CHAIN_REGISTRY_NPM_URL=https://npm.example/
//	GRAPHNEST_SUPPLY_CHAIN_REGISTRY_NPM_TOKEN_FILE=/run/secrets/npm-token      (optional bearer)
//	GRAPHNEST_SUPPLY_CHAIN_REGISTRY_NPM_BASIC_FILE=/run/secrets/npm-basic      (optional "user:password")
//	GRAPHNEST_SUPPLY_CHAIN_REGISTRY_NPM_CA_FILE=/run/secrets/npm-ca.pem        (optional)
//	GRAPHNEST_SUPPLY_CHAIN_REGISTRY_NPM_ALLOW_PRIVATE=true                     (optional; permit private/loopback addresses)
//	GRAPHNEST_SUPPLY_CHAIN_REGISTRY_NPM_NAMESPACES=@acme,@internal            (optional restriction)
func RoutesFromEnv(getenv func(string) string, readFile func(string) ([]byte, error)) ([]Route, error) {
	var routes []Route
	for _, ecosystem := range []string{"npm", "nuget", "maven"} {
		prefix := "GRAPHNEST_SUPPLY_CHAIN_REGISTRY_" + strings.ToUpper(ecosystem) + "_"
		raw := getenv(prefix + "URL")
		if raw == "" {
			for _, suffix := range []string{"TOKEN_FILE", "BASIC_FILE", "CA_FILE", "ALLOW_PRIVATE", "NAMESPACES"} {
				if getenv(prefix+suffix) != "" {
					return nil, fmt.Errorf("%s%s requires %sURL", prefix, suffix, prefix)
				}
			}
			continue
		}
		base, err := url.Parse(raw)
		if err != nil || base.Scheme != "https" || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
			return nil, fmt.Errorf("%sURL must be an HTTPS URL without credentials, query, or fragment", prefix)
		}
		if !strings.HasSuffix(base.Path, "/") {
			base.Path += "/"
		}
		route := Route{Name: ecosystem + ":" + base.Host, Ecosystem: ecosystem, BaseURL: base, Timeout: 15 * time.Second, MaxResponseBytes: 4 << 20}
		switch getenv(prefix + "ALLOW_PRIVATE") {
		case "", "false":
		case "true":
			route.AllowPrivateHosts = true
		default:
			return nil, fmt.Errorf("%sALLOW_PRIVATE must be true or false", prefix)
		}
		if file := getenv(prefix + "TOKEN_FILE"); file != "" {
			token, err := readFile(file)
			if err != nil {
				return nil, fmt.Errorf("%sTOKEN_FILE: %w", prefix, err)
			}
			route.BearerToken = strings.TrimSpace(string(token))
			if route.BearerToken == "" {
				return nil, fmt.Errorf("%sTOKEN_FILE is empty", prefix)
			}
		}
		if file := getenv(prefix + "BASIC_FILE"); file != "" {
			if route.BearerToken != "" {
				return nil, fmt.Errorf("%s: configure either TOKEN_FILE or BASIC_FILE", prefix)
			}
			credential, err := readFile(file)
			if err != nil {
				return nil, fmt.Errorf("%sBASIC_FILE: %w", prefix, err)
			}
			user, password, ok := strings.Cut(strings.TrimSpace(string(credential)), ":")
			if !ok || user == "" {
				return nil, fmt.Errorf("%sBASIC_FILE must contain user:password", prefix)
			}
			route.BasicUser, route.BasicPassword = user, password
		}
		if file := getenv(prefix + "CA_FILE"); file != "" {
			pemBytes, err := readFile(file)
			if err != nil {
				return nil, fmt.Errorf("%sCA_FILE: %w", prefix, err)
			}
			route.CAPEM = pemBytes
		}
		if namespaces := getenv(prefix + "NAMESPACES"); namespaces != "" {
			for _, namespace := range strings.Split(namespaces, ",") {
				if namespace = strings.TrimSpace(namespace); namespace != "" {
					route.AllowedNamespaces = append(route.AllowedNamespaces, strings.ToLower(namespace))
				}
			}
		}
		routes = append(routes, route)
	}
	return routes, nil
}

// ReadSecretFile reads a bounded regular file for RoutesFromEnv.
func ReadSecretFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return nil, errors.New("secret file must be a regular file under 64 KiB")
	}
	return os.ReadFile(path)
}

// ServesNamespace reports whether the route may answer for a namespace.
func (route Route) ServesNamespace(namespace string) bool {
	if len(route.AllowedNamespaces) == 0 {
		return true
	}
	lower := strings.ToLower(namespace)
	for _, allowed := range route.AllowedNamespaces {
		if lower == allowed || strings.HasPrefix(lower, allowed+".") || strings.HasPrefix(lower, allowed+"/") {
			return true
		}
	}
	return false
}

// Fetcher performs route-bound GETs. It pins the origin, rejects redirects
// that leave the route, blocks private and link-local destinations unless
// the route allows them, bounds the body (after decompression), and isolates
// credentials to the route's origin.
type Fetcher struct {
	route  Route
	client *http.Client
}

func NewFetcher(route Route) (*Fetcher, error) {
	roots, err := x509.SystemCertPool()
	if err != nil {
		return nil, err
	}
	if roots == nil {
		roots = x509.NewCertPool()
	}
	if len(route.CAPEM) > 0 && !roots.AppendCertsFromPEM(route.CAPEM) {
		return nil, errors.New("invalid registry CA certificate")
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	// The private-address policy applies to the route host: resolve it and
	// reject any non-public answer (a public name answering with a private
	// address is a rebinding attempt).
	resolveRouteHost := func(ctx context.Context, host string) ([]netip.Addr, error) {
		addresses, err := lookupNetIP(ctx, host)
		if err != nil {
			return nil, err
		}
		for _, candidate := range addresses {
			if !route.AllowPrivateHosts && !publicAddress(candidate) {
				return nil, fmt.Errorf("%w: %s resolves to a non-public address", ErrRouteRejected, host)
			}
		}
		return addresses, nil
	}
	// HTTPS_PROXY/NO_PROXY, like every other outbound client in the process; a
	// cluster behind an egress proxy has no other way out. Read once per
	// route at construction (http.ProxyFromEnvironment caches process-wide).
	proxy := httpproxy.FromEnvironment().ProxyFunc()
	transport := &http.Transport{
		Proxy:                 func(request *http.Request) (*url.URL, error) { return proxy(request.URL) },
		TLSClientConfig:       &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
		ResponseHeaderTimeout: route.Timeout,
		DisableCompression:    false,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			var addresses []netip.Addr
			if strings.EqualFold(host, route.BaseURL.Hostname()) {
				// Direct connection: the answers that passed the policy are the
				// answers dialled, so a rebinding between lookups cannot slip in.
				addresses, err = resolveRouteHost(ctx, host)
			} else {
				// The dial goes to an egress proxy (normally a private address by
				// design); the policy still applies to the route host it tunnels to.
				if _, err = resolveRouteHost(ctx, route.BaseURL.Hostname()); err == nil {
					addresses, err = lookupNetIP(ctx, host)
				}
			}
			if err != nil {
				return nil, err
			}
			var lastErr error
			for _, candidate := range addresses {
				conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(candidate.Unmap().String(), port))
				if err == nil {
					return conn, nil
				}
				lastErr = err
			}
			if lastErr == nil {
				lastErr = errors.New("no addresses")
			}
			return nil, lastErr
		},
	}
	timeout := route.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	client := &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(request *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errRedirectDenied
		}
		if !sameOrigin(request.URL, route.BaseURL) || !strings.HasPrefix(request.URL.Path, route.BaseURL.Path) {
			return errRedirectDenied
		}
		// Go strips Authorization on cross-host redirects; same-origin keeps it, which is what we want.
		return nil
	}}
	return &Fetcher{route: route, client: client}, nil
}

// publicAddress rejects loopback, private, link-local, multicast,
// unspecified, and cloud-metadata ranges.
func publicAddress(address netip.Addr) bool {
	address = address.Unmap()
	if address.IsLoopback() || address.IsPrivate() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsMulticast() || address.IsUnspecified() || address.IsInterfaceLocalMulticast() {
		return false
	}
	for _, blocked := range []string{"169.254.0.0/16", "100.64.0.0/10", "192.0.0.0/24", "198.18.0.0/15", "240.0.0.0/4", "fc00::/7", "fe80::/10", "::ffff:0:0/96"} {
		if prefix, err := netip.ParsePrefix(blocked); err == nil && prefix.Contains(address) {
			return false
		}
	}
	return true
}

func sameOrigin(left, right *url.URL) bool {
	return left.Scheme == right.Scheme && strings.EqualFold(left.Host, right.Host)
}

// Response is a bounded, fully read registry response.
type Response struct {
	Status      int
	Body        []byte
	ContentType string
	FetchedAt   time.Time
}

// Get fetches a path relative to the route base. The path segments are
// escaped individually so a hostile package name cannot traverse. A literal
// "%2F" inside a segment (npm scoped names) is kept as the encoded slash.
func (fetcher *Fetcher) Get(ctx context.Context, accept string, segments ...string) (Response, error) {
	target := *fetcher.route.BaseURL
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." || strings.ContainsAny(segment, "/\\?#") {
			return Response{}, fmt.Errorf("%w: invalid path segment", ErrRouteRejected)
		}
	}
	rawPath := strings.TrimSuffix(target.EscapedPath(), "/")
	for _, segment := range segments {
		rawPath += "/" + strings.ReplaceAll(url.PathEscape(strings.ReplaceAll(segment, "%2F", "\x00")), "%00", "%2F")
	}
	path, err := url.PathUnescape(rawPath)
	if err != nil {
		return Response{}, fmt.Errorf("%w: invalid path", ErrRouteRejected)
	}
	target.Path, target.RawPath = path, rawPath
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return Response{}, err
	}
	request.Header.Set("User-Agent", "GraphNest")
	if accept != "" {
		request.Header.Set("Accept", accept)
	}
	if fetcher.route.BearerToken != "" {
		request.Header.Set("Authorization", "Bearer "+fetcher.route.BearerToken)
	} else if fetcher.route.BasicUser != "" {
		request.SetBasicAuth(fetcher.route.BasicUser, fetcher.route.BasicPassword)
	}
	response, err := fetcher.client.Do(request)
	if err != nil {
		if errors.Is(err, errRedirectDenied) || errors.Is(err, ErrRouteRejected) {
			return Response{}, fmt.Errorf("%w: %v", ErrRouteRejected, unwrapURLError(err))
		}
		return Response{}, fmt.Errorf("%w: request failed", ErrUnavailable)
	}
	defer response.Body.Close()
	limit := fetcher.route.MaxResponseBytes
	if limit <= 0 {
		limit = 4 << 20
	}
	// The transport transparently decompresses gzip; the limit applies to the
	// decompressed bytes we actually read, so a compression bomb is cut off.
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return Response{}, fmt.Errorf("%w: read failed", ErrUnavailable)
	}
	if int64(len(body)) > limit {
		return Response{}, ErrResponseLarge
	}
	return Response{Status: response.StatusCode, Body: body, ContentType: response.Header.Get("Content-Type"), FetchedAt: time.Now().UTC()}, nil
}

func unwrapURLError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
}

// parseAddress and lookupNetIP are small test seams around netip.ParseAddr
// and the default resolver.
func parseAddress(value string) (netip.Addr, error) { return netip.ParseAddr(value) }

var lookupNetIP = func(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}
