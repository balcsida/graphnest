package graphimport

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

// Freshness statuses.
const (
	StatusFresh        = "fresh"
	StatusStale        = "stale"
	StatusUnverifiable = "unverifiable"
)

// ProjectConfig is the part of codegraph.json at the commit that changes file selection.
// Malformed or absent means zero config.
type ProjectConfig struct {
	Extensions       map[string]string
	Exclude, Include []string
}

// Freshness is the result of checking an index against the content of a commit.
type Freshness struct {
	Status      string // "fresh", "stale" or "unverifiable"
	Commit      string
	Producer    string   // indexed_with_version
	Compared    int      // indexed files found at the commit and hashed
	Modified    []string // indexed files whose content at the commit hashes differently
	NotInCommit []string // indexed files absent from the commit tree: untracked or deleted content
	NotIndexed  []string // files at the commit the producer would index that the index lacks
	Unverified  []string // indexed paths that are symlinks or submodules at the commit
	Virtual     int      // node file paths that are not files in the index
	Detail      string
}

// VerifyOptions configures Verify.
type VerifyOptions struct {
	DataDir string // the .codegraph directory name; default ".codegraph"
}

// maxConfigBytes bounds the root .gitignore and codegraph.json read from the archive.
const maxConfigBytes = 1 << 20

// Verify checks snapshot s against the content of commit in repo using the rules of the producer
// that wrote it. It streams the commit once and never writes to the repository.
//
// ponytail: only the root .gitignore applies (as in the producer's flat matcher); includeIgnored,
// embedded repos and .git/info/exclude are not modelled, and any string is accepted as an
// extension override language.
func Verify(ctx context.Context, git Git, repo, commit string, s *Snapshot, options VerifyOptions) (*Freshness, error) {
	f := &Freshness{Commit: commit}
	producer, ok := s.MetadataValue("indexed_with_version")
	if !ok {
		f.Status, f.Detail = StatusUnverifiable, "The index records no indexed_with_version, so the producer's file rules are unknown; re-index with CodeGraph 1.6.0 or newer."
		return f, nil
	}
	f.Producer = producer
	rules, err := RulesFor(producer)
	if err != nil {
		f.Status, f.Detail = StatusUnverifiable, fmt.Sprintf("No file rules are captured for CodeGraph %s; re-index with CodeGraph 1.6.0, 1.6.1 or 1.6.2.", producer)
		return f, nil
	}
	dataDir := options.DataDir
	if dataDir == "" {
		dataDir = ".codegraph"
	}
	indexed := make(map[string]File, len(s.Files))
	for _, file := range s.Files {
		indexed[file.Path] = file
	}
	virtual := map[string]bool{}
	for _, n := range s.Nodes {
		if _, ok := indexed[n.FilePath]; !ok {
			virtual[n.FilePath] = true
		}
	}
	f.Virtual = len(virtual)

	stream, err := git.Archive(ctx, repo, commit)
	if err != nil {
		return nil, err
	}
	var (
		seen       = map[string]bool{}
		candidates []string
		gitignore  string
		configData []byte
		streamErr  error
		reader     = tar.NewReader(stream)
		unverified []string
		modified   []string
		compared   int
	)
	for streamErr == nil {
		var h *tar.Header
		if h, streamErr = reader.Next(); streamErr != nil {
			break
		}
		name := strings.TrimSuffix(h.Name, "/")
		switch h.Typeflag {
		case tar.TypeDir, tar.TypeSymlink, tar.TypeLink:
			// git archive writes a submodule as an empty directory.
			if _, ok := indexed[name]; ok {
				seen[name] = true
				unverified = append(unverified, name)
			}
		case tar.TypeReg:
			if file, ok := indexed[name]; ok {
				seen[name] = true
				var hash string
				if hash, streamErr = ContentHash(rules, h.Size, reader); streamErr != nil {
					break
				}
				compared++
				if hash != file.ContentHash {
					modified = append(modified, name)
				}
				continue
			}
			var data []byte
			switch name {
			case ".gitignore":
				data, streamErr = io.ReadAll(io.LimitReader(reader, maxConfigBytes))
				gitignore = string(data)
			case "codegraph.json":
				configData, streamErr = io.ReadAll(io.LimitReader(reader, maxConfigBytes))
			}
			if !underDataOrGitDir(name, dataDir) {
				candidates = append(candidates, name)
			}
		}
	}
	if errors.Is(streamErr, io.EOF) {
		_, streamErr = io.Copy(io.Discard, stream) // reach the end of git's output so Close reports its exit status
	}
	if closeErr := stream.Close(); closeErr != nil {
		return nil, closeErr
	}
	if streamErr != nil {
		return nil, fmt.Errorf("read archive of %s: %w", commit, streamErr)
	}

	config, configNote := parseProjectConfig(configData)
	for path := range indexed {
		if !seen[path] {
			f.NotInCommit = append(f.NotInCommit, path)
		}
	}
	candidates = slices.DeleteFunc(candidates, func(p string) bool { return !IsSourceFile(rules, p, config.Extensions) })
	if f.NotIndexed, err = notIndexed(ctx, git, rules, config, gitignore, candidates); err != nil {
		return nil, err
	}
	f.Compared, f.Modified, f.Unverified = compared, modified, unverified
	for _, list := range [][]string{f.Modified, f.NotInCommit, f.NotIndexed, f.Unverified} {
		slices.Sort(list)
	}
	f.Status = StatusStale
	switch {
	case len(f.Unverified) > 0:
		f.Status = StatusUnverifiable
	case len(f.Modified)+len(f.NotInCommit)+len(f.NotIndexed) == 0:
		f.Status = StatusFresh
	}
	f.Detail = freshnessDetail(f) + configNote
	return f, nil
}

// notIndexed returns the candidates the producer would index. Defaults and the root .gitignore drop a
// file, a codegraph.json include pattern re-admits it unless a default ignores it, and exclude always wins.
func notIndexed(ctx context.Context, git Git, rules Rules, config ProjectConfig, gitignore string, candidates []string) ([]string, error) {
	defaults := strings.Join(rules.DefaultIgnorePatterns, "\n")
	ignored, err := git.Ignored(ctx, defaults+"\n"+strings.ReplaceAll(gitignore, "\r\n", "\n"), rules.IgnoreCase, candidates)
	if err != nil {
		return nil, err
	}
	kept := without(candidates, ignored)
	if len(config.Include) > 0 && len(ignored) > 0 {
		admitted, err := git.Ignored(ctx, strings.Join(config.Include, "\n"), rules.IgnoreCase, ignored)
		if err != nil {
			return nil, err
		}
		byDefault, err := git.Ignored(ctx, defaults, rules.IgnoreCase, admitted)
		if err != nil {
			return nil, err
		}
		kept = append(kept, without(admitted, byDefault)...)
	}
	if len(config.Exclude) > 0 && len(kept) > 0 {
		excluded, err := git.Ignored(ctx, strings.Join(config.Exclude, "\n"), rules.IgnoreCase, kept)
		if err != nil {
			return nil, err
		}
		kept = without(kept, excluded)
	}
	return kept, nil
}

// without returns the paths that are not in drop.
func without(paths, drop []string) []string {
	set := make(map[string]bool, len(drop))
	for _, p := range drop {
		set[p] = true
	}
	var out []string
	for _, p := range paths {
		if !set[p] {
			out = append(out, p)
		}
	}
	return out
}

func underDataOrGitDir(path, dataDir string) bool {
	segments := strings.Split(path, "/")
	for _, segment := range segments[:len(segments)-1] {
		if segment == ".git" || segment == dataDir || segment == ".codegraph" || strings.HasPrefix(segment, ".codegraph-") {
			return true
		}
	}
	return false
}

// parseProjectConfig reads codegraph.json the way the producer does: bad JSON or values degrade to zero config.
// The note is non-empty when data was present but unusable.
func parseProjectConfig(data []byte) (ProjectConfig, string) {
	var config ProjectConfig
	if data == nil {
		return config, ""
	}
	var raw struct {
		Extensions map[string]any `json:"extensions"`
		Exclude    []any          `json:"exclude"`
		Include    []any          `json:"include"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return config, " codegraph.json at the commit is not valid JSON and was treated as empty, as CodeGraph does."
	}
	for key, value := range raw.Extensions {
		key = strings.ToLower(strings.TrimSpace(key))
		if key != "" && !strings.HasPrefix(key, ".") {
			key = "." + key
		}
		language, _ := value.(string)
		if body := strings.TrimPrefix(key, "."); body == "" || strings.ContainsAny(body, "./\\") || language == "" {
			continue
		}
		if config.Extensions == nil {
			config.Extensions = map[string]string{}
		}
		config.Extensions[key] = language
	}
	patterns := func(entries []any) (out []string) {
		for _, entry := range entries {
			if pattern, _ := entry.(string); strings.TrimSpace(pattern) != "" {
				out = append(out, strings.TrimSpace(pattern))
			}
		}
		return out
	}
	config.Exclude, config.Include = patterns(raw.Exclude), patterns(raw.Include)
	return config, ""
}

func freshnessDetail(f *Freshness) string {
	switch f.Status {
	case StatusFresh:
		return fmt.Sprintf("All %d indexed files match commit %s.", f.Compared, f.Commit)
	case StatusUnverifiable:
		return fmt.Sprintf("%d indexed path(s) are symlinks or submodules at commit %s and cannot be hashed; index a checkout where they are regular files.", len(f.Unverified), f.Commit)
	}
	var parts []string
	for _, p := range []struct {
		count int
		text  string
	}{{len(f.Modified), "modified"}, {len(f.NotInCommit), "not in the commit"}, {len(f.NotIndexed), "missing from the index"}} {
		if p.count > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", p.count, p.text))
		}
	}
	return fmt.Sprintf("The index does not match commit %s (%s); commit your changes and re-run CodeGraph indexing on a clean checkout of that commit.", f.Commit, strings.Join(parts, ", "))
}
