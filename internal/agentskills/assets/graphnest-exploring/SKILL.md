---
name: graphnest-exploring
description: Use when onboarding to an unfamiliar repository or explaining how known code symbols connect.
---

# Exploring with GraphNest

**REQUIRED BACKGROUND:** Read `graphnest-guide`.

Start narrow and expand only from returned UIDs:

1. Call `list_repositories({})`; select the exact repository name or ID.
2. Resolve the entry symbol with `context({"repo":"acme/payments","name":"ProcessPayment","per_category_limit":20})`.
3. If `ambiguous`, retry `context` with the candidate `repository_id` as `repo` and its `uid`. Inspect its categorized `incoming` and `outgoing` edges; use `relations` to restrict noise.
4. Resolve a relevant neighboring symbol with the same repository-qualified UID pattern.
5. Confirm only the relationship you need with a bounded `trace({"repo":"acme/payments","source_uid":"...","target_uid":"...","max_depth":4})`.

Stop when the direct context answers the question or one trace confirms each claimed flow. Do not recursively expand every neighbor. Report `commits`, `boundaries`, confidence, and `no_path`; a missing path or boundary is not proof of no relationship.

| Need | Tool |
|---|---|
| Choose scope | `list_repositories` |
| Discover immediate relationships | `context` |
| Confirm a connection between known UIDs | `trace` |

Do not invent depth or symbol fields for `context`.
