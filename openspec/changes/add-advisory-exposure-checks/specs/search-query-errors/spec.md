# Spec Delta

## Purpose

Make code search tell users exactly what is wrong with a query they wrote,
instead of a generic "search query is invalid", and refuse query syntax the
search backend would silently misread.

## ADDED Requirements

### Requirement: Backend parse errors name the construct
When the search backend rejects a query, `POST /v1/search`, `search_code`, and `find_files` SHALL return the backend's reason, bounded in length and stripped of control characters, after the prefix `search query is invalid: `. REST SHALL keep status `400` and code `invalid_request`.

#### Scenario: Unclosed character class
- **WHEN** a user searches for `FROM node:[0-9 ]`
- **THEN** the error reads "search query is invalid: error parsing regexp: missing closing ]: `[0-9`" followed by a hint

#### Scenario: Extra parenthesis
- **WHEN** a user searches for `foo)`
- **THEN** the error names the unexpected `)`

### Requirement: Hints for known gotchas
Errors caused by whitespace splitting a term, such as an unclosed character class, SHALL add a hint that spaces separate query terms and that the term can be quoted or use `\s`. Errors about unbalanced parentheses SHALL add a hint that parentheses group query terms and must be escaped to match literally.

#### Scenario: Space inside a class
- **WHEN** a query fails with `missing closing ]`
- **THEN** the error adds `hint: spaces separate query terms; quote the term ("FROM node:[0-9 ]") or use \s`

### Requirement: Inline case flags are refused
A query containing an unescaped inline flag group that sets or clears case sensitivity, such as `(?i)`, `(?-i)`, or `(?i:`, SHALL be rejected before it reaches the backend, with a message naming the flag group and pointing to `case:no` or `case:yes`, because the backend mishandles them and returns wrong results. Non-capturing groups `(?:` SHALL pass through.

#### Scenario: Case-insensitive attempt
- **WHEN** a user searches for `(?i)brotli`
- **THEN** the request fails with `search query is invalid: inline flag (?i) is not supported; use case:no brotli`

#### Scenario: Escaped literal
- **WHEN** a user searches for `\(\?i\)` to find the literal text `(?i)`
- **THEN** the query is sent to the backend unchanged

#### Scenario: Non-capturing group
- **WHEN** a user searches for `FROM node:(?:22|20)`
- **THEN** the query is sent to the backend unchanged

### Requirement: GitHub search backend errors
When the GitHub code search backend rejects a query as invalid, the same prefix and GitHub's reason SHALL be returned instead of a generic unavailable error.

#### Scenario: GitHub validation failure
- **WHEN** GitHub responds `422` with a validation message for a query
- **THEN** the caller receives `400 invalid_request` with `search query is invalid: ` and GitHub's message
