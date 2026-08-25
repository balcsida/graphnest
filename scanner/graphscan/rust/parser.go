package rust

import (
	"context"
	_ "embed"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/balcsida/graphnest/internal/graphartifact"
	"github.com/balcsida/graphnest/scanner/graphscan"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_rust "github.com/tree-sitter/tree-sitter-rust/bindings/go"
)

//go:embed queries.scm
var querySource string

func Parse(ctx context.Context, path string, source []byte) (graphscan.File, error) {
	language := tree_sitter.NewLanguage(tree_sitter_rust.Language())
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
		return graphscan.File{}, fmt.Errorf("parse Rust: query did not match")
	}

	file := graphscan.File{Path: path, Module: strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), Language: graphscan.Rust}
	var walk func(*tree_sitter.Node, string, string, string)
	walk = func(node *tree_sitter.Node, module, scope, owner string) {
		if graphscan.BudgetError(ctx) != nil {
			return
		}
		nextModule, nextScope, nextOwner := module, scope, owner
		switch node.Kind() {
		case "mod_item":
			nameNode := node.ChildByFieldName("name")
			name := text(nameNode, source)
			qualified := qualify(module, name)
			graphscan.Add(ctx, &file.Declarations, declaration(path, qualified, name, "Module", nameNode))
			if node.ChildByFieldName("body") != nil {
				nextModule = qualified
			}
		case "use_declaration":
			addUses(ctx, &file, path, node.ChildByFieldName("argument"), "", source)
		case "trait_item", "struct_item":
			nameNode := node.ChildByFieldName("name")
			name := text(nameNode, source)
			kind := "Struct"
			if node.Kind() == "trait_item" {
				kind = "Trait"
			}
			qualified := qualify(module, name)
			graphscan.Add(ctx, &file.Declarations, declaration(path, qualified, name, kind, nameNode))
			nextScope = qualified
			if node.Kind() == "trait_item" {
				nextOwner = name
			}
		case "impl_item":
			typeName := text(node.ChildByFieldName("type"), source)
			nextOwner = typeName
			if trait := node.ChildByFieldName("trait"); trait != nil {
				traitName := text(trait, source)
				child := qualify(module, typeName)
				graphscan.Add(ctx, &file.Heritage, graphscan.Heritage{Path: path, ChildLocalID: child, Candidates: []string{qualify(module, traitName), traitName}, Kind: graphartifact.EdgeImplements, Range: nodeRange(trait)})
			}
		case "function_item", "function_signature_item":
			nameNode := node.ChildByFieldName("name")
			name := text(nameNode, source)
			kind, qualified := "Function", qualify(module, name)
			if owner != "" {
				kind, qualified = "Method", qualify(qualify(module, owner), name)
			}
			graphscan.Add(ctx, &file.Declarations, declaration(path, qualified, name, kind, nameNode))
			nextScope = qualified
		case "call_expression":
			function := node.ChildByFieldName("function")
			raw := text(function, source)
			name := raw
			if function != nil && function.Kind() == "field_expression" {
				name = text(function.ChildByFieldName("field"), source)
			} else if index := strings.LastIndex(raw, "::"); index >= 0 {
				name = raw[index+2:]
			}
			graphscan.Add(ctx, &file.References, graphscan.Reference{Path: path, FromLocalID: scope, Name: name, Candidates: rustCallCandidates(module, owner, name, raw), Range: nodeRange(function), Call: true})
		}
		for i := uint(0); i < node.NamedChildCount(); i++ {
			walk(node.NamedChild(i), nextModule, nextScope, nextOwner)
		}
	}
	walk(tree.RootNode(), file.Module, "", "")
	if err := graphscan.BudgetError(ctx); err != nil {
		return graphscan.File{}, err
	}
	return file, nil
}

func addUses(ctx context.Context, file *graphscan.File, path string, node *tree_sitter.Node, prefix string, source []byte) {
	if node == nil || graphscan.BudgetError(ctx) != nil {
		return
	}
	switch node.Kind() {
	case "scoped_use_list":
		addUses(ctx, file, path, node.ChildByFieldName("list"), joinPath(prefix, text(node.ChildByFieldName("path"), source)), source)
	case "use_list":
		for i := uint(0); i < node.NamedChildCount(); i++ {
			addUses(ctx, file, path, node.NamedChild(i), prefix, source)
		}
	case "use_as_clause":
		target := joinPath(prefix, text(node.ChildByFieldName("path"), source))
		graphscan.Add(ctx, &file.Imports, graphscan.Import{Path: path, Target: target, Alias: text(node.ChildByFieldName("alias"), source), Range: nodeRange(node)})
	case "identifier", "scoped_identifier", "self", "super", "crate":
		target := joinPath(prefix, text(node, source))
		graphscan.Add(ctx, &file.Imports, graphscan.Import{Path: path, Target: target, Alias: lastPart(target), Range: nodeRange(node)})
	case "use_wildcard":
		graphscan.Add(ctx, &file.Imports, graphscan.Import{Path: path, Target: strings.TrimSuffix(joinPath(prefix, text(node, source)), "::*"), Alias: "*", Range: nodeRange(node)})
	}
}

func rustCallCandidates(module, implType, name, raw string) []string {
	if implType != "" && raw == "self."+name {
		return []string{qualify(qualify(module, implType), name), implType + "::" + name, name, raw}
	}
	if raw != name {
		return []string{name, raw}
	}
	return []string{qualify(module, name), name}
}

func joinPath(prefix, value string) string {
	if prefix == "" {
		return value
	}
	if value == "self" {
		return prefix
	}
	return prefix + "::" + value
}

func declaration(path, qualified, name, kind string, node *tree_sitter.Node) graphscan.Declaration {
	return graphscan.Declaration{Path: path, LocalID: qualified, Name: name, QualifiedName: qualified, Kind: kind, Range: nodeRange(node)}
}

func qualify(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "::" + name
}

func lastPart(value string) string {
	if index := strings.LastIndex(value, "::"); index >= 0 {
		return value[index+2:]
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
