package graphscan

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"

	"github.com/grepnest/grepnest/internal/graphartifact"
)

const (
	analyzerName    = "grepnest-scanner"
	analyzerVersion = "1"
)

type symbol struct {
	uid, localID, path, qualifiedName, scip string
}

func CanonicalUID(language Language, path, kind, qualifiedName, signature string) string {
	uid, err := graphartifact.Identity(graphartifact.Node{Language: string(language), Path: path, Kind: graphartifact.NodeSymbol, SymbolKind: kind, QualifiedName: qualifiedName, Signature: signature})
	if err != nil {
		return ""
	}
	return uid
}

func Resolve(repositoryID int64, commit string, files []File) (graphartifact.Artifact, error) {
	ordered := append([]File(nil), files...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })
	artifact := graphartifact.Artifact{SchemaVersion: 1, Analyzer: graphartifact.Analyzer{Name: analyzerName, Version: analyzerVersion}, RepositoryID: repositoryID, Commit: commit}
	repositoryUID := fmt.Sprintf("repository:%d", repositoryID)
	artifact.Nodes = append(artifact.Nodes, graphartifact.Node{UID: repositoryUID, Kind: graphartifact.NodeRepository})

	byLocal := map[string][]symbol{}
	bySCIP := map[string][]symbol{}
	byQualified := map[string][]symbol{}
	filesByTarget := map[string][]string{}
	seenNodes := map[string]bool{repositoryUID: true}
	seenEdges := map[string]bool{}
	addEdge := func(edge graphartifact.Edge) {
		key := fmt.Sprintf("%s\x00%s\x00%d\x00%s\x00%d\x00%d\x00%d\x00%d", edge.SourceUID, edge.TargetUID, edge.Kind, edge.Path, edge.Range.StartLine, edge.Range.StartCharacter, edge.Range.EndLine, edge.Range.EndCharacter)
		if !seenEdges[key] {
			seenEdges[key] = true
			artifact.Edges = append(artifact.Edges, edge)
		}
	}

	for _, file := range ordered {
		fileUID := "file:" + file.Path
		if !seenNodes[fileUID] {
			seenNodes[fileUID] = true
			artifact.Nodes = append(artifact.Nodes, graphartifact.Node{UID: fileUID, Kind: graphartifact.NodeFile, Path: file.Path})
			addEdge(graphartifact.Edge{SourceUID: repositoryUID, TargetUID: fileUID, Kind: graphartifact.EdgeContains, Confidence: 1})
		}
		filesByTarget[file.Path] = append(filesByTarget[file.Path], fileUID)
		if file.Module != "" {
			filesByTarget[file.Module] = append(filesByTarget[file.Module], fileUID)
		}
		declarations := append([]Declaration(nil), file.Declarations...)
		sort.SliceStable(declarations, func(i, j int) bool {
			return declarationKey(declarations[i], file.Path) < declarationKey(declarations[j], file.Path)
		})
		for _, declaration := range declarations {
			path := declaration.Path
			if path == "" {
				path = file.Path
			}
			uid := declaration.SCIPSymbol
			if uid == "" {
				uid = CanonicalUID(file.Language, path, declaration.Kind, declaration.QualifiedName, declaration.Signature)
			}
			if uid == "" {
				return graphartifact.Artifact{}, graphartifact.ErrInvalidArtifact
			}
			s := symbol{uid: uid, localID: declaration.LocalID, path: path, qualifiedName: declaration.QualifiedName, scip: declaration.SCIPSymbol}
			byLocal[s.localID] = append(byLocal[s.localID], s)
			if s.scip != "" {
				bySCIP[s.scip] = append(bySCIP[s.scip], s)
			}
			byQualified[s.qualifiedName] = append(byQualified[s.qualifiedName], s)
			if seenNodes[uid] {
				continue
			}
			seenNodes[uid] = true
			artifact.Nodes = append(artifact.Nodes, graphartifact.Node{UID: uid, Kind: graphartifact.NodeSymbol, Path: path, Language: string(file.Language), SymbolKind: declaration.Kind, QualifiedName: declaration.QualifiedName, Signature: declaration.Signature, SCIPSymbol: declaration.SCIPSymbol, Range: graphRange(declaration.Range)})
			addEdge(graphartifact.Edge{SourceUID: fileUID, TargetUID: uid, Kind: graphartifact.EdgeContains, Confidence: 1})
		}
	}

	for _, file := range ordered {
		fileUID := "file:" + file.Path
		for _, imported := range file.Imports {
			if target, ok := uniqueUID(filesByTarget[imported.Target]); ok {
				addEdge(resolvedEdge(fileUID, target, graphartifact.EdgeImports, importPath(imported, file.Path), imported.Range, "import-target", 1))
			}
		}
		for _, reference := range file.References {
			from, ok := unique(byLocal[reference.FromLocalID])
			if !ok {
				continue
			}
			if target, ok := resolve(reference.Candidates, bySCIP, byQualified, byLocal); ok {
				kind := graphartifact.EdgeReferences
				if reference.Call {
					kind = graphartifact.EdgeCalls
				}
				addEdge(resolvedEdge(from.uid, target.uid, kind, referencePath(reference, file.Path), reference.Range, "candidate", .9))
			}
		}
		for _, heritage := range file.Heritage {
			from, ok := unique(byLocal[heritage.ChildLocalID])
			if !ok || (heritage.Kind != graphartifact.EdgeExtends && heritage.Kind != graphartifact.EdgeImplements) {
				continue
			}
			if target, ok := resolve(heritage.Candidates, bySCIP, byQualified, byLocal); ok {
				addEdge(resolvedEdge(from.uid, target.uid, heritage.Kind, heritagePath(heritage, file.Path), heritage.Range, "candidate", .9))
			}
		}
	}

	sort.Slice(artifact.Nodes, func(i, j int) bool { return artifact.Nodes[i].UID < artifact.Nodes[j].UID })
	sort.Slice(artifact.Edges, func(i, j int) bool { return edgeKey(artifact.Edges[i]) < edgeKey(artifact.Edges[j]) })
	artifact.ContentHash = contentHash(artifact)
	if err := graphartifact.Validate(artifact, graphartifact.Limits{}); err != nil {
		return graphartifact.Artifact{}, err
	}
	return artifact, nil
}

func resolve(candidates []string, bySCIP, byQualified, byLocal map[string][]symbol) (symbol, bool) {
	for _, index := range []map[string][]symbol{bySCIP, byQualified, byLocal} {
		for _, candidate := range candidates {
			if symbol, ok := unique(index[candidate]); ok {
				return symbol, true
			}
			if len(index[candidate]) > 1 {
				return symbol{}, false
			}
		}
	}
	return symbol{}, false
}

func unique(symbols []symbol) (symbol, bool) {
	if len(symbols) == 0 {
		return symbol{}, false
	}
	first := symbols[0]
	for _, candidate := range symbols[1:] {
		if candidate.uid != first.uid {
			return symbol{}, false
		}
	}
	return first, true
}

func uniqueUID(values []string) (string, bool) {
	if len(values) == 0 {
		return "", false
	}
	for _, value := range values[1:] {
		if value != values[0] {
			return "", false
		}
	}
	return values[0], true
}

func graphRange(r Range) graphartifact.Range {
	return graphartifact.Range{StartLine: int32(r.Start.Line), StartCharacter: int32(r.Start.Column), EndLine: int32(r.End.Line), EndCharacter: int32(r.End.Column)}
}
func importPath(value Import, fallback string) string {
	if value.Path != "" {
		return value.Path
	}
	return fallback
}
func referencePath(value Reference, fallback string) string {
	if value.Path != "" {
		return value.Path
	}
	return fallback
}
func heritagePath(value Heritage, fallback string) string {
	if value.Path != "" {
		return value.Path
	}
	return fallback
}
func resolvedEdge(source, target string, kind graphartifact.EdgeKind, path string, r Range, reason string, confidence float32) graphartifact.Edge {
	return graphartifact.Edge{SourceUID: source, TargetUID: target, Kind: kind, Path: path, Range: graphRange(r), ResolutionReason: reason, Confidence: confidence}
}
func declarationKey(value Declaration, fallback string) string {
	path := value.Path
	if path == "" {
		path = fallback
	}
	return value.SCIPSymbol + "\x00" + path + "\x00" + value.QualifiedName + "\x00" + value.Signature + "\x00" + value.LocalID
}
func edgeKey(value graphartifact.Edge) string {
	return fmt.Sprintf("%s\x00%s\x00%d\x00%s\x00%d\x00%d\x00%d\x00%d", value.SourceUID, value.TargetUID, value.Kind, value.Path, value.Range.StartLine, value.Range.StartCharacter, value.Range.EndLine, value.Range.EndCharacter)
}

func contentHash(artifact graphartifact.Artifact) []byte {
	hash := sha256.New()
	write := func(values ...string) {
		for _, value := range values {
			var length [8]byte
			binary.BigEndian.PutUint64(length[:], uint64(len(value)))
			_, _ = hash.Write(length[:])
			_, _ = hash.Write([]byte(value))
		}
	}
	write(fmt.Sprint(artifact.SchemaVersion), artifact.Analyzer.Name, artifact.Analyzer.Version, fmt.Sprint(artifact.RepositoryID), artifact.Commit)
	for _, node := range artifact.Nodes {
		write(node.UID, node.Path, node.Language, node.SymbolKind, node.QualifiedName, node.Signature, node.SCIPSymbol, fmt.Sprint(node.Kind), fmt.Sprint(node.Range))
	}
	for _, edge := range artifact.Edges {
		write(edge.SourceUID, edge.TargetUID, edge.Path, edge.ResolutionReason, fmt.Sprint(edge.Kind), fmt.Sprint(edge.Range), fmt.Sprint(edge.Confidence))
	}
	return hash.Sum(nil)
}
