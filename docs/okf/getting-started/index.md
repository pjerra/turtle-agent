# Getting started

turtle-agent is a headless WoW 1.12 client: it logs a character in and drives it from the command line or a local RPC API.

## First steps

- [Overview](/getting-started/overview.md) — what turtle-agent is, who it is for, and how the pieces fit.
- [First login](/getting-started/first-login.md) — build it, write the settings file, log a character in, send one RPC call.

## Guides

- [Install and build](/getting-started/install.md) — Go toolchain, make targets, cgo and no-cgo builds.
- [Write the settings file](/getting-started/write-settings.md) — the JSON file with the server address, accounts and folders.
- [Log a character in](/getting-started/log-in.md) — the login command, one or two arguments, staying online, logging out.
- [Lease a slot and set it up](/getting-started/lease-slot.md) — the worker flow: lease, slot setup, start the agent, teardown.
- [Log several characters in](/getting-started/fleet-party.md) — the fleet file and the fleet command.
- [Drive the agent with RPC](/getting-started/drive-rpc.md) — the rpc command against a slot dir.

## Concepts

- [What turtle-agent is](/getting-started/what-it-is.md) — one process per character, the RPC server, TortoiseBots testing.
- [The config files](/getting-started/settings-concepts.md) — settings file, slot table and fleet file: what each one holds.
- [Slots and leases](/getting-started/slots-leases-concepts.md) — one slot per worker, lease identity, stale takeovers.

## Reference

- [Command map](/getting-started/command-map.md) — every subcommand and which area documents it.
- [login](/getting-started/login-reference.md) — every login flag with defaults and examples.
- [lease, slot setup, teardown](/getting-started/lease-slot-reference.md) — every flag, printed line and refusal.
- [rpc, fleet, version](/getting-started/rpc-fleet-reference.md) — every flag, outputs and exit codes.
- [Settings file](/getting-started/settings-reference.md) — every settings key with defaults.
- [Troubleshooting](/getting-started/troubleshooting.md) — real error messages, what they mean, the fix.
