package sso

import "net/http"

type Metadata struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	LoginURL string `json:"login_url"`
}

type Provider interface {
	Metadata() Metadata
	Register(*http.ServeMux)
}
