# turtle-agent

[![test](https://github.com/pjerra/turtle-agent/actions/workflows/test.yml/badge.svg)](https://github.com/pjerra/turtle-agent/actions/workflows/test.yml)

A headless World of Warcraft client for **vanilla 1.12 / Turtle WoW 1.18.1** servers (login build 7272, world build 5875).
One process logs one character in, with no game window, and exposes it over a local JSON-RPC API. It was built to test
server-side bots ([TortoiseBots](https://github.com/Sagiroth/TortoiseBots)) on a private test server: log in, hire or
invite bots, start a fight, then read back the combat log and what each bot did.

Testing TortoiseBots with it? Follow the maintainer's vision in [docs/TORTOISEBOTS-VISION.md](docs/TORTOISEBOTS-VISION.md)
and the rules for agents in [AGENTS.md](AGENTS.md).

> Use it only on servers you run or are allowed to test on. It has no Warden emulation and does no rendering.

## Quick start

Requirements: Go 1.27.1 or newer (see `go.mod`), a C++17 compiler for cgo (g++ or clang), and a server whose realmd
and world ports you can reach. `CGO_ENABLED=0` still builds; `path_to` and `walk_path` then answer that the binary
has no navmesh engine.

```sh
make build                      # bin/turtle-agent
make test                       # gofmt check, go vet, builds, go test -race ./...
```

Create the settings file (default `~/.config/turtle-agent/settings.json`, mode **0600**; the agent refuses a file
other users can read) with your server and account in it — placeholders below, never real values:

```json
{
  "realmd": "<server>",
  "accounts": { "<account>": "<password>" },
  "rpc": "127.0.0.1:8765"
}
```

Only `realmd` and `accounts` are required. `rpc` must be a loopback address. See
[the settings reference](docs/okf/getting-started/settings-reference.md) for every key.

Every realmd login waits out the first minutes of each UTC hour: the core's login throttle
(`AuthSocket.cpp:439-457`) wrongly counts every account seen from the IP between hh:00:00 and hh:03:00, so a login that
starts from hh:59:57 (3 s early, for clock skew) to hh:03:05 is held until hh:03:05, plus a random 0-20 s so held logins
do not all fire at once, and logs `login: waiting <s> s for the realmd hour-start window`. The callers that start a login
child (raid-walk, plan-scout, fleet) allow for the hold in their own waits. The window assumes the database's `NOW()` uses a
whole-hour time zone. Set `TA_LOGIN_HOUR_HOLD=0` to turn the hold off.

Raid-walk rosters (`tb-raid-roster` v2) may name a `spec` per batch; for a hunter it also sets the talent build and reads it back
(see [the roster format](docs/okf/raids/roster-format.md)). Known limit: the read-back checks that the spec's tree has the most
points, not the premade's exact point spread, which depends on the hire's level; refusals in TB's answer are caught and a failed
spec is listed in the walk's `spec_failed`.

Log a character in and keep it online with the RPC server running:

```sh
bin/turtle-agent login --rpc 127.0.0.1:8765 <account> <character>
```

```sh
bin/turtle-agent rpc <slot-dir> status
```

It reads the slot's token file itself and prints only the call's `result`. See
[Drive the agent with RPC](docs/okf/getting-started/drive-rpc.md).

## What you can do with it

- Drive one character over RPC: [the agent and its RPC](docs/okf/agent/index.md).
- Run a bot party through a dungeon: [dungeon runs](docs/okf/dungeons/index.md).
- Walk raids, supervise many, regression-check plans: [raids](docs/okf/raids/index.md).
- Write, check and ship run plans: [plans](docs/okf/plans/index.md).
- Walk the navmesh and draw the run as a map: [navigation](docs/okf/navigation/index.md).
- Fight with data: rotations, tactics, the brain: [combat](docs/okf/combat/index.md).
- Watch it live, record and score it: [observability](docs/okf/observability/index.md).

## Documentation

The manual lives in [docs/okf](docs/okf/index.md): one topic per page, each starting with a header that says what
kind of page it is. The format is described in
[How this documentation is written](docs/okf/about/conventions.md), and
[the README section map](docs/okf/about/readme-map.md) shows where every section of the old long README went.

## Development

`make test` must stay green (gofmt, `go vet`, a no-cgo build, `go test -race ./...`, a cgo build, `staticcheck ./...`).
See [docs/CI.md](docs/CI.md) for the jobs. Open PRs against **`dev`**; `main` only takes `dev` after the owner's
review. Contributions that change how the bot plays need the maintainer's decision first — bug fixes go in, behaviour
changes are discussed.

## Licence

GNU Affero General Public License v3.0 or later, see [LICENSE](LICENSE). Versions up to the commit that changed the licence were released under the MIT License (kept in [LICENSES/MIT-turtle-agent-before-agpl.txt](LICENSES/MIT-turtle-agent-before-agpl.txt)); copies you already received under MIT stay under MIT. Parts are ported from [agent-wow](https://github.com/agent-wow/agent-wow) (MIT; the files keep their own MIT headers), and
`internal/navmesh/detour` is Detour from recastnavigation (zlib, `internal/navmesh/detour/License.txt`); see
[NOTICE](NOTICE). Dungeon event data derived from [jrad7/mod-dungeon-clear](https://github.com/jrad7/mod-dungeon-clear) (AGPL-3.0-or-later) is marked with its source where it appears. World of Warcraft is a trademark of Blizzard Entertainment; this project is not affiliated with it.
