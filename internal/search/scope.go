package search

// RepositoryScope is the backend-neutral, authorization-derived repository
// identity. Backends must never discover repositories independently.
type RepositoryScope struct {
	ID, GitHubID int64
	Name         string
	IndexedSHA   string
}
