package githubapp

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"strconv"
	"time"
)

type Signer struct {
	privateKey *rsa.PrivateKey
	appID      string
	now        func() time.Time
}

func NewSigner(appID int64, pemBytes []byte, now func() time.Time) (*Signer, error) {
	if appID <= 0 {
		return nil, errors.New("GitHub App ID must be positive")
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("GitHub App private key is not PEM")
	}
	privateKey, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		parsed, parseErr := x509.ParsePKCS8PrivateKey(block.Bytes)
		if parseErr != nil {
			return nil, fmt.Errorf("parse GitHub App private key: %w", err)
		}
		var ok bool
		privateKey, ok = parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("GitHub App private key is not RSA")
		}
	}
	if now == nil {
		now = time.Now
	}
	return &Signer{privateKey: privateKey, appID: strconv.FormatInt(appID, 10), now: now}, nil
}

func (signer *Signer) JWT() (string, error) {
	now := signer.now()
	header, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(struct {
		IssuedAt  int64  `json:"iat"`
		ExpiresAt int64  `json:"exp"`
		Issuer    string `json:"iss"`
	}{now.Add(-time.Minute).Unix(), now.Add(9 * time.Minute).Unix(), signer.appID})
	if err != nil {
		return "", err
	}
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	hash := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(nil, signer.privateKey, crypto.SHA256, hash[:])
	if err != nil {
		return "", err
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}
