package javascript

import (
	"context"
	_ "embed"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/grepnest/grepnest/internal/graphartifact"
	"github.com/grepnest/grepnest/scanner/graphscan"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_javascript "github.com/tree-sitter/tree-sitter-javascript/bindings/go"
	tree_sitter_typescript "github.com/tree-sitter/tree-sitter-typescript/bindings/go"
)

//go:embed queries.scm
var querySource string

func Parse(ctx context.Context, path string, source []byte) (graphscan.File, error) {
	extension := filepath.Ext(path)
	language, fileLanguage := languageFor(extension)
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
		return graphscan.File{}, fmt.Errorf("parse JavaScript: query did not match")
	}

	file := graphscan.File{Path: path, Module: strings.TrimSuffix(filepath.Base(path), extension), Language: fileLanguage}
	aliases := map[string]string{}
	var exports [][2]string
	var walk func(*tree_sitter.Node, string, string)
	walk = func(node *tree_sitter.Node, scope, class string) {
		if node == nil || graphscan.BudgetError(ctx) != nil {
			return
		}
		nextScope, nextClass := scope, class
		switch node.Kind() {
		case "import_statement":
			addImports(ctx, &file, aliases, path, node, source)
		case "export_statement":
			value := node.ChildByFieldName("value")
			if value != nil && (value.Kind() == "function_expression" || value.Kind() == "arrow_function") {
				graphscan.Add(ctx, &file.Declarations, declarationFor(path, "defaultExport", "defaultExport", "Function", value))
				nextScope = "defaultExport"
			} else if value != nil && value.Kind() == "class" {
				nextScope, nextClass = "defaultExport", "defaultExport"
			}
		case "export_specifier":
			name := text(node.ChildByFieldName("name"), source)
			alias := text(node.ChildByFieldName("alias"), source)
			if alias == "" {
				alias = name
			}
			exports = append(exports, [2]string{name, alias})
		case "function_declaration":
			nameNode := node.ChildByFieldName("name")
			name := text(nameNode, source)
			if name == "" {
				name = "defaultExport"
				nameNode = node
			}
			nextScope = name
			graphscan.Add(ctx, &file.Declarations, declarationFor(path, name, name, "Function", nameNode))
		case "variable_declarator":
			value := node.ChildByFieldName("value")
			if value != nil && (value.Kind() == "arrow_function" || value.Kind() == "function_expression") {
				nameNode := node.ChildByFieldName("name")
				name := text(nameNode, source)
				nextScope = name
				graphscan.Add(ctx, &file.Declarations, declarationFor(path, name, name, "Function", nameNode))
			}
		case "class_declaration":
			nameNode := node.ChildByFieldName("name")
			name := text(nameNode, source)
			nextScope, nextClass = name, name
			graphscan.Add(ctx, &file.Declarations, declarationFor(path, name, name, "Class", nameNode))
			addHeritage(ctx, &file, path, name, node, source)
		case "class":
			nameNode := node.ChildByFieldName("name")
			name := text(nameNode, source)
			if name == "" {
				name = class
				nameNode = node
			}
			if name != "" {
				nextScope, nextClass = name, name
				graphscan.Add(ctx, &file.Declarations, declarationFor(path, name, name, "Class", nameNode))
				addHeritage(ctx, &file, path, name, node, source)
			}
		case "method_definition", "method_signature":
			nameNode := node.ChildByFieldName("name")
			name := text(nameNode, source)
			qualified := class + "." + name
			nextScope = qualified
			graphscan.Add(ctx, &file.Declarations, declarationFor(path, qualified, name, "Method", nameNode))
		case "call_expression":
			function := node.ChildByFieldName("function")
			name, candidates := callCandidates(function, source, aliases, class)
			if name != "" {
				graphscan.Add(ctx, &file.References, graphscan.Reference{Path: path, FromLocalID: scope, Name: name, Candidates: candidates, Range: nodeRange(function), Call: true})
			}
		}
		for i := uint(0); i < node.NamedChildCount(); i++ {
			walk(node.NamedChild(i), nextScope, nextClass)
		}
	}
	walk(tree.RootNode(), "", "")
	for _, exported := range exports {
		for _, declaration := range file.Declarations {
			if declaration.QualifiedName == exported[0] && exported[0] != exported[1] {
				declaration.LocalID = exported[1]
				declaration.Name = exported[1]
				declaration.QualifiedName = exported[1]
				graphscan.Add(ctx, &file.Declarations, declaration)
				break
			}
		}
	}
	if err := graphscan.BudgetError(ctx); err != nil {
		return graphscan.File{}, err
	}
	return file, nil
}

func languageFor(extension string) (*tree_sitter.Language, graphscan.Language) {
	switch extension {
	case ".ts":
		return tree_sitter.NewLanguage(tree_sitter_typescript.LanguageTypescript()), graphscan.TypeScript
	case ".tsx":
		return tree_sitter.NewLanguage(tree_sitter_typescript.LanguageTSX()), graphscan.TypeScript
	default:
		return tree_sitter.NewLanguage(tree_sitter_javascript.Language()), graphscan.JavaScript
	}
}

func addImports(ctx context.Context, file *graphscan.File, aliases map[string]string, path string, node *tree_sitter.Node, source []byte) {
	target := strings.Trim(text(node.ChildByFieldName("source"), source), "\"'")
	added := false
	var visit func(*tree_sitter.Node)
	visit = func(child *tree_sitter.Node) {
		if graphscan.BudgetError(ctx) != nil {
			return
		}
		switch child.Kind() {
		case "import_specifier":
			name := text(child.ChildByFieldName("name"), source)
			alias := text(child.ChildByFieldName("alias"), source)
			if alias == "" {
				alias = name
			}
			aliases[alias] = name
			graphscan.Add(ctx, &file.Imports, graphscan.Import{Path: path, Target: target, Alias: alias, Range: nodeRange(child)})
			added = true
			return
		case "namespace_import":
			alias := text(child.NamedChild(0), source)
			graphscan.Add(ctx, &file.Imports, graphscan.Import{Path: path, Target: target, Alias: alias, Range: nodeRange(child)})
			added = true
			return
		case "import_clause":
			for i := uint(0); i < child.NamedChildCount(); i++ {
				value := child.NamedChild(i)
				if value.Kind() == "identifier" {
					alias := text(value, source)
					graphscan.Add(ctx, &file.Imports, graphscan.Import{Path: path, Target: target, Alias: alias, Range: nodeRange(value)})
					added = true
				}
			}
		}
		for i := uint(0); i < child.NamedChildCount(); i++ {
			visit(child.NamedChild(i))
		}
	}
	visit(node)
	if !added {
		graphscan.Add(ctx, &file.Imports, graphscan.Import{Path: path, Target: target, Range: nodeRange(node)})
	}
}

func addHeritage(ctx context.Context, file *graphscan.File, path, child string, node *tree_sitter.Node, source []byte) {
	var visit func(*tree_sitter.Node)
	visit = func(value *tree_sitter.Node) {
		if graphscan.BudgetError(ctx) != nil {
			return
		}
		kind := graphartifact.EdgeKind(0)
		switch value.Kind() {
		case "class_heritage", "extends_clause":
			kind = graphartifact.EdgeExtends
		case "implements_clause":
			kind = graphartifact.EdgeImplements
		}
		if kind != 0 {
			for i := uint(0); i < value.NamedChildCount(); i++ {
				candidate := value.NamedChild(i)
				if candidate.Kind() == "identifier" || candidate.Kind() == "type_identifier" {
					graphscan.Add(ctx, &file.Heritage, graphscan.Heritage{Path: path, ChildLocalID: child, Candidates: []string{text(candidate, source)}, Kind: kind, Range: nodeRange(candidate)})
				}
			}
			if value.Kind() != "class_heritage" {
				return
			}
		}
		for i := uint(0); i < value.NamedChildCount(); i++ {
			visit(value.NamedChild(i))
		}
	}
	visit(node)
}

func callCandidates(node *tree_sitter.Node, source []byte, aliases map[string]string, class string) (string, []string) {
	if node == nil {
		return "", nil
	}
	name := text(node, source)
	if node.Kind() == "member_expression" {
		name = text(node.ChildByFieldName("property"), source)
		member := text(node, source)
		if text(node.ChildByFieldName("object"), source) == "this" && class != "" {
			return name, []string{class + "." + name, name, member}
		}
		return name, []string{name, member}
	}
	candidates := []string{name}
	if original := aliases[name]; original != "" && original != name {
		candidates = append(candidates, original)
	}
	return name, candidates
}

func declarationFor(path, id, name, kind string, node *tree_sitter.Node) graphscan.Declaration {
	return graphscan.Declaration{Path: path, LocalID: id, Name: name, QualifiedName: id, Kind: kind, Range: nodeRange(node)}
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
