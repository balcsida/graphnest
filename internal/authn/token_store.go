package authn

import "time"

type APITokenRecord struct {
	TokenHash     [32]byte
	Prefix        string
	UserID        int64
	RepositoryIDs []int64
	CreatedAt     time.Time
	ExpiresAt     *time.Time
}
