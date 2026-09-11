package graphservice

import (
	"context"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/balcsida/graphnest/internal/authn"
	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"github.com/balcsida/graphnest/internal/graphquery"
	"github.com/balcsida/graphnest/pkg/api"
	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

type FileInventoryRequest struct {
	Repo                                  api.GraphRepositorySelector
	Branch, Path, Pattern, Format, Cursor string
	Limit                                 int
	MaxDepth                              *int
	IncludeMetadata                       *bool
}

// Metadata is the original producer fact, omitted only by explicit request.
type InventoryFile struct {
	Path     string        `json:"path"`
	Metadata *graphv2.File `json:"metadata,omitempty"`
}

type InventoryTree struct {
	Name      string           `json:"name"`
	Path      string           `json:"path"`
	File      *InventoryFile   `json:"file,omitempty"`
	Children  []*InventoryTree `json:"children,omitempty"`
	Truncated bool             `json:"truncated,omitempty"`
}

type InventoryGroup struct {
	Language string          `json:"language"`
	Files    []InventoryFile `json:"files"`
}

type FileInventoryResponse struct {
	Format      string                     `json:"format"`
	Files       []InventoryFile            `json:"files,omitempty"`
	Tree        []*InventoryTree           `json:"tree,omitempty"`
	Groups      []InventoryGroup           `json:"groups,omitempty"`
	Generations []graphprotocol.Generation `json:"generations"`
	NextCursor  string                     `json:"next_cursor,omitempty"`
	// TotalFiles counts the entire filtered immutable inventory. The projection
	// and group sizes describe only this page; depth can hide additional leaves.
	TotalFiles   int64    `json:"total_files"`
	PageFiles    int      `json:"page_files"`
	VisibleFiles int      `json:"visible_files"`
	Complete     bool     `json:"complete"`
	Boundaries   []string `json:"boundaries,omitempty"`
}

// FileInventory is a bounded presentation of indexed files, not source analysis.
func (s *Service) FileInventory(ctx context.Context, p authn.Principal, r FileInventoryRequest) (FileInventoryResponse, error) {
	if r.Limit < 0 || r.Limit > 100 || len(r.Cursor) > 512 || len(r.Path) > 16384 || !utf8.ValidString(r.Path) || strings.ContainsRune(r.Path, '\x00') || len(r.Pattern) > 1024 || !utf8.ValidString(r.Pattern) || strings.ContainsRune(r.Pattern, '\x00') {
		return FileInventoryResponse{}, ErrInvalidRequest
	}
	if r.Format == "" {
		r.Format = "tree"
	}
	if r.Format != "tree" && r.Format != "flat" && r.Format != "grouped" {
		return FileInventoryResponse{}, ErrInvalidRequest
	}
	// Match the inventory tool's root-prefix spelling, without granting access
	// to a server path. Interior separators remain literal prefix evidence.
	prefix := strings.ReplaceAll(r.Path, "\\", "/")
	for strings.HasPrefix(prefix, "/") || strings.HasPrefix(prefix, "./") {
		if strings.HasPrefix(prefix, "./") {
			prefix = strings.TrimPrefix(prefix, "./")
		} else {
			prefix = strings.TrimPrefix(prefix, "/")
		}
	}
	prefix = strings.TrimRight(prefix, "/")
	if prefix == "." {
		prefix = ""
	}
	if _, err := graphquery.NormalizeFilePath(prefix); err != nil {
		return FileInventoryResponse{}, ErrInvalidRequest
	}
	depth := 20
	if r.MaxDepth != nil {
		depth = max(1, min(20, *r.MaxDepth))
	}
	metadata := r.IncludeMetadata == nil || *r.IncludeMetadata
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	i, err := s.inspectionScope(ctx, p, r.Repo, r.Branch)
	if err != nil {
		return FileInventoryResponse{}, err
	}
	page, err := i.backend.IndexedFiles(ctx, graphprotocol.FilesRequest{Scope: i.scope, Prefix: &prefix, Pattern: r.Pattern, Limit: r.Limit, Cursor: r.Cursor, IncludeCount: true})
	if err != nil {
		return FileInventoryResponse{}, err
	}
	if err := i.generations(page.Generations); err != nil {
		return FileInventoryResponse{}, err
	}
	if page.TotalFiles == nil || *page.TotalFiles < int64(len(page.Files)) || len(page.Files) > 100 {
		return FileInventoryResponse{}, ErrGraphNotReady
	}
	for _, f := range page.Files {
		if f.RepositoryID != i.selected.GitHubID || f.Fact == nil {
			return FileInventoryResponse{}, ErrGraphNotReady
		}
	}
	result := FileInventoryResponse{Format: r.Format, Generations: page.Generations, NextCursor: page.NextCursor, TotalFiles: *page.TotalFiles, PageFiles: len(page.Files), VisibleFiles: len(page.Files), Complete: true}
	if r.Cursor != "" || page.NextCursor != "" {
		result.Complete = false
		result.Boundaries = append(result.Boundaries, "file_page")
	}
	// One request owns its collator; collators are not safe to share across calls.
	order := collate.New(language.Und)
	compare := func(a, b string) int {
		if c := order.CompareString(a, b); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	}
	entries := make([]InventoryFile, 0, len(page.Files))
	for _, file := range page.Files {
		entry := InventoryFile{Path: file.Fact.Path}
		if metadata {
			entry.Metadata = file.Fact
		}
		entries = append(entries, entry)
	}
	switch r.Format {
	case "flat":
		slices.SortStableFunc(entries, func(a, b InventoryFile) int { return compare(a.Path, b.Path) })
		result.Files = entries
	case "grouped":
		groups := map[string]int{}
		for index, file := range page.Files {
			language := file.Fact.Language
			group, ok := groups[language]
			if !ok {
				group = len(result.Groups)
				groups[language] = group
				result.Groups = append(result.Groups, InventoryGroup{Language: language})
			}
			result.Groups[group].Files = append(result.Groups[group].Files, entries[index])
		}
		// Count ties preserve encounter order from the bounded byte-ordered page,
		// as the reference preserves encounter order from its indexed-file list.
		slices.SortStableFunc(result.Groups, func(a, b InventoryGroup) int { return len(b.Files) - len(a.Files) })
		for _, group := range result.Groups {
			slices.SortStableFunc(group.Files, func(a, b InventoryFile) int { return compare(a.Path, b.Path) })
		}
	case "tree":
		root := &InventoryTree{}
		for index, entry := range entries {
			parts := strings.SplitN(entry.Path, "/", depth+1)
			node := root
			end := 0
			for level, part := range parts {
				if level == depth {
					node.Truncated = true
					result.VisibleFiles--
					break
				}
				if level > 0 {
					end++
				}
				end += len(part)
				var child *InventoryTree
				// ponytail: at most 100 siblings per page; index only if that bound grows.
				for _, existing := range node.Children {
					if existing.Name == part {
						child = existing
						break
					}
				}
				if child == nil {
					child = &InventoryTree{Name: part, Path: entry.Path[:end]}
					node.Children = append(node.Children, child)
				}
				node = child
				if level == len(parts)-1 {
					node.File = &entries[index]
				}
			}
		}
		var sortTree func(*InventoryTree)
		sortTree = func(node *InventoryTree) {
			slices.SortStableFunc(node.Children, func(a, b *InventoryTree) int {
				if (a.File == nil) != (b.File == nil) {
					if a.File == nil {
						return -1
					}
					return 1
				}
				return compare(a.Name, b.Name)
			})
			for _, child := range node.Children {
				sortTree(child)
			}
		}
		sortTree(root)
		result.Tree = root.Children
		if result.VisibleFiles < result.PageFiles {
			result.Complete = false
			result.Boundaries = append(result.Boundaries, "max_depth")
		}
	}
	if err := s.finishInspection(ctx, p, i, result.Generations, result); err != nil {
		return FileInventoryResponse{}, err
	}
	return result, nil
}
