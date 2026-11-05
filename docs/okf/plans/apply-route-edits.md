---
type: Guide
title: Apply route edits
description: Move a checked dashboard route version back into dungeons for a PR.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/route_edits_cmd.go
tags: [plans, route-edits]
sources:
  - id: route-edits
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/route_edits_cmd.go
  - id: routeedit
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/routeedit/export.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:58:00+01:00
status: draft
---

# Apply route edits

The dashboard route editor records versions of a plan's route outside the repo: only `via` and `to` positions are editable (`move`, `insert`, `delete` operations)[^route-edits]. `route-edits` moves a checked version back into `dungeons/`.

## 1. List the versions

```sh
turtle-agent route-edits list [<plan>]
```

Each line names the plan, the version (`vNNNN`), its status (`checked`, `unchecked`, `refused`, `accepted`), who wrote it, and how many operations it holds. `--json` prints the records as JSON.

## 2. Export a version for a stopped slot

```sh
turtle-agent route-edits export --to <fresh-dir> <plan> [vN|latest]
```

`export` copies the newest checked version (or the named one) into a fresh folder as `<plan>.json` and prints the run tag (`route_edit=<plan>@vNNNN sha256=...`); pass that tag back to the run so the report records which route it used. The target folder must not exist or must be empty, and it must not overlap a running agent's plan folder. A refused or unchecked version is never exported.

## 3. Promote a version into a worktree

```sh
turtle-agent route-edits promote --worktree <path> <plan> <vN>
```

`promote` writes the version over `dungeons/<plan>.json` of a worktree for a PR and prints the commit message (`--message-file FILE` also writes it to a file). The version's base must match the worktree file: when it does not, edit again from the new file instead of forcing it.
