package graphscan

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/grepnest/grepnest/internal/graphartifact"
)

const (
	analyzerName    = "grepnest-scanner"
	analyzerVersion = "1"
)

type symbol struct {
	uid, localID, path, module, name, qualifiedName, scip string
	language                                              Language
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
	byFile := map[string][]symbol{}
	byModule := map[string][]symbol{}
	filesByTarget := map[string][]string{}
	goInterfaces := map[string]map[string]map[string]string{}
	goEmbeds := map[string]map[string][]string{}
	goMethods := map[string]map[string]map[string]string{}
	goTypes := map[string]map[string]symbol{}
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
		if file.Language == Go {
			scope := goPackageScope(file)
			for _, heritage := range file.Heritage {
				if heritage.Kind != graphartifact.EdgeExtends {
					continue
				}
				if goEmbeds[scope] == nil {
					goEmbeds[scope] = map[string][]string{}
				}
				goEmbeds[scope][heritage.ChildLocalID] = append(goEmbeds[scope][heritage.ChildLocalID], heritage.Candidates...)
			}
		}
		if !seenNodes[fileUID] {
			seenNodes[fileUID] = true
			artifact.Nodes = append(artifact.Nodes, graphartifact.Node{UID: fileUID, Kind: graphartifact.NodeFile, Path: file.Path})
			addEdge(graphartifact.Edge{SourceUID: repositoryUID, TargetUID: fileUID, Kind: graphartifact.EdgeContains, Confidence: 1})
		}
		filesByTarget[file.Path] = append(filesByTarget[file.Path], fileUID)
		filesByTarget[strings.TrimSuffix(file.Path, filepath.Ext(file.Path))] = append(filesByTarget[strings.TrimSuffix(file.Path, filepath.Ext(file.Path))], fileUID)
		if file.Module != "" {
			filesByTarget[file.Module] = append(filesByTarget[file.Module], fileUID)
		}
		declarations := append([]Declaration(nil), file.Declarations...)
		sort.SliceStable(declarations, func(i, j int) bool {
			return declarationLess(declarations[i], declarations[j], file.Path)
		})
		for _, declaration := range declarations {
			path := declaration.Path
			if path == "" {
				path = file.Path
			}
			if file.Language == Go {
				scope := goPackageScope(file)
				if declaration.TypeName != "" {
					if goInterfaces[scope] == nil {
						goInterfaces[scope] = map[string]map[string]string{}
					}
					if goInterfaces[scope][declaration.TypeName] == nil {
						goInterfaces[scope][declaration.TypeName] = map[string]string{}
					}
					goInterfaces[scope][declaration.TypeName][declaration.Name] = declaration.Signature
					continue
				}
				if declaration.Kind == "Interface" {
					if goInterfaces[scope] == nil {
						goInterfaces[scope] = map[string]map[string]string{}
					}
					if goInterfaces[scope][declaration.Name] == nil {
						goInterfaces[scope][declaration.Name] = map[string]string{}
					}
				}
				if declaration.Receiver != "" && !declaration.PointerReceiver {
					if goMethods[scope] == nil {
						goMethods[scope] = map[string]map[string]string{}
					}
					if goMethods[scope][declaration.Receiver] == nil {
						goMethods[scope][declaration.Receiver] = map[string]string{}
					}
					goMethods[scope][declaration.Receiver][declaration.Name] = declaration.Signature
				}
			}
			uid := declaration.SCIPSymbol
			if uid == "" {
				uid = CanonicalUID(file.Language, path, declaration.Kind, declaration.QualifiedName, declaration.Signature)
			}
			if uid == "" {
				return graphartifact.Artifact{}, graphartifact.ErrInvalidArtifact
			}
			s := symbol{uid: uid, localID: declaration.LocalID, path: path, module: file.Module, name: declaration.Name, qualifiedName: declaration.QualifiedName, scip: declaration.SCIPSymbol, language: file.Language}
			byLocal[scoped(file.Language, path, s.localID)] = append(byLocal[scoped(file.Language, path, s.localID)], s)
			if s.scip != "" {
				bySCIP[s.scip] = append(bySCIP[s.scip], s)
			}
			byQualified[scoped(file.Language, "", s.qualifiedName)] = append(byQualified[scoped(file.Language, "", s.qualifiedName)], s)
			byFile[path] = append(byFile[path], s)
			if file.Module != "" {
				byModule[scoped(file.Language, "", file.Module)] = append(byModule[scoped(file.Language, "", file.Module)], s)
			}
			if file.Language == Go && (declaration.Kind == "Type" || declaration.Kind == "Interface") {
				scope := goPackageScope(file)
				if goTypes[scope] == nil {
					goTypes[scope] = map[string]symbol{}
				}
				goTypes[scope][declaration.Name] = s
			}
			filesByTarget[s.qualifiedName] = append(filesByTarget[s.qualifiedName], fileUID)
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
			if target, ok := importedFile(imported.Target, filesByTarget); ok {
				addEdge(resolvedEdge(fileUID, target, graphartifact.EdgeImports, importPath(imported, file.Path), imported.Range, "import-target", 1))
			}
		}
		for _, reference := range file.References {
			path := referencePath(reference, file.Path)
			from, ok := unique(byLocal[scoped(file.Language, path, reference.FromLocalID)])
			if !ok {
				continue
			}
			if target, ok := resolve(file, reference.Candidates, bySCIP, byQualified, byFile, byModule, filesByTarget); ok {
				kind := graphartifact.EdgeReferences
				if reference.Call {
					kind = graphartifact.EdgeCalls
				}
				addEdge(resolvedEdge(from.uid, target.uid, kind, referencePath(reference, file.Path), reference.Range, "candidate", .9))
			}
		}
		for _, heritage := range file.Heritage {
			path := heritagePath(heritage, file.Path)
			from, ok := unique(byLocal[scoped(file.Language, path, heritage.ChildLocalID)])
			if !ok || (heritage.Kind != graphartifact.EdgeExtends && heritage.Kind != graphartifact.EdgeImplements) {
				continue
			}
			if target, ok := resolve(file, heritage.Candidates, bySCIP, byQualified, byFile, byModule, filesByTarget); ok {
				addEdge(resolvedEdge(from.uid, target.uid, heritage.Kind, heritagePath(heritage, file.Path), heritage.Range, "candidate", .9))
			}
		}
	}

	for scope, interfaces := range goInterfaces {
		for interfaceName := range interfaces {
			target, ok := goTypes[scope][interfaceName]
			if !ok {
				continue
			}
			required := interfaceMethodSet(interfaceName, interfaces, goEmbeds[scope], map[string]bool{})
			for receiver, methods := range goMethods[scope] {
				source, ok := goTypes[scope][receiver]
				if ok && containsMethodSet(methods, required) {
					addEdge(resolvedEdge(source.uid, target.uid, graphartifact.EdgeImplements, source.path, Range{}, "go-method-set", .9))
				}
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

func interfaceMethodSet(name string, interfaces map[string]map[string]string, embeds map[string][]string, seen map[string]bool) map[string]string {
	if seen[name] {
		return nil
	}
	seen[name] = true
	methods := map[string]string{}
	for method, signature := range interfaces[name] {
		methods[method] = signature
	}
	for _, parent := range embeds[name] {
		for method, signature := range interfaceMethodSet(parent, interfaces, embeds, seen) {
			methods[method] = signature
		}
	}
	return methods
}

func goPackageScope(file File) string {
	return filepath.Dir(file.Path) + "\x00" + file.Module
}

func containsMethodSet(methods, required map[string]string) bool {
	if len(required) == 0 {
		return false
	}
	for name, signature := range required {
		if methods[name] != signature {
			return false
		}
	}
	return true
}

func resolve(file File, candidates []string, bySCIP, byQualified map[string][]symbol, byFile, byModule map[string][]symbol, filesByTarget map[string][]string) (symbol, bool) {
	for _, candidate := range candidates {
		if match, decided := uniqueMatch(bySCIP[candidate]); decided {
			return match, match.uid != ""
		}
		if match, decided := importedSymbol(file, candidate, byQualified, byFile, filesByTarget); decided {
			return match, match.uid != ""
		}
		local := append([]symbol(nil), byFile[file.Path]...)
		if file.Module != "" {
			local = append(local, byModule[scoped(file.Language, "", file.Module)]...)
		}
		if match, decided := uniqueMatch(matching(local, candidate)); decided {
			return match, match.uid != ""
		}
	}
	return symbol{}, false
}

func importedSymbol(file File, candidate string, byQualified map[string][]symbol, byFile map[string][]symbol, filesByTarget map[string][]string) (symbol, bool) {
	for _, imported := range file.Imports {
		separator := "."
		if file.Language == Rust {
			separator = "::"
		}
		if candidate == imported.Target || strings.HasPrefix(candidate, imported.Target+separator) {
			if match, decided := uniqueMatch(byQualified[scoped(file.Language, "", candidate)]); decided {
				return match, decided
			}
		}
		remainder, ok := importedRemainder(candidate, imported.Alias)
		if !ok {
			continue
		}
		translated := imported.Target
		if remainder != "" {
			translated += separator + remainder
		}
		if match, decided := uniqueMatch(byQualified[scoped(file.Language, "", translated)]); decided {
			return match, decided
		}
		var matches []symbol
		for _, fileUID := range importFileUIDs(imported.Target, filesByTarget) {
			path := strings.TrimPrefix(fileUID, "file:")
			matches = append(matches, matching(byFile[path], remainder)...)
		}
		if match, decided := uniqueMatch(matches); decided {
			return match, decided
		}
	}
	return symbol{}, false
}

func importedRemainder(candidate, alias string) (string, bool) {
	if alias == "*" {
		return candidate, bare(candidate)
	}
	if candidate == alias {
		return "", true
	}
	for _, separator := range []string{".", "::"} {
		if strings.HasPrefix(candidate, alias+separator) {
			return strings.TrimPrefix(candidate, alias+separator), true
		}
	}
	return "", false
}

func matching(symbols []symbol, candidate string) []symbol {
	var exact []symbol
	for _, value := range symbols {
		if value.localID == candidate || value.qualifiedName == candidate {
			exact = append(exact, value)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	var matches []symbol
	last := candidate
	if index := strings.LastIndex(candidate, "::"); index >= 0 {
		last = candidate[index+2:]
	} else if index := strings.LastIndexByte(candidate, '.'); index >= 0 {
		last = candidate[index+1:]
	}
	for _, value := range symbols {
		if value.name == last ||
			strings.HasSuffix(value.qualifiedName, "."+candidate) || strings.HasSuffix(value.qualifiedName, "::"+candidate) {
			matches = append(matches, value)
		}
	}
	return matches
}

func uniqueMatch(symbols []symbol) (symbol, bool) {
	if len(symbols) == 0 {
		return symbol{}, false
	}
	value, ok := unique(symbols)
	if !ok {
		return symbol{}, true
	}
	return value, true
}

func scoped(language Language, path, value string) string {
	return string(language) + "\x00" + path + "\x00" + value
}

func bare(value string) bool {
	return !strings.ContainsAny(value, "./") && !strings.Contains(value, "::")
}

func importedFile(target string, filesByTarget map[string][]string) (string, bool) {
	return uniqueUID(importFileUIDs(target, filesByTarget))
}

func importFileUIDs(target string, filesByTarget map[string][]string) []string {
	candidates := []string{target, strings.TrimPrefix(target, "./")}
	normalized := strings.TrimSuffix(strings.TrimPrefix(target, "./"), filepath.Ext(target))
	candidates = append(candidates, normalized)
	for _, separator := range []string{"/", "::", "."} {
		if index := strings.LastIndex(normalized, separator); index >= 0 {
			candidates = append(candidates, normalized[index+len(separator):])
		}
	}
	var values []string
	seen := map[string]bool{}
	for _, candidate := range candidates {
		for _, value := range filesByTarget[candidate] {
			if !seen[value] {
				seen[value] = true
				values = append(values, value)
			}
		}
		if len(values) > 0 {
			break
		}
	}
	return values
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
func declarationLess(left, right Declaration, fallback string) bool {
	leftPath, rightPath := left.Path, right.Path
	if leftPath == "" {
		leftPath = fallback
	}
	if rightPath == "" {
		rightPath = fallback
	}
	leftFields := [...]string{left.SCIPSymbol, leftPath, left.Name, left.QualifiedName, left.Signature, left.Kind, left.ScopeID, left.Receiver, left.TypeName, fmt.Sprint(left.PointerReceiver), left.LocalID}
	rightFields := [...]string{right.SCIPSymbol, rightPath, right.Name, right.QualifiedName, right.Signature, right.Kind, right.ScopeID, right.Receiver, right.TypeName, fmt.Sprint(right.PointerReceiver), right.LocalID}
	for i := range leftFields {
		if leftFields[i] != rightFields[i] {
			return leftFields[i] < rightFields[i]
		}
	}
	return pointLess(left.Range.Start, right.Range.Start) || left.Range.Start == right.Range.Start && pointLess(left.Range.End, right.Range.End)
}

func pointLess(left, right Point) bool {
	return left.Line < right.Line || left.Line == right.Line && left.Column < right.Column
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
