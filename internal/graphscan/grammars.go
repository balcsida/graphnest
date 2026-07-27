package graphscan

import (
	tree_sitter_kotlin "github.com/tree-sitter-grammars/tree-sitter-kotlin/bindings/go"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_go "github.com/tree-sitter/tree-sitter-go/bindings/go"
	tree_sitter_java "github.com/tree-sitter/tree-sitter-java/bindings/go"
	tree_sitter_javascript "github.com/tree-sitter/tree-sitter-javascript/bindings/go"
	tree_sitter_rust "github.com/tree-sitter/tree-sitter-rust/bindings/go"
	tree_sitter_typescript "github.com/tree-sitter/tree-sitter-typescript/bindings/go"
)

func LanguageForExtension(extension string) (*tree_sitter.Language, bool) {
	switch extension {
	case ".go":
		return tree_sitter.NewLanguage(tree_sitter_go.Language()), true
	case ".js":
		return tree_sitter.NewLanguage(tree_sitter_javascript.Language()), true
	case ".ts":
		return tree_sitter.NewLanguage(tree_sitter_typescript.LanguageTypescript()), true
	case ".tsx":
		return tree_sitter.NewLanguage(tree_sitter_typescript.LanguageTSX()), true
	case ".java":
		return tree_sitter.NewLanguage(tree_sitter_java.Language()), true
	case ".kt":
		return tree_sitter.NewLanguage(tree_sitter_kotlin.Language()), true
	case ".rs":
		return tree_sitter.NewLanguage(tree_sitter_rust.Language()), true
	default:
		return nil, false
	}
}
