# Dependency upgrade

| | |
| --- | --- |
| Dependency | <!-- package / tool name --> |
| Current → Target | <!-- e.g. 1.4.2 → 2.0.1 (exact versions) --> |
| Why | <!-- security fix / EOL / bug fix / feature --> |

## Changelog / release notes

<!-- link(s) to the authoritative sources, if you have them -->

## Suspected breaking changes (optional)

<!-- - ... -->

## Hints (optional)

<!-- - Where the dependency is used: ... -->
<!-- - Out of scope: ... -->

## Done when

- Manifest and lockfile are updated; build, typecheck, and tests pass.
- The verifier replied `VERDICT: ACCEPT`.
- `multica issue verdict <issue-id> accept` succeeded (the issue is done).
