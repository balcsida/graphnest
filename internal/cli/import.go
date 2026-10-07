package cli

import (
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/balcsida/graphnest/internal/graphartifact"
	"github.com/balcsida/graphnest/internal/graphimport"
)

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

const importUsage = "graphnest graph import codegraph --dry-run [--repo DIR] [--index FILE] [--repository-id N] [--commit SHA] [--timeout D]"

// importReport is the JSON document printed by graph import codegraph.
type importReport struct {
	Command      string          `json:"command"`
	DryRun       bool            `json:"dry_run"`
	Index        indexReport     `json:"index"`
	RepositoryID int64           `json:"repository_id,omitempty"`
	Commit       string          `json:"commit"`
	Freshness    freshnessReport `json:"freshness"`
	Counts       countsReport    `json:"counts"`
	NodeKinds    map[string]int  `json:"node_kinds"`
	EdgeKinds    map[string]int  `json:"edge_kinds"`
	Artifact     artifactReport  `json:"artifact"`
	Diagnostics  diagnostics     `json:"diagnostics"`
	Published    bool            `json:"published"`
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

type freshnessReport struct {
	Status string `json:"status"`
	Detail string `json:"detail"`
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
	dryRun := flags.Bool("dry-run", false, "convert and report without publishing or writing (required in this release)")
	repo := flags.String("repo", ".", "repository directory")
	index := flags.String("index", "", "CodeGraph index file (default <repo>/<CODEGRAPH_DIR or .codegraph>/codegraph.db)")
	repositoryID := flags.Int64("repository-id", 0, "GraphNest repository ID")
	commit := flags.String("commit", "", "commit SHA (default: git rev-parse HEAD in --repo)")
	timeout := flags.Duration("timeout", defaultTimeout, "time limit for reading and converting")
	if err := parse(flags, args); err != nil {
		return err
	}
	if !*dryRun {
		return usageError{"artifact output and publication follow in a later release; use --dry-run"}
	}
	if *timeout <= 0 {
		return usageError{"--timeout must be positive"}
	}
	idSet := false
	flags.Visit(func(f *flag.Flag) { idSet = idSet || f.Name == "repository-id" })
	if idSet && *repositoryID <= 0 {
		return usageError{"--repository-id must be a positive integer"}
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
	path := *index
	if path == "" {
		dir := env.Getenv("CODEGRAPH_DIR")
		if dir == "" {
			dir = ".codegraph"
		}
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
	encoded, err := graphartifact.MarshalV2(artifact, graphartifact.Limits{})
	if err != nil {
		return err
	}
	withErrors := 0
	for _, f := range snapshot.Files {
		if f.Errors != nil {
			withErrors++
		}
	}
	return writeJSON(stdout, importReport{
		Command: "graph import codegraph",
		DryRun:  true,
		Index: indexReport{Path: snapshot.Path, SchemaVersion: snapshot.SchemaVersion, Producer: producerReport{
			Name: artifact.Producer.Name, Version: artifact.Producer.Version, Configuration: artifact.Producer.Configuration,
		}},
		RepositoryID: *repositoryID,
		Commit:       *commit,
		Freshness:    freshnessReport{Status: "unverified", Detail: "the index contents are not compared with the commit yet"},
		Counts:       countsReport{Nodes: report.Nodes, Edges: report.Edges, Files: report.Files, Unresolved: report.Unresolved, Metadata: report.Metadata},
		NodeKinds:    report.NodeKinds,
		EdgeKinds:    report.EdgeKinds,
		Artifact:     artifactReport{Repository: identity, Bytes: len(encoded), ContentHash: hex.EncodeToString(hash)},
		Diagnostics:  diagnostics{UnresolvedReferences: report.Unresolved, FilesWithErrors: withErrors, RoundedTimestamps: snapshot.RoundedTimestamps, Dropped: []string{}},
		Published:    false,
	})
}
