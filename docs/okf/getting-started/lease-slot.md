---
type: Guide
title: Lease a slot and set it up
description: Take a test slot with a lease, build its private client dir, start the agent, and tear it down.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/slot_setup_cmd.go
tags: [getting-started, slots, lease, teardown]
sources:
  - id: slot-setup
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/slot_setup_cmd.go
  - id: lease
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/lease_cmd.go
  - id: teardown
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/teardown_cmd.go
  - id: slots
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/slots/config.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:25:00+01:00
status: draft
---

# Lease a slot and set it up

On a shared laptop each test character belongs to a slot, and a lease hands that slot to one worker at a time. `slot setup` then builds a private client dir for the lease: its own binary, `settings.json`, `slot.json`, and folders. `teardown` ends the game presence, stops the agent, and frees the lease.

## Steps

1. Take the slot:

   ```sh
   turtle-agent lease acquire <server> --slot <slot> --holder <name> --ttl 180
   ```

   The answer names the account, character, port and lease id: `leased <server>/<slot> to <name> until <time>: account <account>, character <character>, port <port>, lease <id>`. Keep the lease id: renew and release need it. A holder name holds 1-64 letters, digits, `.` `_` `-`. The default lease is 180 minutes (1-720).

2. Or take any free slot:

   ```sh
   turtle-agent lease acquire <server> --any --holder <name>
   ```

3. Build the client dir:

   ```sh
   turtle-agent slot setup <server> <slot> --holder <name> --mmaps-dir <mmaps-dir> --dungeons-from <plans-dir>
   ```

   Setup refuses unless the slot is leased to you. It prints five lines: the ready line, the account/character/port line, the start line, the rpc line, and the teardown line, plus one `mmaps:` line when `--mmaps-dir` was given and one `dungeons:` line when `--dungeons-from` was given. The ready dir holds `turtle-agent`, `settings.json` (mode `0600`), `slot.json`, `agent.out`, `dungeons/` and `traces/`.

   Setup copies no settings file: it builds `<dir>/settings.json` from scratch (one account, a fresh token file per run) plus `slot.json`, the binary and the `dungeons/`/`traces/` folders, and prints the login line. Settings contents are never printed.

4. Start the agent with the printed start line (it passes the slot dir's own settings and pid file):

   ```sh
   nohup <walk-dir>/turtle-agent login --config <walk-dir>/settings.json --pid-file <walk-dir>/agent.pid <character> > <walk-dir>/agent.out 2>&1 &
   ```

5. Drive it with the slot's rpc command (it reads the dir's own token file; you never open it):

   ```sh
   turtle-agent rpc <walk-dir> status
   ```

6. When the run is over, tear the dir down:

   ```sh
   turtle-agent teardown <walk-dir>
   ```

   Teardown ends the game presence over RPC (run, binding, hires, GM mode, logout), stops the agent, deletes the derived files, and releases the lease. `--keep-lease` keeps the lease; `--dry-run` only prints the plan.

## Real players

A group holding a real player (any member the `commander status` listing does not name) gets no `.bot remove` at all: a GM's remove reaches any bot, so leaving the group — which ends only your own hires — does the cleanup instead. Known limit: a classic bind (commander outside the lead's group) with a real player inside the lead's group is not detectable from the client; bot-only test runs never have that shape.
