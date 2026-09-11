package graphservice

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"github.com/balcsida/graphnest/internal/repository"
	"github.com/balcsida/graphnest/pkg/api"
)

var projectModuleDeclarations = regexp.MustCompile(`(?m)^[ \t]*module\b[^\n]*`)
var projectModule = regexp.MustCompile(`^[ \t]*module[ \t]+("[^"\r\n]*"|[^ \t\r\n"]+)[ \t]*(?://[^\r\n]*)?\r?$`)

type ProjectTokensRequest struct {
	Repo   api.GraphRepositorySelector
	Branch string
}

type ProjectTokensResponse struct {
	Tokens      []string                   `json:"tokens"`
	Generations []graphprotocol.Generation `json:"generations"`
}

// ProjectNameTokens returns request-local DiscoveryConfig.ProjectTerms from the
// selected repository and exact-SHA source, never from artifact metadata or disk.
// ContentReader already bounds source transfer; only 512 lines / 64 KiB of each
// manifest may contribute evidence. Truncated/unavailable manifests contribute none.
func (s *Service) ProjectNameTokens(ctx context.Context, p authn.Principal, r ProjectTokensRequest) (ProjectTokensResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	i, err := s.inspectionScope(ctx, p, r.Repo, r.Branch)
	if err != nil {
		return ProjectTokensResponse{}, err
	}
	page, err := i.backend.Entities(ctx, graphprotocol.EntitiesRequest{Scope: i.scope, Limit: 1})
	if err != nil {
		return ProjectTokensResponse{}, err
	}
	if err = i.generations(page.Generations); err != nil {
		return ProjectTokensResponse{}, err
	}
	result := ProjectTokensResponse{Generations: page.Generations}
	add := func(raw string) {
		token := strings.Map(func(c rune) rune {
			if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
				return c
			}
			return -1
		}, strings.ToLower(path.Base(raw)))
		if len(token) >= 5 && !slices.Contains(result.Tokens, token) {
			result.Tokens = append(result.Tokens, token)
		}
	}
	add(i.scope.Repositories[0].Name)
	if s.Files != nil {
		for _, name := range []string{"go.mod", "package.json"} {
			file, readErr := s.Files.ReadFileAt(ctx, p, api.ReadFileRequest{RepositoryID: i.selected.GitHubID, Path: name, StartLine: 1, EndLine: 512}, i.selected.Commit)
			if ctx.Err() != nil {
				return ProjectTokensResponse{}, ctx.Err()
			}
			if errors.Is(readErr, repository.ErrNotIndexed) {
				return ProjectTokensResponse{}, ErrGraphNotReady
			}
			if readErr != nil {
				continue
			}
			if file.RepositoryID != i.selected.GitHubID || file.IndexedSHA != i.selected.Commit || file.Path != name || file.StartLine != 1 || file.EndLine < 1 || file.EndLine > 512 {
				return ProjectTokensResponse{}, ErrGraphNotReady
			}
			// An explicit EndLine is a selection, not a truncation flag. At
			// the ceiling EOF is unknown, so a partial manifest supplies no token.
			if file.Truncated || file.EndLine == 512 || len(file.Content) > 64<<10 || !utf8.ValidString(file.Content) || strings.ContainsRune(file.Content, 0) {
				continue
			}
			if name == "go.mod" {
				// Count malformed declarations too: a valid line cannot rescue
				// a manifest with a second module declaration.
				declarations := projectModuleDeclarations.FindAllString(file.Content, 2)
				if len(declarations) != 1 {
					continue
				}
				if match := projectModule.FindStringSubmatch(declarations[0]); match != nil {
					module := match[1]
					if strings.HasPrefix(module, `"`) {
						module, _ = strconv.Unquote(module)
					}
					if module != "" {
						add(module)
					}
				}
			} else {
				var pkg struct {
					Name string `json:"name"`
				}
				if json.Unmarshal([]byte(file.Content), &pkg) == nil {
					add(pkg.Name)
				}
			}
		}
	}
	slices.Sort(result.Tokens)
	if err = s.finishInspection(ctx, p, i, result.Generations, result); err != nil {
		return ProjectTokensResponse{}, err
	}
	return result, nil
}
