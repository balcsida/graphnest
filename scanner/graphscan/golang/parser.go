package golang

import (
	"context"
	_ "embed"
	"fmt"
	"strings"

	"github.com/balcsida/graphnest/internal/graphartifact"
	"github.com/balcsida/graphnest/scanner/graphscan"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_go "github.com/tree-sitter/tree-sitter-go/bindings/go"
)

//go:embed queries.scm
var querySource string

func Parse(ctx context.Context, path string, source []byte) (graphscan.File, error) {
	language := tree_sitter.NewLanguage(tree_sitter_go.Language())
	query, queryErr := tree_sitter.NewQuery(language, querySource)
	if queryErr != nil {
		return graphscan.File{}, queryErr
	}
	defer query.Close()
	tree, err := graphscan.ParseTree(ctx, language, source)
	if err != nil {
		return graphscan.File{}, err
	}
	defer tree.Close()
	cursor := tree_sitter.NewQueryCursor()
	defer cursor.Close()
	matches := cursor.Matches(query, tree.RootNode(), source)
	if matches.Next() == nil {
		return graphscan.File{}, fmt.Errorf("parse Go: query did not match")
	}

	file := graphscan.File{Path: path, Language: graphscan.Go}
	var walk func(*tree_sitter.Node, string)
	walk = func(node *tree_sitter.Node, scope string) {
		if node == nil || graphscan.BudgetError(ctx) != nil {
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
			graphscan.Add(ctx, &file.Imports, graphscan.Import{Path: path, Target: target, Alias: alias, Range: nodeRange(node)})
		case "type_spec":
			nameNode := node.ChildByFieldName("name")
			name := text(nameNode, source)
			typeNode := node.ChildByFieldName("type")
			kind := "Type"
			if typeNode != nil && typeNode.Kind() == "interface_type" {
				kind = "Interface"
				required, embedded := interfaceEvidence(typeNode, source)
				for method, signature := range required {
					graphscan.Add(ctx, &file.Declarations, graphscan.Declaration{
						Path: path, LocalID: name + "." + method, Name: method, QualifiedName: name + "." + method,
						Signature: signature, Kind: "Method", TypeName: name, Range: nodeRange(typeNode),
					})
				}
				for _, parent := range embedded {
					graphscan.Add(ctx, &file.Heritage, graphscan.Heritage{Path: path, ChildLocalID: name, Candidates: []string{parent}, Kind: graphartifact.EdgeExtends, Range: nodeRange(typeNode)})
				}
			}
			graphscan.Add(ctx, &file.Declarations, declaration(path, name, name, kind, nameNode))
		case "function_declaration":
			nameNode := node.ChildByFieldName("name")
			name := text(nameNode, source)
			qualified := name
			if file.Module != "" {
				qualified = file.Module + "." + name
			}
			nextScope = qualified
			graphscan.Add(ctx, &file.Declarations, declaration(path, qualified, name, "Function", nameNode))
		case "method_declaration":
			nameNode := node.ChildByFieldName("name")
			name := text(nameNode, source)
			receiver := receiverName(node.ChildByFieldName("receiver"), source)
			qualified := receiver + "." + name
			nextScope = qualified
			value := declaration(path, qualified, name, "Method", nameNode)
			value.Receiver = receiver
			value.Signature = methodSignature(node, source)
			value.PointerReceiver = firstKind(node.ChildByFieldName("receiver"), "pointer_type") != nil
			graphscan.Add(ctx, &file.Declarations, value)
		case "call_expression":
			function := node.ChildByFieldName("function")
			name, candidates := callCandidates(function, source)
			if name != "" {
				graphscan.Add(ctx, &file.References, graphscan.Reference{Path: path, FromLocalID: scope, Name: name, Candidates: candidates, Range: nodeRange(function), Call: true})
			}
		}
		for i := uint(0); i < node.NamedChildCount(); i++ {
			walk(node.NamedChild(i), nextScope)
		}
	}
	walk(tree.RootNode(), "")
	if err := graphscan.BudgetError(ctx); err != nil {
		return graphscan.File{}, err
	}
	return file, nil
}

func declaration(path, id, name, kind string, node *tree_sitter.Node) graphscan.Declaration {
	return graphscan.Declaration{Path: path, LocalID: id, Name: name, QualifiedName: id, Kind: kind, Range: nodeRange(node)}
}

func interfaceEvidence(node *tree_sitter.Node, source []byte) (required map[string]string, embedded []string) {
	required = map[string]string{}
	for i := uint(0); i < node.NamedChildCount(); i++ {
		child := node.NamedChild(i)
		if child.Kind() == "method_elem" {
			required[text(child.ChildByFieldName("name"), source)] = methodSignature(child, source)
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
		return name, []string{receiver + "." + name, name}
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

func methodSignature(node *tree_sitter.Node, source []byte) string {
	return parameterTypes(node.ChildByFieldName("parameters"), source) + resultType(node.ChildByFieldName("result"), source)
}

func parameterTypes(node *tree_sitter.Node, source []byte) string {
	if node == nil {
		return "()"
	}
	var values []string
	for i := uint(0); i < node.NamedChildCount(); i++ {
		parameter := node.NamedChild(i)
		value := parameter.ChildByFieldName("type")
		if value == nil {
			value = parameter
		}
		kind := text(value, source)
		if strings.Contains(parameter.Kind(), "variadic") {
			kind = "..." + kind
		}
		names := 0
		for child := uint32(0); child < uint32(parameter.NamedChildCount()); child++ {
			if parameter.FieldNameForNamedChild(child) == "name" {
				names++
			}
		}
		for range max(1, names) {
			values = append(values, kind)
		}
	}
	return "(" + strings.Join(values, ",") + ")"
}

func resultType(node *tree_sitter.Node, source []byte) string {
	if node == nil {
		return ""
	}
	if node.Kind() == "parameter_list" {
		return parameterTypes(node, source)
	}
	return text(node, source)
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
