package golang

import (
	"context"
	_ "embed"
	"fmt"
	"strings"
	"sync"

	"github.com/grepnest/grepnest/internal/graphartifact"
	"github.com/grepnest/grepnest/internal/graphscan"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_go "github.com/tree-sitter/tree-sitter-go/bindings/go"
)

//go:embed queries.scm
var querySource string

var (
	queryOnce sync.Once
	query     *tree_sitter.Query
	queryErr  error
)

func Parse(ctx context.Context, path string, source []byte) (graphscan.File, error) {
	language := tree_sitter.NewLanguage(tree_sitter_go.Language())
	queryOnce.Do(func() {
		var err *tree_sitter.QueryError
		query, err = tree_sitter.NewQuery(language, querySource)
		if err != nil {
			queryErr = err
		}
	})
	if queryErr != nil {
		return graphscan.File{}, queryErr
	}
	parser := tree_sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(language); err != nil {
		return graphscan.File{}, err
	}
	tree := parser.ParseCtx(ctx, source, nil)
	if tree == nil {
		return graphscan.File{}, ctx.Err()
	}
	defer tree.Close()
	cursor := tree_sitter.NewQueryCursor()
	defer cursor.Close()
	matches := cursor.Matches(query, tree.RootNode(), source)
	if matches.Next() == nil {
		return graphscan.File{}, fmt.Errorf("parse Go: query did not match")
	}

	file := graphscan.File{Path: path, Language: graphscan.Go}
	interfaces := map[string][]string{}
	methods := map[string]map[string]bool{}
	var walk func(*tree_sitter.Node, string)
	walk = func(node *tree_sitter.Node, scope string) {
		if node == nil {
			return
		}
		nextScope := scope
		switch node.Kind() {
		case "package_clause":
			file.Module = text(node.NamedChild(0), source)
		case "import_spec":
			target := unquote(text(node.ChildByFieldName("path"), source))
			alias := text(node.ChildByFieldName("name"), source)
			if alias == "" {
				parts := strings.Split(target, "/")
				alias = parts[len(parts)-1]
			}
			file.Imports = append(file.Imports, graphscan.Import{Path: path, Target: target, Alias: alias, Range: nodeRange(node)})
		case "type_spec":
			nameNode := node.ChildByFieldName("name")
			name := text(nameNode, source)
			typeNode := node.ChildByFieldName("type")
			kind := "Type"
			if typeNode != nil && typeNode.Kind() == "interface_type" {
				kind = "Interface"
				required, embedded := interfaceEvidence(typeNode, source)
				interfaces[name] = required
				for _, parent := range embedded {
					file.Heritage = append(file.Heritage, graphscan.Heritage{Path: path, ChildLocalID: name, Candidates: []string{parent}, Kind: graphartifact.EdgeExtends, Range: nodeRange(typeNode)})
				}
			}
			file.Declarations = append(file.Declarations, declaration(path, name, name, kind, nameNode))
		case "function_declaration":
			nameNode := node.ChildByFieldName("name")
			name := text(nameNode, source)
			qualified := name
			if file.Module != "" {
				qualified = file.Module + "." + name
			}
			nextScope = qualified
			file.Declarations = append(file.Declarations, declaration(path, qualified, name, "Function", nameNode))
		case "method_declaration":
			nameNode := node.ChildByFieldName("name")
			name := text(nameNode, source)
			receiver := receiverName(node.ChildByFieldName("receiver"), source)
			qualified := receiver + "." + name
			nextScope = qualified
			value := declaration(path, qualified, name, "Method", nameNode)
			value.Receiver = receiver
			file.Declarations = append(file.Declarations, value)
			if methods[receiver] == nil {
				methods[receiver] = map[string]bool{}
			}
			methods[receiver][name] = true
		case "call_expression":
			function := node.ChildByFieldName("function")
			name, candidates := callCandidates(function, source)
			if name != "" {
				file.References = append(file.References, graphscan.Reference{Path: path, FromLocalID: scope, Name: name, Candidates: candidates, Range: nodeRange(function), Call: true})
			}
		}
		for i := uint(0); i < node.NamedChildCount(); i++ {
			walk(node.NamedChild(i), nextScope)
		}
	}
	walk(tree.RootNode(), "")
	for receiver, set := range methods {
		for name, required := range interfaces {
			if containsAll(set, required) {
				file.Heritage = append(file.Heritage, graphscan.Heritage{Path: path, ChildLocalID: receiver, Candidates: []string{name}, Kind: graphartifact.EdgeImplements})
			}
		}
	}
	return file, nil
}

func declaration(path, id, name, kind string, node *tree_sitter.Node) graphscan.Declaration {
	return graphscan.Declaration{Path: path, LocalID: id, Name: name, QualifiedName: id, Kind: kind, Range: nodeRange(node)}
}

func interfaceEvidence(node *tree_sitter.Node, source []byte) (required, embedded []string) {
	for i := uint(0); i < node.NamedChildCount(); i++ {
		child := node.NamedChild(i)
		if name := firstKind(child, "field_identifier"); name != nil {
			required = append(required, text(name, source))
		} else if name := firstKind(child, "identifier"); name != nil {
			required = append(required, text(name, source))
		} else if child.NamedChildCount() == 1 && child.NamedChild(0).Kind() == "type_identifier" {
			embedded = append(embedded, text(child.NamedChild(0), source))
		}
	}
	return required, embedded
}

func receiverName(node *tree_sitter.Node, source []byte) string {
	if node == nil {
		return ""
	}
	value := strings.Trim(text(node, source), "() \t\r\n")
	fields := strings.Fields(value)
	value = fields[len(fields)-1]
	return strings.TrimPrefix(value, "*")
}

func callCandidates(node *tree_sitter.Node, source []byte) (string, []string) {
	if node == nil {
		return "", nil
	}
	if node.Kind() == "selector_expression" {
		name := text(node.ChildByFieldName("field"), source)
		receiver := strings.TrimSuffix(text(node.ChildByFieldName("operand"), source), "{}")
		return name, []string{name, receiver + "." + name}
	}
	name := text(node, source)
	return name, []string{name}
}

func firstKind(node *tree_sitter.Node, kind string) *tree_sitter.Node {
	for i := uint(0); i < node.NamedChildCount(); i++ {
		child := node.NamedChild(i)
		if child.Kind() == kind {
			return child
		}
		if found := firstKind(child, kind); found != nil {
			return found
		}
	}
	return nil
}

func containsAll(values map[string]bool, required []string) bool {
	if len(required) == 0 {
		return false
	}
	for _, value := range required {
		if !values[value] {
			return false
		}
	}
	return true
}

func text(node *tree_sitter.Node, source []byte) string {
	if node == nil {
		return ""
	}
	return string(source[node.StartByte():node.EndByte()])
}

func unquote(value string) string { return strings.Trim(value, "`\"") }

func nodeRange(node *tree_sitter.Node) graphscan.Range {
	if node == nil {
		return graphscan.Range{}
	}
	start, end := node.StartPosition(), node.EndPosition()
	return graphscan.Range{Start: graphscan.Point{Line: uint32(start.Row), Column: uint32(start.Column)}, End: graphscan.Point{Line: uint32(end.Row), Column: uint32(end.Column)}}
}
