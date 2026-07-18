package githubapp

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"net/url"
	"time"
)

var ErrUnconfiguredHost = errors.New("GitHub request host is not configured")

type Endpoints struct {
	Web, API, Upload, Git *url.URL
}

func NewHTTPClient(caPEM []byte, endpoints Endpoints, timeout time.Duration) (*http.Client, error) {
	hosts := make(map[string]struct{}, 4)
	for _, endpoint := range []*url.URL{endpoints.Web, endpoints.API, endpoints.Upload, endpoints.Git} {
		if endpoint == nil || endpoint.Scheme != "https" || endpoint.Host == "" {
			return nil, errors.New("GitHub endpoint must be HTTPS")
		}
		hosts[endpoint.Host] = struct{}{}
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if len(caPEM) > 0 && !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("GitHub CA bundle contains no certificates")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	tlsConfig := transport.TLSClientConfig
	if tlsConfig == nil {
		tlsConfig = &tls.Config{}
	} else {
		tlsConfig = tlsConfig.Clone()
	}
	tlsConfig.RootCAs = pool
	tlsConfig.MinVersion = tls.VersionTLS12
	transport.TLSClientConfig = tlsConfig
	return &http.Client{
		Transport: hostLockedTransport{base: transport, hosts: hosts},
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, nil
}

type hostLockedTransport struct {
	base  http.RoundTripper
	hosts map[string]struct{}
}

func (transport hostLockedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Scheme != "https" {
		return nil, ErrUnconfiguredHost
	}
	if _, ok := transport.hosts[request.URL.Host]; !ok {
		return nil, ErrUnconfiguredHost
	}
	return transport.base.RoundTrip(request)
}
