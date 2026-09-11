package graphservice

import (
	"context"
	"errors"

	"github.com/balcsida/graphnest/internal/authn"
	"github.com/balcsida/graphnest/internal/graphartifact"
	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"github.com/balcsida/graphnest/internal/graphprotocol"
	"github.com/balcsida/graphnest/internal/repository"
	"github.com/balcsida/graphnest/pkg/api"
	"google.golang.org/protobuf/proto"
)

type InspectionSource struct {
	EntityID   string             `json:"entity_id,omitempty"`
	Path       string             `json:"path,omitempty"`
	Status     string             `json:"status"`
	Precision  string             `json:"precision,omitempty"`
	Content    string             `json:"content,omitempty"`
	IndexedSHA string             `json:"indexed_sha,omitempty"`
	BlobSHA    string             `json:"blob_sha,omitempty"`
	StartLine  int                `json:"start_line,omitempty"`
	EndLine    int                `json:"end_line,omitempty"`
	Range      *graphv2.Location  `json:"range,omitempty"`
	Selection  *SourceSelection   `json:"selection,omitempty"`
	FileErrors *graphv2.Extension `json:"file_errors,omitempty"`
}

// SourceSelection is an end-exclusive byte range inside the verbatim Content.
// Range retains the producer's original zero-based UTF-16 coordinates.
type SourceSelection struct {
	StartByte int `json:"start_byte"`
	EndByte   int `json:"end_byte"`
}

func (s *Service) entitySource(ctx context.Context, p authn.Principal, i inspectionScope, generations []graphprotocol.Generation, e graphprotocol.Entity, budget *int) (InspectionSource, error) {
	source := InspectionSource{EntityID: e.ID, Path: e.Fact.GetPath(), Range: e.Fact.Location}
	location := e.Fact.Location
	if source.Path == "" {
		source.Status = "virtual_entity"
		return source, nil
	}
	files, err := i.backend.IndexedFiles(ctx, graphprotocol.FilesRequest{Scope: i.scope, Path: &source.Path, Limit: 1})
	if err != nil {
		return InspectionSource{}, err
	}
	if err := sameInspectionGeneration(generations, files.Generations); err != nil {
		return InspectionSource{}, err
	}
	if len(files.Files) == 0 {
		source.Status = "file_not_indexed"
		return source, nil
	}
	if len(files.Files) != 1 || files.Files[0].RepositoryID != i.selected.GitHubID || files.Files[0].Fact == nil || files.Files[0].Fact.Path != source.Path {
		return InspectionSource{}, ErrGraphNotReady
	}
	source.FileErrors = files.Files[0].Fact.Errors
	if location == nil || location.Start == nil || location.Start.Line == nil {
		source.Status = "missing_range"
		return source, nil
	}
	end := int(location.Start.GetLine()) + 1
	if location.End != nil && location.End.Line != nil {
		end = int(location.End.GetLine()) + 1
	}
	return s.readInspectionSource(ctx, p, i, source, int(location.Start.GetLine())+1, end, budget, location)
}

func (s *Service) readInspectionSource(ctx context.Context, p authn.Principal, i inspectionScope, source InspectionSource, start, end int, budget *int, location *graphv2.Location) (InspectionSource, error) {
	if *budget <= 0 {
		source.Status = "budget_exhausted"
		return source, nil
	}
	if s.Files == nil {
		source.Status = "source_unavailable"
		return source, nil
	}
	file, err := s.Files.ReadFileAt(ctx, p, api.ReadFileRequest{RepositoryID: i.selected.GitHubID, Path: source.Path, StartLine: start, EndLine: end}, i.selected.Commit)
	if err != nil {
		if ctx.Err() != nil {
			return InspectionSource{}, ctx.Err()
		}
		if errors.Is(err, repository.ErrNotIndexed) {
			return InspectionSource{}, ErrGraphNotReady
		}
		source.Status = "unreadable"
		switch {
		case errors.Is(err, repository.ErrFileTooLarge):
			source.Status = "oversized"
		case errors.Is(err, repository.ErrInvalidRange), errors.Is(err, repository.ErrLineOutOfRange):
			source.Status = "invalid_range"
		case errors.Is(err, repository.ErrBinaryFile):
			source.Status = "binary"
		}
		return source, nil
	}
	if start == 0 {
		start = 1
	}
	if file.IndexedSHA != i.selected.Commit || file.RepositoryID != i.selected.GitHubID || file.Path != source.Path || file.StartLine != start || file.EndLine < start || end != 0 && file.EndLine > end {
		return InspectionSource{}, ErrGraphNotReady
	}
	source.IndexedSHA = file.IndexedSHA
	source.BlobSHA = file.BlobSHA
	source.StartLine = file.StartLine
	source.EndLine = file.EndLine
	source.Precision = "lines"
	source.Status = "ok"
	content := file.Content
	if file.Truncated {
		source.Status = "truncated"
	} else if location != nil {
		if location.Start != nil && location.End != nil && location.Start.Line != nil && location.Start.Character != nil && location.End.Line != nil && location.End.Character != nil {
			a, b := proto.Clone(location.Start).(*graphv2.Position), proto.Clone(location.End).(*graphv2.Position)
			a.Line = proto.Int32(a.GetLine() - int32(start-1))
			b.Line = proto.Int32(b.GetLine() - int32(start-1))
			from, e1 := graphartifact.SourceOffset(content, a)
			to, e2 := graphartifact.SourceOffset(content, b)
			if e1 != nil || e2 != nil || to < from {
				source.Status = "invalid_range"
				return source, nil
			}
			source.Selection = &SourceSelection{StartByte: from, EndByte: to}
			source.Precision = "utf16"
		} else {
			source.Status = "partial_range"
		}
	}
	if len(content) > *budget {
		source.Status = "budget_exhausted"
		source.Selection = nil
		return source, nil
	}
	source.Content = content
	*budget -= len(content)
	return source, nil
}
