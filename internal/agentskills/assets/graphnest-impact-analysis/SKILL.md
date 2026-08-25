---
name: graphnest-impact-analysis
description: Use when planning a code change or preparing a review-ready blast-radius and test-impact note.
---

# Impact Analysis with GraphNest

**REQUIRED BACKGROUND:** Read `graphnest-guide`.

1. Resolve the target:
   `context({"repo":"acme/api","name":"auth.ValidateToken","file_path":"internal/auth/token.go"})`.
   If `ambiguous`, record the candidates and retry with the selected candidate's `repository_id` as `repo` and its `uid`; if unresolved, stop.
2. Traverse dependents and tests:
   `impact({"repo":"acme/api","target_uid":"<uid>","direction":"upstream","max_depth":3,"limit":100,"include_tests":true,"min_confidence":0.5})`.
3. Use `offset` for another page only when `partial: true` or the result cap requires it. Increase depth only when the review scope requires it.

Write the note in this exact shape:

```markdown
## Impact: <target>
Target: <UID and ambiguity resolution>
Scope: upstream, depth 3, confidence >= 0.5, tests included
May break: <symbols grouped by depth>
Tests: <returned test symbols, plus missing coverage to add>
Evidence limits: <partial, boundaries, low-confidence exclusions>
Graph commits: <repository=commit entries>
```

Treat `by_depth` as reachability, not severity. State when confidence filtering may hide edges. Boundaries and graph commits are required evidence fields, even when empty.

| Condition | Action |
|---|---|
| `status: ambiguous` | Record candidates; resolve before impact |
| `partial: true` | Report it; paginate within the agreed scope |
| boundary present | Name the excluded scope; do not claim completeness |
