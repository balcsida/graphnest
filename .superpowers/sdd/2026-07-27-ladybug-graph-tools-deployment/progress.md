# SDD ledger — plan: docs/superpowers/plans/2026-07-27-ladybug-graph-tools-deployment.md
Task 1: fix round 1/5 (2 addressed, 1 open — PostgreSQL execution evidence; commits e0ad585..35752c6)
Task 1: fix round 2/5 (1 addressed, 0 open — evidence-only, no commit)
Task 1: complete (commits 051ebdf..35752c6, review clean)
Task 2: minor (deferred): validRelations rebuilds its allowlist map per iteration
Task 2: fix round 1/5 (2 addressed, 1 open — partial-boundary propagation coverage; commits a9afb01..d694007)
Task 2: fix round 2/5 (1 addressed, 0 open — commits d694007..6125408)
Task 2: complete (commits 35752c6..6125408, review clean)
Task 3: minor (deferred): OpenAPI checker lacks narrow response/depth assertions
Task 3: fix round 1/5 (1 addressed, 1 open — trace success discriminator mismatch; commits f91feac..7f9d498)
Task 3: fix round 2/5 (1 addressed, 0 open — commits 7f9d498..bc57e22)
Task 3: complete (commits 6125408..bc57e22, review clean)
Task 4: fix round 1/5 (2 addressed, 1 open — context per-category default/cap; commits 8a35409..2c2112e)
Task 4: fix round 2/5 (0 addressed, 1 open — assert context default equals 100; commits 2c2112e..9b10661)
Task 4: fix round 3/5 (1 addressed, 0 open — commits 9b10661..a284651)
Task 4: minor (deferred): other capped-default schema tests assert presence, not exact values
Task 4: complete (commits bc57e22..a284651, review clean)
Task 5: fix round 1/5 (3 addressed, 0 open — commits d37b40d..924b9dc)
Task 5: complete (commits a284651..924b9dc, review clean)
Task 6: minor (deferred): Guide says every response has commits, but list_repositories does not
Task 6: fix round 1/5 (1 addressed, 0 open — commits f721dee..b443169)
Task 6: complete (commits 924b9dc..b443169, review clean)
Task 7: minor (deferred): render_graph duplicates the common Compose render setup
Task 7: fix round 1/5 (1 addressed, 1 open — global writable-mount uniqueness; commits 718adbb..5f89cd0)
Task 7: fix round 2/5 (1 addressed, 0 open — commits 5f89cd0..5e9cb96)
Task 7: complete (commits b443169..5e9cb96, review clean)
Task 8: minor (deferred): Helm resource assertions are partly global and omit scheduling overrides
Task 8: fix round 1/5 (2 addressed, 2 open — arbitrary-UID source proof and unsupported shell tooling; commits 21a7f1e..0f1027d)
Task 8: fix round 2/5 (1 addressed, 1 open — Kubernetes projected-secret symlink support; commits 0f1027d..6f91cc5)
Task 8: fix round 3/5 (0 addressed, 2 open — intermediate-path TOCTOU and pre-read descriptor validation; commits 6f91cc5..7ce691d)
Task 8: fix round 4/5 (3 addressed, 0 open — commits 7ce691d..70d271a)
Task 8: minor (deferred): secretstage leaves sourceRoot open on two invalid-destination returns
Task 8: complete (commits 5e9cb96..70d271a, review clean except deferred minors)
Task 9: minor (deferred): architecture calls graph secret staged without qualifying Helm versus Compose
Task 9: fix round 1/5 (1 addressed, 2 open — manual-recovery semantics and upload persistence path; commits 2c6d2fa..9571c57)
Task 9: fix round 2/5 (2 addressed, 0 open — commits 9571c57..75dd7e2)
Task 9: complete (commits 70d271a..75dd7e2, review clean)
Task 10: fix round 1/5 (4 addressed, 0 open — commits 16b4cc9..82d6cda)
Task 10: complete (commits 75dd7e2..82d6cda, review clean)
Final review: fix wave 1/1 (Critical, 3 Important, and 8 Minor findings addressed; code commits 48313a1..cd6868d; all required gates green)
