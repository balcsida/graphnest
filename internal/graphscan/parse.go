package graphscan

import (
	"context"
	"errors"
	"sync/atomic"

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
	var canceled uintptr
	parser.SetCancellationFlag(&canceled)
	stop := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-ctx.Done():
			atomic.StoreUintptr(&canceled, 1)
		case <-stop:
		}
	}()
	tree := parser.Parse(source, nil)
	close(stop)
	<-stopped
	parser.SetCancellationFlag(nil)
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
