// Package spdxexpr parses SPDX 2.3 license expressions with a bounded
// grammar and a pinned license list. It preserves AND, OR, parentheses, and
// WITH exceptions as a tree; it never flattens an expression into a bag of
// names, never maps NOASSERTION/NONE/UNLICENSED or unknown identifiers to a
// license, and reports what it could not recognize.
//
// Grammar (SPDX 2.3 Annex D):
//
//	expression  := disjunction
//	disjunction := conjunction ( "OR" conjunction )*
//	conjunction := primary ( "AND" primary )*
//	primary     := simple [ "WITH" exception ] | "(" expression ")"
//	simple      := license-id [ "+" ] | "LicenseRef-" idstring | "DocumentRef-" idstring ":" "LicenseRef-" idstring
package spdxexpr

import (
	"bufio"
	"embed"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ListVersion is the pinned SPDX License List release embedded in this
// package. Recorded on every parsed evidence row so a list upgrade is a new
// resolver version, not a silent change.
const ListVersion = "3.27.0"

//go:embed licenses.tsv exceptions.tsv
var lists embed.FS

// Status classifies a raw license value before or after parsing.
type Status string

const (
	// StatusParsed: a syntactically valid expression whose every identifier
	// is on the pinned list (LicenseRef/DocumentRef identifiers are allowed
	// and reported separately).
	StatusParsed Status = "parsed"
	// StatusUnknownTerms: valid syntax with at least one identifier that is
	// not on the pinned list and not a LicenseRef.
	StatusUnknownTerms Status = "unknown_terms"
	// StatusNoAssertion: NOASSERTION (or empty): no claim was made.
	StatusNoAssertion Status = "no_assertion"
	// StatusNone: NONE: the producer asserts there is no license information.
	StatusNone Status = "none"
	// StatusUnlicensed: npm's UNLICENSED sentinel: use is not granted.
	StatusUnlicensed Status = "unlicensed"
	// StatusInvalid: the value is not an SPDX expression (free text, "SEE
	// LICENSE IN ...", unbalanced parentheses, bad operators).
	StatusInvalid Status = "invalid"
)

// Kind is the node type of a parsed expression.
type Kind string

const (
	KindLicense   Kind = "license"
	KindWith      Kind = "with"
	KindAnd       Kind = "and"
	KindOr        Kind = "or"
	KindReference Kind = "license_ref"
)

// Node is one node of the expression tree. Children is ordered as written.
type Node struct {
	Kind Kind `json:"kind"`
	// ID is the license identifier for KindLicense (canonical list casing),
	// or the full LicenseRef-/DocumentRef- identifier for KindReference.
	ID string `json:"id,omitempty"`
	// OrLater is the "+" suffix on a license identifier.
	OrLater bool `json:"or_later,omitempty"`
	// Exception is the exception identifier for KindWith; Children[0] is the
	// licensed term the exception applies to.
	Exception string  `json:"exception,omitempty"`
	Children  []*Node `json:"children,omitempty"`
	// Known is false for KindLicense/KindWith identifiers that are not on the
	// pinned list. It is always true for KindReference nodes.
	Known bool `json:"known"`
	// Deprecated marks identifiers the list marks deprecated (kept, flagged).
	Deprecated bool `json:"deprecated,omitempty"`
}

// Result is the outcome of Parse.
type Result struct {
	Raw        string
	Status     Status
	Expression *Node
	// Normalized is the canonical rendering (list casing, single spaces,
	// minimal parentheses preserved as written). Empty unless Status is
	// parsed or unknown_terms.
	Normalized string
	// UnknownTerms lists identifiers not on the pinned list, in order.
	UnknownTerms []string
	// References lists LicenseRef-/DocumentRef- identifiers, in order.
	References []string
	// Deprecated lists deprecated identifiers used, in order.
	Deprecated []string
	// Problem explains StatusInvalid without echoing more than a bounded
	// prefix of the input.
	Problem string
}

// Licenses returns whether an identifier is on the pinned list, its canonical
// casing, and whether it is deprecated.
type Licenses interface {
	License(id string) (canonical string, deprecated, ok bool)
	Exception(id string) (canonical string, deprecated, ok bool)
}

type list struct {
	licenses   map[string]entry
	exceptions map[string]entry
}

type entry struct {
	id         string
	deprecated bool
}

var pinned = mustLoad()

func mustLoad() *list {
	result := &list{licenses: map[string]entry{}, exceptions: map[string]entry{}}
	load := func(name string, target map[string]entry) {
		file, err := lists.Open(name)
		if err != nil {
			panic(err)
		}
		defer file.Close()
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			id, flags, _ := strings.Cut(line, "\t")
			target[strings.ToLower(id)] = entry{id: id, deprecated: strings.Contains(flags, "D")}
		}
		if err := scanner.Err(); err != nil {
			panic(err)
		}
	}
	load("licenses.tsv", result.licenses)
	load("exceptions.tsv", result.exceptions)
	return result
}

func (l *list) License(id string) (string, bool, bool) {
	item, ok := l.licenses[strings.ToLower(id)]
	return item.id, item.deprecated, ok
}

func (l *list) Exception(id string) (string, bool, bool) {
	item, ok := l.exceptions[strings.ToLower(id)]
	return item.id, item.deprecated, ok
}

// Pinned returns the embedded license list.
func Pinned() Licenses { return pinned }

// Count reports the embedded list sizes for tests and diagnostics.
func Count() (licenses, exceptions int) { return len(pinned.licenses), len(pinned.exceptions) }

const (
	maxInputBytes = 4096
	maxTokens     = 512
	maxDepth      = 32
)

var errTooLong = errors.New("expression exceeds the size limit")

// Parse classifies and parses a raw license value using the pinned list.
func Parse(raw string) Result {
	return ParseWith(raw, pinned)
}

// ParseWith parses against a caller-supplied list (tests, future versions).
func ParseWith(raw string, licenses Licenses) Result {
	result := Result{Raw: raw}
	trimmed := strings.TrimSpace(raw)
	switch {
	case trimmed == "" || strings.EqualFold(trimmed, "NOASSERTION"):
		result.Status = StatusNoAssertion
		return result
	case strings.EqualFold(trimmed, "NONE"):
		result.Status = StatusNone
		return result
	case strings.EqualFold(trimmed, "UNLICENSED"):
		result.Status = StatusUnlicensed
		return result
	}
	if len(trimmed) > maxInputBytes {
		result.Status, result.Problem = StatusInvalid, errTooLong.Error()
		return result
	}
	tokens, err := tokenize(trimmed)
	if err != nil {
		result.Status, result.Problem = StatusInvalid, err.Error()
		return result
	}
	parser := &parser{tokens: tokens, licenses: licenses}
	node, err := parser.expression(0)
	if err == nil && parser.pos != len(parser.tokens) {
		err = fmt.Errorf("unexpected %q after the expression", parser.tokens[parser.pos].text)
	}
	if err != nil {
		result.Status, result.Problem = StatusInvalid, err.Error()
		return result
	}
	result.Expression = node
	result.Normalized = node.String()
	result.UnknownTerms, result.References, result.Deprecated = parser.unknown, parser.references, parser.deprecated
	result.Status = StatusParsed
	if len(result.UnknownTerms) > 0 {
		result.Status = StatusUnknownTerms
	}
	return result
}

type token struct {
	kind string // "id", "(", ")", "AND", "OR", "WITH", "+"
	text string
}

func tokenize(input string) ([]token, error) {
	var tokens []token
	index := 0
	for index < len(input) {
		character := input[index]
		switch {
		case character == ' ' || character == '\t' || character == '\n' || character == '\r':
			index++
		case character == '(' || character == ')':
			tokens = append(tokens, token{kind: string(character), text: string(character)})
			index++
		case character == '+':
			tokens = append(tokens, token{kind: "+", text: "+"})
			index++
		case isIDByte(character):
			start := index
			for index < len(input) && isIDByte(input[index]) {
				index++
			}
			text := input[start:index]
			switch strings.ToUpper(text) {
			case "AND", "OR", "WITH":
				tokens = append(tokens, token{kind: strings.ToUpper(text), text: text})
			default:
				tokens = append(tokens, token{kind: "id", text: text})
			}
		default:
			return nil, fmt.Errorf("unexpected character %q", sanitizeRune(input[index:]))
		}
		if len(tokens) > maxTokens {
			return nil, errTooLong
		}
	}
	if len(tokens) == 0 {
		return nil, errors.New("empty expression")
	}
	return tokens, nil
}

// isIDByte accepts the SPDX idstring alphabet (letters, digits, "-", ".")
// plus ":" so DocumentRef-x:LicenseRef-y stays one token.
func isIDByte(character byte) bool {
	return character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' || character == '.' || character == ':'
}

func sanitizeRune(rest string) string {
	r, size := utf8.DecodeRuneInString(rest)
	if size == 0 {
		return ""
	}
	if unicode.IsPrint(r) {
		return string(r)
	}
	return fmt.Sprintf("U+%04X", r)
}

type parser struct {
	tokens     []token
	pos        int
	licenses   Licenses
	unknown    []string
	references []string
	deprecated []string
}

func (p *parser) peek() (token, bool) {
	if p.pos >= len(p.tokens) {
		return token{}, false
	}
	return p.tokens[p.pos], true
}

func (p *parser) expression(depth int) (*Node, error) {
	if depth > maxDepth {
		return nil, errors.New("expression nesting exceeds the depth limit")
	}
	left, err := p.conjunction(depth)
	if err != nil {
		return nil, err
	}
	var children []*Node
	for {
		next, ok := p.peek()
		if !ok || next.kind != "OR" {
			break
		}
		p.pos++
		right, err := p.conjunction(depth)
		if err != nil {
			return nil, err
		}
		if children == nil {
			children = []*Node{left}
		}
		children = append(children, right)
	}
	if children == nil {
		return left, nil
	}
	return &Node{Kind: KindOr, Children: flatten(KindOr, children), Known: true}, nil
}

// flatten merges children that are the same associative operator so
// "(A OR B) OR C" and "A OR B OR C" produce one tree. Order is preserved;
// mixed operators keep their grouping.
func flatten(kind Kind, children []*Node) []*Node {
	result := make([]*Node, 0, len(children))
	for _, child := range children {
		if child.Kind == kind {
			result = append(result, child.Children...)
			continue
		}
		result = append(result, child)
	}
	return result
}

func (p *parser) conjunction(depth int) (*Node, error) {
	left, err := p.primary(depth)
	if err != nil {
		return nil, err
	}
	var children []*Node
	for {
		next, ok := p.peek()
		if !ok || next.kind != "AND" {
			break
		}
		p.pos++
		right, err := p.primary(depth)
		if err != nil {
			return nil, err
		}
		if children == nil {
			children = []*Node{left}
		}
		children = append(children, right)
	}
	if children == nil {
		return left, nil
	}
	return &Node{Kind: KindAnd, Children: flatten(KindAnd, children), Known: true}, nil
}

func (p *parser) primary(depth int) (*Node, error) {
	next, ok := p.peek()
	if !ok {
		return nil, errors.New("expression ends where a license was expected")
	}
	switch next.kind {
	case "(":
		p.pos++
		inner, err := p.expression(depth + 1)
		if err != nil {
			return nil, err
		}
		closing, ok := p.peek()
		if !ok || closing.kind != ")" {
			return nil, errors.New("missing closing parenthesis")
		}
		p.pos++
		// A parenthesized group is kept as-is; parentheses are re-rendered
		// around compound children when they change grouping.
		return inner, nil
	case "id":
		p.pos++
		node := p.simple(next.text)
		if plus, ok := p.peek(); ok && plus.kind == "+" {
			p.pos++
			if node.Kind != KindLicense {
				return nil, errors.New("\"+\" is only valid after a license identifier")
			}
			node.OrLater = true
		}
		if with, ok := p.peek(); ok && with.kind == "WITH" {
			p.pos++
			exception, ok := p.peek()
			if !ok || exception.kind != "id" {
				return nil, errors.New("WITH must be followed by an exception identifier")
			}
			p.pos++
			canonical, deprecated, known := p.licenses.Exception(exception.text)
			if !known {
				canonical = exception.text
				p.unknown = append(p.unknown, exception.text)
			}
			if deprecated {
				p.deprecated = append(p.deprecated, canonical)
			}
			return &Node{Kind: KindWith, Exception: canonical, Children: []*Node{node}, Known: known, Deprecated: deprecated}, nil
		}
		return node, nil
	case ")":
		return nil, errors.New("unexpected closing parenthesis")
	default:
		return nil, fmt.Errorf("unexpected %q where a license was expected", next.text)
	}
}

func (p *parser) simple(text string) *Node {
	lower := strings.ToLower(text)
	if strings.HasPrefix(lower, "licenseref-") || strings.HasPrefix(lower, "documentref-") {
		p.references = append(p.references, text)
		return &Node{Kind: KindReference, ID: text, Known: true}
	}
	canonical, deprecated, known := p.licenses.License(text)
	if !known {
		p.unknown = append(p.unknown, text)
		return &Node{Kind: KindLicense, ID: text, Known: false}
	}
	if deprecated {
		p.deprecated = append(p.deprecated, canonical)
	}
	return &Node{Kind: KindLicense, ID: canonical, Known: true, Deprecated: deprecated}
}

// String renders the canonical expression. Compound children of a different
// operator are parenthesized so structure is never lost.
func (node *Node) String() string {
	if node == nil {
		return ""
	}
	switch node.Kind {
	case KindLicense:
		if node.OrLater {
			return node.ID + "+"
		}
		return node.ID
	case KindReference:
		return node.ID
	case KindWith:
		return node.Children[0].String() + " WITH " + node.Exception
	case KindAnd, KindOr:
		operator := " AND "
		if node.Kind == KindOr {
			operator = " OR "
		}
		parts := make([]string, 0, len(node.Children))
		for _, child := range node.Children {
			text := child.String()
			if (child.Kind == KindAnd || child.Kind == KindOr) && child.Kind != node.Kind {
				text = "(" + text + ")"
			}
			parts = append(parts, text)
		}
		return strings.Join(parts, operator)
	}
	return ""
}

// Terms returns the distinct leaf identifiers (licenses with their "+" and
// WITH exception rendered, and references) in sorted order. It is a display
// aid; policy evaluation must walk the tree.
func (node *Node) Terms() []string {
	seen := map[string]bool{}
	var walk func(*Node)
	walk = func(current *Node) {
		switch current.Kind {
		case KindAnd, KindOr:
			for _, child := range current.Children {
				walk(child)
			}
		default:
			seen[current.String()] = true
		}
	}
	walk(node)
	terms := make([]string, 0, len(seen))
	for term := range seen {
		terms = append(terms, term)
	}
	sort.Strings(terms)
	return terms
}

// Equal reports structural equality (same tree, same identifiers).
func Equal(left, right *Node) bool {
	if left == nil || right == nil {
		return left == right
	}
	if left.Kind != right.Kind || left.ID != right.ID || left.OrLater != right.OrLater || left.Exception != right.Exception || len(left.Children) != len(right.Children) {
		return false
	}
	for index := range left.Children {
		if !Equal(left.Children[index], right.Children[index]) {
			return false
		}
	}
	return true
}
