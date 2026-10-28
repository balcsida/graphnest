package search

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxQueryErrorReason = 300

// QueryError is a query that GraphNest or the search backend refused, with a
// reason that is safe to show the caller.
type QueryError struct {
	Reason string
	// Query is the caller's query; it is used only to phrase hints.
	Query string
}

// NewQueryError drops control characters from reason and bounds it, so a
// backend message echoing a hostile query stays printable and short.
func NewQueryError(reason string) *QueryError {
	reason = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, reason)
	if len(reason) > maxQueryErrorReason {
		cut := maxQueryErrorReason
		for cut > 0 && !utf8.RuneStart(reason[cut]) {
			cut--
		}
		reason = reason[:cut]
	}
	return &QueryError{Reason: reason}
}

func (err *QueryError) Error() string { return "invalid query: " + err.Reason }

func (err *QueryError) Unwrap() error { return ErrInvalidQuery }

// Message is the caller-facing text, with a hint when the reason matches a
// known query-syntax gotcha.
func (err *QueryError) Message() string {
	message := "search query is invalid: " + err.Reason
	if hint := queryHint(err.Reason, err.Query); hint != "" {
		message += "; hint: " + hint
	}
	return message
}

func queryHint(reason, query string) string {
	switch {
	case strings.Contains(reason, "missing closing ]"):
		if query != "" && len(query) <= 200 && !strings.Contains(query, `"`) && strings.IndexFunc(query, unicode.IsControl) < 0 {
			return `spaces separate query terms; quote the term ("` + query + `") or use \s`
		}
		return `spaces separate query terms; quote the term or use \s`
	case strings.Contains(reason, "missing close paren"), strings.Contains(reason, `extra tokens found at end input: ")"`):
		return `parentheses group query terms; escape them as \( and \) to match them literally`
	}
	return ""
}
