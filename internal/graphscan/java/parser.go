package java

import (
	"context"
	_ "embed"
	"fmt"
	"strings"

	"github.com/grepnest/grepnest/internal/graphartifact"
	"github.com/grepnest/grepnest/internal/graphscan"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_java "github.com/tree-sitter/tree-sitter-java/bindings/go"
)

//go:embed queries.scm
var querySource string

func Parse(ctx context.Context, path string, source []byte) (graphscan.File, error) {
	language := tree_sitter.NewLanguage(tree_sitter_java.Language())
	query, err := tree_sitter.NewQuery(language, querySource)
	if err != nil {
		return graphscan.File{}, err
	}
	defer query.Close()
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
		return graphscan.File{}, fmt.Errorf("parse Java: query did not match")
	}

	file := graphscan.File{Path: path, Language: graphscan.Java}
	imports := map[string]string{}
	var walk func(*tree_sitter.Node, string, string)
	walk = func(node *tree_sitter.Node, scope, class string) {
		if graphscan.BudgetError(ctx) != nil {
			return
		}
		nextScope, nextClass := scope, class
		switch node.Kind() {
		case "package_declaration":
			file.Module = firstName(node, source)
		case "import_declaration":
			target := firstName(node, source)
			alias := lastPart(target)
			if firstKind(node, "asterisk") != nil {
				alias = "*"
			}
			imports[alias] = target
			graphscan.Add(ctx, &file.Imports, graphscan.Import{Path: path, Target: target, Alias: alias, Range: nodeRange(node)})
		case "class_declaration", "interface_declaration":
			nameNode := node.ChildByFieldName("name")
			name := text(nameNode, source)
			qualified := qualify(file.Module, name)
			kind := "Class"
			if node.Kind() == "interface_declaration" {
				kind = "Interface"
			}
			graphscan.Add(ctx, &file.Declarations, declaration(path, qualified, name, kind, nameNode))
			nextScope, nextClass = qualified, qualified
			addJavaHeritage(ctx, &file, path, qualified, node, source, imports)
		case "method_declaration":
			nameNode := node.ChildByFieldName("name")
			name := text(nameNode, source)
			qualified := qualify(class, name)
			value := declaration(path, qualified, name, "Method", nameNode)
			value.Signature = methodSignature(node, source)
			value.LocalID += value.Signature
			graphscan.Add(ctx, &file.Declarations, value)
			nextScope = value.LocalID
		case "method_invocation":
			nameNode := node.ChildByFieldName("name")
			name := text(nameNode, source)
			object := text(node.ChildByFieldName("object"), source)
			candidates := memberCandidates(file.Module, class, name, object)
			graphscan.Add(ctx, &file.References, graphscan.Reference{Path: path, FromLocalID: scope, Name: name, Candidates: candidates, Range: nodeRange(nameNode), Call: true})
		}
		for i := uint(0); i < node.NamedChildCount(); i++ {
			walk(node.NamedChild(i), nextScope, nextClass)
		}
	}
	walk(tree.RootNode(), "", "")
	if err := graphscan.BudgetError(ctx); err != nil {
		return graphscan.File{}, err
	}
	return file, nil
}

func addJavaHeritage(ctx context.Context, file *graphscan.File, path, child string, node *tree_sitter.Node, source []byte, imports map[string]string) {
	for _, field := range []struct {
		name string
		kind graphartifact.EdgeKind
	}{{"superclass", graphartifact.EdgeExtends}, {"interfaces", graphartifact.EdgeImplements}, {"extends_interfaces", graphartifact.EdgeExtends}} {
		parent := node.ChildByFieldName(field.name)
		if field.name == "extends_interfaces" {
			parent = firstKind(node, field.name)
		}
		if parent == nil {
			continue
		}
		var visit func(*tree_sitter.Node)
		visit = func(value *tree_sitter.Node) {
			if graphscan.BudgetError(ctx) != nil {
				return
			}
			if value.Kind() == "type_identifier" {
				name := text(value, source)
				graphscan.Add(ctx, &file.Heritage, graphscan.Heritage{Path: path, ChildLocalID: child, Candidates: nameCandidates(file.Module, imports, name), Kind: field.kind, Range: nodeRange(value)})
				return
			}
			for i := uint(0); i < value.NamedChildCount(); i++ {
				visit(value.NamedChild(i))
			}
		}
		visit(parent)
	}
}

func nameCandidates(module string, imports map[string]string, name string) []string {
	if target := imports[name]; target != "" {
		return []string{target, name}
	}
	return []string{qualify(module, name), name}
}

func memberCandidates(module, class, name, object string) []string {
	if object == "this" && class != "" {
		return []string{qualify(class, name), lastPart(class) + "." + name, name, object + "." + name}
	}
	if object != "" {
		return []string{name, object + "." + name}
	}
	return []string{qualify(module, name), name}
}

func firstName(node *tree_sitter.Node, source []byte) string {
	for i := uint(0); i < node.NamedChildCount(); i++ {
		child := node.NamedChild(i)
		if child.Kind() == "identifier" || child.Kind() == "scoped_identifier" {
			return text(child, source)
		}
	}
	return ""
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

func declaration(path, qualified, name, kind string, node *tree_sitter.Node) graphscan.Declaration {
	return graphscan.Declaration{Path: path, LocalID: qualified, Name: name, QualifiedName: qualified, Kind: kind, Range: nodeRange(node)}
}

func methodSignature(node *tree_sitter.Node, source []byte) string {
	var parameters []string
	if list := node.ChildByFieldName("parameters"); list != nil {
		for i := uint(0); i < list.NamedChildCount(); i++ {
			parameter := list.NamedChild(i)
			if parameter.Kind() != "formal_parameter" && parameter.Kind() != "spread_parameter" {
				continue
			}
			kind := compact(text(parameter.ChildByFieldName("type"), source))
			if parameter.Kind() == "spread_parameter" {
				kind += "..."
			}
			parameters = append(parameters, kind)
		}
	}
	return "(" + strings.Join(parameters, ",") + "):" + compact(text(node.ChildByFieldName("type"), source))
}

func compact(value string) string { return strings.Join(strings.Fields(value), "") }

func qualify(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}

func lastPart(value string) string {
	if index := strings.LastIndexByte(value, '.'); index >= 0 {
		return value[index+1:]
	}
	return value
}

func text(node *tree_sitter.Node, source []byte) string {
	if node == nil {
		return ""
	}
	return string(source[node.StartByte():node.EndByte()])
}

func nodeRange(node *tree_sitter.Node) graphscan.Range {
	if node == nil {
		return graphscan.Range{}
	}
	start, end := node.StartPosition(), node.EndPosition()
	return graphscan.Range{Start: graphscan.Point{Line: uint32(start.Row), Column: uint32(start.Column)}, End: graphscan.Point{Line: uint32(end.Row), Column: uint32(end.Column)}}
}
