package authn

import (
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"io"

	"golang.org/x/crypto/argon2"
)

var ErrInvalidPasswordCredential = errors.New("invalid password credential")

const (
	defaultPasswordMemoryKiB   = 64 * 1024
	defaultPasswordIterations  = 3
	defaultPasswordParallelism = 2
	maxPasswordBytes           = 1 << 20
)

type PasswordCredential struct {
	Salt, Hash    []byte
	MemoryKiB     uint32
	Iterations    uint32
	Parallelism   uint8
	ForceRotation bool
}

func HashPassword(password []byte, random io.Reader) (PasswordCredential, error) {
	defer clear(password)
	if len(password) > maxPasswordBytes {
		return PasswordCredential{}, ErrInvalidPasswordCredential
	}
	if random == nil {
		random = rand.Reader
	}
	credential := PasswordCredential{
		Salt:        make([]byte, 16),
		MemoryKiB:   defaultPasswordMemoryKiB,
		Iterations:  defaultPasswordIterations,
		Parallelism: defaultPasswordParallelism,
	}
	if _, err := io.ReadFull(random, credential.Salt); err != nil {
		return PasswordCredential{}, err
	}
	credential.Hash = argon2.IDKey(password, credential.Salt, credential.Iterations, credential.MemoryKiB, credential.Parallelism, 32)
	return credential, nil
}

func VerifyPassword(password []byte, credential PasswordCredential) bool {
	defer clear(password)
	if len(password) > maxPasswordBytes || credential.Validate() != nil {
		return false
	}
	hash := argon2.IDKey(password, credential.Salt, credential.Iterations, credential.MemoryKiB, credential.Parallelism, uint32(len(credential.Hash)))
	return subtle.ConstantTimeCompare(hash, credential.Hash) == 1
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
