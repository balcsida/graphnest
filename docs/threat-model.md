# Threat Model

## Protected Assets

- private source and repository metadata;
- bearer and future GitHub installation credentials;
- authorization scope and Zoekt repository filters;
- service and index availability.

## Milestones 0-1 Controls

- Zoekt is internal and cannot authenticate callers itself;
- static bearer tokens are secret-backed and compared in constant time;
- repository IDs sent to Zoekt are selected by the server;
- request, query, result, timeout, and response sizes are bounded;
- errors and logs exclude tokens, authorization headers, and unbounded source;
- fixture indexing invokes pinned executables with argument arrays and never
  executes repository code.

## Deferred Risks

GitHub webhook validation, installation-token handling, custom enterprise CAs,
durable job leases, container isolation, and network policies belong to
Milestones 2 and 3. Their absence prevents a production-readiness claim.
