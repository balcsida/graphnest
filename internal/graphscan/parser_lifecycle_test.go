package graphscan_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/grepnest/grepnest/internal/graphscan"
	"github.com/grepnest/grepnest/internal/graphscan/golang"
	"github.com/grepnest/grepnest/internal/graphscan/java"
	"github.com/grepnest/grepnest/internal/graphscan/javascript"
	"github.com/grepnest/grepnest/internal/graphscan/kotlin"
	"github.com/grepnest/grepnest/internal/graphscan/rust"
)

func TestParsersReleaseCanceledNativeHandles(t *testing.T) {
	parsers := map[string]graphscan.Parser{
		".go":   golang.Parse,
		".js":   javascript.Parse,
		".ts":   javascript.Parse,
		".tsx":  javascript.Parse,
		".java": java.Parse,
		".kt":   kotlin.Parse,
		".rs":   rust.Parse,
	}
	for extension, parser := range parsers {
		t.Run(extension, func(t *testing.T) {
			for range 20 {
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				if _, err := parser(ctx, "smoke"+extension, parserSource(extension)); !errors.Is(err, context.Canceled) {
					t.Fatalf("Parse() error = %v, want context.Canceled", err)
				}

				ctx, cancel = context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
				if _, err := parser(ctx, "smoke"+extension, parserSource(extension)); !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("Parse() error = %v, want context.DeadlineExceeded", err)
				}
				cancel()
			}
			if _, err := parser(t.Context(), "smoke"+extension, parserSource(extension)); err != nil {
				t.Fatalf("Parse() after cancellation = %v", err)
			}
		})
	}
}

func parserSource(extension string) []byte {
	return map[string][]byte{
		".go":   []byte("package smoke\nfunc run() {}\n"),
		".js":   []byte("function run() {}\n"),
		".ts":   []byte("function run(): void {}\n"),
		".tsx":  []byte("function Run() { return <div /> }\n"),
		".java": []byte("class Smoke { void run() {} }\n"),
		".kt":   []byte("fun run() {}\n"),
		".rs":   []byte("fn run() {}\n"),
	}[extension]
}
