# CI jobs

The workflows under `.github/workflows/` run four checks beside the `test` gate.

## `test.yml` — the PR gate

`test` runs on every push to `dev`/`main` and every non-draft PR whose base is either branch (or a stacked feature
branch). It runs `make fmt-check vet test` (gofmt, `go vet`, a no-cgo build, `go test -race ./...`), then a cgo build,
and `staticcheck ./...` with the pinned release **2026.2.2** (`go install honnef.co/go/tools/cmd/staticcheck@2026.2.2`).
The repo has no suppressions: a staticcheck finding is fixed in the code or, when a check is a false positive, disabled
one check at a time in a commented `staticcheck.conf` (never a whole category).

## `navmesh.yml` — the real map files

Runs on pushes to `dev`/`main`, on non-draft PRs (`opened`, `synchronize`, `reopened`, `ready_for_review`) and by hand.
It downloads the server's navmesh files from the **private** `pjerra/tb-server-data` repository, release tag
`data-2026-10-29`, into `$RUNNER_TEMP/mmaps`, then:

- runs `go test -count=1 ./internal/navmesh/ ./internal/dungeon/ ./internal/dashboard/` with `CGO_ENABLED=1`: the
  `*_test.go` files of `internal/navmesh` and `internal/dungeon` that skip without `TURTLE_AGENT_MMAPS`, plus
  `internal/dashboard` (its dashboards read the same plans and routes);
- builds `turtle-agent` with cgo and runs `tools/ci/plan-check-all.sh`, which checks every plan whose status starts with
  `live-checked` or `mapped` (“live or prep”: a `draft` has not had its mapping walk and is left out of the navmesh gate).

The download is `tools/ci/fetch-mmaps.sh`: it joins split assets (`<name>.tar.zst.part-aa`, `.part-ab`, ...`) into
`<name>.tar.zst`, verifies the release's `SHA256SUMS`, unpacks the archives into one flat folder and checks at least one
checksum. `actions/cache` keeps the folder keyed on the release tag and the SHA256SUMS hash.

If the `TB_DATA_TOKEN` secret is absent (forks, or before the owner adds it), the job prints
`navmesh checks skipped: TB_DATA_TOKEN not set` and succeeds without running the navmesh tests or the plan check.

### The `TB_DATA_TOKEN` secret

Create a **fine-grained personal access token** with:

- repository access: only `pjerra/tb-server-data`;
- permission: **Contents — Read-only** (nothing else; no metadata beyond what GitHub requires).

Store it as an Actions secret named `TB_DATA_TOKEN` on this repository. The workflows pass it to `gh` as `GH_TOKEN`;
it is never written to a file or printed. A fork's PRs do not receive repository secrets, so they take the skip path.

Local equivalent:

```sh
GH_TOKEN=... tools/ci/fetch-mmaps.sh data-2026-10-29 pjerra/tb-server-data /tmp/tb-data /tmp/mmaps
TURTLE_AGENT_MMAPS=/tmp/mmaps tools/ci/plan-check-all.sh --bin ./bin/turtle-agent --dir dungeons --mmaps /tmp/mmaps
```

## `plan-drift.yml` — plans versus TortoiseBots exports

Runs on pushes to `dev`/`main`, nightly at 04:17 UTC, and by hand. It checks out the public
`Sagiroth/TortoiseBots` repository at `dev` (read-only, no token) under `tb/` and runs:

```sh
go run ./tools/plandrift --plans dungeons --sql tb/data/sql/world
```

`tools/plandrift` finds the newest migration with a plan's `ai_dungeon_plan` row (a migration that only replaces one
leg's steps is an update, not an export), builds the rows the current exporter writes from `dungeons/<plan>.json`, and
reports per plan: `in sync`, `differs` (row counts and the first differing step), `not exported upstream`, or
`current export refused` (with the exporter's reason). The plan row's `source` and `comment` are not compared: the
source names the exporting commit and the comments carry export-time notes. The output is a table in the job's step
summary. The first version is **report-only** (`continue-on-error: true`): a diverge is expected while a plan is edited
before its next export, so the job never blocks a PR. A local run is the same `go run` line with `--sql
~/TortoiseBots/data/sql/world`.

## `fuzz.yml` — nightly parser fuzzing

Runs nightly at 03:17 UTC and by hand: `make fuzz FUZZTIME=2m` (2 minutes per `Fuzz*` target of `internal/game`,
`internal/objects` and `internal/tbm`), with a 60-minute job timeout. A crash fails the job and the new corpus files
under `**/testdata/fuzz/**` are uploaded as the `fuzz-corpus` artifact (14 days) so the seed can be committed with the
fix.
