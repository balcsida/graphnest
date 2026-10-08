package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/balcsida/graphnest/internal/client"
	"github.com/balcsida/graphnest/internal/graphartifact"
	"github.com/balcsida/graphnest/internal/graphimport"
)

const doctorUsage = "graphnest doctor [--repo DIR] [--index FILE] [--repository-id N] [--timeout D]"

// Check statuses.
const (
	statusOK   = "ok"
	statusWarn = "warn"
	statusFail = "fail"
)

// doctorReport is the JSON document printed by doctor.
type doctorReport struct {
	Command string        `json:"command"`
	Checks  []doctorCheck `json:"checks"`
	OK      bool          `json:"ok"`
}

type doctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

func runDoctor(ctx context.Context, args []string, env Environment, stdout, stderr io.Writer) error {
	flags := newFlags("doctor", doctorUsage, stderr)
	repo := flags.String("repo", ".", "repository directory")
	index := flags.String("index", "", "CodeGraph index file (default <repo>/<CODEGRAPH_DIR or .codegraph>/codegraph.db)")
	repositoryID := flags.Int64("repository-id", 0, "GraphNest repository ID, to compare with the server's indexed commit")
	timeout := flags.Duration("timeout", defaultTimeout, "time limit for all checks")
	if err := parse(flags, args); err != nil {
		return err
	}
	idSet := false
	flags.Visit(func(f *flag.Flag) { idSet = idSet || f.Name == "repository-id" })
	if idSet && *repositoryID <= 0 {
		return usageError{"--repository-id must be a positive integer"}
	}
	if *timeout <= 0 {
		return usageError{"--timeout must be positive"}
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	report := doctorReport{Command: "doctor", Checks: []doctorCheck{}, OK: true}
	add := func(name, status, format string, a ...any) {
		report.Checks = append(report.Checks, doctorCheck{name, status, fmt.Sprintf(format, a...)})
		report.OK = report.OK && status != statusFail
	}

	if out, err := env.Git(ctx, ".", "--version"); err != nil {
		add("git", statusFail, "git cannot be run (%s); install git and put it on PATH", gitFailure(err))
	} else {
		add("git", statusOK, "%s", strings.TrimSpace(string(out)))
	}

	head := ""
	if out, err := env.Git(ctx, *repo, "rev-parse", "--verify", "HEAD^{commit}"); err != nil {
		add("repository", statusFail, "%s is not a git work tree with a commit at HEAD (%s)", *repo, gitFailure(err))
	} else {
		head = strings.TrimSpace(string(out))
		add("repository", statusOK, "%s at commit %s", *repo, head)
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
		add("index", statusFail, "%s: %v", path, err)
	} else {
		producer, _ := snapshot.MetadataValue("indexed_with_version")
		state, _ := snapshot.MetadataValue("index_state")
		add("index", statusOK, "%s: schema %d, CodeGraph %s, index_state=%s, %d nodes, %d edges, %d files",
			snapshot.Path, snapshot.SchemaVersion, orMissing(producer), orMissing(state), len(snapshot.Nodes), len(snapshot.Edges), len(snapshot.Files))
	}

	switch {
	case snapshot == nil:
		add("producer-rules", statusWarn, "skipped: no readable index")
	default:
		producer, _ := snapshot.MetadataValue("indexed_with_version")
		if rules, err := graphimport.RulesFor(producer); err != nil {
			add("producer-rules", statusFail, "no file rules for CodeGraph %s, so the index cannot be verified for freshness; re-index with CodeGraph 1.6.0, 1.6.1 or 1.6.2", orMissing(producer))
		} else {
			add("producer-rules", statusOK, "rules of CodeGraph %s (max source file %d bytes, %d extensions, %d default ignore patterns)",
				rules.Version, rules.MaxSourceFileSize, len(rules.ExtensionMap), len(rules.DefaultIgnorePatterns))
		}
	}

	var notes []string
	if snapshot != nil {
		if state, _ := snapshot.MetadataValue("index_state"); state != "complete" {
			notes = append(notes, fmt.Sprintf("index_state is %s, not complete: CodeGraph is still indexing or failed; finish indexing and retry", orMissing(state)))
		}
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(path + suffix); err == nil {
			notes = append(notes, "codegraph.db"+suffix+" exists: a CodeGraph process may hold the index open (reads stay consistent)")
		}
	}
	if len(notes) > 0 {
		add("index-state", statusWarn, "%s", strings.Join(notes, "; "))
	} else {
		add("index-state", statusOK, "no WAL sidecars next to the index")
	}

	if snapshot == nil || head == "" {
		add("freshness", statusWarn, "skipped: needs a repository commit and a readable index")
	} else if freshness, err := graphimport.Verify(ctx, env.Repository, *repo, head, snapshot, graphimport.VerifyOptions{DataDir: dir}); err != nil {
		add("freshness", statusWarn, "unverifiable: %v", err)
	} else if freshness.Status == graphimport.StatusFresh {
		add("freshness", statusOK, "%s", freshness.Detail)
	} else {
		add("freshness", statusWarn, "%s: %s (modified %d, not in commit %d, not indexed %d, unverified %d)", freshness.Status, freshness.Detail,
			len(freshness.Modified), len(freshness.NotInCommit), len(freshness.NotIndexed), len(freshness.Unverified))
	}

	if env.Getenv("GRAPHNEST_SERVER_URL") == "" {
		add("server", statusWarn, "GRAPHNEST_SERVER_URL is not set; server checks skipped")
	} else {
		status, detail := checkServer(ctx, env, *repositoryID, head)
		add("server", status, "%s", detail)
	}

	if err := writeJSON(stdout, report); err != nil {
		return err
	}
	if !report.OK {
		return errors.New("one or more checks failed")
	}
	return nil
}

// checkServer reads the repository (with an ID) and the graph status; the first error ends it as a failure.
func checkServer(ctx context.Context, env Environment, repositoryID int64, head string) (string, string) {
	config, err := client.FromEnv(env.Getenv, env.ReadFile, storedLogins(env))
	if err == nil {
		var server *client.Client
		if server, err = client.New(config); err == nil {
			status, detail := serverChecks(ctx, server, repositoryID, head)
			return status, "credentials: " + config.Source + "; " + detail
		}
	}
	return statusFail, "configuration: " + err.Error()
}

func serverChecks(ctx context.Context, server *client.Client, repositoryID int64, head string) (string, string) {
	status := statusOK
	var parts []string
	if repositoryID == 0 {
		return statusOK, "configuration is valid; pass --repository-id to check the repository and publication rights"
	}
	summary, err := server.Repository(ctx, repositoryID)
	if err != nil {
		return statusFail, describeServerError(err).Error()
	}
	if head != "" && summary.IndexedSHA != head {
		status = statusWarn
		parts = append(parts, fmt.Sprintf("indexed_sha %s differs from HEAD %s", summary.IndexedSHA, head))
	} else {
		parts = append(parts, "indexed_sha "+summary.IndexedSHA)
	}
	graph, err := server.GraphStatus(ctx, repositoryID)
	if err != nil {
		return statusFail, describeServerError(err).Error()
	}
	if graph.Publication != nil && graph.Publication.Permitted {
		parts = append(parts, "this token may publish")
	} else {
		status = statusWarn
		parts = append(parts, "this token may not publish; "+grantHint)
	}
	return status, strings.Join(parts, "; ")
}

func orMissing(value string) string {
	if value == "" {
		return "(missing)"
	}
	return value
}

// gitFailure returns git's own message when it ran and failed, else the error.
func gitFailure(err error) string {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if text := strings.TrimSpace(string(exit.Stderr)); text != "" {
			return text
		}
	}
	return err.Error()
}
