# AGENTS.md

Rules for AI agents (and people) who use or change turtle-agent.

## Using turtle-agent to test TortoiseBots

- Read [docs/TORTOISEBOTS-VISION.md](docs/TORTOISEBOTS-VISION.md) first. Judge what you see against it: bots should
  play like real players (quest, grind, travel on foot and by flight path, use towns), stay active all the time, earn
  their gold and gear, and obey explicit player orders. Report anything synthetic (teleport travel, bots that freeze,
  handouts, seeded auction listings) as a finding.
- When a finding leads to a TortoiseBots change, say whether it is a **bug fix** (the bot fails at what it is meant to
  do) or a **behaviour change** (it would play differently). Behaviour changes need the maintainer's decision: describe
  what changes in game and ask. Anything outside the vision belongs behind a config option that is off by default.
- Your own GM commands are test tooling, never bot behaviour. Use them only on your own character and bots: no command
  that changes the world for everyone (`.npc set/add/delete/move`, `.gobject add/delete`, `.reload`, `.server`,
  `.account`, `.ban`), no config edits, no restarts.
- Report only what you observed (event lines, numbers, log lines with times). Mark anything inferred. Never put account
  names or passwords in reports, captures, fixtures or PRs.
- Clean up with `turtle-agent teardown <dir>` (it stops the run, unbinds, ends your own hires, `.gm off`, logs out, ends your agent by its pid file and releases the slot lease). Never pkill or kill by name.

## Changing turtle-agent

- The agent must behave like a real 1.12 client: send only packets a real client sends, and keep movement
  anticheat-clean. Check packet layouts against the server core, not later expansions.
- Prefer features that make vision checks possible (positions, auras, NPC use, loot, travel).
- `make test` and `make build` must pass. Update README.md for every new RPC or field.
