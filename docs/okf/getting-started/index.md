# Getting started

turtle-agent is a headless WoW 1.12 client: it logs a character in and drives it from the command line or a local RPC API.

## First steps

- [Overview](overview.md) — what turtle-agent is, who it is for, and how the pieces fit.
- [First login](first-login.md) — build it, write the settings file, log a character in, send one RPC call.

## Guides

- [Install and build](install.md) — Go toolchain, make targets, cgo and no-cgo builds.
- [Write the settings file](write-settings.md) — the JSON file with the server address, accounts and folders.
- [Log a character in](log-in.md) — the login command, one or two arguments, staying online, logging out.
- [Lease a slot and set it up](lease-slot.md) — the worker flow: lease, slot setup, start the agent, teardown.
- [Log several characters in](fleet-party.md) — the fleet file and the fleet command.
- [Drive the agent with RPC](drive-rpc.md) — the rpc command against a slot dir.

## Concepts

- [What turtle-agent is](what-it-is.md) — one process per character, the RPC server, TortoiseBots testing.
- [The config files](settings-concepts.md) — settings file, slot table and fleet file: what each one holds.
- [Slots and leases](slots-leases-concepts.md) — one slot per worker, lease identity, stale takeovers.

## Reference

- [Command map](command-map.md) — every subcommand and which area documents it.
- [login](login-reference.md) — every login flag with defaults and examples.
- [lease, slot setup, teardown](lease-slot-reference.md) — every flag, printed line and refusal.
- [rpc, fleet, version](rpc-fleet-reference.md) — every flag, outputs and exit codes.
- [Settings file](settings-reference.md) — every settings key with defaults.
- [Troubleshooting](troubleshooting.md) — real error messages, what they mean, the fix.
