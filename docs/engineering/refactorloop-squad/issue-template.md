# Refactoring direction

<!-- One paragraph: what feels wrong structurally and where it hurts. This is
     the only input the human gives. -->

<!-- For a system-level direction (layer separation, module split, interface
     extraction across a boundary), name the boundary explicitly: which layer
     owns what after the change, and which imports must stop crossing it. A
     vague "refactor the project" produces one small safe slice and the squad
     accepts on round 1. A pointed direction lets the architect mine a
     multi-point roadmap that the squad works through across rounds. -->

## Pain / evidence (optional but decisive for system-level work)

<!-- - file:line, e.g. "changing X always breaks Y" / "this module imports
     three layers down" / "tests for Z need the whole app running"
     For system-level directions, list at least 2-3 sites that share the same
     structural pain — they become the ranked points on the architect's
     roadmap. -->

## Must not change

<!-- Behaviors, contracts, or outputs that stay byte-identical. Defaults to:
     everything the current test suite asserts. -->

## Out of scope (optional)

<!-- - areas not to touch this time: ... -->

## Done when

- The architect's first mine produced a ranked roadmap (more than one point
  when the direction names a system-level concern), and every point on it is
  either applied in a round, explicitly descoped with a reason, or reported
  as needing a behavior change (which goes back to the human).
- The dispatched slice is refactored; the full test suite is green with no
  weakened or deleted assertions.
- The reviewer replied `VERDICT: APPROVE`.
- The architect recorded the verdict (`multica issue verdict <issue-id>
  accept` succeeded; the issue is done).
