//go:build unix

package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/grepnest/grepnest/internal/enrichment"
	"github.com/grepnest/grepnest/internal/graphscan"
	"github.com/grepnest/grepnest/internal/graphscanner"
)

func main() {
	if len(os.Args) < 2 {
		// ponytail: compatibility idle; milestone 7 removes the legacy workload.
		ctx, cancel := signalContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		<-ctx.Done()
		return
	}
	if os.Args[1] != "enrich" {
		fmt.Fprintln(os.Stderr, "usage: grepnest-scanner enrich")
		os.Exit(2)
	}
	ctx, cancel := signalContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := runEnrichment(ctx, os.Args[2:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "enrichment failed")
		os.Exit(1)
	}
}

var signalContext = func(parent context.Context, signals ...os.Signal) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, signals...)
}

func runEnrichment(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("enrich", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	protocol := flags.Int("protocol", 0, "")
	root := flags.String("root", "", "")
	repositoryID := flags.Int64("repository-id", 0, "")
	commit := flags.String("commit", "", "")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *protocol != enrichment.ProtocolVersion ||
		*root == "" || *repositoryID <= 0 || len(*commit) != 40 {
		return errors.New("invalid enrichment request")
	}
	if _, err := hex.DecodeString(*commit); err != nil || *commit != strings.ToLower(*commit) {
		return errors.New("invalid enrichment request")
	}
	artifact, err := graphscanner.Scan(ctx, graphscan.Request{RepositoryID: *repositoryID, Commit: *commit, Root: *root})
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(enrichment.Response{Version: enrichment.ProtocolVersion, Artifact: artifact})
}
