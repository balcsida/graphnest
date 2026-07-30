package httpclient

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"
)

const maxResponseBytes = 1 << 20

var errResponseTooLarge = errors.New("HTTP response exceeds 1 MiB")

func New(caPEM []byte) (*http.Client, error) {
	roots, err := x509.SystemCertPool()
	if err != nil {
		return nil, err
	}
	if roots == nil {
		roots = x509.NewCertPool()
	}
	if len(caPEM) > 0 && !roots.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("invalid CA certificate")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	return &http.Client{
		Transport: boundedTransport{transport},
		Timeout:   10 * time.Second,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) > 0 && origin(request.URL) != origin(via[0].URL) {
				return errors.New("cross-origin HTTP redirect rejected")
			}
			return nil
		},
	}, nil
}

type boundedTransport struct{ http.RoundTripper }

func (transport boundedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := transport.RoundTripper.RoundTrip(request)
	if err == nil && response.Body != nil {
		response.Body = &boundedBody{ReadCloser: response.Body, remaining: maxResponseBytes}
	}
	return response, err
}

type boundedBody struct {
	io.ReadCloser
	remaining int64
}

func (body *boundedBody) Read(buffer []byte) (int, error) {
	if body.remaining == 0 {
		var one [1]byte
		n, err := body.ReadCloser.Read(one[:])
		if n > 0 {
			return 0, errResponseTooLarge
		}
		return 0, err
	}
	if int64(len(buffer)) > body.remaining {
		buffer = buffer[:body.remaining]
	}
	n, err := body.ReadCloser.Read(buffer)
	body.remaining -= int64(n)
	return n, err
}

func origin(value *url.URL) string { return value.Scheme + "://" + value.Host }
