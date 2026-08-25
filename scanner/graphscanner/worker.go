package graphscanner

import (
	"context"
	"time"

	"github.com/balcsida/graphnest/internal/graphartifact"
	"github.com/balcsida/graphnest/scanner/graphscan"
	"github.com/balcsida/graphnest/scanner/graphscan/golang"
	"github.com/balcsida/graphnest/scanner/graphscan/java"
	"github.com/balcsida/graphnest/scanner/graphscan/javascript"
	"github.com/balcsida/graphnest/scanner/graphscan/kotlin"
	"github.com/balcsida/graphnest/scanner/graphscan/rust"
)

func Scan(ctx context.Context, request graphscan.Request) (graphartifact.Artifact, error) {
	return graphscan.Scan(ctx, request, map[string]graphscan.Parser{
		".go": golang.Parse, ".js": javascript.Parse, ".ts": javascript.Parse,
		".tsx": javascript.Parse, ".java": java.Parse, ".kt": kotlin.Parse, ".rs": rust.Parse,
	}, graphscan.Limits{
		MaxFileBytes: 2 << 20, MaxTotalBytes: 1 << 30, MaxFiles: 100_000,
		MaxNodes: 1_000_000, MaxEdges: 2_000_000, ParseTimeout: 5 * time.Second,
		SkipDirectories: []string{"node_modules", "vendor", "target", "build", "dist", ".gradle"},
	})
}
