# Specification lifecycle

> Status: Current contract
> Last reviewed: 2026-07-16
> Authority: Repository documentation policy.

The documents in this directory have an explicit lifecycle. Check a document's
status before treating it as current behavior.

## Statuses

- **Proposed** — a design under consideration. It is not an implementation
  commitment until accepted and implemented.
- **Current contract** — behavior, security properties, or compatibility rules
  that the implementation and tests must preserve.
- **Implemented decision** — historical context, rationale, and lasting
  tradeoffs for completed work. Current code, migrations, and tests define the
  implementation details.
- **Superseded** — retained only when its historical context remains useful. The
  document must link to its replacement.

## Metadata

Each document starts with its status, the date it was last reconciled against
repository evidence, and its authority:

```markdown
> Status: Current contract
> Last reviewed: YYYY-MM-DD
> Authority: Behavioral contract; current code and tests provide implementation evidence.
```

`Last reviewed` records an actual comparison with current code, migrations, or
tests. Git history and blame can identify when text changed, but do not prove
that a review occurred.

## Maintenance

Current contracts retain externally relevant behavior, security boundaries,
and compatibility rules. Avoid duplicating implementation structure that is
clearer in source or tests.

Implemented decisions retain the problem, decision, durable rationale,
tradeoffs, and outcome. Remove delivery plans, embedded migrations, exhaustive
test checklists, and stale future-version promises after implementation.

When behavior changes, update the relevant current contract in the same change.
When a proposal is completed, either promote its behavioral parts to a current
contract or condense it into an implemented decision.
