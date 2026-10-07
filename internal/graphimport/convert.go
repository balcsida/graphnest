package graphimport

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"

	"github.com/balcsida/graphnest/internal/graphartifact"
	graphv2 "github.com/balcsida/graphnest/internal/graphartifact/v2"
	"google.golang.org/protobuf/proto"
)

// Options configures Convert.
type Options struct {
	Repository       string            // artifact repository identity
	Commit           string            // 40 lowercase hex characters
	Producer         *graphv2.Producer // artifact producer; nil means "codegraph" at project_metadata indexed_with_version
	IdentityProducer *graphv2.Producer // producer for edge/unresolved occurrence identities; defaults to Producer
	IdentityScope    string            // repository argument of graphartifact.IdentityV2; defaults to Repository
	ImportedAt       *int64            // artifact import time; nil leaves it unset
}

// Report counts what Convert produced.
type Report struct {
	Nodes, Edges, Files, Unresolved, Metadata int
	NodeKinds, EdgeKinds                      map[string]int
}

// Convert maps a CodeGraph snapshot to a v2 artifact without altering any fact.
// The artifact content hash is left empty.
func Convert(s *Snapshot, options Options) (*graphv2.Artifact, *Report, error) {
	producer := options.Producer
	if producer == nil {
		i := slices.IndexFunc(s.Metadata, func(m MetadataEntry) bool { return m.Key == "indexed_with_version" })
		if i < 0 {
			return nil, nil, errors.New("project_metadata has no indexed_with_version; set Options.Producer")
		}
		producer = &graphv2.Producer{Name: "codegraph", Version: s.Metadata[i].Value}
	}
	identityProducer := options.IdentityProducer
	if identityProducer == nil {
		identityProducer = producer
	}
	identityScope := options.IdentityScope
	if identityScope == "" {
		identityScope = options.Repository
	}
	a := &graphv2.Artifact{SchemaVersion: 2, Repository: options.Repository, Commit: options.Commit, Producer: producer}
	if options.ImportedAt != nil {
		a.ImportedAt = *options.ImportedAt
	}
	report := &Report{NodeKinds: map[string]int{}, EdgeKinds: map[string]int{}}

	for _, r := range s.Nodes {
		n := &graphv2.Node{SourceId: r.ID, Occurrence: r.ID, Kind: r.Kind, Name: r.Name, QualifiedName: r.QualifiedName, Path: proto.String(r.FilePath), Language: r.Language, Documentation: r.Docstring, Signature: r.Signature, Visibility: r.Visibility, IsExported: flag(r.IsExported), IsAsync: flag(r.IsAsync), IsStatic: flag(r.IsStatic), IsAbstract: flag(r.IsAbstract), ReturnType: r.ReturnType, UpdatedAt: proto.Int64(r.UpdatedAt)}
		var err error
		if n.Decorators, err = stringList(r.Decorators); err != nil {
			return nil, nil, fmt.Errorf("node %s decorators: %w", r.ID, err)
		}
		if n.TypeParameters, err = stringList(r.TypeParameters); err != nil {
			return nil, nil, fmt.Errorf("node %s type_parameters: %w", r.ID, err)
		}
		start, err := point(&r.StartLine, &r.StartColumn)
		if err != nil {
			return nil, nil, fmt.Errorf("node %s: %w", r.ID, err)
		}
		end, err := point(&r.EndLine, &r.EndColumn)
		if err != nil {
			return nil, nil, fmt.Errorf("node %s: %w", r.ID, err)
		}
		n.Location = &graphv2.Location{Start: start, End: end}
		a.Nodes = append(a.Nodes, n)
		report.NodeKinds[r.Kind]++
	}

	var unknown []string
	for _, r := range s.Edges {
		if _, ok := graphartifact.ParseRelationship(r.Kind); !ok && !slices.Contains(unknown, r.Kind) {
			unknown = append(unknown, r.Kind)
		}
	}
	if len(unknown) > 0 {
		slices.Sort(unknown)
		return nil, nil, fmt.Errorf("unknown CodeGraph edge kinds: %q", unknown)
	}
	ordinals := map[string]int{}
	for _, r := range s.Edges {
		kind, _ := graphartifact.ParseRelationship(r.Kind)
		location, err := location(r.Line, r.Col)
		if err != nil {
			return nil, nil, fmt.Errorf("edge %d: %w", r.ID, err)
		}
		e := &graphv2.Edge{SourceId: strconv.FormatInt(r.ID, 10), Source: r.Source, Target: r.Target, Kind: kind.WireKind(), Location: location, Provenance: r.Provenance}
		if r.Metadata != nil {
			e.Extensions = []*graphv2.Extension{{Namespace: "codegraph.edge-metadata", Json: []byte(*r.Metadata)}}
			var metadata struct {
				Confidence *float64
				ResolvedBy *string
			}
			if err = json.Unmarshal(e.Extensions[0].Json, &metadata); err != nil {
				return nil, nil, fmt.Errorf("edge %d metadata: %w", r.ID, err)
			}
			e.Confidence = metadata.Confidence
			e.ResolutionReason = metadata.ResolvedBy
		}
		if e.Occurrence, err = occurrence(identityProducer, identityScope, "edge", e, ordinals); err != nil {
			return nil, nil, fmt.Errorf("edge %d: %w", r.ID, err)
		}
		a.Edges = append(a.Edges, e)
		report.EdgeKinds[r.Kind]++
	}

	for _, r := range s.Files {
		f := &graphv2.File{Path: r.Path, ContentHash: r.ContentHash, Language: r.Language, Size: r.Size, ModifiedAt: proto.Int64(r.ModifiedAt), IndexedAt: proto.Int64(r.IndexedAt), NodeCount: r.NodeCount, Generated: proto.Bool(r.Generated != 0)}
		if r.Errors != nil {
			f.Errors = &graphv2.Extension{Namespace: "codegraph.extraction-errors", Json: []byte(*r.Errors)}
		}
		a.Files = append(a.Files, f)
	}

	ordinals = map[string]int{}
	for _, r := range s.Unresolved {
		location, err := location(&r.Line, &r.Col)
		if err != nil {
			return nil, nil, fmt.Errorf("unresolved ref %d: %w", r.ID, err)
		}
		u := &graphv2.UnresolvedReference{SourceId: strconv.FormatInt(r.ID, 10), Source: r.FromNodeID, Name: r.ReferenceName, Kind: r.ReferenceKind, Location: location, Path: proto.String(r.FilePath), Language: proto.String(r.Language), Status: proto.String(r.Status), NameTail: proto.String(r.NameTail)}
		if u.Candidates, err = stringList(r.Candidates); err != nil {
			return nil, nil, fmt.Errorf("unresolved ref %d candidates: %w", r.ID, err)
		}
		if u.Occurrence, err = occurrence(identityProducer, identityScope, "ref", u, ordinals); err != nil {
			return nil, nil, fmt.Errorf("unresolved ref %d: %w", r.ID, err)
		}
		a.Unresolved = append(a.Unresolved, u)
	}

	for _, r := range s.Metadata {
		a.Metadata = append(a.Metadata, &graphv2.MetadataEntry{Key: r.Key, Value: r.Value, UpdatedAt: proto.Int64(r.UpdatedAt)})
	}
	report.Nodes, report.Edges, report.Files, report.Unresolved, report.Metadata = len(a.Nodes), len(a.Edges), len(a.Files), len(a.Unresolved), len(a.Metadata)
	return a, report, nil
}

// occurrence derives a content-derived identity from every field except the
// source row ID, plus an ordinal separating identical repeated evidence.
func occurrence[M interface {
	proto.Message
	GetSourceId() string
}](producer *graphv2.Producer, scope, prefix string, m M, ordinals map[string]int) (string, error) {
	clone := proto.Clone(m)
	clone.ProtoReflect().Clear(clone.ProtoReflect().Descriptor().Fields().ByName("source_id"))
	key, err := proto.MarshalOptions{Deterministic: true}.Marshal(clone)
	if err != nil {
		return "", err
	}
	id, err := graphartifact.IdentityV2(producer, scope, prefix, string(key))
	if err != nil {
		return "", err
	}
	ordinal := ordinals[id]
	ordinals[id]++
	return id + ":" + strconv.Itoa(ordinal), nil
}

func flag(v *int64) *bool {
	if v == nil {
		return nil
	}
	return proto.Bool(*v != 0)
}

func stringList(v *string) (*graphv2.StringList, error) {
	if v == nil {
		return nil, nil
	}
	var values []string
	if err := json.Unmarshal([]byte(*v), &values); err != nil {
		return nil, err
	}
	return &graphv2.StringList{Values: values}, nil
}

// point converts a one-based CodeGraph line and column to a zero-based line position.
func point(line, col *int64) (*graphv2.Position, error) {
	if line == nil && col == nil {
		return nil, nil
	}
	p := &graphv2.Position{}
	if line != nil {
		if *line < math.MinInt32+1 || *line > math.MaxInt32 {
			return nil, fmt.Errorf("line %d out of range", *line)
		}
		p.Line = proto.Int32(int32(*line) - 1)
	}
	if col != nil {
		if *col < math.MinInt32 || *col > math.MaxInt32 {
			return nil, fmt.Errorf("column %d out of range", *col)
		}
		p.Character = proto.Int32(int32(*col))
	}
	return p, nil
}

func location(line, col *int64) (*graphv2.Location, error) {
	p, err := point(line, col)
	if p == nil || err != nil {
		return nil, err
	}
	return &graphv2.Location{Start: p}, nil
}
