---
type: Reference
title: rpc, fleet, version
description: Every rpc, fleet and version flag with defaults, outputs and exit codes.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/rpc_cmd.go
tags: [getting-started, reference, rpc, fleet]
sources:
  - id: rpc
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/rpc_cmd.go
  - id: fleet
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/fleet.go
  - id: cli-args
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/cli_args.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:25:00+01:00
status: draft
---

# rpc, fleet, version

## rpc

```text
turtle-agent rpc [--timeout DUR] [--raw] <dir> <method> [<params-json>|-]
```

- `<dir>`: slot dir made by `slot setup` (its `slot.json` is checked; paths outside the dir are refused). Example: `rpc <walk-dir> status`.
- `<method>`: RPC method name. Required. Example: `rpc <walk-dir> gm '{"text": ".bot run list"}'`.
- `<params-json>` (optional): method parameters as a JSON object or array. Omit for none. Example: `rpc <walk-dir> commander '{"op": "status"}'`.
- `-`: read the parameters from stdin. Example: `echo '{"text": ".bot follow start"}' | turtle-agent rpc <walk-dir> gm -`.
- `--timeout DUR`: per-call timeout as a Go duration. Default `30s`. Example: `--timeout 10s`.
- `--raw`: print the result compactly (one line). Default: off (indented JSON). Example: `--raw`.

Prints only the `result` as JSON. Exit `0` on answer, `2` on bad flags/arguments/dir, `15` when the agent cannot be reached (`rpc: cannot reach the agent at 127.0.0.1:<port>: ...`) or the reply/token is bad (`rpc: the token file is not an agent token`, `rpc: bad reply from the agent: ...`), `16` when the agent answers a JSON-RPC error (`error: <code> <message>`). The token never reaches stdout or stderr. The calls themselves live in [The agent and its RPC](/agent/index.md).

The dir must match, the port must be valid, outside paths are refused. `params-json` is an object or an array; `-` reads it from stdin; omitted means no params; `--timeout` bounds the call. A reply that is not a 2.0 envelope for the request (any non-200 status; a body that is not an object with `"jsonrpc":"2.0"`, `"id"` 1 and exactly one of a present `"result"` or an `"error"` object with a numeric code and a string message) is `rpc: bad reply from the agent: <reason>` on stderr, exit 15, never 0. The token never appears in output, errors or help: it is redacted from every printed line.

## fleet

```text
turtle-agent fleet --config FILE [--settings FILE] [--stagger DURATION] [--wait DURATION] [--reconnect N] [--stop-on-failure]
```

- `--config FILE`: fleet file (required): `{"members":[{"account","character","rpc"}]}`. Example: `--config <fleet-file>`.
- `--settings FILE`: settings file. Default: the default settings path. Example: `--settings <settings-file>`.
- `--stagger DURATION`: pause between starting children. Default `2s`. Example: `--stagger 3s`.
- `--wait DURATION`: give up if not every character is in world after this long. Default `2m`. Example: `--wait 3m`.
- `--reconnect N`: each child logs in again up to N times after an unplanned disconnect. Default `0` (off). A member's `"reconnect"` overrides it (`0` turns it off). Example: `--reconnect 3`.
- `--stop-on-failure`: stop every child when one fails. Default: off (report it and keep the others running).

Exit `0` only when every child exited 0; exit `9` when any child exited non-zero, was killed or could not be launched, or when the fleet is not fully in world after `--wait`. Exit `2` on bad flags or files.

## version

```sh
turtle-agent version
```

Prints `turtle-agent <version>`. No flags.
