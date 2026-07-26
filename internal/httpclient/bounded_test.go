package httpclient_test

import (
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/grepnest/grepnest/internal/httpclient"
)

func TestBoundedClientRejectsOversizedResponses(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write(make([]byte, (1<<20)+1))
	}))
	defer server.Close()

	client, err := httpclient.New(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if _, err := io.ReadAll(response.Body); err == nil {
		t.Fatal("oversized response succeeded")
	}
}

func TestBoundedClientRejectsCrossOriginRedirects(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer target.Close()
	source := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Redirect(writer, nil, target.URL, http.StatusFound)
	}))
	defer source.Close()

	caPEM := append(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: source.Certificate().Raw}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: target.Certificate().Raw})...,
	)
	client, err := httpclient.New(caPEM)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Get(source.URL); err == nil {
		t.Fatal("cross-origin redirect succeeded")
	}
}
