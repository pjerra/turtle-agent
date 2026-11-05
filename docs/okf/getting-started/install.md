---
type: Guide
title: Install and build
description: Build turtle-agent with make, including the Go toolchain, cgo, and the test targets.
resource: https://github.com/pjerra/turtle-agent/blob/dev/Makefile
tags: [getting-started, install, build, cgo, make]
sources:
  - id: makefile
    resource: https://github.com/pjerra/turtle-agent/blob/dev/Makefile
  - id: readme
    resource: https://github.com/pjerra/turtle-agent/blob/dev/README.md
  - id: gomod
    resource: https://github.com/pjerra/turtle-agent/blob/dev/go.mod
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:25:00+01:00
status: draft
---

# Install and build

## What you need

- Go 1.27.1 or newer (see `go.mod`).
- A C++17 compiler (g++ or clang) for cgo: it builds the navmesh engine the agent walks on.
- A server whose realmd and world ports you can reach.

## Steps

1. Build the binary:

   ```sh
   make build
   ```

   This writes `bin/turtle-agent`.

2. Check your change the way CI does:

   ```sh
   make test
   ```

   This runs the gofmt check, `go vet`, a no-cgo build, and `go test -race ./...`.

3. If you only changed SMSG parsers, fuzz them:

   ```sh
   make fuzz FUZZTIME=30s
   ```

   The default is 5 seconds per target. `make test` runs only their seeds.

## Build variants

- `CGO_ENABLED=0` still builds, but `path_to` and `walk_path` then answer that the binary has no navmesh engine. Copying the server's navmesh files is covered in [Write the settings file](/getting-started/write-settings.md).
- `make tbm-contract` checks the addon protocol against the real TortoiseBotsManager addon (needs lua5.1; not part of `make test`).

## Branches

Work goes feature branch -> `dev` -> `main`. Open pull requests against `dev`. The `test` check must be green to merge into either branch. `main` only takes `dev` after the owner's review.
