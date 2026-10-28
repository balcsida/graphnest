package search

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestQueryErrorMessage(t *testing.T) {
	const unclosedClass = "error parsing regexp: missing closing ]: `[0-9`"
	for _, test := range []struct {
		name, reason, query, want string
	}{
		{"space inside a class", unclosedClass, "FROM node:[0-9 ]",
			"search query is invalid: error parsing regexp: missing closing ]: `[0-9`; hint: spaces separate query terms; quote the term (\"FROM node:[0-9 ]\") or use \\s"},
		{"query already quoted", unclosedClass, `"a" [0-9 ]`,
			"search query is invalid: error parsing regexp: missing closing ]: `[0-9`; hint: spaces separate query terms; quote the term or use \\s"},
		{"query too long to quote", unclosedClass, strings.Repeat("a", 200) + " [0-9 ]",
			"search query is invalid: error parsing regexp: missing closing ]: `[0-9`; hint: spaces separate query terms; quote the term or use \\s"},
		{"query with a control character", unclosedClass, "FROM\nnode:[0-9 ]",
			"search query is invalid: error parsing regexp: missing closing ]: `[0-9`; hint: spaces separate query terms; quote the term or use \\s"},
		{"stray closing parenthesis", `query: extra tokens found at end input: ")"`, "foo)",
			`search query is invalid: query: extra tokens found at end input: ")"; hint: parentheses group query terms; escape them as \( and \) to match them literally`},
		{"missing closing parenthesis", "query: missing close paren, got token EOF", "(foo",
			`search query is invalid: query: missing close paren, got token EOF; hint: parentheses group query terms; escape them as \( and \) to match them literally`},
		{"no known gotcha", `query: unknown case argument "maybe", want {yes,no,auto}`, "case:maybe foo",
			`search query is invalid: query: unknown case argument "maybe", want {yes,no,auto}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := NewQueryError(test.reason)
			err.Query = test.query
			if got := err.Message(); got != test.want {
				t.Fatalf("Message() =\n%s\nwant\n%s", got, test.want)
			}
		})
	}
}

func TestNewQueryErrorSanitizes(t *testing.T) {
	if got := NewQueryError("bad\x00\x1b[31m\nquery").Reason; got != "bad[31mquery" {
		t.Fatalf("control characters kept: %q", got)
	}
	long := NewQueryError(strings.Repeat("é", 500)).Reason
	if len(long) != 300 || !utf8.ValidString(long) {
		t.Fatalf("long reason = %d bytes, valid UTF-8 %v; want 300 valid bytes", len(long), utf8.ValidString(long))
	}
	odd := NewQueryError("x" + strings.Repeat("é", 500)).Reason
	if len(odd) != 299 || !utf8.ValidString(odd) {
		t.Fatalf("reason cut inside a rune: %d bytes, valid UTF-8 %v", len(odd), utf8.ValidString(odd))
	}
	if !errors.Is(NewQueryError("x"), ErrInvalidQuery) {
		t.Fatal("QueryError does not match ErrInvalidQuery")
	}
}
