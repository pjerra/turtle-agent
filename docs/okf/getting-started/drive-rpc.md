---
type: Guide
title: Drive the agent with RPC
description: Send one JSON-RPC call to a slot's agent with the rpc command.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/rpc_cmd.go
tags: [getting-started, rpc, slots]
sources:
  - id: rpc
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/rpc_cmd.go
  - id: cli-args
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/cli_args.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:25:00+01:00
status: draft
---

# Drive the agent with RPC

`turtle-agent rpc` sends one JSON-RPC call to the agent of a slot dir, for live-test workers that must never read its token file. It checks `slot.json` like `teardown` does, reads the token file derived from the port itself, and posts to `127.0.0.1:<port>`. It prints only the `result` as JSON.

## Steps

1. Ask a running slot agent for its status:

   ```sh
   turtle-agent rpc <walk-dir> status
   ```

2. Pass parameters as JSON:

   ```sh
   turtle-agent rpc <walk-dir> gm '{"text": ".bot run list"}'
   turtle-agent rpc <walk-dir> commander '{"op": "status"}' --timeout 10s
   ```

3. Pipe parameters from stdin with `-`:

   ```sh
   echo '{"text": ".bot follow start"}' | turtle-agent rpc <walk-dir> gm -
   ```

4. Get the compact form for scripts:

   ```sh
   turtle-agent rpc --raw <walk-dir> status
   ```

## Exit codes

- `0`: the call answered; only the result was printed.
- `2`: bad flags, arguments or slot dir.
- `15`: the agent cannot be reached, answered a bad reply, or the token file is not an agent token (the content is never printed).
- `16`: the agent answered with a JSON-RPC error, printed as `error: <code> <message>`.

The calls themselves, with parameters and results, live in [The agent and its RPC](/agent/index.md).
