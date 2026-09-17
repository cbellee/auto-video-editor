## Agent skills

### Issue tracker

Issues and specs are tracked in this repository's GitHub Issues. See `docs/agents/issue-tracker.md`.

### Triage labels

Triage uses the five canonical default label names. See `docs/agents/triage-labels.md`.

### Domain docs

Domain documentation uses a single-context layout. See `docs/agents/domain.md`.

## Ticket implementation workflow

- Never implement a ticket directly on `main`.
- Before starting, require a clean working tree, switch to `main`, and update it with `git pull --ff-only`.
- Create a branch from `main` named `issue-<number>-<short-description>`.
- Implement, test, and review the ticket on that branch.
- Commit with the issue number in the commit subject.
- Push the branch and create a pull request targeting `main`.
- Include `Closes #<number>` in the pull request body.
- Do not close the issue manually; allow merging the pull request to close it.
- Never force-push or rewrite shared branch history without explicit approval.

## Go development

Before generating, modifying, or reviewing Go code, invoke the `golang-pro` skill and follow its guidance. This applies to implementation, tests, refactoring, and code review.
