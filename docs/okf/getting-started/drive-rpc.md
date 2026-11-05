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

### HTTP shape

`rpc` posts to `<loopback>:<port>/rpc` with `Content-Type: application/json`. Only `POST /rpc` works: a wrong path answers `404`, a non-POST answers `405` (with `Allow: POST`). Arrays (batches) answer `Invalid Request`. The body cap is 64 KiB. Answers carry `Cache-Control: no-store`.

Browser or off-host requests are refused with `403 local process requests only`: any `Origin` header, any `Sec-Fetch-Site` header, or a non-loopback `Host`. Auth runs before the body or method lookup, so `401` is answered before the body is read.

A wrong or missing token gets `401` with the body `unauthorized` and a `WWW-Authenticate: Bearer` header. The token check is case-insensitive on the `Bearer` scheme and compares SHA-256 digests in constant time.

There is no Python client in this repository. The RPC needs the token file, so a client must read it: read `rpc-<port>.token` from the settings folder (or take an `rpc_token` you set), strip the trailing newline, and send it as the `Authorization: Bearer <token>` header.

```sh
TOKEN=$(cat <settings-dir>/rpc-8765.token)
curl -s -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"status"}' http://127.0.0.1:8765/rpc
```
