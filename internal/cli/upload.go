package cli

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"strconv"

	"github.com/balcsida/graphnest/internal/graphartifact"
)

const uploadUsage = "graphnest graph upload ARTIFACT --repository-id N [--expected-generation N] [--replace-producer] [--timeout D] [--format json|text]"

// uploadReport is the JSON document printed by graph upload.
type uploadReport struct {
	Command      string             `json:"command"`
	RepositoryID int64              `json:"repository_id"`
	Commit       string             `json:"commit"`
	Artifact     uploadArtifact     `json:"artifact"`
	Server       *serverReport      `json:"server"`
	Publication  *publicationReport `json:"publication"`
	Published    bool               `json:"published"`
}

type uploadArtifact struct {
	Path        string         `json:"path"`
	Bytes       int            `json:"bytes"`
	ContentHash string         `json:"content_hash"`
	Producer    producerReport `json:"producer"`
	Nodes       int            `json:"nodes"`
	Edges       int            `json:"edges"`
	Files       int            `json:"files"`
}

func runGraphUpload(ctx context.Context, args []string, env Environment, stdout, stderr io.Writer) error {
	flags := newFlags("upload", uploadUsage, stderr)
	repositoryID := flags.Int64("repository-id", 0, "GraphNest repository ID")
	expected := flags.Int64("expected-generation", 0, "refuse unless the active published generation is `N` (0: none)")
	replaceProducer := flags.Bool("replace-producer", false, "replace a generation published by another producer")
	timeout := flags.Duration("timeout", defaultTimeout, "time limit for the whole upload")
	format := addFormatFlag(flags)
	// The artifact may come before or after the flags.
	path := ""
	for len(args) > 0 {
		if err := flags.Parse(args); err != nil {
			if err == flag.ErrHelp {
				return err
			}
			return usageError{}
		}
		if flags.NArg() == 0 {
			break
		}
		if path != "" {
			flags.Usage()
			return usageError{fmt.Sprintf("unexpected argument %q", flags.Arg(0))}
		}
		path, args = flags.Arg(0), flags.Args()[1:]
	}
	if path == "" {
		flags.Usage()
		return usageError{"the artifact file is required"}
	}
	if *repositoryID <= 0 {
		return usageError{"--repository-id must be a positive integer"}
	}
	expectedSet := false
	flags.Visit(func(f *flag.Flag) { expectedSet = expectedSet || f.Name == "expected-generation" })
	if expectedSet && *expected < 0 {
		return usageError{"--expected-generation must not be negative"}
	}
	if *timeout <= 0 {
		return usageError{"--timeout must be positive"}
	}
	if err := checkFormat(*format); err != nil {
		return err
	}

	data, err := env.ReadFile(path)
	if err != nil {
		return err
	}
	artifact, err := graphartifact.ParseV2(data, graphartifact.Limits{})
	if err != nil {
		return fmt.Errorf("%s is not a valid v2 graph artifact: %w", path, err)
	}
	if want := strconv.FormatInt(*repositoryID, 10); artifact.Repository != want {
		return fmt.Errorf("the artifact was created for repository %s, not %s", artifact.Repository, want)
	}
	hash := artifact.ContentHash
	if len(hash) == 0 {
		if hash, err = graphartifact.SemanticHashV2(artifact, graphartifact.Limits{}); err != nil {
			return err
		}
	}
	producer := producerReport{Name: artifact.GetProducer().GetName(), Version: artifact.GetProducer().GetVersion(), Configuration: artifact.GetProducer().GetConfiguration()}

	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	request := publishRequest{RepositoryID: *repositoryID, Commit: artifact.Commit, Producer: producer.Name, Data: data, ReplaceProducer: *replaceProducer, Timeout: *timeout}
	if expectedSet {
		request.ExpectedGeneration = expected
	}
	server, publication, err := publishArtifact(ctx, env, stderr, request)
	if err != nil {
		return err
	}
	result := uploadReport{
		Command:      "graph upload",
		RepositoryID: *repositoryID,
		Commit:       artifact.Commit,
		Artifact: uploadArtifact{Path: path, Bytes: len(data), ContentHash: hex.EncodeToString(hash), Producer: producer,
			Nodes: len(artifact.Nodes), Edges: len(artifact.Edges), Files: len(artifact.Files)},
		Server:      server,
		Publication: publication,
		Published:   true,
	}
	if *format == "text" {
		_, err = io.WriteString(stdout, formatUploadReport(result))
		return err
	}
	return writeJSON(stdout, result)
}
