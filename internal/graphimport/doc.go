// Package graphimport reads a CodeGraph SQLite index (.codegraph/codegraph.db)
// and converts its facts to the v2 graph artifact.
//
// It links a pure-Go SQLite driver, so only the graphnest command-line tool may
// depend on it; server binaries must not (see boundary_test.go).
package graphimport
