package graphscanner

import (
	"context"
	"time"

	"github.com/grepnest/grepnest/internal/graphartifact"
	"github.com/grepnest/grepnest/internal/graphscan"
	"github.com/grepnest/grepnest/internal/graphscan/golang"
	"github.com/grepnest/grepnest/internal/graphscan/java"
	"github.com/grepnest/grepnest/internal/graphscan/javascript"
	"github.com/grepnest/grepnest/internal/graphscan/kotlin"
	"github.com/grepnest/grepnest/internal/graphscan/rust"
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
