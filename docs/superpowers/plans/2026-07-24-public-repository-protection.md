# Public Repository Protection Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make GrepNest safe to publish as source while preserving a practical solo-maintainer workflow.

**Architecture:** Reuse the two fixes already proven by PR #1, add only the missing local secret and reporting safeguards, then enable GitHub security settings. Keep visibility private until the owner changes it; immediately afterward, create one `main` ruleset and enable public-only security features.

**Tech Stack:** Git, GitHub Actions, Go 1.26.5, POSIX shell, GitHub REST API

## Global Constraints

- Keep `balcsida/grep-nest` private until the owner changes visibility.
- Commit directly to `main` as explicitly authorized.
- Every commit must be signed, atomic, conventional, single-line, and at most 72 characters.
- Keep Helm pinned to v4.2.3.
- Add no dependency, CODEOWNERS file, scheduled release workflow, or artifact publishing.
- Require the `verify`, `integration`, `e2e`, and `helm` check names on public `main`.
- Require zero approving reviews for the solo-maintainer pull-request rule.
- Do not configure an administrator bypass.

---

### Task 1: Land the patched Go toolchain

**Files:**
- Modify: `.github/workflows/ci.yml:18`
- Modify: `.github/workflows/ci.yml:28`
- Modify: `.github/workflows/ci.yml:42`
- Modify: `go.mod:3`

**Interfaces:**
- Consumes: existing signed commit `3d7d467e25b791a528c0e808c1b9dd583fff14d7`
- Produces: Go 1.26.5 as the module and CI toolchain floor

- [ ] **Step 1: Confirm the recorded failure**

Read GitHub Actions job `88348866846` from run `29741435806`.

Expected: `govulncheck` reports 13 reachable Go 1.26.0 standard-library
vulnerabilities, including `GO-2026-5856`, fixed in Go 1.26.5.

- [ ] **Step 2: Apply the existing signed fix**

Run:

```bash
git cherry-pick -S 3d7d467e25b791a528c0e808c1b9dd583fff14d7
```

Expected: one commit named `fix(ci): require patched Go toolchain` changes only
the four version lines.

- [ ] **Step 3: Verify the minimal fix**

Run:

```bash
rg -n 'go-version: "1\.26\.5"|^go 1\.26\.5$' .github/workflows/ci.yml go.mod
make test-race govulncheck
git diff HEAD^ --check
git status --short --branch
```

Expected: four version matches, both Make targets exit 0, no whitespace errors,
and `main` is ahead only by committed changes.

### Task 2: Remove the undeclared Helm-test dependency

**Files:**
- Modify: `deploy/helm/grepnest/tests/render.sh`

**Interfaces:**
- Consumes: existing signed commit `ace5e683868badb21820bd2124fd5ad3d5dc3afd`
- Produces: a Helm render test that uses POSIX `grep -E` instead of `rg`

- [ ] **Step 1: Confirm the recorded failure**

Read GitHub Actions job `88348866859` from run `29741435806`.

Expected: `deploy/helm/grepnest/tests/render.sh: 16: rg: not found` and exit 127.

- [ ] **Step 2: Apply the existing signed fix**

Run:

```bash
git cherry-pick -S ace5e683868badb21820bd2124fd5ad3d5dc3afd
```

Expected: one commit named `fix(ci): remove ripgrep dependency` changes only
the Helm render test.

- [ ] **Step 3: Verify the script and Helm pin**

Run:

```bash
sh -n deploy/helm/grepnest/tests/render.sh
make helm-lint helm-test
rg -n 'version: v4\.2\.3' .github/workflows/ci.yml
! rg -n '\brg\b' deploy/helm/grepnest/tests/render.sh
git diff HEAD^ --check
```

Expected: shell syntax and both Helm targets pass, Helm remains v4.2.3, the
script contains no `rg` command, and the diff has no whitespace errors.

### Task 3: Ignore common local secret files

**Files:**
- Modify: `.gitignore`

**Interfaces:**
- Consumes: current three-line ignore file
- Produces: ignore rules for environment files and common private-key bundles

- [ ] **Step 1: Record the missing ignore behavior**

Run:

```bash
git check-ignore .env service.env production.env.local signing.key signing.pem certificate.p12
```

Expected: exit 1 with no paths printed.

- [ ] **Step 2: Add the minimal rules**

Append exactly:

```gitignore

.env
*.env
*.env.*
!.env.example
*.key
*.pem
*.p12
*.pfx
```

- [ ] **Step 3: Verify the ignore behavior**

Run:

```bash
git check-ignore .env service.env production.env.local signing.key signing.pem certificate.p12
test "$(git check-ignore .env.example || true)" = ""
git diff --check
```

Expected: all six sensitive paths are printed, `.env.example` is not ignored,
and the diff has no whitespace errors.

- [ ] **Step 4: Commit**

Run:

```bash
git status --short
git add .gitignore
git commit -S -m "chore: ignore local secret files"
```

Expected: one signed commit changes only `.gitignore`.

### Task 4: Add usable private reporting channels

**Files:**
- Modify: `SECURITY.md`
- Modify: `CODE_OF_CONDUCT.md`

**Interfaces:**
- Consumes: GitHub advisory URL and existing commit email
- Produces: concrete private reporting routes for security and conduct reports

- [ ] **Step 1: Record the missing contact details**

Run:

```bash
rg -n 'security/advisories/new|balcsida@gmail\.com' SECURITY.md CODE_OF_CONDUCT.md
```

Expected: exit 1 with no matches.

- [ ] **Step 2: Replace the security reporting text**

Set `SECURITY.md` to:

```markdown
# Security

Report suspected vulnerabilities through
[GitHub private vulnerability reporting](https://github.com/balcsida/grep-nest/security/advisories/new).
Do not file public issues for vulnerabilities until a fix and disclosure plan
exist.
```

- [ ] **Step 3: Add the conduct contact**

Append to `CODE_OF_CONDUCT.md`:

```markdown

## Reporting

Report unacceptable behavior privately to
[balcsida@gmail.com](mailto:balcsida@gmail.com).
```

- [ ] **Step 4: Verify links and formatting**

Run:

```bash
rg -n 'security/advisories/new|mailto:balcsida@gmail\.com' SECURITY.md CODE_OF_CONDUCT.md
git diff --check
```

Expected: one advisory link, one mail link, and no whitespace errors.

- [ ] **Step 5: Commit**

Run:

```bash
git status --short
git add SECURITY.md CODE_OF_CONDUCT.md
git commit -S -m "docs: add private reporting channels"
```

Expected: one signed commit changes only the two policy files.

### Task 5: Verify and publish the private-repository changes

**Files:**
- Verify: all files changed by Tasks 1-4

**Interfaces:**
- Consumes: the four atomic implementation commits and design/plan commits
- Produces: a green, pushed `main` before any visibility change

- [ ] **Step 1: Run the repository verification gates**

Run:

```bash
make fmt lint staticcheck govulncheck test test-race postgres-integration integration e2e build
make helm-lint helm-test
git diff origin/main...HEAD --check
```

Expected: every target exits 0 and the complete unpublished diff has no
whitespace errors.

- [ ] **Step 2: Audit commit scope and signatures**

Run:

```bash
git status --short --branch
git log --show-signature --format='%h %G? %s' origin/main..HEAD
git diff --stat origin/main...HEAD
```

Expected: the worktree is clean, every new commit has signature status `G`,
and only the approved documentation, CI, shell, ignore, and policy files differ.

- [ ] **Step 3: Push `main`**

Run:

```bash
git push origin main
```

Expected: a fast-forward update of `origin/main`.

- [ ] **Step 4: Verify GitHub Actions**

Inspect the workflow run for the pushed `main` SHA.

Expected: `verify`, `integration`, `e2e`, and `helm` all complete successfully.

### Task 6: Enable private-repository security updates

**Files:**
- Remote settings: `balcsida/grep-nest`

**Interfaces:**
- Consumes: authenticated `gh` access to `github.com`
- Produces: vulnerability alerts and Dependabot security updates

- [ ] **Step 1: Confirm authentication**

Run:

```bash
gh auth status --hostname github.com
```

Expected: account `balcsida` is authenticated with repository administration
access. If the credential is stale, stop and request reauthentication rather
than exposing or replacing credentials.

- [ ] **Step 2: Enable vulnerability alerts**

Run:

```bash
gh api --method PUT repos/balcsida/grep-nest/vulnerability-alerts
```

Expected: HTTP 204.

- [ ] **Step 3: Enable Dependabot security updates**

Run:

```bash
gh api --method PUT repos/balcsida/grep-nest/automated-security-fixes
```

Expected: HTTP 204.

- [ ] **Step 4: Verify both settings**

Run:

```bash
gh api repos/balcsida/grep-nest/vulnerability-alerts --include
gh api repos/balcsida/grep-nest/automated-security-fixes
```

Expected: alerts return HTTP 204 and automated security fixes report
`enabled: true`.

### Task 7: Apply public-only protections after visibility changes

**Files:**
- Remote ruleset: `balcsida/grep-nest`
- Remote security settings: `balcsida/grep-nest`

**Interfaces:**
- Consumes: public repository visibility and authenticated `gh` administration
- Produces: enforced solo-maintainer `main` rules and public security features

- [ ] **Step 1: Wait for explicit public visibility**

Run:

```bash
gh repo view balcsida/grep-nest --json visibility --jq .visibility
```

Expected: `PUBLIC`. Do not change visibility in this plan.

- [ ] **Step 2: Create the active `main` ruleset**

Save this request body in a temporary file outside the repository:

```json
{
  "name": "Protect main",
  "target": "branch",
  "enforcement": "active",
  "bypass_actors": [],
  "conditions": {
    "ref_name": {
      "include": ["~DEFAULT_BRANCH"],
      "exclude": []
    }
  },
  "rules": [
    {"type": "deletion"},
    {"type": "non_fast_forward"},
    {"type": "required_signatures"},
    {
      "type": "pull_request",
      "parameters": {
        "required_approving_review_count": 0,
        "dismiss_stale_reviews_on_push": false,
        "require_code_owner_review": false,
        "require_last_push_approval": false,
        "required_review_thread_resolution": true,
        "automatic_copilot_code_review_enabled": false,
        "allowed_merge_methods": ["merge", "squash", "rebase"]
      }
    },
    {
      "type": "required_status_checks",
      "parameters": {
        "strict_required_status_checks_policy": true,
        "do_not_enforce_on_create": false,
        "required_status_checks": [
          {"context": "verify"},
          {"context": "integration"},
          {"context": "e2e"},
          {"context": "helm"}
        ]
      }
    }
  ]
}
```

Run:

```bash
gh api --method POST repos/balcsida/grep-nest/rulesets --input /tmp/grepnest-main-ruleset.json
```

Expected: HTTP 201 with `name: Protect main`, `enforcement: active`, and no
bypass actors.

- [ ] **Step 3: Enable public security features**

Run:

```bash
gh api --method PATCH repos/balcsida/grep-nest \
  -F security_and_analysis[secret_scanning][status]=enabled \
  -F security_and_analysis[secret_scanning_push_protection][status]=enabled
gh api --method PUT repos/balcsida/grep-nest/private-vulnerability-reporting
gh api --method PATCH repos/balcsida/grep-nest/code-scanning/default-setup \
  -f state=configured \
  -f query_suite=default \
  -f languages[]=go
```

Expected: repository security metadata reports both secret-scanning settings
enabled, private reporting returns HTTP 204, and CodeQL default setup reports
`configured`.

- [ ] **Step 4: Verify the final protection state**

Run:

```bash
gh api repos/balcsida/grep-nest/rulesets
gh api repos/balcsida/grep-nest
gh api repos/balcsida/grep-nest/private-vulnerability-reporting
gh api repos/balcsida/grep-nest/code-scanning/default-setup
```

Expected: one active `Protect main` ruleset with the four required checks,
signed commits, pull requests with zero approvals, conversation resolution,
deletion protection, non-fast-forward protection, and no bypass actors; the
repository is public with all requested security features enabled.
