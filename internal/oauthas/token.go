// Package oauthas is GraphNest's OAuth 2.1 authorization server for MCP
// clients: dynamic public-client registration, PKCE authorization code flow
// gated by an explicit consent page, and rotating refresh tokens whose access
// tokens authenticate the /mcp endpoint as the granting user.
package oauthas

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"strings"
)

// Token prefixes let the bearer authenticator route a token to the right
// table without a lookup and keep the kinds visually distinct in logs.
const (
	AccessTokenPrefix  = "gno_"
	RefreshTokenPrefix = "gnr_"
	CodePrefix         = "gnac_"
	ClientIDPrefix     = "gnc_"
	requestHandleLen   = 32
)

var errCiphertext = errors.New("oauth: invalid ciphertext")

// newSecret returns 32 random bytes and their prefixed base64url form.
func newSecret(reader io.Reader, prefix string) (string, [32]byte, error) {
	if reader == nil {
		reader = rand.Reader
	}
	raw := make([]byte, 32)
	if _, err := io.ReadFull(reader, raw); err != nil {
		return "", [32]byte{}, err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(raw), sha256.Sum256(raw), nil
}

// hashSecret recovers the sha256 of a presented token; ok is false when the
// token does not have the expected prefix and exactly 32 decoded bytes.
func hashSecret(token, prefix string) ([32]byte, bool) {
	if !strings.HasPrefix(token, prefix) {
		return [32]byte{}, false
	}
	encoded := strings.TrimPrefix(token, prefix)
	if len(encoded) != base64.RawURLEncoding.EncodedLen(32) {
		return [32]byte{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) != 32 || base64.RawURLEncoding.EncodeToString(raw) != encoded {
		return [32]byte{}, false
	}
	return sha256.Sum256(raw), true
}

// HasAccessTokenShape reports whether a bearer credential is an OAuth access
// token, so the bearer path can pick an authenticator without a lookup.
func HasAccessTokenShape(token string) bool {
	_, ok := hashSecret(token, AccessTokenPrefix)
	return ok
}

// verifyPKCE checks BASE64URL(SHA256(verifier)) == challenge (S256, RFC 7636).
func verifyPKCE(verifier, challenge string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	for _, r := range verifier {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '.' || r == '_' || r == '~') {
			return false
		}
	}
	sum := sha256.Sum256([]byte(verifier))
	expected := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(expected), []byte(challenge)) == 1
}

// validChallenge accepts only the base64url shape an S256 challenge has.
func validChallenge(challenge string) bool {
	if len(challenge) != base64.RawURLEncoding.EncodedLen(32) {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(challenge)
	return err == nil && base64.RawURLEncoding.EncodeToString(raw) == challenge
}

// Sealer encrypts GitHub user tokens at rest with AES-256-GCM. The grant ID is
// authenticated additional data so a ciphertext cannot be moved between rows.
type Sealer struct{ aead cipher.AEAD }

func NewSealer(key []byte) (*Sealer, error) {
	if len(key) != 32 {
		return nil, errors.New("oauth: sealing key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Sealer{aead: aead}, nil
}

func (s *Sealer) Seal(reader io.Reader, grantID int64, plaintext string) ([]byte, error) {
	if reader == nil {
		reader = rand.Reader
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(reader, nonce); err != nil {
		return nil, err
	}
	return s.aead.Seal(nonce, nonce, []byte(plaintext), aad(grantID)), nil
}

func (s *Sealer) Open(grantID int64, ciphertext []byte) (string, error) {
	size := s.aead.NonceSize()
	if len(ciphertext) < size+s.aead.Overhead() {
		return "", errCiphertext
	}
	plaintext, err := s.aead.Open(nil, ciphertext[:size], ciphertext[size:], aad(grantID))
	if err != nil {
		return "", errCiphertext
	}
	return string(plaintext), nil
}

func aad(grantID int64) []byte {
	buffer := make([]byte, 8)
	binary.BigEndian.PutUint64(buffer, uint64(grantID))
	return buffer
}
