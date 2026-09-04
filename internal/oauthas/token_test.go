package oauthas

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
)

func TestSecretsRoundTripThroughPrefixedHashes(t *testing.T) {
	token, hash, err := newSecret(strings.NewReader(strings.Repeat("a", 32)), AccessTokenPrefix)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(token, "gno_") || len(token) != 4+43 {
		t.Fatalf("token=%q", token)
	}
	got, ok := hashSecret(token, AccessTokenPrefix)
	if !ok || got != hash || got != sha256.Sum256([]byte(strings.Repeat("a", 32))) {
		t.Fatalf("hash mismatch ok=%v", ok)
	}
	for _, bad := range []string{"", "gno_", "gnp_" + token[4:], token + "x", token[:len(token)-1], "gno_" + strings.Repeat("!", 43), "gno_" + strings.Repeat("A", 43) + "="} {
		if _, ok := hashSecret(bad, AccessTokenPrefix); ok {
			t.Errorf("accepted malformed token %q", bad)
		}
	}
	if !HasAccessTokenShape(token) || HasAccessTokenShape("gnp_"+token[4:]) {
		t.Fatal("access token shape detection is wrong")
	}
}

func TestPKCEVerification(t *testing.T) {
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	if !verifyPKCE(verifier, challenge) || !validChallenge(challenge) {
		t.Fatal("valid S256 pair rejected")
	}
	if verifyPKCE(verifier+"x", challenge) || verifyPKCE(verifier, challenge[:len(challenge)-1]+"A") {
		t.Fatal("tampered pair accepted")
	}
	if verifyPKCE(strings.Repeat("a", 42), challenge) || verifyPKCE(strings.Repeat("a", 129), challenge) || verifyPKCE(strings.Repeat("a", 43)+"!", challenge) {
		t.Fatal("verifier outside RFC 7636 grammar accepted")
	}
	if validChallenge(challenge+"=") || validChallenge("short") {
		t.Fatal("challenge outside base64url(32 bytes) accepted")
	}
}

func TestSealerBindsCiphertextToGrant(t *testing.T) {
	sealer, err := NewSealer(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewSealer([]byte("short")); err == nil {
		t.Fatal("short key accepted")
	}
	ciphertext, err := sealer.Seal(nil, 42, "gho_secret")
	if err != nil {
		t.Fatal(err)
	}
	if plaintext, err := sealer.Open(42, ciphertext); err != nil || plaintext != "gho_secret" {
		t.Fatalf("plaintext=%q err=%v", plaintext, err)
	}
	if _, err := sealer.Open(43, ciphertext); err == nil {
		t.Fatal("ciphertext opened under another grant id")
	}
	ciphertext[len(ciphertext)-1] ^= 1
	if _, err := sealer.Open(42, ciphertext); err == nil {
		t.Fatal("tampered ciphertext opened")
	}
	if _, err := sealer.Open(42, []byte("tiny")); err == nil {
		t.Fatal("truncated ciphertext opened")
	}
	other, _ := sealer.Seal(nil, 42, "gho_secret")
	if bytes.Equal(other, ciphertext) {
		t.Fatal("nonce reuse: identical ciphertexts for identical plaintext")
	}
}
