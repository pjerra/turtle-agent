---
type: Guide
title: Start and run the dashboard
description: The dashboard flags, which agents and folders it reads, and how to reach it from another machine.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/dashboard_cmd.go
tags: [dashboard, guide]
sources:
  - id: dashboard-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/dashboard_cmd.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:37:00+01:00
status: draft
---

# Start and run the dashboard

The dashboard polls running agents over loopback RPC and serves one page. It is read-only unless you add `--edit` or `--control`.
The agents it watches are covered in [the agent and its RPC](/agent/index.md); the dungeon and raid runs it shows in [dungeon runs](/dungeons/index.md) and [raids](/raids/index.md).

## 1. Serve the page for every agent in your settings

```sh
turtle-agent dashboard
```

This listens on `127.0.0.1:8790` and watches `auto`: the `rpc` field of each `settings*.json` in the config dir. The agent list is re-read every 10 s, so an agent started later appears on its own.

## 2. Watch a named set of agents

```sh
turtle-agent dashboard --agents 8765,8766
```

A comma list of RPC ports or addresses replaces `auto`; `auto,8770` adds one to the automatic set. Only loopback addresses are accepted.

## 3. Point it at your folders

```sh
turtle-agent dashboard --plans-dir <walk-dir>/dungeons --tests-dir <walk-dir> --traces-dir ~/turtle-agent/traces --minimap-dir ~/turtle-agent-minimaps --mmaps-dir <walk-dir>/mmaps
```

Every flag and its default:

| Flag | Default | What it does |
| --- | --- | --- |
| `--listen` | `127.0.0.1:8790` | address the page is served on; a non-loopback address prints a warning |
| `--allow-host` | none | host names or IPs the page accepts in its `Host` header besides loopback, comma separated; anything else gets 403 |
| `--agents` | `auto` | `auto`, a comma list of RPC ports/addresses, or `auto,8770` to add one |
| `--tests-dir` | `~/TortoiseBots-audit/tests` | the only folder `/files/...` serves; a missing folder means no files are linked |
| `--traces-dir` | `~/turtle-agent/traces` | where agents write run traces; read for orders and the binary SHA, never served |
| `--plans-dir` | `~/turtle-agent/dungeons` | plan files for the map, comma separated; reloaded when a file changes |
| `--minimap-dir` | `~/turtle-agent-minimaps` | map images from `minimap-extract`; the run map draws over the image of its map id |
| `--embed-from` | none | extra `http://host:port` origins that may show the page in a frame |
| `--config-dir` | `~/.config/turtle-agent` | settings files and the `rpc-<port>.token` files |
| `--mmaps-dir` | none | the navmesh folder of the server the plans run on; enables the real-route plan view |

The table above leaves out the `--edit*` and `--control*` flags on purpose: route editing lives in [Turn on route editing](/observability/route-editing-guide.md) and run controls in [Control a run from the dashboard](/observability/run-controls-guide.md), with every flag also listed in [Command and flag reference](/observability/commands-reference.md).

The page polls each agent every 1.5 s (`Interval`), with a 2 s bound per agent (`CallTimeout`). It calls four RPC methods only: `status`, `dungeon_status`, `tune_list`, and `bot_view`.

## 4. Open the page from another machine

Serve on a Tailscale address and name it:

```sh
turtle-agent dashboard --listen <tailscale-ip>:8790 --allow-host <machine>.<tailnet>.ts.net
```

A non-loopback `--listen` prints `dashboard: warning: ... is not loopback; anyone who can reach it sees the runs and the files under --tests-dir`. The `--allow-host` name is what you open the page by; it is never trusted as a caller without `--edit-trust-local` or `--control-trust-local`.

## 5. Draw the real route on the plan view

With `--mmaps-dir`, the plan view (`#/plan/<name>`) draws the path the bots walk, not just the plan file's points: every walk leg to leg (`link: true`), pull-group approaches cut at the look point, amber dotted `direct` hops, violet `check_skip` and `gm_cross` hops, and unpathable segments dashed red with the reason on hover. Without a navmesh the endpoint answers 503.

## 6. Real route numbers

The real-route endpoint (`GET /api/plan/<name>/route[?agent]`) re-anchors plan stops to the navmesh and restyles hops: plain walks, `direct` amber dotted, `check_skip`/`gm_cross` violet, unpathable red dashed with the reason on hover. One route computes at a time; the answer caches by plan SHA (≤48 kept). Route editing versions feed the same view; see [Turn on route editing](/observability/route-editing-guide.md).
