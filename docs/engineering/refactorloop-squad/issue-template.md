# Refactoring direction

<!-- One paragraph: what feels wrong structurally and where it hurts. This is
     the only input the human gives. -->

## Pain / evidence (optional)

<!-- - file:line, e.g. "changing X always breaks Y" / "this module imports
     three layers down" / "tests for Z need the whole app running" -->

## Must not change

<!-- Behaviors, contracts, or outputs that stay byte-identical. Defaults to:
     everything the current test suite asserts. -->

## Out of scope (optional)

<!-- - areas not to touch this time: ... -->

## Done when

- The dispatched slice is refactored; the full test suite is green with no
  weakened or deleted assertions.
- The reviewer replied `VERDICT: APPROVE`.
- The architect recorded the verdict (`multica issue verdict <issue-id>
  accept` succeeded; the issue is done).
