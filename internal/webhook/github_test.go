package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"testing"
)

func TestVerify(t *testing.T) {
	secret := []byte("webhook-secret")
	body := []byte(`{"action":"created"}`)
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body)
	signature := "sha256=" + fmtHex(mac.Sum(nil))

	for _, test := range []struct {
		name      string
		signature string
		want      bool
	}{
		{"valid", signature, true},
		{"wrong prefix", "sha1=" + signature[7:], false},
		{"wrong digest", "sha256=" + fmtHex(make([]byte, sha256.Size)), false},
		{"non hexadecimal", "sha256=" + string(make([]byte, sha256.Size*2)), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := Verify(secret, body, test.signature); got != test.want {
				t.Fatalf("Verify() = %v, want %v", got, test.want)
			}
		})
	}
}

func fmtHex(value []byte) string {
	const hex = "0123456789abcdef"
	result := make([]byte, len(value)*2)
	for index, byteValue := range value {
		result[index*2] = hex[byteValue>>4]
		result[index*2+1] = hex[byteValue&0x0f]
	}
	return string(result)
}
