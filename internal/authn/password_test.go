package authn

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

type retainingErrorReader struct {
	buffer []byte
}

func (reader *retainingErrorReader) Read(buffer []byte) (int, error) {
	reader.buffer = buffer
	copy(buffer, bytes.Repeat([]byte{7}, 8))
	return 8, io.ErrUnexpectedEOF
}

func TestPasswordCredentialValidate(t *testing.T) {
	valid := PasswordCredential{
		Salt: make([]byte, 16), Hash: make([]byte, 32),
		MemoryKiB: 65536, Iterations: 3, Parallelism: 2, ForceRotation: true,
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	tests := []PasswordCredential{
		{Salt: make([]byte, 15), Hash: valid.Hash, MemoryKiB: 65536, Iterations: 3, Parallelism: 2},
		{Salt: valid.Salt, Hash: make([]byte, 31), MemoryKiB: 65536, Iterations: 3, Parallelism: 2},
		{Salt: valid.Salt, Hash: valid.Hash, MemoryKiB: 65535, Iterations: 3, Parallelism: 2},
		{Salt: valid.Salt, Hash: valid.Hash, MemoryKiB: 65536, Iterations: 0, Parallelism: 2},
		{Salt: valid.Salt, Hash: valid.Hash, MemoryKiB: 65536, Iterations: 3, Parallelism: 9},
	}
	for _, credential := range tests {
		if err := credential.Validate(); err == nil || !strings.Contains(err.Error(), "invalid password credential") {
			t.Fatalf("error=%v credential=%#v", err, credential)
		}
	}
}

func TestHashPasswordUsesBoundedArgon2idDefaults(t *testing.T) {
	password := []byte("sixteen-byte-secret")
	credential, err := HashPassword(password, bytes.NewReader(bytes.Repeat([]byte{7}, 16)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(credential.Salt, bytes.Repeat([]byte{7}, 16)) ||
		len(credential.Hash) != 32 || credential.MemoryKiB != 65536 ||
		credential.Iterations != 3 || credential.Parallelism != 2 {
		t.Fatalf("credential = %#v", credential)
	}
	if !bytes.Equal(password, make([]byte, len(password))) {
		t.Fatal("caller-owned password was not cleared")
	}
}

func TestVerifyPasswordAcceptsOnlyCorrectPassword(t *testing.T) {
	credential, err := HashPassword([]byte("sixteen-byte-secret"), bytes.NewReader(make([]byte, 16)))
	if err != nil {
		t.Fatal(err)
	}
	correct := []byte("sixteen-byte-secret")
	if !VerifyPassword(correct, credential) {
		t.Fatal("correct password rejected")
	}
	wrong := []byte("wrong-password")
	if VerifyPassword(wrong, credential) {
		t.Fatal("wrong password accepted")
	}
	if !bytes.Equal(correct, make([]byte, len(correct))) || !bytes.Equal(wrong, make([]byte, len(wrong))) {
		t.Fatal("caller-owned password was not cleared")
	}
}

func TestPasswordHashingRejectsOversizeBeforeWork(t *testing.T) {
	password := bytes.Repeat([]byte{'x'}, maxPasswordBytes+1)
	if _, err := HashPassword(password, bytes.NewReader(nil)); err == nil {
		t.Fatal("oversize password accepted")
	}
	if !bytes.Equal(password, make([]byte, len(password))) {
		t.Fatal("oversize password was not cleared")
	}

	credential := PasswordCredential{
		Salt: make([]byte, 16), Hash: make([]byte, 32),
		MemoryKiB: 65536, Iterations: 3, Parallelism: 2,
	}
	if VerifyPassword(bytes.Repeat([]byte{'x'}, maxPasswordBytes+1), credential) {
		t.Fatal("oversize password verified")
	}
}

func TestHashPasswordClearsSaltAfterEntropyFailure(t *testing.T) {
	random := &retainingErrorReader{}
	password := []byte("sixteen-byte-secret")
	if _, err := HashPassword(password, random); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("error=%v", err)
	}
	if !bytes.Equal(random.buffer, make([]byte, 16)) {
		t.Fatalf("salt buffer not cleared: %x", random.buffer)
	}
	if !bytes.Equal(password, make([]byte, len(password))) {
		t.Fatal("password was not cleared")
	}
}

func TestVerifyPasswordRejectsParametersBeforeAllocation(t *testing.T) {
	credential := PasswordCredential{
		Salt: make([]byte, 16), Hash: make([]byte, 32),
		MemoryKiB: ^uint32(0), Iterations: 3, Parallelism: 2,
	}
	if VerifyPassword([]byte("sixteen-byte-secret"), credential) {
		t.Fatal("unbounded credential verified")
	}
}
