---
name: grepnest-debugging
description: Use when logs, errors, or a failing test identify a code symbol and graph evidence could narrow the cause or blast radius.
---

# Debugging with GrepNest

**REQUIRED BACKGROUND:** Read `grepnest-guide`.

Anchor the investigation at the observed failure, not a guessed cause:

1. Resolve it: `context({"repo":"acme/payments","name":"receipts.Deliver","file_path":"receipts/deliver.go"})`. On `ambiguous`, retry with the candidate `repository_id` as `repo` and its `uid`.
2. Find inputs/callers: `impact({"repo":"acme/payments","target_uid":"<observed>","direction":"upstream","max_depth":3,"limit":50,"include_tests":true})`.
3. Find affected code: `impact({"repo":"acme/payments","target_uid":"<observed>","direction":"downstream","max_depth":3,"limit":50,"include_tests":true})`.
4. Correlate nearby candidates with the deployed diff, logs, and failing input; select the single best-supported one. The graph shows relationships, not which change caused the incident.
5. Confirm that one hypothesis: `trace({"repo":"acme/payments","source_uid":"<candidate>","target_uid":"<observed>","max_depth":6})`.

Report the confirmed path, unconfirmed candidates, tests, confidence, `partial`, boundaries, and graph commits. `no_path` disproves only the bounded graph path.

| Mistake | Correction |
|---|---|
| Starting at a suspected fix | Start at the logged or failing symbol |
| Calling `impact` with a name | Resolve a UID with `context` first |
| Declaring the nearest node causal | Correlate it with runtime and change evidence |
