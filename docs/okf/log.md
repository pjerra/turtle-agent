# Change log

## 2026-11-07

- **Added**: `raid-walk --practice <entry>` keeps one boss fight alive for mechanic practice on the test servers (owner-approved 2026-11-07): per-cycle `practice.jsonl` rows, GM sustain at each cycle, and a bounded reset (stop, `.npc evade`, revive, respawn the dead Sons, rest, resend instance data, resume). The mechanic table starts with Hakkar 14834 (Blood Siphon 24324 at 90 s, enrage 600 s, Sons 11357). The GM hands `gm_evade`, `gm_set_hp`, `gm_respawn` and `gm_revive` are closed-set, gated like `gm_kill`; the autopilot goal is `practice:<entry>:<cycles>`, met by cycles and never by a kill.
- **Update**: dev-to-main check. `raid-walk --gm-instance-data` sends on every launch (reuse restarts included) and again after each recovered wipe; the sent set lives in one out-dir marker per instance and leg, no ledger mark. The autopilot takes a fresh instance at once after a wipe at a boss for a `--gm-instance-data` goal, up to `--boss-wipe-max` (default 3: two relaunches, the third stops the id); a same-instance placement that finds half the hires dead or the start leg's boss on the party exits 29 with `fresh instance needed`. A boss wipe resumes from the goal's own start, the boss counts as a quiet-start hazard, and the quiet notes a later-row boss 25-30 yd out. The autopilot's slot rules, strict-first-read targets file, one bad line skipped, exit-29 retry, done-id slot frees, `--ray` on `path-check`, the 16-entry plan list cap, the ZG trash lists, the lease `reap` command with heartbeat renew and dead-holder reaping, the `taxi` RPC with the `taxi_reply` event, and `ci/**` CI branches are covered in their pages.

## 2026-11-05

- **Update**: fact-check fixes against the code (commands, flags, defaults, errors, plan fields).
- **Update**: the README is now a short overview; its detail moved into these docs (see [the README map](about/readme-map.md)).
- **Creation**: the eight area folders, 153 pages: getting started, the agent and its RPC, dungeon runs, raids, plans, navigation, combat, and observability. Every page is `status: draft` until a person reviews it.
- **Creation**: the bundle, its root index and the [conventions](about/conventions.md) page. Eight area folders are being written from the code: getting started, the agent and its RPC, dungeon runs, raids, plans, navigation, combat, and observability.
