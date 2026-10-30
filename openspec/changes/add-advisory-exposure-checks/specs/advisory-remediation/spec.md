# Spec Delta

## Purpose

Track how the list of exposed repositories shrinks: record who decided a
repository is not affected or accepted the risk, show signs that a fix is
underway, and keep the history that produces the burn-down.

## ADDED Requirements

### Requirement: Exceptions
An administrator, or a principal holding the repository's supply-chain review grant, SHALL be able to record an exception of kind `not_affected`, `risk_accepted`, or `rebuilt` for one advisory and repository, with a required reason. `not_affected` and `risk_accepted` SHALL require an expiry at most one year ahead.

#### Scenario: Risk accepted
- **WHEN** a review-grant holder records `risk_accepted` with a reason and an expiry in 90 days for an open repository
- **THEN** the repository's status becomes `excepted`, it leaves the open count, and the exception shows the reason, recorder, and expiry

#### Scenario: No grant
- **WHEN** a user with read access but no review grant tries to record an exception
- **THEN** the request fails with `403 forbidden`

#### Scenario: Expiry too far
- **WHEN** an exception's expiry is more than one year ahead
- **THEN** it is rejected with a message naming `expires_at`

### Requirement: Exceptions bind to the evidence they were made on
Every exception SHALL store a fingerprint of the repository's counted evidence and SHALL be recorded only when the caller echoes the current fingerprint as `basis`, otherwise `409 stale_basis`. When the counted evidence changes or the expiry passes, the exception SHALL lapse and the repository SHALL return to its computed status.

#### Scenario: Evidence changes
- **WHEN** a repository with a `not_affected` exception adds a new vulnerable Dockerfile
- **THEN** the exception lapses, the repository returns as `vulnerable`, and the history records why the exception lapsed

#### Scenario: Stale basis
- **WHEN** a reviewer submits an exception with a fingerprint from before the latest re-check
- **THEN** the request fails with `409 stale_basis` and nothing is recorded

### Requirement: Rebuilt applies to floating tags only
An exception of kind `rebuilt` SHALL be accepted only while the repository's computed status is `needs_rebuild`. It SHALL need no expiry and SHALL lapse when the evidence changes.

#### Scenario: Rebuilt on a pinned version
- **WHEN** a reviewer records `rebuilt` for a repository whose status is `vulnerable`
- **THEN** the request fails with `400 invalid_request` explaining that `rebuilt` only applies to floating tags

### Requirement: Exceptions are append-only
Exceptions SHALL never be edited or deleted. A new exception or a `withdrawn` record SHALL supersede the previous one, and all records SHALL stay in the repository's history.

#### Scenario: Withdrawal
- **WHEN** a reviewer withdraws an exception
- **THEN** the repository returns to its computed status and both records remain in the history

### Requirement: Status history
GraphNest SHALL keep an append-only history per advisory and repository of status changes with time, commit, and advisory version, plus exceptions, lapses, signals first seen, and advisory edits, closures, and reopenings. History entries SHALL stay readable after the snapshots they describe are pruned.

#### Scenario: Repository timeline
- **WHEN** a user opens a repository in an advisory
- **THEN** the timeline shows when it became vulnerable and at which commit, each re-check against a new advisory version, and when and at which commit it was fixed

### Requirement: Burn-down
Each advisory SHALL expose, for the caller's authorized repositories, the number of open repositories per UTC day from its first check to now, marking the advisory's publication date and the days it was edited. Excepted repositories SHALL NOT count as open.

#### Scenario: Shrinking list
- **WHEN** 71 authorized repositories were open on the day of the first check and 53 are open now
- **THEN** the series starts at 71, ends at 53, and marks the day version 2 was published

### Requirement: Fix signals
For open repositories of open advisories, GraphNest SHALL show read-only signals at least hourly: pull requests whose title or body mentions the advisory id or an alias, with state; branches whose names mention them; and whether the counted evidence changed since the first check. Signals SHALL NOT change a repository's status.

#### Scenario: Pull request found
- **WHEN** a pull request titled `chore: bump node to 22.21.1 (CVE-2026-41827)` is open in an affected repository
- **THEN** the repository row shows that pull request as an open signal and its status is unchanged

#### Scenario: Branch found
- **WHEN** an affected repository has a branch `fix/cve-2026-41827`
- **THEN** the row shows that branch as a signal

### Requirement: GitHub lookups degrade visibly
Owner and signal lookups SHALL only read from GitHub. When the GitHub App lacks `Pull requests: read`, pull request signals SHALL be reported as unavailable naming that permission. When it lacks `Administration: read`, admin teams SHALL be skipped and reported the same way. Rate limiting SHALL defer lookups, never fail a report.

#### Scenario: Missing pull request permission
- **WHEN** the installation does not grant `Pull requests: read`
- **THEN** each affected row says pull request signals are unavailable because the App lacks `Pull requests: read`, and branch signals still appear

#### Scenario: Rate limited
- **WHEN** GitHub rate-limits a signal refresh
- **THEN** the previous signals stay visible with their last refresh time and the refresh is retried later
