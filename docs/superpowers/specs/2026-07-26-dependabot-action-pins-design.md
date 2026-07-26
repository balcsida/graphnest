# Dependabot and Action Pins

## Scope

- Add weekly Dependabot updates for Go modules, GitHub Actions, Docker Compose,
  and Helm.
- Group updates within each ecosystem to limit pull-request noise.
- Upgrade every action in `.github/workflows/ci.yml` to its latest release and
  pin it by full commit SHA with a version comment.

## Files

- `.github/dependabot.yml`
- `.github/workflows/ci.yml`

## Verification

- Parse both YAML files.
- Confirm every `uses:` reference has a 40-character SHA and version comment.
- Run the existing CI-oriented repository checks.
