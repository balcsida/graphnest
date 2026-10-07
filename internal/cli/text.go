package cli

import (
	"flag"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/balcsida/graphnest/internal/graphimport"
)

const maxListed = 20

// addFormatFlag registers --format; checkFormat validates it after parsing.
func addFormatFlag(flags *flag.FlagSet) *string {
	return flags.String("format", "json", "report format: `json` or text")
}

func checkFormat(format string) error {
	if format != "json" && format != "text" {
		return usageError{fmt.Sprintf("--format must be json or text, not %q", format)}
	}
	return nil
}

func formatImportReport(r importReport, refusal error) string {
	var b strings.Builder
	writeIndexBlock(&b, r.Index)
	fmt.Fprintf(&b, "\nRepository\n  commit: %s\n", r.Commit)
	if r.RepositoryID != 0 {
		fmt.Fprintf(&b, "  repository id: %d\n", r.RepositoryID)
	}
	writeFreshnessBlock(&b, r.Freshness)
	writeCountsBlock(&b, r.Counts, r.NodeKinds, r.EdgeKinds)
	writeDiagnosticsBlock(&b, r.Diagnostics)
	fmt.Fprintf(&b, "\nArtifact\n  repository identity: %s\n  bytes: %d\n  content hash: %s\n", r.Artifact.Repository, r.Artifact.Bytes, r.Artifact.ContentHash)
	if r.Output != nil {
		fmt.Fprintf(&b, "  written to: %s (%d bytes)\n", r.Output.Path, r.Output.Bytes)
	}
	writeServerBlocks(&b, r.Server, r.Publication)
	next := ""
	switch {
	case refusal != nil:
		next = refusal.Error()
	case r.Published:
		next = "nothing; the generation is published."
	case r.Output != nil:
		next = fmt.Sprintf("upload the file with: graphnest graph upload %s --repository-id %d", r.Output.Path, r.RepositoryID)
	case r.Freshness.Status != graphimport.StatusFresh:
		next = r.Freshness.Detail
	default:
		next = "write the artifact with --output FILE, or drop --dry-run to publish it."
	}
	fmt.Fprintf(&b, "\nNext step: %s\n", next)
	return b.String()
}

func formatUploadReport(r uploadReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Artifact\n  path: %s\n  bytes: %d\n  content hash: %s\n  producer: %s %s\n  nodes: %d\n  edges: %d\n  files: %d\n",
		r.Artifact.Path, r.Artifact.Bytes, r.Artifact.ContentHash, r.Artifact.Producer.Name, r.Artifact.Producer.Version, r.Artifact.Nodes, r.Artifact.Edges, r.Artifact.Files)
	fmt.Fprintf(&b, "\nRepository\n  repository id: %d\n  commit: %s\n", r.RepositoryID, r.Commit)
	writeServerBlocks(&b, r.Server, r.Publication)
	b.WriteString("\nNext step: nothing; the generation is published.\n")
	return b.String()
}

func writeIndexBlock(b *strings.Builder, index indexReport) {
	fmt.Fprintf(b, "Index\n  path: %s\n  producer: %s %s\n  schema version: %d\n", index.Path, index.Producer.Name, index.Producer.Version, index.SchemaVersion)
	if index.Producer.Configuration != "" {
		fmt.Fprintf(b, "  configuration: %s\n", index.Producer.Configuration)
	}
}

func writeFreshnessBlock(b *strings.Builder, f graphimport.Freshness) {
	fmt.Fprintf(b, "\nFreshness\n  status: %s\n  detail: %s\n", f.Status, f.Detail)
	for _, list := range []struct {
		label string
		items []string
	}{{"modified", f.Modified}, {"not in commit", f.NotInCommit}, {"not indexed", f.NotIndexed}, {"unverified", f.Unverified}} {
		if len(list.items) == 0 {
			continue
		}
		fmt.Fprintf(b, "  %s (%d):\n", list.label, len(list.items))
		for _, item := range list.items[:min(len(list.items), maxListed)] {
			fmt.Fprintf(b, "    %s\n", item)
		}
		if len(list.items) > maxListed {
			fmt.Fprintf(b, "    ... and %d more\n", len(list.items)-maxListed)
		}
	}
}

func writeCountsBlock(b *strings.Builder, c countsReport, nodeKinds, edgeKinds map[string]int) {
	fmt.Fprintf(b, "\nCounts\n  nodes: %d\n  edges: %d\n  files: %d\n", c.Nodes, c.Edges, c.Files)
	for _, kinds := range []struct {
		label string
		count map[string]int
	}{{"node kinds", nodeKinds}, {"edge kinds", edgeKinds}} {
		fmt.Fprintf(b, "  %s:\n", kinds.label)
		for _, kind := range slices.Sorted(maps.Keys(kinds.count)) {
			fmt.Fprintf(b, "    %s: %d\n", kind, kinds.count[kind])
		}
	}
}

func writeDiagnosticsBlock(b *strings.Builder, d diagnostics) {
	dropped := "none"
	if len(d.Dropped) > 0 {
		dropped = strings.Join(d.Dropped, ", ")
	}
	fmt.Fprintf(b, "\nDiagnostics\n  unresolved references: %d\n  files with extraction errors: %d\n  rounded timestamps: %d\n  dropped facts: %s\n",
		d.UnresolvedReferences, d.FilesWithErrors, d.RoundedTimestamps, dropped)
}

func writeServerBlocks(b *strings.Builder, server *serverReport, publication *publicationReport) {
	if server != nil {
		fmt.Fprintf(b, "\nServer\n  indexed commit: %s\n  publication permitted: %t\n", server.IndexedSHA, server.Permitted)
		for _, g := range []struct {
			label string
			id    string
		}{{"active generation before", generation(server, true)}, {"active generation after", generation(server, false)}} {
			fmt.Fprintf(b, "  %s: %s\n", g.label, g.id)
		}
	}
	if publication != nil {
		r := publication.Result
		fmt.Fprintf(b, "\nPublication\n  generation: %d\n  replaced generation: %d\n  deduplicated: %t\n  attempts: %d\n  expected generation: %d\n  replace producer: %t\n  content hash: %s\n",
			r.Generation, r.ReplacedGeneration, r.Deduplicated, publication.Attempts, publication.ExpectedGeneration, publication.ReplaceProducer, r.ContentHash)
	}
}

func generation(server *serverReport, before bool) string {
	g := server.After
	if before {
		g = server.Before
	}
	if g == nil {
		return "none"
	}
	return fmt.Sprintf("%d (%s %s, commit %s)", g.ID, g.Producer, g.ProducerVersion, g.Commit)
}
