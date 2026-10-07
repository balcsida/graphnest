package cli

import (
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/balcsida/graphnest/internal/graphartifact"
	"github.com/balcsida/graphnest/internal/graphimport"
)

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

const importUsage = "graphnest graph import codegraph (--dry-run | --output FILE) [--repo DIR] [--index FILE] [--repository-id N] [--commit SHA] [--timeout D]"

// importReport is the JSON document printed by graph import codegraph.
type importReport struct {
	Command      string                `json:"command"`
	DryRun       bool                  `json:"dry_run"`
	Index        indexReport           `json:"index"`
	RepositoryID int64                 `json:"repository_id,omitempty"`
	Commit       string                `json:"commit"`
	Freshness    graphimport.Freshness `json:"freshness"`
	Counts       countsReport          `json:"counts"`
	NodeKinds    map[string]int        `json:"node_kinds"`
	EdgeKinds    map[string]int        `json:"edge_kinds"`
	Artifact     artifactReport        `json:"artifact"`
	Output       *outputReport         `json:"output"`
	Diagnostics  diagnostics           `json:"diagnostics"`
	Published    bool                  `json:"published"`
}

type indexReport struct {
	Path          string         `json:"path"`
	SchemaVersion int            `json:"schema_version"`
	Producer      producerReport `json:"producer"`
}

type producerReport struct {
	Name          string `json:"name"`
	Version       string `json:"version"`
	Configuration string `json:"configuration"`
}

type outputReport struct {
	Path  string `json:"path"`
	Bytes int    `json:"bytes"`
}

type countsReport struct {
	Nodes      int `json:"nodes"`
	Edges      int `json:"edges"`
	Files      int `json:"files"`
	Unresolved int `json:"unresolved"`
	Metadata   int `json:"metadata"`
}

type artifactReport struct {
	Repository  string `json:"repository"` // the identity the hash was computed for; "unassigned" without --repository-id
	Bytes       int    `json:"bytes"`
	ContentHash string `json:"content_hash"`
}

type diagnostics struct {
	UnresolvedReferences int      `json:"unresolved_references"`
	FilesWithErrors      int      `json:"files_with_errors"`
	RoundedTimestamps    int      `json:"rounded_timestamps"` // fractional-millisecond timestamps rounded on import
	Dropped              []string `json:"dropped"`
}

func runImportCodeGraph(ctx context.Context, args []string, env Environment, stdout, stderr io.Writer) error {
	flags := newFlags("import codegraph", importUsage, stderr)
	dryRun := flags.Bool("dry-run", false, "convert and report without writing (exactly one of --dry-run and --output)")
	output := flags.String("output", "", "write the artifact to `FILE` after verifying the index is fresh and complete")
	repo := flags.String("repo", ".", "repository directory")
	index := flags.String("index", "", "CodeGraph index file (default <repo>/<CODEGRAPH_DIR or .codegraph>/codegraph.db)")
	repositoryID := flags.Int64("repository-id", 0, "GraphNest repository ID")
	commit := flags.String("commit", "", "commit SHA (default: git rev-parse HEAD in --repo)")
	timeout := flags.Duration("timeout", defaultTimeout, "time limit for reading and converting")
	if err := parse(flags, args); err != nil {
		return err
	}
	switch {
	case *dryRun && *output != "":
		return usageError{"--dry-run and --output are mutually exclusive"}
	case !*dryRun && *output == "":
		return usageError{"publication follows in a later release; use --dry-run or --output"}
	}
	if *timeout <= 0 {
		return usageError{"--timeout must be positive"}
	}
	idSet := false
	flags.Visit(func(f *flag.Flag) { idSet = idSet || f.Name == "repository-id" })
	if idSet && *repositoryID <= 0 {
		return usageError{"--repository-id must be a positive integer"}
	}
	if *output != "" && !idSet {
		return usageError{"artifact output needs --repository-id, the identity the artifact is published under"}
	}

	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	if *commit == "" {
		out, err := env.Git(ctx, *repo, "rev-parse", "HEAD")
		if err != nil {
			return fmt.Errorf("cannot determine the commit with git rev-parse HEAD in %s (pass --commit): %w", *repo, err)
		}
		*commit = strings.TrimSpace(string(out))
	}
	if !commitPattern.MatchString(*commit) {
		return errors.New("commit must be 40 lowercase hexadecimal characters")
	}
	dir := env.Getenv("CODEGRAPH_DIR")
	if dir == "" {
		dir = ".codegraph"
	}
	path := *index
	if path == "" {
		path = filepath.Join(*repo, dir, "codegraph.db")
	}

	snapshot, err := graphimport.Read(ctx, path, graphartifact.Limits{})
	if err != nil {
		return err
	}
	// The artifact needs a non-empty repository identity; hashes computed without
	// --repository-id differ from the published ones, which carry the numeric ID.
	identity := "unassigned"
	if idSet {
		identity = strconv.FormatInt(*repositoryID, 10)
	}
	artifact, report, err := graphimport.Convert(snapshot, graphimport.Options{Repository: identity, Commit: *commit, IdentityScope: identity})
	if err != nil {
		return err
	}
	if i := slices.IndexFunc(snapshot.Metadata, func(m graphimport.MetadataEntry) bool { return m.Key == "indexed_with_extraction_version" }); i >= 0 {
		artifact.Producer.Configuration = "extraction-version=" + snapshot.Metadata[i].Value
	}
	hash, err := graphartifact.SemanticHashV2(artifact, graphartifact.Limits{})
	if err != nil {
		return err
	}
	artifact.ContentHash = hash
	encoded, err := graphartifact.MarshalV2(artifact, graphartifact.Limits{})
	if err != nil {
		return err
	}
	freshness, err := graphimport.Verify(ctx, env.Repository, *repo, *commit, snapshot, graphimport.VerifyOptions{DataDir: dir})
	if err != nil {
		freshness = &graphimport.Freshness{
			Status: graphimport.StatusUnverifiable, Commit: *commit, Detail: err.Error(),
			Modified: []string{}, NotInCommit: []string{}, NotIndexed: []string{}, Unverified: []string{},
		}
	}
	var refusal error
	if *output != "" {
		refusal = outputRefusal(freshness, snapshot, *output, filepath.Join(*repo, dir))
	}
	var written *outputReport
	if *output != "" && refusal == nil {
		if err := os.WriteFile(*output, encoded, 0o644); err != nil {
			return err
		}
		written = &outputReport{Path: *output, Bytes: len(encoded)}
	}
	withErrors := 0
	for _, f := range snapshot.Files {
		if f.Errors != nil {
			withErrors++
		}
	}
	if err := writeJSON(stdout, importReport{
		Command: "graph import codegraph",
		DryRun:  *dryRun,
		Index: indexReport{Path: snapshot.Path, SchemaVersion: snapshot.SchemaVersion, Producer: producerReport{
			Name: artifact.Producer.Name, Version: artifact.Producer.Version, Configuration: artifact.Producer.Configuration,
		}},
		RepositoryID: *repositoryID,
		Commit:       *commit,
		Freshness:    *freshness,
		Counts:       countsReport{Nodes: report.Nodes, Edges: report.Edges, Files: report.Files, Unresolved: report.Unresolved, Metadata: report.Metadata},
		NodeKinds:    report.NodeKinds,
		EdgeKinds:    report.EdgeKinds,
		Artifact:     artifactReport{Repository: identity, Bytes: len(encoded), ContentHash: hex.EncodeToString(hash)},
		Diagnostics:  diagnostics{UnresolvedReferences: report.Unresolved, FilesWithErrors: withErrors, RoundedTimestamps: snapshot.RoundedTimestamps, Dropped: []string{}},
		Output:       written,
		Published:    false,
	}); err != nil {
		return err
	}
	return refusal
}

// outputRefusal returns why the artifact must not be written, or nil.
func outputRefusal(freshness *graphimport.Freshness, snapshot *graphimport.Snapshot, output, dataDir string) error {
	if freshness.Status != graphimport.StatusFresh {
		return fmt.Errorf("the index is not fresh (status=%s): %s", freshness.Status, freshness.Detail)
	}
	if state, _ := snapshot.MetadataValue("index_state"); state != "complete" {
		if state == "" {
			state = "missing"
		}
		return fmt.Errorf("the index is not complete (index_state=%s); finish indexing with CodeGraph and retry", state)
	}
	if insideDir(output, dataDir) {
		return errors.New("refusing to write into the CodeGraph data directory")
	}
	return nil
}

// insideDir reports whether path resolves inside dir, following symlinks of the deepest existing ancestor.
func insideDir(path, dir string) bool {
	rel, err := filepath.Rel(resolve(dir), resolve(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func resolve(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	var tail []string
	for p := abs; ; p = filepath.Dir(p) {
		if real, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(append([]string{real}, tail...)...)
		}
		if p == filepath.Dir(p) {
			return abs
		}
		tail = append([]string{filepath.Base(p)}, tail...)
	}
}
