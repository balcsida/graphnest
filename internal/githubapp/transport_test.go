package githubapp

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestHTTPClientTrustsConfiguredCAAndLocksHosts(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/redirect" {
			http.Redirect(writer, request, "/ok", http.StatusFound)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	certificate := server.Certificate()
	caPEM := pemCertificate(t, certificate)
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewHTTPClient(caPEM, Endpoints{Web: endpoint, API: endpoint, Upload: endpoint, Git: endpoint}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Get(server.URL + "/ok")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", response.StatusCode)
	}
	response, err = client.Get(server.URL + "/redirect")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusFound {
		t.Fatalf("redirect status = %d", response.StatusCode)
	}
	_, err = client.Get("https://example.com")
	if err == nil || !errors.Is(err, ErrUnconfiguredHost) {
		t.Fatalf("unconfigured host error = %v", err)
	}
}

func pemCertificate(t *testing.T, certificate *x509.Certificate) []byte {
	t.Helper()
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw})
}
