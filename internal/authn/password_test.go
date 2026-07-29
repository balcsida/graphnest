package authn

import (
	"strings"
	"testing"
)

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
