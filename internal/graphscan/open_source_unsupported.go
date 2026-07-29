//go:build !darwin && !linux

package graphscan

import (
	"errors"
	"os"
)

var ErrUnsupportedPlatform = errors.New("scanner source opening unsupported on this platform")

func openSourceRoot(string) (*sourceRoot, error)           { return nil, ErrUnsupportedPlatform }
func openSourceFile(*sourceRoot, string) (*os.File, error) { return nil, ErrUnsupportedPlatform }
