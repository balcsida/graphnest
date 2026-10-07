# SCIP fixtures

`go-demo/` is a two-file Go module indexed with scip-go. Its module path
(`github.com/oldorg/demo`) deliberately differs from the repository name the
tests store it under, so the fixture also covers a repository transferred to
another owner after its module path was chosen.

Regenerate `index.scip` from the directory (`go.mod`, `main.go`,
`greet/greet.go`) with the pinned indexer:

```sh
cd test/fixtures/scip/go-demo
scip-go --output index.scip   # scip-go 0.2.7
```

Keep the Go sources exactly as checked in: every test expectation names
zero-based lines and UTF-8 columns from this index.
