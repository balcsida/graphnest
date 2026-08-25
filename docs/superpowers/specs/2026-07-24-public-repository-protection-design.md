# Public Repository Protection Design

## Goal

Prepare GraphNest for public source visibility without publishing it, then apply
solo-maintainer branch and security protections as soon as GitHub permits them.

## Current constraints

- The repository is private and its current GitHub plan rejects branch
  protection and repository rulesets until it becomes public or the owner
  upgrades to GitHub Pro.
- `main` is clean at `0be518a`, but its latest CI run fails `verify` because Go
  1.26.0 contains reachable standard-library vulnerabilities and fails `helm`
  because the render test assumes `rg` is installed.
- The history and tracked files contain no confirmed secrets or private
  workplace identifiers. Documented development tokens and pinned commit SHAs
  account for the secret scanner's matches.

## Private-repository preparation

Keep the repository private. Commit the following signed, atomic changes
directly to `main`, as explicitly authorized:

1. Reuse the existing Go 1.26.5 CI fix from `3d7d467`.
2. Reuse the existing POSIX `grep` Helm-test fix from `ace5e68`.
3. Ignore common environment and private-key files while retaining an
   exception for example environment files.
4. Direct security reports to GitHub private vulnerability reporting and use
   the existing commit email for private Code of Conduct reports.

CI is pinned to Helm v4.2.3 because its schema error paths match the render
harness.

Enable vulnerability alerts and Dependabot security updates when authenticated
GitHub settings access is available. Do not add scheduled dependency-update
configuration, CODEOWNERS, release automation, or artifact publishing: the
existing vulnerability check covers Go code, solo ownership makes CODEOWNERS
redundant, and the project explicitly has no releasable image yet.

## Public-repository settings

After the owner changes visibility to public, apply one active ruleset to
`main`:

- require a pull request with zero approvals;
- require `verify`, `integration`, `e2e`, and `helm` on the latest base;
- require signed commits and resolved review conversations;
- block direct pushes, force-pushes, and branch deletion;
- provide no administrator bypass.

Enable secret scanning, push protection, private vulnerability reporting, and
CodeQL default setup. Keep the existing read-only workflow token and
immutable action pins.

## Verification and handoff

Before pushing each change, run its smallest relevant local check, then run the
documented full verification gates. Confirm all four GitHub checks pass on the
new `main` SHA. Re-read repository settings after every remote mutation.

Because GitHub cannot protect this private repository on its current plan,
publication is a two-step handoff: the owner makes it public, then the ruleset
and public-only security features are applied and verified immediately.
