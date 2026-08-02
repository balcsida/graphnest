package graphscan

import (
	"context"
	"errors"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

func ParseTree(ctx context.Context, language *tree_sitter.Language, source []byte) (*tree_sitter.Tree, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	parser := tree_sitter.NewParser()
	if err := parser.SetLanguage(language); err != nil {
		parser.Close()
		return nil, err
	}
	length := len(source)
	tree := parser.ParseWithOptions(func(i int, _ tree_sitter.Point) []byte {
		if i < length {
			return source[i:]
		}
		return nil
	}, nil, &tree_sitter.ParseOptions{
		ProgressCallback: func(tree_sitter.ParseState) bool { return ctx.Err() != nil },
	})
	parser.Close()
	if err := ctx.Err(); err != nil {
		if tree != nil {
			tree.Close()
		}
		return nil, err
	}
	if tree == nil {
		return nil, errors.New("tree-sitter parse failed")
	}
	return tree, nil
}
