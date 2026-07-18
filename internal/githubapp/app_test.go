package githubapp

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"
	"time"
)

func TestJWTUsesRS256AndGitHubClaims(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	signer, err := NewSigner(123, pemBytes, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	token, err := signer.JWT()
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token = %q", token)
	}
	var header map[string]string
	decodePart(t, parts[0], &header)
	if header["alg"] != "RS256" || header["typ"] != "JWT" {
		t.Fatalf("header = %#v", header)
	}
	var claims struct {
		IssuedAt  int64  `json:"iat"`
		ExpiresAt int64  `json:"exp"`
		Issuer    string `json:"iss"`
	}
	decodePart(t, parts[1], &claims)
	if claims.Issuer != "123" || claims.IssuedAt != now.Add(-time.Minute).Unix() || claims.ExpiresAt != now.Add(9*time.Minute).Unix() {
		t.Fatalf("claims = %#v", claims)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, hash[:], signature); err != nil {
		t.Fatal(err)
	}
}

func TestNewSignerSupportsPKCS8RSA(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := NewSigner(123, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded}), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := signer.JWT(); err != nil {
		t.Fatal(err)
	}
}

func TestNewSignerReportsPKCS8ParseFailure(t *testing.T) {
	_, err := NewSigner(123, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("not a key")}), time.Now)
	if err == nil || !strings.Contains(err.Error(), "PKCS#8") {
		t.Fatalf("NewSigner() error = %v", err)
	}
}

func decodePart(t *testing.T, value string, target any) {
	t.Helper()
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatal(err)
	}
}
