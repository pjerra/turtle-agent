---
type: Guide
title: Brain and follow
description: Start the fighting brain, follow a lead bot across a plan, and stop both.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/brain_methods.go
tags: [agent, brain, follow, combat]
sources:
  - id: brain-methods-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/brain_methods.go
    title: brain_start, brain_stop, brain_status
  - id: follow-methods-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/follow_methods.go
    title: follow_start, follow_stop, follow_status
  - id: controller-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/brain/controller.go
    title: Brain controller and status
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T22:45:00+01:00
status: draft
---

# Brain and follow

## 1. Start the brain

```sh
turtle-agent rpc <walk-dir> brain_start '{"strategies": ["frost_mage"]}'
```

Use one preset (see [the brain](/agent/brain-concept.md)) or list strategies. Add `"role": "tank"` or `"role": "healer"` only when the preset supports it, `"tank": "<name>"` to assist a tank as DPS, `"no_move": true` to fight without walking. The call needs the character in world:

`brain_start needs the character in the world (state <state>)`

## 2. Watch it think

```sh
turtle-agent rpc <walk-dir> brain_status
```

`running`, the active `strategies`, tick and action counts, the last decisions and the current cast holds tell you whether it fights or idles, and why.

## 3. Follow a lead bot

```sh
turtle-agent rpc <walk-dir> follow_start '{"plan": "<plan>", "lead": "<tank>"}'
```

`plan` is a plan file name (`a-z`, `0-9`, `_`, `-`); `lead` is the lead bot's name and cannot be the player itself. Optional: `near` (default 8, 3–30 yards), `far` (default 15, `near+2` to 45), `assist` (attack the lead's target, default false), `lost_s` (default 30, 10–120 seconds before the trail gives up). Repeating `follow_start` while running only changes the lead; anything else needs `follow_stop` first:

`following with plan <plan>: follow_stop first`

## 4. Stop in reverse order

```sh
turtle-agent rpc <walk-dir> follow_stop
turtle-agent rpc <walk-dir> brain_stop
```

`follow_stop` settles the loop and returns its final status (`state: "ending"` while it drains). `brain_stop` is refused while a dungeon run owns the brain — stop the run first. A dungeon run and a follow can never hold movement together; the second starter is refused until the first ends.
