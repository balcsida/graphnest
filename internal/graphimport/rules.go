package graphimport

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
)

// ErrUnknownProducer means no captured rules exist for a producer version, so an index it wrote cannot be verified.
var ErrUnknownProducer = errors.New("unknown CodeGraph producer version")

//go:embed codegraph-rules-1.6.0.json
var rules160 []byte

//go:embed codegraph-rules-1.6.2.json
var rules162 []byte

// Rules are the file-selection and hashing rules of one pinned CodeGraph version.
type Rules struct {
	Version               string
	MaxSourceFileSize     int64
	OversizeStamp         bool // 1.6.1+: files over the limit hash "codegraph:oversize:<size>"
	ExtensionMap          map[string]string
	DefaultIgnorePatterns []string
	IgnoreCase            bool
}

// RulesFor returns the rules for a producer version recorded in project_metadata
// indexed_with_version: "1.6.0" and "1.6.1"/"1.6.2". Unknown versions return
// ErrUnknownProducer (unverifiable), never a guess.
func RulesFor(version string) (Rules, error) {
	switch version {
	case "1.6.0":
		return parseRules("1.6.0", rules160)
	case "1.6.1", "1.6.2":
		// 1.6.1 has the same file-limits module, extension map and default ignore patterns as 1.6.2.
		return parseRules("1.6.2", rules162)
	}
	return Rules{}, fmt.Errorf("%w: %q", ErrUnknownProducer, version)
}

// producerRulesFile is the part of producer-rules.json that Rules uses.
type producerRulesFile struct {
	ExtensionMap          map[string]string `json:"extension_map"`
	DefaultIgnorePatterns []string          `json:"default_ignore_patterns"`
	IgnoreCase            bool              `json:"ignore_case"`
	MaxSourceFileSize     int64             `json:"max_source_file_size_bytes"`
	OversizeHash          *json.RawMessage  `json:"oversize_hash"`
}

func parseRules(version string, data []byte) (Rules, error) {
	var f producerRulesFile
	if err := json.Unmarshal(data, &f); err != nil {
		return Rules{}, fmt.Errorf("embedded rules %s: %w", version, err)
	}
	return Rules{Version: version, MaxSourceFileSize: f.MaxSourceFileSize, OversizeStamp: f.OversizeHash != nil, ExtensionMap: f.ExtensionMap, DefaultIgnorePatterns: f.DefaultIgnorePatterns, IgnoreCase: f.IgnoreCase}, nil
}
