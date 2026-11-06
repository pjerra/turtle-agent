---
type: Guide
title: Turn on route editing
description: Let the dashboard save navmesh-checked versions of a plan's route, with tokens.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/dashboard_cmd.go
tags: [dashboard, route-editor]
sources:
  - id: dashboard-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/dashboard_cmd.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:38:00+01:00
status: draft
---

# Turn on route editing

Without `--edit` the page is GET/HEAD only. With it, the one thing the page can write is a checked version of a plan's route, through `POST /api/edit/<plan>`.

## 1. Make a token file

Create a file of `label token` lines, mode 0600, owned by you. The label is recorded as who made a version.

## 2. Start the dashboard with the editor on

```sh
turtle-agent dashboard --mmaps-dir <walk-dir>/mmaps --plans-dir <walk-dir>/dungeons --edit --edit-dir ~/turtle-agent-route-edits --edit-tokens ~/.config/turtle-agent/route-edit-tokens
```

Every flag and its default:

| Flag | Default | What it does |
| --- | --- | --- |
| `--edit` | off | turns the editor on (on/off: takes `=`, e.g. `--edit=true`) |
| `--mmaps-dir` | none, required with `--edit` | the navmesh of the server the plans run on; every version records it |
| `--edit-source` | the first `--plans-dir` | whose plan files an edit starts from; must not lie inside any slot's `dungeons_dir` |
| `--edit-dir` | `~/turtle-agent-route-edits` | where versions go: `<plan>/vNNNN.json`, `vNNNN.meta.json`, `vNNNN.diff` |
| `--edit-tokens` | `~/.config/turtle-agent/route-edit-tokens` | `label token` lines, mode 0600 |
| `--edit-allow-default-dir` | none | settings files with an `rpc` but no `dungeons_dir` you vouch are not the edits folder; any other such file stops the editor |
| `--edit-trust-local` | off | saves from this machine need no token typed (read the threat model first) (on/off: takes `=`, e.g. `--edit-trust-local=true`) |
| `--edit-trust-label` | `owner` | the token label trusted saves are made as |

## 3. Edit a route on the plan view

Open `#/plan/<name>`. The route is editable at once: there is no Edit button. Drag a point to move it, click a line to add a point there, click a point and press Delete to remove it, Ctrl+Z / Ctrl+Y undo and redo. Locked points (packs, bosses, steps, legs, `direct` or `check_skip` via points) are grey. Save asks for the editing token (kept in the tab only, sent in the `Authorization` header).

## 4. Read the save answer

The server applies your ops to a copy of the base file it reads itself, snaps each point to the navmesh floor, and runs the plan check on the whole plan. A version is `checked` only when the edited plan has no plan-check issue; an edit that adds none to a plan that already has issues is kept as `unchecked`. An edit that makes a new issue is refused with HTTP 422 and keeps only the record and the diff.

## 5. Skip typing the token on your own laptop

Add `--edit-trust-local` (needs `--edit`). Then a save with no `Authorization` header, from a loopback socket owned by the dashboard's user or from a literal Tailscale address in `--allow-host`, is made as the `--edit-trust-label` token's owner. The token itself never reaches the page. Start with the threat model in the README ("No token typing on your own laptop"): names are never trusted as callers, a relay of another user is not trusted, and any request carrying `Authorization` is judged by that token alone.

## 6. Ops, gates, limits

A save posts `{base_sha256, note, ops[{leg, kind: move|insert|delete, index, x, y}]}`; points snap to the navmesh floor nearest the old height (±25 yd window; a choice is flagged `floor_picked`) and must lie within 200 yd of the plan's own points. Locked kinds (packs, bosses, steps, `direct`/`check_skip` via points) stay grey. A version covering a plan that already has plan-check issues is `unchecked`; edits adding a new issue get HTTP 422. `route-edits list/export/promote` moves a version into the run folders; the trace records it as `trace_meta.route_edit`.
