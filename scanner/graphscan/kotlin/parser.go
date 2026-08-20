package kotlin

import (
	"context"
	_ "embed"
	"fmt"
	"strings"

	"github.com/grepnest/grepnest/internal/graphartifact"
	"github.com/grepnest/grepnest/scanner/graphscan"
	tree_sitter_kotlin "github.com/tree-sitter-grammars/tree-sitter-kotlin/bindings/go"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

//go:embed queries.scm
var querySource string

func Parse(ctx context.Context, path string, source []byte) (graphscan.File, error) {
	language := tree_sitter.NewLanguage(tree_sitter_kotlin.Language())
	query, err := tree_sitter.NewQuery(language, querySource)
	if err != nil {
		return graphscan.File{}, err
	}
	defer query.Close()
	tree, parseErr := graphscan.ParseTree(ctx, language, source)
	if parseErr != nil {
		return graphscan.File{}, parseErr
	}
	defer tree.Close()
	cursor := tree_sitter.NewQueryCursor()
	defer cursor.Close()
	matches := cursor.Matches(query, tree.RootNode(), source)
	if matches.Next() == nil {
		return graphscan.File{}, fmt.Errorf("parse Kotlin: query did not match")
	}

	file := graphscan.File{Path: path, Language: graphscan.Kotlin}
	imports := map[string]string{}
	var walk func(*tree_sitter.Node, string, string)
	walk = func(node *tree_sitter.Node, scope, class string) {
		if graphscan.BudgetError(ctx) != nil {
			return
		}
		nextScope, nextClass := scope, class
		switch node.Kind() {
		case "package_header":
			file.Module = text(node.NamedChild(0), source)
		case "import":
			target, alias := kotlinImport(text(node, source))
			imports[alias] = target
			graphscan.Add(ctx, &file.Imports, graphscan.Import{Path: path, Target: target, Alias: alias, Range: nodeRange(node)})
		case "class_declaration":
			nameNode := node.ChildByFieldName("name")
			name := text(nameNode, source)
			qualified := qualify(file.Module, name)
			kind := "Class"
			if hasDirectChild(node, "interface") {
				kind = "Interface"
			}
			graphscan.Add(ctx, &file.Declarations, declaration(path, qualified, name, kind, nameNode))
			nextScope, nextClass = qualified, qualified
			addKotlinHeritage(ctx, &file, path, qualified, node, source, imports)
		case "object_declaration":
			nameNode := node.ChildByFieldName("name")
			name := text(nameNode, source)
			qualified := qualify(file.Module, name)
			graphscan.Add(ctx, &file.Declarations, declaration(path, qualified, name, "Object", nameNode))
			nextScope, nextClass = qualified, qualified
			addKotlinHeritage(ctx, &file, path, qualified, node, source, imports)
		case "function_declaration":
			nameNode := node.ChildByFieldName("name")
			name := text(nameNode, source)
			kind, qualified := "Function", qualify(file.Module, name)
			if class != "" {
				kind, qualified = "Method", qualify(class, name)
			}
			value := declaration(path, qualified, name, kind, nameNode)
			value.Signature = functionSignature(node, source)
			value.LocalID += value.Signature
			graphscan.Add(ctx, &file.Declarations, value)
			nextScope = value.LocalID
		case "call_expression":
			function := node.NamedChild(0)
			raw := text(function, source)
			name := raw
			if index := strings.LastIndexByte(name, '.'); index >= 0 {
				name = name[index+1:]
			}
			graphscan.Add(ctx, &file.References, graphscan.Reference{Path: path, FromLocalID: scope, Name: name, Candidates: kotlinCallCandidates(file.Module, class, name, raw), Range: nodeRange(function), Call: true})
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

func kotlinImport(value string) (string, string) {
	value = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(value), "import"))
	target, alias, found := strings.Cut(value, " as ")
	if found {
		return strings.TrimSpace(target), strings.TrimSpace(alias)
	}
	target = strings.TrimSpace(target)
	if strings.HasSuffix(target, ".*") {
		return strings.TrimSuffix(target, ".*"), "*"
	}
	return target, lastPart(target)
}

func addKotlinHeritage(ctx context.Context, file *graphscan.File, path, child string, node *tree_sitter.Node, source []byte, imports map[string]string) {
	specifiers := firstKind(node, "delegation_specifiers")
	if specifiers == nil {
		return
	}
	for i := uint(0); i < specifiers.NamedChildCount(); i++ {
		specifier := specifiers.NamedChild(i)
		nameNode := firstKind(specifier, "identifier")
		name := text(nameNode, source)
		if name == "" {
			continue
		}
		kind := graphartifact.EdgeImplements
		if firstKind(specifier, "constructor_invocation") != nil {
			kind = graphartifact.EdgeExtends
		}
		if !graphscan.Add(ctx, &file.Heritage, graphscan.Heritage{Path: path, ChildLocalID: child, Candidates: nameCandidates(file.Module, imports, name), Kind: kind, Range: nodeRange(nameNode)}) {
			return
		}
	}
}

func kotlinCallCandidates(module, class, name, raw string) []string {
	if raw == "this."+name && class != "" {
		return []string{qualify(class, name), lastPart(class) + "." + name, name, raw}
	}
	if raw != name {
		return []string{name, raw}
	}
	return []string{qualify(module, name), name}
}

func nameCandidates(module string, imports map[string]string, name string) []string {
	if target := imports[name]; target != "" {
		return []string{target, name}
	}
	return []string{qualify(module, name), name}
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

func hasDirectChild(node *tree_sitter.Node, kind string) bool {
	for i := uint(0); i < node.ChildCount(); i++ {
		if node.Child(i).Kind() == kind {
			return true
		}
	}
	return false
}

func declaration(path, qualified, name, kind string, node *tree_sitter.Node) graphscan.Declaration {
	return graphscan.Declaration{Path: path, LocalID: qualified, Name: name, QualifiedName: qualified, Kind: kind, Range: nodeRange(node)}
}

func functionSignature(node *tree_sitter.Node, source []byte) string {
	var parameters []string
	if list := firstKind(node, "function_value_parameters"); list != nil {
		for i := uint(0); i < list.NamedChildCount(); i++ {
			parameter := list.NamedChild(i)
			if parameter.Kind() != "parameter" {
				continue
			}
			if count := parameter.NamedChildCount(); count > 1 {
				parameters = append(parameters, compact(text(parameter.NamedChild(count-1), source)))
			}
		}
	}
	result := "Unit"
	seenParameters := false
	for i := uint(0); i < node.NamedChildCount(); i++ {
		child := node.NamedChild(i)
		if child.Kind() == "function_value_parameters" {
			seenParameters = true
		} else if seenParameters && strings.Contains(child.Kind(), "type") {
			result = compact(text(child, source))
			break
		}
	}
	return "(" + strings.Join(parameters, ",") + "):" + result
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
