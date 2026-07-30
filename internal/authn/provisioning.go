package authn

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"strings"
)

var ErrProvisioningUnauthenticated = errors.New("provisioning authentication required")

type ProvisioningAuthenticator struct{ Expected [32]byte }

func NewProvisioningAuthenticator(secret []byte) (ProvisioningAuthenticator, error) {
	if len(secret) < 32 {
		return ProvisioningAuthenticator{}, errors.New("provisioning secret must contain at least 32 bytes")
	}
	return ProvisioningAuthenticator{Expected: sha256.Sum256(secret)}, nil
}

func (a ProvisioningAuthenticator) Authenticate(headerValues []string) error {
	if len(headerValues) != 1 || !strings.HasPrefix(headerValues[0], "Bearer ") {
		return ErrProvisioningUnauthenticated
	}
	token := strings.TrimPrefix(headerValues[0], "Bearer ")
	if token == "" || strings.ContainsAny(token, " \t\r\n,") {
		return ErrProvisioningUnauthenticated
	}
	presented := sha256.Sum256([]byte(token))
	if subtle.ConstantTimeCompare(a.Expected[:], presented[:]) != 1 {
		return ErrProvisioningUnauthenticated
	}
	return nil
}
