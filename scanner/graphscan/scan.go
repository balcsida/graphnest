package graphscan

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/balcsida/graphnest/internal/graphartifact"
)

var (
	ErrInvalidRequest = errors.New("invalid scanner request")
	ErrLimitExceeded  = errors.New("scanner limit exceeded")
)

type Request struct {
	RepositoryID int64
	Commit       string
	Root         string
}

type Limits struct {
	MaxFileBytes, MaxTotalBytes  int64
	MaxFiles, MaxNodes, MaxEdges int
	ParseTimeout                 time.Duration
	SkipDirectories              []string
}

type Parser func(context.Context, string, []byte) (File, error)

func Scan(ctx context.Context, request Request, parsers map[string]Parser, limits Limits) (graphartifact.Artifact, error) {
	if err := validRequest(request, limits); err != nil {
		return graphartifact.Artifact{}, err
	}
	root, err := filepath.Abs(request.Root)
	if err != nil {
		return graphartifact.Artifact{}, ErrInvalidRequest
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return graphartifact.Artifact{}, ErrInvalidRequest
	}
	source, err := openSourceRoot(root)
	if err != nil {
		return graphartifact.Artifact{}, err
	}
	defer source.Close()
	skip := map[string]bool{".git": true}
	for _, directory := range limits.SkipDirectories {
		skip[directory] = true
	}
	var files []File
	var total int64
	count, nodes, edges := 0, 1, 0
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			if path != root && skip[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		parser := parsers[filepath.Ext(entry.Name())]
		if parser == nil {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return ErrInvalidRequest
		}
		rel = filepath.ToSlash(filepath.Clean(rel))
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > limits.MaxFileBytes || info.Size() > limits.MaxTotalBytes-total || count == limits.MaxFiles {
			return ErrLimitExceeded
		}
		data, err := readBounded(source, rel, limits.MaxFileBytes, limits.MaxTotalBytes-total)
		if err != nil {
			return err
		}
		nul := bytes.IndexByte(data, 0)
		if int64(len(data)) > limits.MaxFileBytes || int64(len(data)) > limits.MaxTotalBytes-total || nul >= 0 {
			if nul >= 0 {
				return nil
			}
			return ErrLimitExceeded
		}
		if !within(nodes, 1, limits.MaxNodes) || !within(edges, 1, limits.MaxEdges) {
			return ErrLimitExceeded
		}
		parseCtx, cancel := context.WithTimeout(ctx, limits.ParseTimeout)
		parseCtx = withIRBudget(parseCtx, limits.MaxNodes-nodes-1, limits.MaxEdges-edges-1)
		file, err := parser(parseCtx, rel, data)
		parseErr := parseCtx.Err()
		cancel()
		if parentErr := ctx.Err(); parentErr != nil {
			return parentErr
		}
		if errors.Is(parseErr, context.DeadlineExceeded) {
			return ErrLimitExceeded
		}
		if err != nil {
			return err
		}
		if parseErr != nil {
			return parseErr
		}
		file.Path = rel
		if !within(nodes, 1, limits.MaxNodes) || !within(nodes+1, len(file.Declarations), limits.MaxNodes) ||
			!within(edges, 1, limits.MaxEdges) || !within(edges+1, len(file.Declarations), limits.MaxEdges) ||
			!within(edges+1+len(file.Declarations), len(file.Imports), limits.MaxEdges) ||
			!within(edges+1+len(file.Declarations)+len(file.Imports), len(file.References), limits.MaxEdges) ||
			!within(edges+1+len(file.Declarations)+len(file.Imports)+len(file.References), len(file.Heritage), limits.MaxEdges) {
			return ErrLimitExceeded
		}
		files = append(files, file)
		count++
		total += int64(len(data))
		nodes += 1 + len(file.Declarations)
		edges += 1 + len(file.Declarations) + len(file.Imports) + len(file.References) + len(file.Heritage)
		return nil
	})
	if err != nil {
		return graphartifact.Artifact{}, err
	}
	artifact, err := Resolve(request.RepositoryID, request.Commit, files)
	if err != nil || len(artifact.Nodes) > limits.MaxNodes || len(artifact.Edges) > limits.MaxEdges {
		if err != nil {
			return graphartifact.Artifact{}, err
		}
		return graphartifact.Artifact{}, ErrLimitExceeded
	}
	return artifact, nil
}

func validRequest(request Request, limits Limits) error {
	if request.RepositoryID <= 0 || request.Root == "" || limits.MaxFileBytes <= 0 || limits.MaxFileBytes == math.MaxInt64 || limits.MaxTotalBytes <= 0 || limits.MaxFiles <= 0 || limits.MaxNodes <= 0 || limits.MaxEdges <= 0 || limits.ParseTimeout <= 0 {
		return ErrInvalidRequest
	}
	if _, err := Resolve(request.RepositoryID, request.Commit, nil); err != nil {
		return ErrInvalidRequest
	}
	return nil
}

func readBounded(root *sourceRoot, path string, max, remaining int64) ([]byte, error) {
	file, err := openSourceFile(root, path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > max || info.Size() > remaining {
		return nil, ErrLimitExceeded
	}
	return io.ReadAll(io.LimitReader(file, max+1))
}

func within(current, added, max int) bool { return current <= max && added <= max-current }
