package authn

import "time"

type APITokenRecord struct {
	TokenHash     [32]byte
	Prefix        string
	UserID        int64
	RepositoryIDs []int64
	// DelegationOnly tokens have no ceiling and no repository access; they
	// exist solely to call the delegation endpoint. See Principal.
	DelegationOnly bool
	// InventoryOnly tokens exist solely to read the repository inventory,
	// unbounded by a ceiling; they are stored with an empty one. See Principal.
	InventoryOnly bool
	// Delegated tokens were minted by the delegation endpoint and may not
	// delegate again. See Principal.
	Delegated bool
	CreatedAt time.Time
	ExpiresAt *time.Time
}

type APITokenMetadata struct {
	ID             int64
	Prefix         string
	RepositoryIDs  []int64
	DelegationOnly bool
	InventoryOnly  bool
	Delegated      bool
	CreatedAt      time.Time
	LastUsedAt     *time.Time
	ExpiresAt      *time.Time
}
