package authn

import "errors"

var ErrInvalidPasswordCredential = errors.New("invalid password credential")

type PasswordCredential struct {
	Salt, Hash    []byte
	MemoryKiB     uint32
	Iterations    uint32
	Parallelism   uint8
	ForceRotation bool
}

func (credential PasswordCredential) Validate() error {
	if len(credential.Salt) != 16 || len(credential.Hash) != 32 ||
		credential.MemoryKiB < 65536 || credential.MemoryKiB > 262144 ||
		credential.Iterations < 1 || credential.Iterations > 10 ||
		credential.Parallelism < 1 || credential.Parallelism > 8 {
		return ErrInvalidPasswordCredential
	}
	return nil
}
