# Public Repository UI Smoke

## Goal

Run a real-browser smoke test on every pull request against the embedded GrepNest UI and two public GitHub repositories at fixed revisions. The smoke must catch failures in static inventory, repository filtering, searching, non-interactive static result paths, and exact indexed-source links without requiring credentials or executing repository code.

## Test topology

The repository will add an exact-pinned `@playwright/test` development dependency and committed npm lockfile. A dedicated POSIX shell harness will create a temporary workspace, fetch only these revisions, and verify each checkout before indexing:

- `octocat/Hello-World` at `7fd1a60b01f91b314f59955a4e4d4e80d8edf11d`
- `octocat/Spoon-Knife` at `d0dd1f61b33d64e29d8bc1372a94ef6a2fee76a9`

The harness will assign distinct Zoekt IDs and names, build one temporary index, write a matching static registry, start Zoekt and GrepNest on loopback, wait for readiness, and run one Playwright Chromium spec through the production bearer-token page. A trap will stop child processes, print service logs on failure, and remove the temporary workspace.

The Playwright spec will sign in with a development-only token, verify both inventory rows and their short SHAs, verify both repository filter options, search each repository for stable pinned content, and assert that only the selected repository renders. Each result path must not be a button, and `Open indexed source` must point to the selected repository, exact pinned SHA, file, and line anchor. External GitHub links will not be opened.

## CI and developer entry points

`make ui-smoke` will install no dependencies; it will require Node modules and Playwright Chromium to be present, then run the harness. A separate `ui-smoke` job in the existing CI workflow will use the SHA-pinned Node setup Action with its native `lts/*` selector, run `npm ci`, install Playwright Chromium with its system dependencies, install the pinned Zoekt tools through the existing Make target, and invoke `make ui-smoke`. The separate job keeps failures visible and runs on every pull request through the existing workflow triggers.

## Boundaries

The test uses no GitHub token, disables interactive Git authentication and submodules, and runs no checked-out repository code. The public repositories remain an intentional network dependency; pinned SHAs make content deterministic but cannot eliminate GitHub availability failures. No application behavior, release image, database, or authentication policy changes are included.
