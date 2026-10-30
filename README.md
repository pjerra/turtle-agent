# turtle-agent

[![test](https://github.com/pjerra/turtle-agent/actions/workflows/test.yml/badge.svg)](https://github.com/pjerra/turtle-agent/actions/workflows/test.yml)

A headless World of Warcraft client for **vanilla 1.12 / Turtle WoW 1.18.1** servers (login build 7272, world build 5875).
One process logs one character in, with no game window, and exposes it over a local JSON-RPC API. Use it to drive a
test character from scripts or AI agents: chat, channels and GM commands, addon messages, NPC gossip, party, units
around you, target, attack, cast, short walks, walks on the server's own navmesh and GM teleports, raid icons, group
loot rolls, using items from the bags, corpse loot, mounts, equipping from the backpack, buying and selling at
vendors, trainers and quests, and an ordered event log of chat, combat, marks, rolls and NPC results.

It was built to test server-side bots ([TortoiseBots](https://github.com/Sagiroth/TortoiseBots)) on a private test
server: log in, hire or invite bots, start a fight, then read back the combat log and what each bot did.

Testing TortoiseBots with it? Follow the maintainer's vision in [docs/TORTOISEBOTS-VISION.md](docs/TORTOISEBOTS-VISION.md)
and the rules for agents in [AGENTS.md](AGENTS.md).

> Use it only on servers you run or are allowed to test on. It has no Warden emulation (see [What the server sees](#what-the-server-sees)) and does no rendering or
> inventory management beyond listing the bags, using and equipping items, taking corpse loot, and buying and selling.

## Branches and CI

Work goes feature branch -> `dev` -> `main`. Open PRs against **`dev`**; `main` only takes `dev` after the owner's review.
The `test` check (gofmt, `go vet`, a no-cgo build, `go test -race ./...`, and a cgo build) must be green to merge into
either branch. The navmesh tests skip without mmaps.

## Quick start

Requirements: Go 1.27.1 or newer (see `go.mod`), a C++17 compiler for cgo (g++ or clang; it builds the navmesh
engine), and a server whose realmd and world ports you can reach. `CGO_ENABLED=0` still builds; `path_to` and
`walk_path` then answer that the binary has no navmesh engine.

```sh
make build                      # bin/turtle-agent
make test                       # gofmt check, go vet, a no-cgo build, go test -race
make fuzz FUZZTIME=30s         # go test -fuzz on every SMSG parser, 30 s each (default 5 s); `make test` runs only their seeds
make tbm-contract               # TBM addon protocol check (needs lua5.1; see TBM contract test)
```

Create the settings file (default `~/.config/turtle-agent/settings.json`, mode **0600**; the agent refuses a file other
users can read):

```json
{
  "realmd": "127.0.0.1:3724",
  "accounts": { "MYACCOUNT": "my-password" },
  "rpc": "127.0.0.1:8765",
  "spot": { "map": 1, "x": -1287.0, "y": -2379.6, "z": 91.7 },
  "test_spell": 52524,
  "mmaps_dir": "~/turtle-agent-mmaps"
}
```

Only `realmd` and `accounts` are required. `rpc` must be a loopback address. `realm` and `world_addr` override what the
realm list returns; `rpc_token` and `rpc_auth` set the [RPC authentication](#rpc-authentication); `bots`, `spot` and `test_spell` are optional hints for scripts. `mmaps_dir` is a folder holding
copies of the server's navmesh files, for `path_to` and `walk_path` (see [Navmesh walking](#navmesh-walking)), and to
land falls and knockbacks on the ground (see [Falling](#falling-and-knockbacks)). `addons` replaces the addon list sent
at world login (see [What the server sees](#what-the-server-sees)). `tbm_addon_dir` is the TortoiseBotsManager checkout
the [addon host](#addon-host) runs (default: the pinned copy `tools/tbmcontract/fetch.sh` keeps in `.cache/`). `rotations_dir` is a folder of extra [rotation files](#rotations-as-data) (the built-in ones need none; a bad file stops the login). `dungeons_dir` is the folder
`dungeon_run` reads plans from (default `dungeons` under the working directory; see [Dungeon runs](#dungeon-runs)). `traces_dir` is the folder run traces go to (default `traces`, or `$TURTLE_AGENT_TRACES_DIR`; see [Run traces and checks](#run-traces-and-checks)). `run_meta` (`{"model", "effort", "run", "server_trace"}`) is the slot's default AI driver of its runs: `dungeon_run`'s `run_meta` wins when it names one (see [Run traces and checks](#run-traces-and-checks); `server_trace` is off by default).

Log a character in and keep it online with the RPC server running:

```sh
bin/turtle-agent login --rpc 127.0.0.1:8765 MYACCOUNT MyCharacter
```

When the settings file holds exactly one account, leave the account name off (`login MyCharacter`): it then stays out of `ps` and shell history.

Ctrl-C logs out cleanly. If the server refuses the logout (in combat), the agent retries for 35 s; a second Ctrl-C quits.
If the server never answers the logout, the agent closes the socket after 30 s and exits 10.

A logout, from Ctrl-C, `--stay` or the `logout` RPC, always ends the process at once (no disconnect grace window).
The final `session` event carries `data.outcome`:
- `complete`: `SMSG_LOGOUT_COMPLETE` arrived. Exit 0.
- `closed_by_server`: the server accepted the logout (`SMSG_LOGOUT_RESPONSE` reason 0) and then closed the socket
  instead of completing it. Exit 0, state `logged_out`.
- `lost`: the socket died before the server accepted the logout. The core keeps a dropped character in world for
  about 20 s (`ForcePlayerLogoutDelay`), so this is not a logout. RPC -32002, exit 10, state `disconnected`.
- `timeout`: no answer within 30 s; the agent closes the socket. RPC -32004, exit 10; the character may stay in world.

Tortoise 1.18.1 sometimes closes the socket right after a GM's logout request without sending either packet
(issues #9, #18; seen in the open world and inside instances). That ends as `lost`, although the server's char.log
recorded a normal logout each time it was checked.

## CLI

```
turtle-agent login [--config FILE] [--capture FILE] [--stay DURATION] [--rpc ADDR] [--reconnect N] [--create race,class[,gender]] [--tune FILE] [<account>] <character>
turtle-agent fleet --config FILE [--settings FILE] [--stagger DURATION] [--wait DURATION] [--reconnect N] [--stop-on-failure]
turtle-agent scrub-check [--config FILE] <capture>...
turtle-agent capture-stats <capture>
turtle-agent capture events [--from RFC3339] [--to RFC3339] [--kinds a,b] [--out FILE [--force]] <capture>
turtle-agent map [--capture FILE] [--events FILE] [--report FILE] [--botwatch FILE [--members a,b]] [--plan dungeons/x.json] [--out run.svg] [--html] [--force] [--mmaps DIR | --config FILE] [--map N] [--box x0,y0,x1,y1] [--from RFC3339] [--to RFC3339] [--pad YD] [--floors auto|off|z,z] [--background=false] [--width PX]
turtle-agent tune-diff <trace.jsonl>
turtle-agent plan-export [--dir dungeons] [--allow-draft] [--drop-companions-loot] [--carrier-chests] [--skip-legs NAME,...] [--mmaps DIR] [--rev SHA] [--stamp YYYYMMDDHHMMSS] [--force] --out FILE|DIR (<plan>... | --all-live)
turtle-agent plan-check [--dir dungeons] --mmaps DIR [--from-leg N [--from-pull-group N]] [--json] <plan>...
turtle-agent route-oracle --plans DIR --mmaps DIR --out DIR [--mode walk|bosses] [--objectives plan|bosses] [--plan NAME]...
turtle-agent dashboard [--listen 127.0.0.1:8790] [--allow-host HOST[,HOST]] [--agents auto|<rpc,...>] [--tests-dir DIR] [--traces-dir DIR] [--plans-dir DIR[,DIR]] [--minimap-dir DIR] [--embed-from ORIGIN[,ORIGIN]] [--config-dir DIR]
turtle-agent minimap-extract --client DIR --out DIR [--maps 70,209|all-instances] [--mmaps DIR[,DIR]] [--prefer worldmap,minimap,navmesh] [--list]
turtle-agent trace-score [--json] [--summary] [--set check=threshold]... <trace.jsonl>...
turtle-agent pulltest [--json] <result.json> | --compare <a.json> <b.json>
turtle-agent rotation list | show [--level N] <name|file.json> | check <file.json>...
turtle-agent version
```

| Exit code | Meaning |
| ---: | --- |
| 0 | OK |
| 2 | bad flags, arguments or settings file (also: RPC address busy or not loopback) |
| 3 | realmd rejected the login |
| 4 | realm missing or offline |
| 5 | world server rejected the session or the character |
| 6 | the server closed the connection |
| 7 | `scrub-check` found a secret in a capture |
| 8 | the server kept refusing the logout |
| 9 | `fleet`: a child exited non-zero (the others kept running unless `--stop-on-failure`), or not every character reached the world in time |
| 10 | a logout did not complete (`lost` or `timeout`); in a fleet, the other characters keep running |
| 11 | `trace-score` found a failed check (see [Run traces and checks](#run-traces-and-checks)) |

`--capture` records every decrypted packet to a file (opened exclusively, mode 0600). The auth session body and
Warden packets are never written, and any packet holding the account name, password or session key is redacted.
`scrub-check` proves a capture is clean before you share or commit it.

`capture events` decodes a finished run: it replays the capture's server packets through the same session handlers
that built the live event log (no socket, no login) and prints the events the `events` RPC returned while the capture
was recorded, one JSON object per line (the `events` entries as they are, so `jq`, `wow-play-harness` and the like read
them unchanged). Every event of the run is there, not one 500-event page, so a run can always be analysed afterwards.
`seq` counts from 1 as it did live, `time` is the time of the capture record that produced the event, and `mono_ms` counts
milliseconds from the capture's first record (live it counts from the agent's start). The character
is the one the capture logged in. `--from` and `--to` (RFC 3339, both inclusive) keep a time window and `--kinds` the
listed event kinds; `seq` stays the run's own. Events go to stdout, or to `--out FILE` (mode 0600; an existing file is
refused unless `--force`, and a capture file, the input included, is never overwritten). A summary and the count per kind go to stderr. A capture cut off in its last record (the agent was killed), or corrupt
from some record on (a bad direction byte, an opcode past `NUM_MSG_TYPES`, time running back by over a minute), is
decoded up to that record, with a warning.

The decode equals the live log except where the capture lacks the input: packets stored redacted (they held a secret),
events whose text the live run redacted (the replay does not know the account names, so such a name can show), the final
`disconnected` event (a capture does not record how the socket ended), and flights the agent simulated after walking by
RPC or over navmesh floors (a replay has neither, so `knocked_back` starts from the last position the replay knows and a
flight lands at its starting height). Events inferred from the agent's own requests (`vendor_sell`, `quest_accepted`,
`quest_abandoned`) come back because the capture holds those requests.

`--reconnect N` (off by default) logs the character in again after an unplanned disconnect, at most N login
attempts in all. Attempts wait 5 s, then 10 s, 20 s and so on up to 2 min, and the agent keeps its own realmd logins
under realmd's per-IP throttle (15 accounts per 300 s; after a "busy" answer it waits the whole 300 s). A rejection
that cannot change (unknown account, wrong password, ban, build) stops the attempts at once, since failed logins count
towards realmd's autoban. The RPC address and the event log stay the same across a reconnect, so event cursors stay
valid; a `session` event with state `reconnected` marks the new session. Out of attempts, the agent behaves as without
the flag (status stays up for 30 s, then exit 6). `--reconnect` cannot be combined with `--capture`.

`--create race,class[,gender]` (off by default) creates the character at login when the account's list lacks the
name: the agent sends `CMSG_CHAR_CREATE` with that race, class and gender (numbers, checked before realmd is contacted:
race 1-10, class 1-5, 7-9 or 11, gender 0 or 1; gender defaults to 0), a default look, outfit 0 and no challenge mode,
then re-asks the list and logs the new character in. A failed create ends the login at once with the server's reason
(name in use, reserved or invalid name, too many characters, ...), like an unknown character today. A fleet member's
`"create": "race,class[,gender]"` passes the same spec to that child's `login`. It is for test servers and your own
accounts: a typo in the name creates a character with that name.

## What the server sees

**Warden is never answered.** The agent drops `SMSG_WARDEN_DATA` and never sends `CMSG_WARDEN_DATA`. What that does
depends on the core (tortoise-wow 1181dev, read, not tested against every build):

- With `USE_ANTICHEAT` (the default), `Anticheat/Warden/Warden.cpp` makes a Warden for every session (the agent says
  it is a Windows x86 client). Its constructor sends `WARDEN_SMSG_MODULE_USE` **only if a Windows module file exists in
  `Warden.ModuleDir`** (`WardenModuleMgr.cpp:88-93`, `Warden.cpp:81-95`) and then starts the response clock. Without a
  module the agent sees no Warden packet at all.
- With a module, the clock runs out after `Warden.Timeout` seconds (anticheat.conf, default 30). `Warden::Update` then
  logs `WARDEN: Account <id> ip <ip> timeout` to the anticheat log, **once per session** (the clock is zeroed), and
  the `KickPlayer()` next to it is commented out (`Warden.cpp:494-500`). That commented line is the only thing keeping an
  agent online: a server that enables it kicks every agent about `Warden.Timeout` seconds after login. No config key
  does that; it is a source change. The other Warden kicks (a failed challenge, a failed module load, an unknown
  opcode) need a client reply, which the agent never sends.
- `Warden.WinEnabled` / `Warden.OSXEnabled` (mangosd.conf, `0` in the template and on the test server) gate the older
  `Anticheat/WardenAnticheat` code, built without `USE_ANTICHEAT`; there `0` means no Warden and no timeout. In the
  `USE_ANTICHEAT` build `WinEnabled` only feeds an assert (`Warden.cpp:61`), so `0` does **not** stop the request when a
  module is present. `Warden.Enable` in anticheat.conf is read into the config and, as far as the code shows, nothing
  tests it. The old code kicks after `Warden.ClientResponseDelay` (120 s) without an answer.
- `Warden.PlayersOnly` and the level gates (`Warden.MinimumLevel`) only matter once scans are answered.

So: expect one `WARDEN: ... timeout` line per agent session on a server that ships a module, and treat it as noise.
Answering Warden is not implemented (it needs the module's own check logic); see the issue "Answer Warden requests".

**Addon list and fingerprint.** CMSG_AUTH_SESSION lists the 11 `Blizzard_*` addons a stock 1.12.1 client ships
(`worldconn.StockAddons`), each enabled with the standard modulus CRC. The anticheat keeps a per-client fingerprint in
the flags byte of four of them (BindingUI, InspectUI, MacroUI, RaidUI); a client that sends flag `0x01` on all four has
none yet and the server generates one (`ADDON: Generated new fingerprint: 0x...` in the anticheat log) and answers with
it in `SMSG_ADDON_INFO`. A real client stores it and presents it at every later login. The agent does the same:
it saves the announced value in `addon-fingerprint-<hash of the account name>` (mode 0600) next to the settings file
and sends it back at every login of that account. The file is read only if it is a regular file (not a symlink)
that only you can access. Two logins of the same account saving at once keep whichever linked its file first; the
other presents that value from its next login. Delete the file to look like a new installation.

The file is per account on purpose: the core's account analyser bans by a "HWprint", a hash that includes the
fingerprint and the IP (`AccountAnalyser.cpp`), so one fingerprint shared by a whole fleet would let a single marked
print hit every account. Set `"shared_fingerprint": true` to use one `addon-fingerprint` for all accounts (one PC
with several accounts) and accept that.

Set `"addons": [...]` in the settings file to send another list; it must contain the four fingerprint addons above.
A real Tortoise 1.18.1 client may also list `Turtle_General` and `Turtle_GroupUI` (the core accepts them when built
with `ALLOW_TURTLE_ADDONS`); add them there if you want to look like that. This was not checked against a real client.

## JSON-RPC API

JSON-RPC 2.0 over HTTP POST on the loopback address (`Content-Type: application/json`, no batches). Requests from a
browser (`Origin` header) and other `Host` names are refused (403). Every call must also carry the bearer token, see
[RPC authentication](#rpc-authentication); without it the answer is a bare `401 unauthorized`.

### RPC authentication

Loopback binding keeps other machines and browsers out, not other local processes (or, under WSL2, probably Windows
processes through localhost forwarding; inferred, not tested). Any of them could otherwise drive the character,
`gm` commands included. So every call needs `Authorization: Bearer <token>`:

- At start the agent makes a random token (32 bytes from `crypto/rand`, 43 characters) and writes it, mode 0600, to
  `rpc-<port>.token` in the **folder of the settings file** (`~/.config/turtle-agent/rpc-8765.token` for the default
  settings and port; the port is the one actually bound, so `--rpc 127.0.0.1:0` works too). The startup line
  `rpc: calls need the token in <file>` names the file, never the token. The file is removed at exit; after a crash a
  stale one stays until the next start on that port replaces it (a client that reads it meanwhile gets 401). The
  agent removes the file only while it still holds its own token, so a restart on the same port keeps its new file;
  it refuses to write into a world-writable folder, and fleet refuses to read a token file that is a symlink, not a
  regular file, or readable by group or others (clients should do the same).
- Or set the token yourself: `"rpc_token": "<16+ visible ASCII characters>"` in the settings file. No file is written then,
  and every agent started from that settings file (a whole fleet) shares the token.
- A missing or wrong token, a wrong scheme or an empty one gets `401` with the body `unauthorized` and nothing else; the
  check runs before the body is read or the method looked up, and compares SHA-256 digests in constant time. The
  loopback, `Host`, `Origin` and `Sec-Fetch-Site` checks stay in front of it.
- Transition switch: `"rpc_auth": "off"` in the settings file turns the check off (default `"on"`). The agent then writes no
  file and says on stderr that authentication is OFF. It is for old scripts that cannot send a header yet; remove it
  once they can. `rpc_auth: "off"` together with an `rpc_token` is refused.

curl (the file ends in a newline; `$(...)` drops it):

```sh
TOKEN=$(cat ~/.config/turtle-agent/rpc-8765.token)
curl -s -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"status"}' http://127.0.0.1:8765/rpc
rpc() { curl -s -H "Authorization: Bearer $(cat ~/.config/turtle-agent/rpc-${PORT:-8765}.token)" \
  -H 'Content-Type: application/json' \
  -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"$1\",\"params\":${2:-{\}}}" "http://127.0.0.1:${PORT:-8765}/rpc"; echo; }
```

Python (`urllib`): read the token file, strip it, and add `"Authorization": "Bearer " + token` to the request headers.

| Method | Params | Result |
| --- | --- | --- |
| `status` | | state, character, map, x/y/z, `falling`, `z_unknown`, `dead`/`ghost`, health, power, `combo_points`/`combo_target`, `gcd_ms` (see [Agent state](#agent-state)), target, last event seq, `mono_ms`, `base_time` and `wall_offset_ms` (the monotonic axis of events: `base_time` is the wall time of mono_ms 0; `wall_offset_ms` is the wall clock now minus `base_time` + `mono_ms`, so host clock steps and drift show) |
| `chat` | `type` (say, party, yell, emote, guild, raid, whisper, channel), `text`, `to` (whisper target or channel name) | |
| `gm` | `text` (a `.`-command; the dot is added if missing) | |
| `events` | `since`, `kinds`, `limit`, `wait_ms` | page: `events`, `next`, `more`, `dropped` |
| `chat_log` | `since`, `limit` | chat and system events only |
| `invite` / `accept_invite` / `leave_group` | `name` / – / – | |
| `promote` | `guid` or `name` (a group member) | Leader only. Returns once the server's group list shows the new leader |
| `assistant` | `guid` or `name` (a raid member, or the player itself), `clear` (true takes the flag away) | Leader of a raid only |
| `convert_raid` | | Leader only: turns the party into a raid |
| `raid_move` | `guid` or `name`, `subgroup` (1-8), `swap_with` (name) | Leader or assistant of a raid: puts the member in the subgroup; with `swap_with` trades places with that member of the target subgroup, which also works when it is full (five) |
| `disband` | | Leader only: removes every other member, which ends the group. `leave_group` only takes the player out and the group lives on |
| `loot_method` | `method` (`free_for_all`, `round_robin`, `master_loot`, `group_loot`, `need_before_greed`), `master` or `master_name` (master_loot: the master looter; the player when neither), `threshold` (item quality 1-5; 0 keeps the group's) | Leader only (`CMSG_LOOT_METHOD`, GroupHandler.cpp:372-403). Returns once the group list shows the new rules |
| `party` | | `in_group`, `raid`, `leader`, `assistant` and `subgroup` (the player's own), `members[]` with health, power (`power_type`, `power`, `max_power`), online state, `assistant` and `subgroup` (health, power and level of a member in view come from its unit fields: the server sends party member stats only to members out of range, so a member in view would otherwise keep its first reading; a member out of view keeps the last stats values), `marks` (raid icon name to guid), `loot_method`, `loot_master` and `loot_threshold` (the group's loot rules from the group list) |
| `trade` | `to` (a player's name in view, or a guid as `0x...`), `items` (`[{"item": entry, "count": n}]`, count 1 when omitted), `gold` (copper), `wait_ms` (the other side's accept, default 12000), `open_wait_ms` (the window opening, default 8000) | Hands items and gold to a player within 10 yd and returns `result` (`completed`), `partner`, `partner_name`, `gave[]`, `gold`, `received[]` and `received_gold` (what the other side put in), `partner_accepted`, `verified`. A trade that does not complete is an error whose `data` is the same object with `result` `timeout`, `cancelled`, `too_far`, `busy`, `ignored`, ...; see [Trading](#trading) |
| `units` | `within` (yards, 3D; `near` is its older name), `entry`, `hostile`, `name`, `limit` | nearby units, nearest first: guid, name, level, health, dead, hostile, in_party, `x`/`y`/`z`/`o`, `pos_age_ms`, `moving`, `distance` (3D) and `distance_2d` from the player (see [Unit positions](#unit-positions)), `in_combat`, `target`, `stand_state`, `auras[]`, `casting` (see [Unit state](#unit-state)); `hostile` means an attackable-looking creature (no NPC flags, not player-controlled), not a faction check: it counts neutral critters and never an enemy-faction player |
| `unit` | `guid` or `name` (prefers living units; the player's own name or guid gives the player, at the `status` position) | one unit, as a `units` row; an error when none is in view |
| `target` | `guid` or `name` (prefers living units) | |
| `gameobjects` | `within` (yards, 3D; `radius` is the same), `entry`, `type` (a name such as `door`, `button`, `chest`, `goober`, `spell_focus` or its number 0-30), `name`, `limit` | nearby game objects, nearest first: `guid`, `entry`, `name`, `type`, `type_name`, `x`/`y`/`z`/`o`, `distance`, `distance_2d`, `state` (`ready`, `active`, `active_alt`), `flags`, `dyn_flags`, `locked`, `usable`, `display_id`, `created_by` (see [Game objects](#game-objects)) |
| `use_object` | `guid`, `wait_ms` (default 3000) | `since`, `object` (as a `gameobjects` row), `outcome` (the kind of the first answering event, or `none`), `event`, `hint` (why nothing came back, when likely). Sends `CMSG_GAMEOBJ_USE`; see [Game objects](#game-objects) |
| `open_object` | `guid`, `spell_id` (one Opening spell; omitted: 6477, then 3365, 6247, 6478, 22810 as far as the spell book has them, the next after a bad-targets or low-skill refusal), `take` (take everything and close the window), `wait_ms` (default 15000, at most 25000: the cast takes about 5 s) | `window` (as `loot`), `taken` (the slots now in the bags, with `take`), `money`, `left`. Casts an Opening spell at a chest or node in view and waits for its loot window; see [Game objects](#game-objects) |
| `dungeon_run` | `name` (a plan in `dungeons_dir`, `a-z0-9_-`), `allow_draft` (run a plan whose status is `draft`; refused without it, see [Dungeon plans](#dungeon-plans)), `from_leg` (a leg number from 0 or its name), `from_step` (an observed run only: the run leader's own step number, from 1, in place of from_leg/from_pull_group), `teleport` (GM-teleport to the start and summon the party), `no_shortcuts` (no GM revive), `setup` (the GM commands you ran to set the run up, as a list of text such as `["go entrance", "levelup -15"]`; they go into the report's `setup_shortcuts`, the run sends none of them), `buffs` (optional GM boost for the companions: a spell id, a preset name or a list of both, `"world"` being the classic world buffs; see [Dungeon runs](#dungeon-runs)), `brain` (a [bot brain](#bot-brain) preset such as `"frost_mage"`: it plays the leader's character during the run's fights; off by default), `gm` (`false`: the leader's account is a player's and the run sends no GM command at all; `true`: a GM, no probe; omitted: the run asks the server and a probe with no answer counts as a player, see [Dungeon runs](#dungeon-runs)), `gm_level` (with `gm: true`: the account level, 1 to 6, default 3; it gates teleport, `.gm visible on`, revive and buffs), `recover_wipe` (the party, all dead, runs back to its corpses and the run goes on; **on by default in a commander run** (`mode "commander"`, the bots are revived with `.bot cmd revive`; a recovered wipe retries the same step, at most twice per run, and the run goes on after a refused revive), off by default in a group run; `false` turns it off in a commander run; an **observe** run refuses any `recover_wipe` value, true or false, and its trace options say off), `wait_sickness` (wait resurrection sickness out after the leader's corpse run), `resume` (`true`: skip what the party has already passed when it starts inside the leg, off by default, see **Resume** under [Dungeon runs](#dungeon-runs)), `from_pull_group` (remote mode: start inside `from_leg`, at that pull group), `start_here` (remote mode: `from_leg` without the move, the old behaviour) and `pulltest` (`{"leg": 2, "pull_group": 1, "tries": 3, "reset": "auto"}` runs one pull as a scored scenario, in commander mode scoring the party, in leader mode the character, see [Start anywhere and the pull test](#start-anywhere-and-the-pull-test)), `mode` (the [run mode](#run-modes): `"commander"` (the older `"remote"`): lead a bot group from outside it, see [Remote leader](#remote-leader); `"leader"` (the older `"group"`, the default without a brain): the agent's character is in the group, leads it and plays; `"observe"`: a TortoiseBots run leader clears the dungeon and the agent only watches, see [Observe mode](#observe-mode)), `observe` (observe mode: `{"plan":"uldaman","poll_s":15,"max_run_s":10800,"stall_s":600,"paused_wait_s":600}`, all optional), `rotation` (leader and group mode: a [rotation file](#rotations-as-data) that takes the place of the brain preset's class rotation), `role` (leader and group mode: the brain's role, `dps`, `tank` or `healer`; with `mode: "leader"` and no `brain` it also picks the preset of the character's class) and, for commander mode, `lead_bot`, `bots`, `tank`, `healer`, `cc`, `carrier` (the bot that does the object steps), `loot_restore` (`group`, `rr`, `master` or `nbg`: the loot method put back after its free-for-all loot), `object_fallback` (`agent`, the default, or `off`: whether the agent's own character takes an object step over when the carrier's order fails twice, see [Remote leader](#remote-leader)), `team` (`alliance` or `horde`: the party's side for the team gate; omitted: the agent's own), `preflight` (`warn`, the default, `strict` or `off`: the kit check before the run), `on_end` (`stay`, the default, `logout` or `none`), `keep_on_fail` (remote: a failed run keeps its binding so a resume re-enters the same instance; default off, `dungeon_stop` lets it go), `enter` (`auto`, the default, `gm`, `cmd`, `walk` or `off`: how the run gets a party that is outside the plan's map in, see [Remote leader](#remote-leader)), `ignore_entry_limit` (try a 6th new instance within the hour) and `prepare` (`{"online":true,"fresh":true,"gm_setup":["gm visible off"],"online_wait_s":45}`: log the bots in, get a fresh instance, send the GM setup lines, all before the bind, see [Prepare](#prepare-one-step-before-the-bind); `{"party":"hire","hire":{...}}` instead hires, checks and binds the party and takes `lead_bot`, `bots`, `tank` and `healer` out of the call, see [Hired party](#hired-party-prepare-party-hire)) for it, and in either mode `watchdog` (`{"warn_s":120,"recover_s":240,"fail_s":420}`: the stall levels of the [run watchdog](#run-watchdog), seconds without progress; a field left out takes the plan's or the default, a negative one turns its level off) | `started`, `plan`, `map`, `from_leg`, `from_pull_group`, `legs[]`, `buffs[]` (the spells the run will apply, `Name (id)`), `brain` (with a rotation: `preset+rot:<name>@<digest of the file>`), `leader_gm` (`true`, `false` or `probe`), `mode` (`group`, `remote` or `observe`), `run_mode` (`leader`, `commander` or `observe`), `lead_bot`; the run goes on in the background, see [Dungeon runs](#dungeon-runs) A commander-mode run first walks the plan on the navmesh ([Plan check](#plan-check)): a live-checked plan with a failing point, or one that cannot be checked, is refused unless `force` is given; any other status runs with the findings as a warning. `force_start` (commander mode only: leader and observe mode refuse it) runs a `from_leg` / `from_pull_group` / `pulltest` start that is under 30 yd from a live pack or boss of a skipped step in a fresh instance ([Start anywhere](#start-anywhere-and-the-pull-test)); `force` does not pass that gate and `force_start` does not pass the plan check. The result carries `plan_check` (`passed: ...`, `forced past plan check ...`, `warning (...)`) |
| `death_status` | | `dead` (health 0, spirit not released), `ghost` (released), `resurrect` (an open offer: `caster`, `name`, `sickness`, `timer`; null for none), `reclaim_in_ms` (the corpse may be reclaimed then); see [Death and the corpse run](#death-and-the-corpse-run) |
| `accept_resurrect` | | Answers the open offer with yes (CMSG_RESURRECT_RESPONSE) and returns it. Errors when the player is alive or nothing is on offer |
| `release_spirit` | | CMSG_REPOP_REQUEST: the dead player becomes a ghost at the graveyard. Errors when alive or already a ghost |
| `corpse_query` | | MSG_CORPSE_QUERY: `found`, `map`, `x`/`y`/`z`, `corpse_map`. A corpse in a dungeon the ghost is not in shows the dungeon's entrance (`map` is the entrance's map, `corpse_map` the dungeon's) |
| `reclaim_corpse` | | CMSG_RECLAIM_CORPSE for a ghost within 39 yd of its corpse and past the reclaim delay (an error before it) |
| `area_trigger` | `id` | CMSG_AREATRIGGER, as the client sends it on stepping into a trigger's volume (the server checks the position); a ghost uses it at a dungeon's portal |
| `pet` | | The player's pet, from `UNIT_FIELD_SUMMON` of the player and the pet's own unit row: `present` (false: no pet), `guid`, `entry`, `name`, `level`, `dead`, `health`, `max_health`, `power_type` (2 focus for a hunter's pet, 0 mana for a demon), `power`, `max_power`, `happiness` (the pet's POWER5, 0 to 1,000,000) and `happiness_state` (1 unhappy, 2 content, 3 happy; absent for a demon), `loyalty`, `in_combat`, `target` (what it attacks), `distance`, `moving`; and its action bar, from `SMSG_PET_SPELLS`: `bar` (the session has it), `react` (`passive`, `defensive`, `aggressive`), `command` (`stay`, `follow`), `enabled` (false while the bar is off, e.g. mounted), `spells[]` (`spell_id`, `name`, `slot` 3 to 6 or -1 off the bar, `autocast`, `passive`), `refused` (the reason of its last refused cast). See [Pets](#pets) |
| `pet_action` | `action` (`attack`, `follow`, `stay`, `passive`, `defensive`, `aggressive`, `cast`, `autocast_on`, `autocast_off`), `target` (guid; `attack` and `cast` default to the player's target), `slot` / `spell_id` / `name` (which spell, for `cast` and the autocast actions; it must be on the pet's bar) | CMSG_PET_ACTION (or CMSG_PET_SPELL_AUTOCAST for the autocast actions), as a client sends them from the pet bar; returns the `pet` result with the order applied. Errors without a pet, with a dead pet, while the bar is off, and for a spell not on the bar. The server drops what it refuses (a target the player may not attack: `pet_error` event with `data.feedback`) |
| `plan_check` | `name` (a plan in `dungeons_dir`), `from_leg` (as `dungeon_run`), `from_pull_group` (with it; 0-based) | `checked` (false: `skipped` says why, no navmesh files or planner), `plan`, `map`, `points`, `issues` (each `leg`, `leg_name`, `part`, `pull_group` / `pull_groups`, `point`, `from`, `reason`, `detail`), `warnings`, `exempt`, `run_leader` (the same rows); see [Plan check](#plan-check) |
| `dungeon_status` | | `running`, `state` (`idle`, `running`, `done`, `failed`, `stopped`), `report`, and the [run watchdog](#run-watchdog)'s view for a poller: `last_progress_at`, `stalled_for_s`, `stall_level` (`none`, `warn`, `recover`, `fail`) |
| `dungeon_stop` | | the same as `dungeon_status`, after the run has ended |
| `dungeon_pause`, `dungeon_resume`, `dungeon_skip` | | One set of commands that acts on the active run by its mode. In a **commander** run (`mode: "remote"`) only `dungeon_pause` exists: it halts the run so that it can be resumed: the run ends `stopped` with the bots stood still (`cmd stay`) and the **commander binding kept**, so the next `dungeon_run` (`resume`, `from_leg`) re-enters the same group and instance; `report.remote.unbind_via` says `kept (the run was paused ...)`. `dungeon_stop` unbinds, which returns the hires to the commander's raid and loses the instance (ZF-2 run 3b). With no run going `dungeon_pause` changes nothing; a group-mode run has no binding and is refused (`dungeon_stop` ends it). `on_end: "logout"` still unbinds. In an **observe** run ([Observe mode](#observe-mode)) the three ask the TortoiseBots run leader for `.bot run pause`, `resume` or `skip`; the watch sends it on its next look (within a second) and the answer is in `report.remote.observe.controls`. `dungeon_resume` and `dungeon_skip` are refused when no run goes or the run is a commander one (a paused commander run goes on with `dungeon_run`). Same result as `dungeon_status` |
| `dungeon_run` (live tuning) | `tune` (remote mode: the tunables file; default the login's `--tune`, else `<plan>.tune.json` beside the plan file, which may be absent; a named file must exist), `live_reload` (`false`: the run does not read the plan file and the tunables file again) | the report's `remote.tune_changes[]` and `remote.plan_reloads[]`; see [Live tuning](#live-tuning) |
| `tune_set` | `key` (a tunable, see [Live tuning](#live-tuning)), `value` (a number, or `"default"` or `null` to put the key back) | `key`, `old` (the value now), `new`, `applies`. Refused at once as invalid params when the key is unknown, the value is outside its limits or a whole number is wanted, or it breaks a rule between two values; else taken, with the same `tune_change` event as a file's change, at the run's next leg start or pull-group start. Errors when no remote run goes |
| `tune_list` | | `tunables[]`: `key`, `unit`, `min`, `max`, `default`, `base` (what the run was made with), `value` (now), `source` (`base`, `file`, `rpc`), `queued` (a `tune_set` waiting for the boundary), `doc` |
| `brain_start` | `strategies` (a list of preset or strategy names, e.g. `["frost_mage"]`), `role` (`dps`, `tank`, `healer`; empty takes the role of the strategies, `dps` when none names one; it must fit them), `tank` (the party member to follow; default: found by class and target), `no_move` (never walk the player: no positioning, no stepping out of ground effects, no run to the tank; default off; a dungeon run's brain moves), `rotation` (a [rotation file](#rotations-as-data) in place of the preset's class rotation) | the `brain_status` result. Turns the [bot brain](#bot-brain) on; refused while one runs |
| `dungeon_run` (run traces) | `run_id` (name the run: letters, digits, `.`, `_`, `-`, at most 64; default `r-<utc yyyymmddThhmmss>-<4 hex>`), `trace` (`false` writes no trace file; on by default), `trace_meta` (free text for the trace header such as `{"tb_sha": "..."}`, at most 16 entries), `run_meta` (the AI driver `{"model", "effort", "run", "server_trace"}`: written into the trace header and the report; with `server_trace: true`, off by default, a remote run also sends it to the server's run record and stops that trace at the end — read the limits in [Run traces and checks](#run-traces-and-checks) first; default: the settings file's `run_meta`, and an explicit `{}` leaves that default in place) | `run_id`, `trace_file` (where the run's trace goes). The report has `run_id`, `run_meta` (when given), `trace_file` and, once the run ended, `trace` (path, counts, `verdict`, `failed[]`, `checks[]`); see [Run traces and checks](#run-traces-and-checks) |
| `brain_start` (run id) | `run_id`, `trace`, `trace_meta` as for `dungeon_run` | the status also carries `run_id`; every brain event has `data.run_id` |
| `brain_stop` | | the `brain_status` result, after the brain has ended |
| `brain_status` | | `running`, `strategies[]`, `role`, `tank`, `no_move`, `move_held` (the dungeon driver holds the move lock), `rest_ok`, `ticks`, `actions`, `idle` (why the newest tick did nothing), `last_decisions[]` (newest first), `holds[]` (spells backed off, with `reason` and `left_ms`) |
| `map_snapshot` | `floor` (from 0), `plan` (a plan name, drawn over the run), `floors` (`auto`, `off` or heights `"-30,-10"`), `background` (false: no navmesh), `html` (also return the replay), `pad` (yards) | `svg` (one floor as a string), `html`, `map`, `floor`, `floors[]` (names), `seconds`, `members`, `creatures`, `events`, `background`, `notes[]`: the run so far as a top-down map, see [Map images](#map-images) |
| `attack` / `stop_attack` | | |
| `cast` | `spell_id`, `target` | |
| `cancel_cast` | `spell_id` (optional; none cancels whatever is being cast) | |
| `spells` | `spell_id`, `name` (a part of it), `highest` (only the highest known rank of each name), `passive` (include passives) | the known spells with their template data, cooldown and `ready`/`remaining_ms`/`why`, `affordable`; see [Agent state](#agent-state) |
| `cast_state` | | the cast (`idle`, `casting`, `channelling`), `last_cast`, `last_failure` with its reason, the `gcd`, the combo points; see [Agent state](#agent-state) |
| `face` | `guid` | |
| `walk_to` | `x`, `y`, `z` (short, straight-line walks that ignore terrain; see Movement; `walk_path` follows the terrain) | |
| `path_to` | `x`, `y`, `z` | a navmesh path from where you stand, without moving: `map`, `from`, `to`, `status` (complete, partial, no_path), `reason`, `points`, `length`, `engine` |
| `walk_path` | `x`, `y`, `z` | walks the navmesh path: `result` (arrived, partial, no_path), `reason`, `goal`, `position`, `dist_left`, `walked` (yards the agent walked itself; a server move such as fear, a charge or a teleport is not counted, and a step the agent's own anticheat check refuses ends the count at the last point the server had), `replans`, `seconds`, `trace` |
| `teleport` | `x`, `y`, `z`, `map` (a GM `.go xyz`; needs a GM account) | |
| `mark` | `guid` or `name`, `icon` (skull, cross, square, moon, triangle, diamond, circle, star, clear) | `icon`, `guid` once the server echoes the mark. Errors at once outside a group or when the player is neither leader nor assistant; errors after 2 s without an echo |
| `roll` | `roll_id` or `item_slot` (plus `looted` when two corpses share a slot), `choice` (need, greed, pass) | `roll_id`, `looted`, `item_slot`, `item_entry`, `choice` |
| `inventory` | `bags` (true: the items in the four bags too) | backpack items (bag 255, slots 23-38), then with `bags` the bag items (bag 19-22, the slot in that bag): `bag`, `slot`, `guid`, `item_entry`, `count`; `item_entry` and `count` are 0 until the item's create block arrives |
| `equip` | `item_entry` (first match in the backpack, then the bags), or `slot` (and `bag`: 255 for the backpack, the default, or 19-22 for a bag) | the item sent; an empty slot is an error; a refusal arrives as an `inventory_error` event |
| `use_item` | `item_entry`, or `slot` (and `bag`, as `equip`), `target` (unit guid; none uses it on yourself) | the item sent; the outcome arrives as events (see [Items, loot and mounts](#items-loot-and-mounts)) |
| `loot` | `guid` or `name` (nearest dead unit), `wait_ms` (default 3000); no params: the open window | the window: `guid`, `type`, `money`, `items[]` (`slot`, `item_entry`, `count`, `display_id`, `random_property`, `slot_type`) |
| `loot_take` | `slot`, or `all`; `money`; `wait_ms` (default 3000) | `taken`, `left`, `skipped` (slot numbers), `money` (copper asked for), `window` (what remains, null once closed) |
| `loot_close` | `wait_ms` (default 3000) | |
| `mount` | `item_entry` or `slot` (and `bag`), or `spell_id`; `wait_ms` (default 12000) | `mounted`, `display_id`, `via` (item, spell), `result`, `learned_spell` (the spell a bridle taught, when the agent cast it) |
| `dismount` | `spell_id` (only when the mount aura is unknown), `wait_ms` (default 3000) | `mounted`, `display_id` |
| `addon_send` | `prefix`, `text`, `channel` (party, raid, guild, officer, raid_leader, raid_warning, battleground, battleground_leader, channel), `to` (channel name) | |
| `tbm` | `command` (a `.bot` command without `.bot`), `via` (chat, the default, or addon) | `since` |
| `tbm_handshake` | `via`, `wait_ms` (default 5000) | `since`, `next`, `version`, `caps`, `transport`, `roster[]`, `bot_state`, `errors`, `lines`, `complete` |
| `tbm_buttons` | `tab` (Fight, Camp, Party, Roster, Guild, Raid, BotPanel; default all) | `addon_commit`, `send_delay_ms`, `poll_after_cmd_ms`, `action_timeout_ms`, `buttons[]` (`name`, `label`, `tab`, `command`, `bot_command`, `args`, `optional`, `scope`, `toggle`, `needs_enemy`, `pull_seconds`, `reply`, `intent`, `ok`, `fail`, `follow_up`, `mini_bar`, `requires_cap`, `multi`, `source`) |
| `tbm_click` | `button` (`Fight.AoE`), `args` (`state`, `bot`, `name`, `mark`, `seconds`, `bag`, `slot`, …), `transport` (auto, chat, addon), `wait_ms` (default 8000), `force`, `follow_up` | `since`, `next`, `button`, `scope`, `sent` (`transport`, `text`, `channel`, `prefix`), `transport_reason`, `refreshed`, `reply` (`kind` ack, err, text, inventory, snapshot or timeout; `ok`, `line`, `ack`, `acks`, `err`, `lines`), `follow_up` (the roster response) |
| `tbm_send` | `command` (with or without `.bot`), `transport`, `wait_ms` | like `tbm_click` without `button` and `scope`: the reply is the first ACK/ERR or roster response |
| `commander` | `op` (`bind`, `unbind`, `status`, `view`, `cmd` or `run`), `lead_bot` and `bots` (bind), `on` (view), `verb` and `args` (cmd: `attack`, `pull`, `pullback`, `cc`, `focus`, `move`, `stay`, `follow`, `rest`, `revive`, `summon`, `loot`, `open`, `use`, `ritual`, `lootmethod`, `state`, `gossip`), `wait_ms` (default 4000) | `sent` (the `.bot` line), `reply` (`kind` `ack`, `err` or `timeout`, `ack`/`err` parsed, `lines`). `loot`, `open`, `use` and `ritual` wait through `ACTION_PENDING` for the final line and add `order` (`pending`, `loot`, `opened`, `ritual` and `participants` for `cmd ritual`, `spell`, `waited_ns`). The [remote commander](#remote-leader) by hand |
| `bot_view` | `after_n` and `wait_ms` (wait for a frame newer than the `n` of the last answer) | `have`, `n`, `age_ms`, `frame` (`seq`, `lead`, `party[]`, `hostiles[]`, `threats[]`, `truncated`), `stats` (the feed's counters), `binding_ends[]`. The latest `TBM:VIEW` frame, see [Remote leader](#remote-leader) |
| `addon_start` | `clock` (real, the default, or manual); the addon is the settings' `tbm_addon_dir`, else `.cache/tbmcontract/addon-<pinned commit>` under the working directory, run by `lua5.1` from PATH | `dir`, `version` (the .toc version), `files`, `clock`, `sent` (what the addon sent while loading) |
| `addon_stop` | | |
| `addon_click` | `button` (a `tbm_buttons` name or label), or `tab` + `label` for any widget; `bot` (BotPanel; the Party row), `name`/`names` (Roster and Guild rows to tick), `mark` (CC marks row), `row`, `view` (BotPanel Gear, Bags, Behaviour), `bag` + `slot` (BotPanel items), `before`, `force`, `wait_ms` (real clock; default 8000, 0 = no wait) | `clicked` (each widget pressed and `/tbm` typed), `sent[]` (`mono_ms`, `kind` addon, chat or target, `channel`, `prefix`, `text`, `to`, `error`), `reply` (server lines the addon got), `timed_out`, `state` (as `addon_state`) |
| `addon_state` | | `server` (`version`, `caps`, `transport` PARTY or null for chat), `window` (`shown`, `mode`, `tab`, `scope`), `status`, `switches[]` (`key`, `text`, `lamp` green, grey or gold, `state` on, off, mixed or unknown, `enabled`), `bots` (BOTSTATE by name), `roster[]`, `roster_shown[]` (Roster tab open), `claimed[]`, `cc_marks`, `party_rows[]` (Party tab open), `bot_panel` (open: `bot`, `view`, `title`, `sub`, `money`, `state`, `inventory`, `behaviour[]`), `raid` (Raid tab open), `last_ack`, `last_error`, `chat[]` (what reached the chat frame), `lua_errors[]`, `clock`, `sent[]` (last 50), `host` (`dir`, `clock`, `cursor`, `dropped`: how often the event log had already dropped events before the addon got them) |
| `addon_tick` | `ms` (1-120000) | `clock` (the addon's `GetTime()`), `sent[]` |
| `join_channel` / `leave_channel` | `name`, `password` / `name` | |
| `gossip_hello` | `guid`, `wait_ms` | `since`, `event`: the first answer within `wait_ms`: the menu (`gossip`), `gossip_complete`, or what the NPC opens at once (`quest_list`, `quest_details`, `quest_request_items`, `quest_offer_reward`, `vendor_list`, `trainer_list`) |
| `gossip_select` | `guid`, `option`, `code` (coded options only), `wait_ms` | `since`, `event` (next menu, `gossip_complete`, or the list or quest panel the option opens) |
| `vendor_list` | `guid`, `wait_ms` | `since`, `event` (`vendor_list`, or `vendor_sell_failed` when no vendor is in reach; a dead player gets no answer) |
| `buy` | `guid`, `item_entry`, `count` (purchases, default 1, at most 255; each hands over the line's `buy_count` items), `bag` + `slot` (optional destination: bag 255 is the backpack, 19-22 a bag), `wait_ms` | `since`, `event` (`vendor_buy`, `vendor_buy_failed` or `inventory_error`) |
| `sell` | `guid`, `item_entry` (first in the backpack) or `slot` (+ `bag`, as for `equip`), `count` (default 0: the whole stack, at most 255), `wait_ms` | `since`, `item` (the item sent), `event` (`vendor_sell` or `vendor_sell_failed`) |
| `trainer_list` | `guid`, `wait_ms` | `since`, `event` (`trainer_list`) |
| `train` | `guid`, `spell_id`, `wait_ms` | `since`, `event` (`trainer_trained` or `trainer_failed`) |
| `quest_hello` | `guid`, `wait_ms` | like `gossip_hello`, through CMSG_QUESTGIVER_HELLO |
| `quest_query` | `guid`, `quest_id`, `wait_ms` | `since`, `event` (`quest_details`) |
| `quest_accept` | `guid`, `quest_id`, `wait_ms` | `since`, `event` (`quest_accepted` or `quest_error`) |
| `quest_complete` | `guid`, `quest_id`, `wait_ms` | `since`, `event` (`quest_offer_reward`, or `quest_request_items` while the items are missing, or `quest_error`) |
| `quest_reward` | `guid`, `quest_id`, `choice` (index into the offer's choices, 0-5; optional when the offer has at most one), `wait_ms` | `since`, `event` (`quest_rewarded`, `quest_error`, or the offer again when the server refuses) |
| `quest_abandon` | `quest_id`, `wait_ms` | `since`, `event` (`quest_abandoned`) |
| `quest_log` | | `quests[]` from the character's own quest log: `slot`, `quest_id`, `complete`, `failed`, `counts[4]` |
| `logout` | | `outcome` (`complete` or `closed_by_server`); `lost` (-32002) and `timeout` (-32004) are errors with `data.outcome`; the reply is written before the process exits |

Events have `seq`, `time`, `mono_ms` (ms since the agent started, from the monotonic clock, so it never jumps with the host clock; `time` stays the wall clock), `kind`, `source`, `target`, `spell_id`, `amount`, `crit`, `text`, `party` and `data`.
Kinds cover chat, system, whispers, melee, spell damage, periodic ticks, heals, energize, spell start/go/miss/fail,
auras, deaths, kills, party changes, raid icons (`raid_mark`), the pet (`pet_summoned`, `pet_died`, `pet_revived`, `pet_dismissed`, `pet_error`; see [Pets](#pets)), game object changes (`gameobject`), group loot rolls (`loot_roll_start`, `loot_roll`, `loot_roll_won`,
`loot_all_passed`, tied together by `data.roll_id`), refused equips and item uses (`inventory_error`), NPC dealings (vendor, trainer and quest events, see
[Vendors, trainers and quests](#vendors-trainers-and-quests)), corpse loot (`loot_open`,
`loot_taken`, `loot_money`, `loot_money_share`, `loot_error`, `loot_closed`), the player's own mount and stand state (`mount`,
`mount_result`, `stand_state`), trade windows (`trade_status`, see [Trading](#trading)) and session state. Cursors are exclusive; `dropped` says older events were evicted.
A chat or system line whose colour codes were stripped keeps the original in `data.raw` (protocol lines such as
`TBM:ROSTER|…|removing` lose `|r` otherwise).

Group control (`promote`, `assistant`, `convert_raid`, `raid_move`, `disband`, `loot_method`): the server drops a group command it
refuses without a reply, so each call checks the rank it can see (leader, or assistant for `raid_move`), sends the
packet and waits up to the `MarkWait` setting (default 2 s) for the group list that shows the change; it errors at once without the rank or outside a
raid, and on timeout with "no SMSG_GROUP_LIST". A call that finds the group already in the wanted state (already
leader, already a raid, flag already set, already in the subgroup) succeeds without sending. `subgroup` counts 1-8 as
the raid frame does (the wire counts 0-7); `party` reports it only for a raid. `promote` needs the target online
(`HandleGroupSetLeaderOpcode`, GroupHandler.cpp:334-352); an offline member errors before anything is sent.
`disband` works from the member list it holds when called: someone who joins meanwhile stays in the group, and in
a battleground the core refuses removing members (`CanUninviteFromGroup`), so it times out there. Not verified against a live server.

Addons, channels and NPCs:

- `addon` events are LANG_ADDON chat, what the client raises as `CHAT_MSG_ADDON`: `source` is the sender, `text`
  the payload after the tab, `data.prefix` and `data.type` (guild for replies the server sends you, party, raid, …).
  The core accepts addon messages on the channels `addon_send` lists; it drops addon whispers, so `addon_send`
  refuses them. A server with the addon channel switched off drops them all without an answer.
- `tbm_click` sends exactly what a TortoiseBotsManager button sends, from a table generated from the addon source
  (`internal/tbm/buttons_gen.go`, written by `tools/tbmbuttongen` from a pinned addon commit, `addon_commit` in
  `tbm_buttons`). Regenerate with `git clone https://github.com/Sagiroth/TortoiseBotsManager /tmp/tbm`, check out
  the commit, then `go run ./tools/tbmbuttongen -in /tmp/tbm -out internal/tbm/buttons_gen.go`; the generator fails
  when a command it lists, or an entry it should have read, is not in the addon source. `go test ./tools/tbmbuttongen`
  fetches the pinned addon (`tools/tbmcontract/fetch.sh`, cached in `.cache/`) and checks the committed table against
  it; it skips only when the addon cannot be downloaded.
  - Transport (`Core.lua SendBotCommand`, `Comms.lua AddonCommandChannel`): the addon channel (`TBM` message on
    PARTY, text without `.bot`) while the module's last roster response said `TBM:TRANSPORT|party` for the group
    you are in now, else `.bot <command>` as say chat. A battleground group (the module says `none`), no group,
    no verdict yet, or a group that changed since the verdict (the addon drops its verdict on every group event)
    all mean chat. `transport: auto` (default) polls the roster first when the verdict is missing or stale,
    over chat (`refreshed: true`); `chat` or `addon` forces one. `tbm_handshake` and every click feed the same
    verdict. Sends are spaced by the addon's `SEND_DELAY` (350 ms), and the pre-click roster poll waits out the addon's
    5 s roster throttle (the cached verdict and states are used, and `transport_reason` says so); the follow-up poll
    after a command is forced, as in the addon.
  - Scope: the addon sends no scope. Action buttons carry the same text for the party and for one bot, and the
    server resolves the scope from your target, so `scope` in the result is the one the addon would show: `bot:<name>`
    while you target one of your online bots (a roster row or a `BOTSTATE` entry), else `party`. Formation pills
    are narrowed in the text (`formation <bot> <id>`); per-bot buttons (`BotPanel.*`, `Fight.CCAssign`) take `bot`.
  - Switches (`Fight.AoE`, `Fight.AutoCC`, `Fight.Loot`, `BotPanel.Behavior.*`) send the opposite of what the bots
    report in `BOTSTATE` (the addon's rule); `args.state` `on` or `off` overrides. `Fight.Pull` and `Fight.Pullback`
    append their timer (10 s and 3 s, or `args.seconds`) only when the module advertised `pull-seconds`.
    Buttons the addon greys out without an enemy target (`needs_enemy`, from the addon's `targetOnly` list), or
    without a capability in `TBM:CAPS` (`requires_cap`: Flee needs `flee`), are refused unless `force` is set. A
    switch ACK sets the state the next click flips from.
  - Reply: the matching `TBM:ACTION_ACK` or `TBM:ACTION_ERR` (same intent), a text line for roster verbs, the
    `TBM:INV_END` of the bot, or the roster trailer, read only from the transport that carried the request (system
    messages for chat, `TBM` addon messages for the addon channel). Other lines are kept in `reply.lines`. After a
    roster verb, formation, role or CC click the addon polls the roster 1.4 s later; `follow_up` does that
    and returns the response (`follow_up: false` skips it). A click that gets no answer in `wait_ms` returns `reply.kind` `timeout`. Raid
    "Check bots" and "Check" (`multi`) are answered by one ACK per bot with no end marker: the reply keeps every ACK in
    `reply.acks` and closes after 600 ms without a new one.
  - Request matching has no request id (the protocol has none). One click or send runs at a time per agent, the
    replies that arrived since the last request are drained first, and text replies are anchored and name the bot
    (case-insensitive). A late ACK of a click that timed out is passed over within 30 s of the timeout, by intent. What
    this cannot separate: a reply to something else on the same intent sent by another client (the TBM addon in a
    real client, a `tbm` or `addon_send` call), and two identical text replies.
  - `tbm_send` is the same transport rule for any command text.
- `tbm_handshake` sends what the TortoiseBotsManager addon sends on login (`.bot help`, then `.bot roster`) and
  collects the module's `TBM:` reply, from system lines or `TBM` addon lines, up to the `TBM:TRANSPORT` trailer.
  `via: addon` sends over the party addon channel instead, which the module only reads while you are grouped.
- `channel_notify` events are `SMSG_CHANNEL_NOTIFY`: `data.notice` (you_joined, joined, left, not_member,
  wrong_password, player_kicked, …), `data.channel`, the player concerned as `source` (a kick or ban: `target`
  and who did it as `source`), and the client's line as `text` where it names no player by GUID.
- `gossip` events carry an NPC menu: `source` is the NPC, `data.text_id`, `data.options[]` (`index`, `icon`,
  `coded`, `text`) and `data.quests[]`. `gossip_select` only picks an option of the open menu; `gossip_complete`
  closes it. An NPC out of reach does not answer `gossip_hello` at all.

Text the server echoes back (for example the account name in a GM reply) is redacted before it reaches events, logs
or replies.

## Items, loot and mounts

`use_item` sends `CMSG_USE_ITEM` for an item in the backpack or a bag, as a right-click does. It targets yourself or a unit only: no item or game-object targets yet (poisons, enchants, lockpicking). An on-use trinket or other equipped item is used from bag 255 and its equipment slot (the core refuses an item that needs equipping anywhere else, `SpellHandler.cpp:78-85`); `inventory` lists the bags and backpack only, so pass `bag` 255 and the slot yourself. An `item_entry` picks the first
item with that entry (backpack first); `bag`/`slot` pick one place (see `inventory` with `bags`). Without `target` the item is
used on yourself (target mask 0), which is what the client sends with nothing selected; the agent does not fall back to the
current target the way `cast` does. The server answers through the usual events: `spell_start`/`spell_go`, `aura_applied`
(the Food or Drink aura), `stand_state` (`data.state` sit while eating or drinking, stand when it ends), `spell_fail`, or an
`inventory_error` when it refuses the item (dead, in combat, wrong level, no such item).

`loot` opens a corpse with `CMSG_LOOT` and waits for `SMSG_LOOT_RESPONSE`. The server checks reach, facing and permission and
answers a refusal with a reason (`too_far`, `didnt_kill`, `locked`, `not_standing`, ...), which `loot` returns as an error. Only
dead creatures and corpses open this way (`name` needs the corpse to be lootable, `UNIT_DYNFLAG_LOOTABLE`); chests, lootable items (`CMSG_OPEN_ITEM`) and herbs are not covered. Each item has a `slot_type`: only
`allow_loot` and `owner` can be taken; `roll_ongoing` (a group roll is running, answer it with `roll`), `master` and `locked`
cannot, and `loot_take` refuses a single slot of those and skips them with `all`. `loot_take` sends
`CMSG_AUTOSTORE_LOOT_ITEM` per slot, and `CMSG_LOOT_MONEY` for the money (`all` takes both), then waits up to `wait_ms` for
`SMSG_LOOT_REMOVED`/`SMSG_LOOT_CLEAR_MONEY`. A slot still in `left` was not taken, usually a full bag (see the
`inventory_error` event). Like the client, the agent releases a window by itself once nothing is left in it, and `loot_close`
(`CMSG_LOOT_RELEASE`) closes one early; opening another corpse closes the first on the server. `loot` with no params returns the
window now open, or null.

`mount` uses a mount item (`CMSG_USE_ITEM`) or casts a mount spell on yourself, then waits for the mount display
(`UNIT_FIELD_MOUNTDISPLAYID`). It errors when already mounted, when the player's own cast fails (at once, with the reason), or when the display does not
appear in time, naming what to read (`spell_fail`, `inventory_error` and `mount_result` events). A mount is a cast, so the time is the cast time (up to a few seconds)
plus latency. `walk_to` and `walk_path` work while mounted: the server sets the mounted run speed, and the agent acks it like any
speed change. `dismount` cancels the mount aura with `CMSG_CANCEL_AURA` (1.12 has no separate dismount packet). The agent cannot tell which aura is the mount's, so it
remembers the one new buff that appeared in the same update as the mount display; with several, or after a login while
mounted, `dismount` asks for `spell_id`.

A vanilla mount item is a bridle, and its use is a cast of its own: spell 46499 ("Add Mount to Collection", about 4 s in the #61
run). When that cast ends the server sends `SMSG_LEARNED_SPELL` (`spell_learned`), teaches the mount spell and uses the item up;
no mount follows. So `mount` with an `item_entry` waits for that learn, lets 250 ms pass for a second one, and then casts the
learned spell on yourself, as the spellbook does, provided it is a mount spell. The list is `internal/spells/mounts.txt.gz`: the
spells that apply `SPELL_AURA_MOUNTED` (aura 78) in an effect, from the core's `spell_template` by `make spellnames`. The reply
then carries `learned_spell`. The mount is a 3 s cast, and `wait_ms` (default 12000) covers the use and the mount. Nothing is
cast when the use taught two spells, when the spell is not a mount, or when the learn came after `wait_ms`: the error names the
spell (`data.learned_spell`, when there is one), and `mount` with its `spell_id` casts it if that is what you want. The agent has no item data, so a mount item is
recognised only by what it teaches.

Run live (m910q, build 17, #61): `use_item` food and drink (Food and Drink aura, `stand_state` sit), `loot` (by name, `too_far`
refusal, `loot_take` one slot, `all` and `money`), `mount` by spell and `dismount`. A vanilla mount item (a bridle) is a use-to-learn item: `mount` with an `item_entry` ended in the "not mounted" error in
that run (turtle-agent#64), which read the use as having no cast; the use is a 4 s cast (spell 46499). The learned-spell cast
above is not run live yet, so mount by `spell_id` until it is. The packets of the trainer list, the quest
panels, the loot window and the forced run speed are tested against bytes from that run
(`internal/game/realpackets_live_test.go`).

## Trading

`trade` hands items and gold to another player, as a player at the trade window would: `CMSG_INITIATE_TRADE`, then the other
player's client answers with `CMSG_BEGIN_TRADE` and the window opens (`SMSG_TRADE_STATUS` open), `CMSG_SET_TRADE_ITEM` and `CMSG_SET_TRADE_GOLD`
fill it, `CMSG_ACCEPT_TRADE` accepts, and the trade completes when the other side accepts too. The agent does not take part in a
trade the other side starts: `trade_status` `begin` is logged (`data.declined`) and answered with `CMSG_CANCEL_TRADE`, because the core opens the trade on both sides and a player with one open cannot start another. It does not take items: `received` only reports
what the other side put in the window, which the accept takes along.
An idle agent therefore declines every incoming trade, a bot's included: a TortoiseBots bot starts one on the `trade` whisper (`TradeAction.cpp:47-49`), from its RPG sub-actions and from the `.bot` trade command. Accepting gifts from party members or owned bots would be a follow-up, off by default.

- **Range.** The other player must be in view and within 10 yd (the core allows 11.11, `TRADE_DISTANCE`, at the start and again when it
  completes the trade); farther is an error before anything is sent, naming the distance. Walk up with `walk_to` first. A dead player,
  yourself, a creature, too much gold, an item the bags lack, and more than six stacks (the window has six slots) are errors before anything is sent too.
- **Counts.** The window takes whole stacks: `CMSG_SET_TRADE_ITEM` has no count. A request is met by one stack of exactly that count; else by
  splitting the count off the smallest stack that has more (`CMSG_SPLIT_ITEM` into a free slot of the backpack or a bag, chosen when the split is sent, once the
  window is open); else by whole stacks, the largest first, and the last piece the same way. Two entries for one item add up. A split stack that
  stays behind when the trade fails is not merged back.
- **Timing.** The core refuses an accept less than a second after the last change to the window (`back_to_trade`; the delay counts whole seconds).
  The agent waits 1.1 s after filling the window, accepts, and accepts again when `back_to_trade` comes (up to four times; the other side changing
  the window causes it too). Nothing is cancelled while the other side is still deciding: a trade that times out, or ends in a refusal, is
  closed with `CMSG_CANCEL_TRADE` so nobody is left in a window.
- **Result.** `completed` also says `verified`: the bags no longer hold the items and the purse is down by the gold, seen in the update
  fields after the status (the purse allows for gold the other side put in; false with a `detail` when the change does not show within 2.2 s). A failure carries the last status in `data.status`.
  Every `SMSG_TRADE_STATUS` is a `trade_status` event: `data.status` (`begin`, `open`, `accepted`, `back_to_trade`, `complete`, `cancelled`,
  `too_far`, `busy`, `no_target`, `ignored`, ...) and `data.code`, the other player as `source`; `accepted` and `complete` add `data.their_items` and `data.their_gold`.
- **TortoiseBots bots.** `TradeStatusAction` (`ai/playerbot/strategy/actions/TradeStatusAction.cpp`) opens the window for the bot's master and
  for members of its group (otherwise it cancels), whispers its inventory ("=== Inventory ===") and accepts when the agent accepts. A bot
  that is not a random-pool bot takes a gift at once. A random-pool bot goes through its buy check: an item it has no use for
  ("I don't need this"), or gold with no item to price, makes it not accept, and the agent times out. A mage bot may add conjured food and
  water to the window (`AutoShareConjured`); they show in `received` and the accept takes them. The bot says why it refuses in a whisper:
  read `chat_log`.

Not verified live: see [docs/LIVE-CHECKS.md](docs/LIVE-CHECKS.md), "Trade". The packet bodies are checked against the core's `TradeHandler.cpp` and
fixtures built from it; the dungeon driver has no `give` step yet.

## Game objects

`gameobjects` lists the game objects the server has put in view: doors, levers, chests, altars, braziers, nodes, campfires, seats.
They come from the same update stream as `units` and never appear in it. A row says what the object is (`entry`, `name`,
`type` and `type_name`, the core's `GameobjectTypes`), where it is (`x`/`y`/`z`/`o`, `distance` and `distance_2d` from the player;
both -1 for a ship or elevator that rides a transport path and has no position of its own) and what state it is in:

- `state` is `GAMEOBJECT_STATE`: `ready` for a door that is closed or an object not yet used, `active` for one that is open or used,
  `active_alt` for the alternative use. An absent field is `active` (0), as the client reads it.
- `flags` is `GAMEOBJECT_FLAGS` (1 in use, 2 locked, 4 interact condition, 16 no interact, 32 never despawn) and `dyn_flags`
  `GAMEOBJECT_DYN_FLAGS` (bit 0 is the sparkle of a chest or node that can be opened). `locked` is flag 2.
- `usable` is a hint, not the server's verdict: false for a generic object and for one flagged no-interact, in use or with an
  interact condition. The server also checks reach and its own conditions (a key, a quest, a faction), and says nothing when it refuses.
- `name` is empty until the server's `SMSG_GAMEOBJECT_QUERY_RESPONSE` has arrived; the agent asks for it when the object comes into view,
  once per entry (an entry the server does not know is named `gameobject <entry>`).

`use_object` sends `CMSG_GAMEOBJ_USE` for a game object in view, as a right-click does, and waits up to `wait_ms` for the first thing the
server does about it: a `gameobject` event of that object (a door or lever changing), a `loot_open` or `loot_error`, a `gossip` or quest
panel, a `vendor_list` or `trainer_list` of the object, or a spell the object casts or the player casts at the object (`spell_start`,
`spell_go`; one at another target, a `spell_fail` and an `inventory_error` name no object and are no answers, read them with `events`
from `since`). A door that another player opens during the wait counts as well: it is the same `gameobject` event. `outcome` is that event's kind and `event` the event; `since` is the log cursor from before the
use, so `events` with `since` reads everything that followed (a door sends a `flags` change and a `state` change, and a lever
may open other doors). An object not in view is an error. The core ignores a use out of reach, of a generic object, of one flagged
no-interact and of one the player may not use (`HandleGameObjectUseOpcode`), without a reply: then `outcome` is `none` and `hint` names
what the update fields make likely (distance over 5 yards, unusable flags, locked). `use_object` does not walk: bring the player next to the object first
(`walk_path`).

Chests, mining and herb nodes and other locked objects are not opened by `CMSG_GAMEOBJ_USE` on this core: its chest branch only runs the
object's scripts (`GameObject.cpp:1493-1514`), `CMSG_LOOT` on a game object guid is refused (`LootHandler.cpp:414-450`), and the client
opens them by casting an Opening spell at the object. `use_object` on one therefore ends in `none`; `open_object` is the call for them.

Without `spell_id`, `open_object` casts the Opening spells the character's spell book has, 6477 first (the core drops a cast of a spell the player does not have without an answer, so an unknown one would only run out the wait; a given `spell_id` that is not in the book is refused at once). A refusal that another Opening spell may not share (`bad_targets`: the lock names another type; `low_castlevel`: skill) goes on to the next one; the error then lists each spell and the `SpellCastResult` by name and number.

`open_object` casts the spell (default 6477 "Opening", effect 33 `SPELL_EFFECT_OPEN_LOCK`; every race has 3365, 6247, 6477, 6478, 21651 and 22810, all
the same, and the core lets a client cast only a spell in its book, `SpellHandler.cpp:340-347`) with `CMSG_CAST_SPELL`: `u32 spell`, `u16 0x0800`
(`TARGET_FLAG_OBJECT`), the object's packed guid (`Spell.cpp:198-199`). The server checks the lock (`CanOpenLock`, `SpellEffects.cpp:1915-1988`),
the reach (the interact distance, about 5 yards) and the line of sight (`Spell.cpp:5920-5935`), and after the cast time (about 5 s in the
spell table) answers with `SMSG_LOOT_RESPONSE` (type 2 on the wire: `Player.cpp:9614-9637`), the same window a corpse gives, so
`loot_take` and `loot_close` work on it, and `CMSG_AUTOSTORE_LOOT_ITEM` and `CMSG_LOOT_RELEASE` are the corpse's. The player must stand within
reach and not walk during the cast. With `take: true` the call takes everything and closes the window (`taken` lists the slots). A cast
that fails ends the call with the reason (`spell_fail` carries the code); a server that says nothing (the object is not spawned, or
gone) runs out the wait. An object that is not a chest (type 3) is refused before anything is sent: doors, levers and goobers take `use_object`.
Which lock types the Opening spells open (lock 93 of Baelog's Chest was not readable without `Lock.dbc`) is checked live: [LIVE-CHECKS.md](docs/LIVE-CHECKS.md).

A change of a game object the agent already knows is a `gameobject` event with the object as `source` and `data.change`: `state`
(`old`, `new` as above), `flags` (`old`, `new` numbers, `locked`), `dyn_flags` (numbers), `animation` (`anim`: `SMSG_GAMEOBJECT_CUSTOM_ANIM`),
`reset` (`SMSG_GAMEOBJECT_RESET_STATE`), `spawn` and `despawn` (the animation packets) and `destroyed` (`SMSG_DESTROY_OBJECT`). `data.entry` and
`data.type` (the type name) go along when the object is known; an animation packet for one the agent never saw has neither. The create block of an object reports nothing, so entering a zone does not flood the log, and an object
leaving range is not reported either.

## Dungeon plans

A dungeon plan is a JSON file, `dungeons/<name>.json`, that says where a dungeon leader walks, which packs it pulls, which doors it
opens and which boss it kills (issue #73). The format, the loader and a pure stepper over it live in `internal/dungeon`; the driver
that carries a plan out through the RPC is described under [Dungeon runs](#dungeon-runs). `dungeons/uldaman.json` is the first one (all of Uldaman, see
[The Uldaman plan](#the-uldaman-plan)). `dungeons/zulfarrak.json` (Zul'Farrak, map 209) is the second: eighteen legs (the road to Antu'sul in six: `antusul_a`, `antusul_a2`, `antusul_a3`, `antusul`, `antusul_c`, which starts with the hop to 1633 868 so that the walk to the pack at 1680 871 passes 30 yd south of the Shadowcaster camp, and `antusul_b`, which starts over the ledge 1769 803 and ends with Antu'sul; then Theka the Martyr and Hydromancer Velratha in two legs, `velratha` (the west road, ending at the pack of four at 1601-1610 1104-1111) and `velratha_b` (starting at the north stand 1645 1144, with the pack of five at 1636 1105, which is walled off from the west, then Velratha); then the optional `gahzrilla` leg (the Gong with the Mallet of Zul'Farrak, see [Zul'Farrak events](#zulfarrak-events-gahzrilla-the-courtyard-and-zumrah)), then, PR B, the `courtyard` after it and `zumrah`, the Witch Doctor behind an `area_trigger` step, then six legs written from the instance script, [the stairs event](#zulfarrak-the-stairs-event-bly-and-ukorz): `stairs`, `stairs_top`, `stairs_base`, `weegli_door`, `bly` and `ukorz`), written from the world database export in `tools/dungeongen/testdata/zulfarrak`; it has no `entrance_tele` (the game has no teleport inside the instance) and a remote run enters by `cmd enter 924` at the `portal` (TortoiseBots #939; on a module without that order the entry fails and the run says so: the module takes an area trigger only from the master's `CMSG_AREATRIGGER`, `PlayerbotAI.cpp:304`, `TeleportAction.cpp:41-47`, a headless bot never sends one, `.tele name` takes `game_tele` names only and `.go xyz` moves only the caller). The plan's `entrance_trigger` 924 targets map 209 at the plan's `entrance`. The plan holds every part of the instance but the Turtle-custom Farraki Arena wing: the Gong is the optional `gahzrilla` leg, Witch Doctor Zum'rah (faction 35 until area trigger 962 fires) is in it since PR B, and the stairs event, Bly's crew, the End Door and Ukorz with Ruuzlu were moved into it from the retired `zulfarrak-extra.json` when the `gossip` step and the unit waits made them runnable. The positions were checked live on 2026-10-14 by a GM-invisible walk of the whole instance (about 3130 yd, nothing attacked): static spawns match the export, Theka and Velratha patrol (the plan meets them on their patrol, boss fights search 80 yd), the End Door is shut and no leg routes through it, the graveyard of trapped Shallow Grave chests is avoided (the `zumrah` leg goes in and the `stairs` leg out, both with the bots' looting off). The six legs after Zum'rah have not run live. The tests (`TestZulfarrakOnNavmesh`; map 209 mmaps in `$TURTLE_AGENT_MMAPS`, `~/tb-ci/mmaps-turtle209` or `~/tortoise-wow-server/data/mmaps`) put every waypoint on the map 209 navmesh.

```json
{
  "version": 1, "name": "uldaman", "map": 70,
  "entrance": {"x": -228.9, "y": 46.1, "z": -46},
  "legs": [
    {"name": "keystone", "to": {"x": -247.5, "y": 173.1, "z": -43.5},
     "pull_groups": [{"entries": [4850, 4850], "ambush": true, "positions": [{"x": -232, "y": 198.1, "z": -44.5}, {"x": -223.6, "y": 198, "z": -44.5}]}],
     "skip": [11073],
     "steps": [
       {"use_object": {"entry": 124371, "near": {"x": -234.7, "y": 239.6, "z": -50.9}, "stand": {"x": -234.7, "y": 237, "z": -50.4}}},
       {"wait_for": {"gameobject": {"entry": 124372, "state": "active"}, "timeout_s": 60}}],
     "boss": {"entry": 7228, "position": {"x": -235.7, "y": 309.6, "z": -47.6}, "first": []}}
  ]
}
```

- `kind` (`"dungeon"`, the default, or `"raid"`), `status` (`draft`, `mapped` or `live-checked`, then free text: how far the plan has been checked), `hire_composition`
  (a dungeon's party, `class:role` each, the same rules as `prepare.hire.composition`; the [hired party](#hired-party-prepare-party-hire) takes it when the prepare block gives none), `fleers`
  (creature entries that run at low health, EventAI "flee at 15 % HP"; remote mode kills them before they are out of reach and waits for one that got away, see *Runners* under tank first) and `raid`
  (`{"size": 40, "tanks": 4, "healers": 10, "dps": 26}`, required for a raid plan, size 10, 20 or 40, the roles a placeholder). A **draft** is the skeleton `tools/dungeongen` writes from the world export
  (`"status": "draft (not mapped, not live-checked)"`): `dungeon_run` refuses it unless `allow_draft` is passed (the plan has had no mapping walk) and refuses every raid plan (no raid driver yet).
  Every dungeon and raid of the server has a draft or better; see [docs/PLANS.md](docs/PLANS.md) for the table (level band, status, mmaps, the events, rituals, gossip and keys each needs).
- `version` is 1 (a file of another version is refused), `map` the instance map id, `entrance` where the party stands after the
  portal. `notes` (plan and leg) are free text for people.
- `entrance_trigger` (the id of the entrance's area trigger, Uldaman 286) is what a ghost's corpse run sends at the portal. A remote run's
  enter phase reads two more optional keys: `entrance_tele` (the `game_tele` name that puts a player at the entrance inside, Uldaman
  `ulda`, game_tele 169; letters, digits and `_`) and `portal` (`{"map": 0, "x": .., "y": .., "z": ..}`, the centre of the entrance's area
  trigger on the outer map; Uldaman: areatrigger 286, radius 10). `outside_tele` (a `game_tele` name; Uldaman `uldaman`, the entrance on the outer map, map 0) is what the
  `prepare.fresh` step teleports the bots and the agent to before it unbinds them. A plan without them can only be entered by hand.
- `level_band` (optional, `[low, high]`, 1 to 60; Uldaman `[41, 45]`, Zul'Farrak `[44, 48]`) is the levels the dungeon is meant for: the
  [hired party](#hired-party-prepare-party-hire) takes the middle of it as its level and accepts no other level than the band's.
- `tank_min_hp` (optional, `0` or `100` to `20000`) is the least max health the hired tank should have for the plan's level band (Ragefire
  Chasm `800` at 13-18); the [kit check](#remote-leader) warns below it. `0` or absent: no check.
- `hire_composition` (optional, the form of `prepare.hire.composition`) is the hired party when the run's `prepare.hire` gives none; the loader
  checks it like the run's (one tank, one healer, two to five bots). Zul'Farrak hires `warrior:tank`, `priest:healer`, `mage`, `hunter`, `warlock`:
  the mage is the cc bot of its big packs.
- `cc_priority` (optional, creature entries, best first) is where the pack CC puts its mark (see **big packs** under [Dungeon runs](#dungeon-runs)):
  the casters and healers its cc bot's Polymorph takes. `cc_always` (optional, entries) get the CC in any pack they are in, below `big_pack` too,
  and are not picked as the pull target while another member is there. Zul'Farrak (see `kill_first`): `cc_priority` Shadowhunter (7246),
  Shadowcasters (5648); `cc_always` the Shadowhunter, whose Hex (11641) takes the tank out (owner rule 2026-10-14). The Witch Doctor (5650) is not sheeped: it drops the Healing Ward and casts Flash Heal, so it is in `kill_first` (owner decision 2026-10-15).
- `no_cc` (optional, entries) are entries the pack and reactive CC never take, besides the plan's bosses (`boss.entry` and `boss.first`), its `kill_first` entries and any unit with a pet's GUID (never CC'd either: live, Velratha was sheeped 8 times with no cast landing, the Witch Doctor 10 times against the plan, Zombies and Dead Heroes 9 times for one cast). Zul'Farrak: the Zombie 7286 and the Dead Hero 7276.
- `kill_first` (optional, creature entries, best first) are the entries the party kills before the rest of a fight, unless one is crowd-controlled:
  after the tank's order (`rankByTank`) the remote driver stable-sorts them ahead, in the list's order. It only orders targets the fight already has.
  A `kill_first` entry is never crowd-controlled, unless it is in `cc_always` too (the loader refuses one that is only in `cc_priority`): then the cc bot sheeps one
  of them (it holds one CC) and the others are killed first. Ragefire Chasm: the Shaman 11319 (rfc-live-2, the wipe on leg 2 pull group 3).
  `summoned_kill_first` (optional, a subset of `kill_first`) names summoned units that fight nobody (a healing ward): such a unit is part of the
  fight only with encounter evidence, standing within 10 yd of a living hostile that fights the party; a ward by an unengaged pack is ignored. It is
  then sent at with no threat wait (a planned healer with no target still waits for the tank's threat) and is never a target of the reactive CC.
  Zul'Farrak (issue #190, from nine commander runs where every wipe was on a pack with a healer): `cc_priority` [7246, 5648] and
  `cc_always` [7246] (the mage sheeps the Shadowhunter that Hexes, in every pack it is in; the Witch Doctor is killed first and never sheeped: owner decision 2026-10-15), `kill_first`
  [7785, 8179, 5650, 7247] (Zum'rah's Ward, the Greater Healing Ward the Witch Doctor's Healing Ward summons, the Witch Doctor, the Soul Eater), `summoned_kill_first` [7785, 8179].
- `watchdog` (optional, `{"warn_s": .., "recover_s": .., "fail_s": ..}`) sets the plan's stall levels for the [run watchdog](#run-watchdog);
  the loader refuses levels that are not in order (`warn_s` < `recover_s` < `fail_s`, defaults filled in). `dungeon_run`'s `watchdog` wins field by field.
- `avoid` (optional, at most 32) lists regions of the map that kill although the navmesh calls them ground:
  `{"name": "lava basin of the trogg cave", "min": {"x":..,"y":..,"z":..}, "max": {..}, "why": ".."}`, a box from `min` to `max` (a floor
  band: its top just above the deadly surface, so ledges above it stay walkable). The mmap generator flags a liquid only where it is at
  least the agent's height deep, so shallow lava (Ragefire Chasm's basin, under a yard deep, 605-610 damage a tick) is plain ground to both
  the agent's planner and the server's. The loader refuses a box with `min` not below `max`, wider than 400 yd, and any plan point in a box
  (entrance, `via`, `to`, pull group positions, boss, step stands); [Plan check](#plan-check) fails a look point in one, and a point the
  navmesh reaches only without the boxes fails as `cut by an avoid box`, naming the box. The agent's planner marks every navmesh polygon
  whose part inside a box (the polygon clipped by the box, heights included) covers a quarter of its area or more as lava
  (`navmesh.Planner.SetAvoid`, `navmesh.AvoidShare`), so paths, floor snaps and walkability rays keep out of it while a box that only clips
  a corner of a ramp or doorway polygon leaves it walkable. The remote driver also: cuts a walk's hop at a path corner when its straight
  line would pass over a box (the server walks each `move` on its own navmesh); does not pull on the way a hostile the party reaches only
  round a box (path longer than twice the straight line plus 25 yd); summons no bot onto a lead bot standing in a box or on a **deadly
  spot**; and raises no body lying in a box in place (it releases it). Before a summon a lead bot on such ground is moved to the nearest
  safe floor (up to 20 yd), twice at most; then, once per leg, it walks back to the most recent place it stood on alive and safe in this run
  (its history, each place tagged with the run's progress: never one ahead of where the run is now; a run put back, and a wipe, drop the
  places past it or of the step under way) that is 10 yd or more clear of every box and spot and reached by a route that crosses no box
  (each segment tested exactly against each box; the first may only leave the box the lead bot stands in, never deeper) and passes no spot
  within 6 yd, and the party is summoned there. No such place, a failed walk back, or a second need in the leg fails the step
  (`the lead bot is stuck on deadly ground`): the recovery never loops. A **silent
  death** is a bot that died out of combat at 80% health or more one frame before (the feed has no line for lava, slime or a fall):
  inside a box it is only noted; outside, one alone is noted and a second within 6 yd makes a deadly spot. A spot is forgotten 5 min
  after its last death, or once a living bot has stood on it 5 s. Each silent death is a `silent_death` trace event.
- `via` (optional, per leg) lists waypoints walked in order before `to`: `{"x":..,"y":..,"z":..}` is a plain walk on the navmesh, `"exact": true` on such a point makes a remote run hold the lead bot on it (see below); with `"direct": true` the step is a straight line, for a gap the
  navmesh does not bridge (Uldaman's scorpid-pit ramp; [Plan check](#plan-check) warns of a direct hop the navmesh already joins). The remote driver sends it as `.bot cmd move x y z direct` (TortoiseBots #942: every bot walks the line; each must be within 25 yd
  across and 8 yd up or down of the point, and the point needs ground within 3 yd of its height). The loader refuses a direct step without a via point before it (where the party gathers) or longer than
  those limits. **The party is gathered on the lead bot before every send of a direct step and again once the lead bot has arrived** (TortoiseBots #948 takes the step only with every follower within 5 yd of the lead bot's spot, 1 yd up or down, in its sight, or a navmesh path of at most 40 yd to it; Uldaman hires-2 run 10 was refused `too-far: Hega is not with ...` one second after the first step): every living bot within 5 yd of the lead bot (horizontally; the module takes a straight join within 5 yd) and 1.5 yd above or below it (`cmd follow` holds the bots 5-7 yd back, so the old 3 yd bar always failed); nothing is sent for the first 3 s, then one `cmd move <the lead bot's x y z>` (the party walks to the lead bot's spot; sent once more at 9 s for a bot still apart), and once the party stands there a `cmd stay` holds it on the spot. **`cmd summon` is never sent for a gather**: it set the bots' follow to the commander, who stood outside the instance, and 15 s later the whole party was ejected to Northshire (Uldaman hires-3, run 1). The wait ends at 25 s whatever happens (a note names who was still away, `direct_gather_timeouts`, and the same step does not wait again). Counters: `direct_gathers`, `direct_gather_wait_s`, `direct_gather_timeouts`, `direct_gather_moves`, `direct_gather_stays`. If the module answers `too-far` naming a bot (`<Bot> is not with <Lead> ...`: it fell behind after the gather) the party is moved to the lead bot's spot at once (again after 2 s) and the step is sent once more; other `too-far` answers (a follower still catching up) send the step again; `no-ground` (no floor under the point) and `no-los` (a bot cannot see the point: the step would cut through a wall) fail the leg at once; `invalid` means a server without the flag and fails the leg with
  that error (nothing is sent in its place). The agent's own companions walk a direct waypoint as a plain one.
  **`"exact": true` on that via point** (opt-in; the loader allows it only on a plain waypoint whose next via is a direct step that is not a
  `gm_cross`, and refuses any other use) makes the remote driver hold the lead bot on that point before the direct step goes out, instead of
  letting the walk's own `move_arrive` arrival (5 yd) do. Without it nothing changes for any plan (Uldaman leg 5's launch point at the scorpid
  pit lip ran live under the 5 yd arrival and still does; Wailing Caverns' foot uses it, and the step was taken live three times from a foot held at
  0.00 yd, tests/wc-hop-probe-2, though no run has used the driver on that hop yet). An `exact` point is always walked, whatever the leader's distance — even standing on
  it: the driver's tighten is where the proof below runs, and it leaves out the movement when there is nothing to move. The point must be
  reached tightly:
  the walk's arrival is wider than the last stretch, so the lead bot is often **still walking in** when the walk ends (no order goes out over
  that, and the driver waits) and **the module carries out no plain move shorter than about 6 yd** (`min_move`, ZF-1 run 4: a stretch of 5.2 yd
  was ACKed and ignored five times), **measured on the ground plane** (`GetDistance2d`: a stretch of 6.3 yd on a slope but 5.3 yd flat was never
  walked, rfc-live-2), so a move onto a point 2-5 yd away does nothing. The driver therefore walks the lead bot **back along the path it just
  came** — the points the walk sent it to, never a point off that path and never one past the waypoint — to a point at least `min_move` + 0.5 yd
  out **on the ground plane** from both the lead bot and the point (so both the move back and the move onto it are carried out), and then moves
  it onto the point. Before the direct step goes out it wants **proof**: fresh frames showing the lead bot within 1.5 yd of the point **and**
  standing within 0.5 yd of one fixed anchor position for 2 s of fresh looks — a quiet feed, a feed that does not list the lead bot, and a bot
  that crawls on (each look inside a fresh frame, but drifting from the anchor) prove nothing. The whole tighten is capped at 25 s (a fight at
  the point has its time given back) and at most three rounds of the walk back and the move onto the point. **No proof by the cap fails the
  step** and **no direct step goes out**, with the error naming the waypoint, the distance and why (still moving, never still, no lead bot in
  the feed, a quiet feed): `launch waypoint x y z of the next direct step: N yd away and <why> after 25 s; the direct step is not sent`. The
  point may be refused by the module's own `no-los`/`no-ground` when the step is sent; there is no soft path from short of it.
  **`"gm_cross": true`** (on a direct waypoint; the Disciple hop of `dungeons/wailing_caverns-extra.json`, a gap no walk crosses; Wailing Caverns' ramp was the example until 2026-10-18, when its hop became a plain direct step with a `check_skip`) is not walked: a GM account moves the party to the point (the commander's own character `.appear <lead bot>` when it is not near and `.go xyz`, then `.bot summon <lead bot>` with the lead bot held where it lands, `cmd summon <bot>` for each bot that stands apart, `cmd follow`; the same machinery as `from_leg`'s start), and the run goes on once every expected living member (the lead bot and the named bots; a bot a frame does not list is summoned by name and counts as not there) is listed by a frame published after the orders and stands within 10 yd of the point; `cmd follow` is sent up to three times and refused three times fails the step, and an empty or partial frame never counts as "already there" (a note `gm_cross: gm_cross at x y z: N bots moved`, `remote.gm_crosses`, each GM line in `setup_shortcuts` marked `(GM crossing)`). The point may be anywhere on the map (no direct limits, no via point before it). It needs a remote run with a GM account that may `.go xyz` (level 1 and up, read with `.account`: the same probe as keep-near's `.appear`, so a run that notes "the agent's account level is 3" passes; the group driver's `leader_gm` and `gm_caps` report fields stay false on a remote run and are not read): `dungeon_run` refuses a plan with a gm_cross from `from_leg` on before anything is sent when the account is not one (or the game cannot move the commander's character), and the group driver refuses the plan; `plan-export` refuses it (the TortoiseBots run leader has no such step). The plan check does not walk to it (it lists it as exempt) but wants mesh floor under it.
- A **leg** is done in this order: walk the `via` waypoints, then to `to`; pull each of `pull_groups` in order (`entries` are the creature entries, one
  position per member in `positions`, the first is the one to pull; `ambush: true` marks a pack of stealthed mobs, see
  [Dungeon runs](#dungeon-runs); `needs_walk` (set by tools/dungeongen on a pull group, a leg or a boss whose position has no navmesh floor within reach; the mapping walk replaces it); `equiv` (a label of up to 40 characters) marks packs of the leg that are equivalent for the [pull test](#start-anywhere-and-the-pull-test); `radius` (yards, 1-60, default 25) is how far from a position a unit of the entries may stand and still be
  a member of the group, for packs of one entry that stand closer together than that; `patrol` (optional,
  `{"entries": [..], "path": [{"x":..,"y":..,"z":..}, ..], "period_s": .., "source": ".."}`) is a patrol that walks by the group: its members' entries,
  its waypoints in walking order (at least two; it goes on from the last to the first, as the core's waypoint movement does: take them from
  `creature_movement` for the spawn guid) and one round in seconds (1-900: the path's length at 2.5 yd/s for `speed_walk` 1); `period_estimate: true` says that round is only that estimate and not measured: the driver then predicts no phase from it (the patrol is a danger while the pull lies within 150 yd ahead of a member on its path, so the party waits until it is past, and a patrol out of view is looked for the whole `patrol_locate_s`), notes it once per group, and the plan check warns for each such patrol (the mapping walk measures the round and drops the flag); see **patrols** under
  [Dungeon runs](#dungeon-runs)); `approach` (optional, at most 4 points, also on `boss`) is where the TortoiseBots run leader's party walks before that pull, in order, the last
  being where it stands to engage: `plan-export` writes them as `move` rows before the pull, the plan check walks them, and the agent's own driver does not use them (it looks at the pack
  from its path); `stand_pull` (optional, also on `boss`; needs `approach`, never with `hold` or `ambush`) makes the run leader hold at the last approach point and pull the pack from there
  once a unit of it is in the tank's shot and, on a pull group, no other patrol stands within `clear_yd` (1-60, 0: the run leader's 40) of the lead bot: `plan-export` sets flag 8192
  on the pull row and writes `clear_yd` as its `param` (TortoiseBots `specs/rl-patrol-timed-pull.md`, for formations that walk loops near other formations); `hold` (optional, also on `boss`: `{"instance_data": 1, "spot": {..}, "radius": 30, "timeout_s": 600, "entries": [..]}`) makes the run leader's row for the group a
  scripted event's `hold` (TortoiseBots #1041: stand on `spot`, fight what the event sends within `radius` until instance data `instance_data` reads DONE; `entries` are more of the
  event's mobs, the group's own and a boss's entry and `first` are always in); the agent's driver ignores it; run `steps` in order; walk to `boss.position` and kill the
  boss; run `after_boss` (steps of the same kinds, done once the boss is dead or skipped). Each part is optional, but a leg needs at least one of pull groups, steps, a boss and after_boss steps. `skip` lists creature entries the leg
  never waits for (rares, escorts, friendly NPCs, adds that die with the boss). `boss.first` lists entries to kill before the boss,
  in order. `rest` overrides the leg's rest gates (all keys optional, `0` or absent keeps the default): `{"health": 90, "healer_mana": 50, "mana": 35, "max_s": 60}`
  (percentages 1-100; `max_s` 1-600 seconds; `walk_yd`, `walk_health`, `walk_healer_mana` and `walk_mana` set the walking rest, see **rest gate** under
  [Dungeon runs](#dungeon-runs)). `companions_loot: false` on the **leg** has the party's looting off for the whole leg, from its start to its end (`.bot behavior <bot> loot off` for every bot, `on` after; remote runs). A leg that cannot have it does not start: a game that cannot send behavior orders, or a bot that refuses, fails the leg before it moves, naming the bots (an empty party counts as a failure), and the bots already turned off are turned on again. A bot that joins the party during the leg is turned off too, before the next step (a bot that cannot be fails the step). At the leg's end each bot's looting is turned back on with up to 3 tries; a bot that still refuses, or a game that cannot send the order, ends the run **"done (degraded: looting left off on <bots>)"**: the state stays `done`, the report's `degraded` list and the `dungeon_done` event carry it, and the next run's start turns looting on for everyone. The same marks a run whose agent could not be put back at its safe point after an area trigger (a failed walk **and** a failed or ineffective `.go xyz`). Group mode refuses a leg with `companions_loot: false` at its start (only a remote run can enforce it for a whole leg), and the loader accepts it together with a `boss.loot_item` in the same leg (the remote run's `cmd loot` is an explicit order; the leg's mute is not turned back on by the boss; Zul'Farrak's stairs leg takes the Executioner's Key that way). the Zul'Farrak graveyard, where a bot that loots opens a Shallow Grave chest and wakes what lies in it. It is not the boss's `companions_loot` (below), which is only for the fight and the leader's loot; a boss in a leg that has it off does not turn the looting back on. Absent or true leaves the companions as they are.
- `leave` on a **leg** (`[{"entry": 7246, "spawn": 81610, "at": {"x":..,"y":..,"z":..}, "why": ".."}]`; `entry`, `at` and `why` are required, `spawn` is the creature guid, optional) names spawns a run must not pull, fight or walk up to: Zul'Farrak's lone Shadowhunter on a navmesh island (zf-r6-19z24: the run leader's hop pull engaged it and the tank ended on the island, where every move was refused). A spawn stands for the whole run, so every leg's points are held to every leg's list. The loader refuses a leave that is a pull group position of its own leg. The plan check (`internal/dungeon/leave_clear_test.go`, runs with `go test`) fails a plan with an approach point, a computed or written stand, a `to` or a via point within `LeaveClearYd` (15 yd, the run leader's hop pull reach) of a leave, and walks each pull group's approach rows on the navmesh (`TURTLE_AGENT_MMAPS`) to check the walk keeps 15 yd too (shortest distance over every segment), and the run leader's own walk from each stand toward its pack and back; the Zul'Farrak walks that plan data cannot move (5.0, 5.0 and 13.6 yd) are recorded as known entries. The agent's driver does not read the list. **Deploy order:** the loader refuses unknown fields, so a binary older than this change refuses `dungeons/zulfarrak.json` with `unknown field "leave"` (and any plan with a `leave`): ship the loader (turtle-agent dev) before the plan file. **`plan-export`** has no row for it: the list is written as free text in the leg row's comment (`leave (not read by the run leader yet): 7246 spawn 81610 at 1591 1047.7: <why>`, cut to 255 characters) and is one warning (`leave`, not in the rows); a module that reads it would need a step kind or a flag on the leg's pull and hop rows (PLAN-ZF-LESSONS #3, TortoiseBots part).
- `optional` on a **leg** (`{"item": 9240, "name": "Mallet of Zul'Farrak"}`) marks a leg that needs an item no honest run carries (Zul'Farrak's gong needs the Mallet of Zul'Farrak): at the leg's start a remote run
  reads the bags (`.bot inv`) of the carrier and the lead bot and, when neither has the item, **skips the whole leg**: nothing of it is walked, pulled or used, its `legs[i].result` is `skipped: no Mallet of Zul'Farrak (item 9240) in the bags of <carrier> or <lead> (...)`,
  a note says so, and the run goes on at the next leg; it is never a failure. A bag that cannot be read is an error, not a skip. A group-mode run does the same with the leader's bags. The run-start checks of `requires_item` (a start past the leg that makes the item,
  the leader-mode bag check) leave an optional leg out, and the leg's item must be the `requires_item` of one of its `use_object` steps (the plan is refused otherwise). The step that uses the key is done by the bot that carries it (the carrier, else the lead bot), never by the agent's GM character (`use_object.via_item: true` makes the step `cmd use <item> <bot> <object>`: the required item is used ON the object, and the `stand` must be within 4.5 yd of `near`; remote runs only, and `plan-export` refuses it). Every order of an object step is a trace event `object_order` with its command, result (`ack` or the error code) and reply: a GM
  in GM mode goes through a lock without the item and would prove nothing about it. `plan-export` leaves an optional leg out of the run leader's rows with a warning (its bots carry no such item) and the later legs close up. `give_items` (see **Give items** under [Prepare](#prepare-one-step-before-the-bind)) puts the item in a bot's bags for a test.
- A **step** is exactly one of `use_object`, `wait_for`, `loot_object`, `use_item`, `ritual`, `area_trigger` and `gossip`. `use_object` names a game object by `entry`, by `near` (a position;
  the object nearest to it within `range` yards, default 8) or both, and may give a `stand` position to use it from (otherwise the
  driver walks next to it), and `requires_item` (`{"entry": 7733, "name": "Staff of Prehistoria"}`): the item a non-GM leader must carry
  for the use to do anything, as the object's lock names it (the Uldaman Keystone needs the Staff; the core ignores the use of a
  locked object otherwise, so the run would only fail 60 s later at the wait). The driver checks the leader's bags (backpack and bags)
  before the run starts, for every such step from the first leg on, and again before the step, and fails with an error naming the item.
  Only a leader the caller says is a GM (`gm: true`) is not checked: the core lets a GM through a lock in GM mode (`.gm on`), which the
  agent cannot see, so an account that probes as a GM but plays with `.gm off` is checked like a player. A list of bags that is empty
  (the inventory not sent yet) or has items still loading is not taken for a missing item: the check before the run lets it pass, and the
  one before the use waits up to 3 s (`ItemsWait`) for an empty list to fill, then fails if it stays bare. `wait_for` is a `gameobject` condition (the same reference plus `state`: `ready`, `active` or
  `active_alt`, the `state` of a `gameobjects` row) or `hostiles_gone` (`radius`, optional `center`: no hostile living unit that
  close) or `unit` (a creature's state, below), with a `timeout_s` (default 60, at most 600) after which the plan fails, and an optional `fail_text` (up to 400 characters, one line) that is added to that failure: the reason a person should read (Uldaman's altar waits name the three-participant ritual). A door that a lever or key opens may do so well after
  the use (the Seal of Khaz'Mul took 27 s live): keep the 60 s.
- `wait_for` a `unit`: `{"wait_for": {"unit": {"entries": [7271], "near": {"x": 1909, "y": 1015, "z": 11.5}, "radius": 40, "state": "hostile"}, "timeout_s": 20}}`. `entries` are creature
  entries (at least one), `state` is one of `seen` (a living unit of the entries is in view), `hostile` (one is in view and hostile to the party), `dead` (a unit of the entries is dead, as a corpse in
  view or as the run's kill ledger saw it, and none is alive in view) and `gone` (no living unit of the entries is in view; a corpse does not count). **`gone` holds only after a matching unit was seen alive in this wait**, and then only after two whole new frames without one (none truncated or stale): an empty or stale feed is unknown, not an answer, and a blink of the feed does not pass it. With `near`, the death the run saw (its kill ledger) counts for `dead` only when it happened within `radius` of `near`: an earlier kill of the same entry elsewhere proves nothing. `near` and `radius` (yards, default 50, only with `near`) narrow it to
  units within that radius of the point; without `near` a unit anywhere in view counts. It reads the bots' feed (the hostiles they see) **and** the agent's own object store (what the agent sees when it stands near: the friendly units
  the feed does not list), the feed first for a unit both have. A driver that cannot see units fails the step at once with "this driver does not see units". It is not carried into the run leader's rows (below).
- `area_trigger` takes an **area trigger** the way a player's client does (`CMSG_AREATRIGGER`, sent when the player steps into its zone): `{"area_trigger": {"id": 962, "center": {"x": 1909.27, "y": 1015.11, "z": 11.5}, "radius": 10, "stand": {"x": 1903, "y": 1014, "z": 11.8}, "by": "auto", "expect": "unit 7271 hostile", "timeout_s": 15}}`.
  `id` is the trigger's id, `center` and `radius` its zone (a sphere), `stand` where whoever takes it stands (inside the zone: the loader refuses a stand outside the radius), `expect` what must be true afterwards in the `unit <entry>[,<entry>...] <state>` form of the unit wait above, and
  `timeout_s` (default 60, at most 600) how long the wait for it lasts after the packet. A trigger is taken only from a client packet; a headless bot never sends one, and the Witch Doctor Zum'rah (7271) is friendly until trigger 962 (at_zumrah) runs its script.
  `by` says who takes it: `auto` (the default) lets the **lead bot** walk to `stand` first (the module runs the trigger's script for a bot that enters a zone it lists, TortoiseBots `AiPlayerbot.BotAreaTriggers`, #1002), and when `expect` does not hold within 5 s
  the **agent's own character** takes it: `.appear <lead bot>` when it is more than 40 yd from the party (a GM assist during the run, `run_gm_assists`), a walk to `stand`, the packet (`session.AreaTrigger`) and the wait for `expect`. **The step ends as soon as `expect` holds**: the fight the trigger starts is the boss step's (its own timeout, kill credit as for any boss), not the trigger step's. **Only a GM that the setup made invisible** (`gm visible off`) does this; any other account is refused with that reason (the boss the trigger wakes stands 9 yd from the stand and would attack a visible character). The agent stays in the zone, invisible, through the fight, and walks back to its **safe point** (where it stood just before this trigger's `.appear`; one on another map is not walked to) when the boss step is done or before the next walk, never through a fight: it waits for the party to be out of combat (at most 5 min; a walk that a fight stops is made again) and must get back within 6 yd. Once the boss is dead a return that fails, is blocked or outlasts the wait is **never an error**: a note and a GM `.go xyz` back to the safe point (counted in `run_gm_assists`). Before the boss is dead (the trigger's `expect` did not hold, or a later walk comes first) a failed return fails the step it ran for. `bot` leaves it to the lead bot (the step fails when `expect` never holds, naming `BotAreaTriggers`), `agent` to the agent. One attempt by each, never a loop: the step fails with the reason
  (the packet sent and `expect` not true within `timeout_s`; the agent too far and unable to follow; no hands for the packet) and ends the plan. `expect` true when the step begins, or the unit already dead (the kill ledger or its corpse in view: a resumed run), passes it with a note and sends nothing. Group mode (the agent leads) walks to `stand`, sends the packet and waits;
  `bot` fails there ("needs a lead bot"). The report's `remote.area_triggers` (`leg`, `trigger`, `by`: `lead bot`, `agent` or `already`, `result`, `seconds`, `error`) says who took each and `remote.agent_triggers` counts the agent's packets; the trace has an `area_trigger` event.
- `loot_object` is `use_object` for a chest or node (an optional `spawn`, the world database's gameobject guid, with the entry names that exact object: the remote run opens it by GUID, and skips it when `cmd state` says it is opened or gone): the same reference (`entry`, `near`, `range`), `stand`, the same walk to it, then the Opening
  cast and the loot (`open_object` with `take`: see [Game objects](#game-objects)), and `expect_item` (`{"entry": 7740, "name": "Gni'kiv Medallion"}`):
  the item that must be in the bags afterwards. The driver waits up to 8 s for it to show and fails the step, naming the item, when it does not;
  a leader that carries it already (a chest looted before, a GM's `.additem`) passes the step without opening anything (noted). One retry on a failed open.
- `ritual` has several bots use a **summoning ritual** object together (game object type 18; Uldaman's Altar of the Keepers and Altar of Archaedas need three different
  players): `{"ritual": {"entry": 130511, "position": [104.8, 272.5, -26.5], "participants": 3, "stand": {"x": 101.5, "y": 271.2, "z": -26.5}}}`. `position` is `[x, y, z]` or
  `{"x":..,"y":..,"z":..}` (the server finds the object by `entry` near it), `participants` 2-5 is how many bots use it (the object's own number is in its template; the module
  answers `not-enough` when fewer are named), `stand` is optional like `use_object`'s. **Remote runs only**: the driver orders `cmd ritual entry:<id>@x,y,z <carrier> <bot> <bot>` (the
  carrier first, then DPS bots, the healer, the tank and the lead bot last), the module walks them to the object, has them use it in turn and keeps them still until it is active and the
  ritual spell has run, and the one final answer ends the step and is its result. Do **not** put a `wait_for` the altar `active` after it: when the bots are let go their own movement
  ends the helper channel and the core sends the altar back to `ready`; wait for what lasts (the door the Stone Keepers' death opens, the boss). Group mode
  (the agent plus its own companions) fails a ritual step with "remote mode only".
- `gossip` talks to an NPC and picks one option of its menu (`CMSG_GOSSIP_HELLO`, then `CMSG_GOSSIP_SELECT_OPTION`): Weegli Blastfuse (7607) blows Zul'Farrak's End Door, Sergeant Bly (7604)
  starts his fight. `{"gossip": {"npc": 7607, "near": {"x": 1858.2, "y": 1114.4, "z": 10.5}, "text": "blow", "by": "tank", "stand": {"x": 1856, "y": 1114, "z": 10.5}, "retry_s": 15, "fail_text": "the event is not over"}}`.
  `npc` is the creature entry and `near` where it stands (its spawn): the NPC is the living creature of that entry nearest to `near` within 15 yd. The option is **`option`** (the menu's
  own index, as `gossip_hello` shows it) or **`text`** (a case-insensitive substring of the line; a text that two lines hold fails the step, the plan has to say which): exactly one of the two.
  A coded option (the client asks for a code) fails the step. `by` says who talks: `agent` (the default), `carrier` or `tank`. `stand` is optional: where the talker stands, within 5 yd of
  `near` (the core answers a hello only within about 5 yd: live 2026-10-15, Weegli and Bly answered at 3.6 yd and not at 9-10); without it the agent stops 2.5 yd short of the NPC.
  `retry_s` (1-120, default 15) is how long a menu that **lacks the option** is asked again (every 2 s): the NPC's phase lags one update behind the event that adds the option. `fail_text`
  (one line, up to 400 characters) is the reason a person should read when it still lacks it. The step fails at once, without a retry, for an NPC that is not in view within 15 yd of `near`
  (missing) or is dead, and for another panel (a vendor list) in the place of the menu.
  - **By the agent**: the agent's own character is brought up to the NPC and uses the `gossip_hello` and `gossip_select` calls; the option is matched by `option` or `text`. In a commander run
    the agent is first brought to the lead bot (the keep-near rule: `.appear <lead bot>` for a GM that the setup made invisible, else a walk, a GM assist counted in `run_gm_assists`), then it walks
    (never through a fight) to the `stand`, or 2.5 yd short of the NPC. It stays where it ends: the step does not walk it back, so an option that starts a fight (Bly's) belongs to `by: tank` or
    `carrier`, or to a plan whose later steps keep the agent out of it. In group mode the leader is the agent's own character: it does every gossip step itself (`carrier` and `tank` are noted).
  - **By the carrier or the tank** (commander runs): the party is walked to the NPC like for a chest (the approach), then the bot gets `cmd gossip <npc entry> <bot> <option>`
    (TortoiseBots #1012; `<option>` is the plan's `option` plus 1 (the order counts the menu from 1 and refuses 0, the plan counts from 0: the menu index the client sends), or the words of `text`, which the module matches as a substring: a bot's `text` may use only letters, digits and `_ . , : + -` in each
    word, and must not be a number, which the plan loader checks). The final ACK ends the step; any other refusal is sent again until `retry_s` has passed (`busy`, `in-combat` and the other
    waits as for the other orders), then the step fails with it and the `fail_text`. A module that does not know the verb (an unknown-verb refusal, or no answer to the order at all, as for
    `cmd enter`) makes **the agent take the step once** (noted, `fallback_used` in the step's report, the notes say `the module has no cmd gossip`); the agent's attempt is the last one.
  - The step **ends at `SMSG_GOSSIP_COMPLETE`** (the agent) or the order's ACK (a bot): it does not check what the option did. Put a `wait_for` after it (the door `active`, the unit hostile). A select that is
    answered with another menu, another panel or nothing within 5 s fails the step: the option was sent, but nothing says it ran.
  - It is the commander report's `object_steps` entry with `kind: "gossip"` (`carrier`: the bot, or `agent`), and `plan-export` writes a gossip step that names its `option` and is `by` `tank` or `carrier` as a `gossip` row for the TortoiseBots run leader (param = `option` + 1, the number `.bot cmd gossip` takes); a gossip by `text` or `by` `agent` still cuts the export at its leg (see the end of the Zul'Farrak stairs section).
- `use_item` uses an item from the leader's bags (`entry`, `name` for the messages) with `CMSG_USE_ITEM` on itself, waits for the
  cast, and checks `expect_item` the same way (spell 9594 of the Gni'kiv Medallion makes the Staff of Prehistoria, 7733). `requires_item` is a
  second item the use consumes (the Shaft of Tsol, 7741): checked before the use, so that the failure names it. A leader that carries the
  `expect_item` already passes the step. `requires_item` of a `use_object` that an earlier step of the run makes (a `loot_object` or `use_item` with that `expect_item`,
  or a `boss.loot_item`) is not asked for at the start of the run; a `use_item` step's own `entry` is asked for there unless an earlier step makes it (a run that starts at the
  revelosh leg needs the Medallion or the Staff in the bags).
- `boss.loot_item` (`{"entry": 7741, "name": "The Shaft of Tsol"}`): after the kill the leader walks to the boss's corpse, loots it and checks that the item is in
  its bags; the leg fails otherwise (a refused or empty corpse says the same: a companion may have looted it first). A leader that carries the item, or the product a later
  `use_item` makes of it (the Staff), passes. Without it the corpse is left to the companions, who loot their kills too.
  - **The loot method.** Under `round_robin`, `group_loot` and `need_before_greed` an item below the group's threshold (the Shaft is a white,
    quality 1, under the default uncommon threshold) shows only to the member whose turn it was at the kill, until that member opens and closes
    the corpse (LootMgr.cpp:922-946, LootHandler.cpp:668-684). With the companions' looting off that member never does: live run 4 found the
    Shaft (a plain 100 % drop of Revelosh, `creature_loot_template` 6910/7741, no quest needed) hidden from the leader, the window showing only the
    money and the rolled gloves. The window's rights come from the method at the moment the corpse is opened (Player.cpp:9566-9590), so a leader
    that leads the group sets `free_for_all` (keeping the threshold) just before its loot and puts the old method back after (both noted). The run
    fails at the start when a leg from `from_leg` on has a `boss.loot_item`, the group is not on `free_for_all` (or `master_loot` with the
    leader as master) and the leader does not lead the group: promote it, or set `loot_method free_for_all` first.
- `boss.companions_loot: false`: the driver orders `.bot action loot off` (TortoiseBots `player-controls.md`) before the boss is pulled and `.bot action loot on` after the leader's loot, also
  when the fight or the loot fails (noted in the report). Needed for a boss whose drop the plan takes: under round-robin or group loot the corpse is open to one player at a time, and a
  companion that loots first takes the item (`Player.cpp:17280-17293`). Absent or true leaves the companions alone.
  - Both orders reach every party bot. The bot module scopes a loot order to the one bot the leader has selected, so the leader targets itself
    before each order; the earlier selection is not put back afterwards.
  - **The restore is a blanket `on`.** TortoiseBots keeps each bot's own setting (`TBM:BOTSTATE|<bot>|move=…,loot=on` or `loot=off`, in the BOTSTATE
    block of the roster response), but the driver does not read it, so a bot the player had turned off is turned on again after the boss. To keep
    one off after the run, type `.bot behavior <Name> loot off`.
  - **A crash between the two orders leaves the companions' looting off.** Turn it back on in party chat with no bot selected:
    `.bot action loot on`.
- **Team gate**: `boss.team` and a pull group's `team` (`"alliance"` or `"horde"`). The driver takes the leader's team from its race (human, dwarf, night elf, gnome and high elf
  are the Alliance; orc, undead, tauren, troll and goblin the Horde: `Player::TeamForRace`) and skips what is gated to the other team, with a line in the report's
  `notes` and a `dungeon_step` event (`action: skip_team`). A leader whose race is not known yet runs everything. Uldaman's Baelog, Eric and Olaf (6906-6908) are faction 122
  quest givers, friendly to the Alliance and hostile to the Horde only: their boss is `"team": "horde"`.
- A bad file is refused with every problem at once, each with its path: `uldaman.json: legs[2].steps[0].use_object: needs an entry,
  a near position, or both`. Unknown fields are errors too, and so is an entry that is in `skip` and also in a pull group or in
  `boss.first` (the leg would fight it and never wait for it).

The stepper (`dungeon.NewStepper(plan, world, clock)`) does no networking. `Next` returns the action to do now (`walk`, `pull`,
`use_object`, `loot_object`, `use_item`, `ritual`, `area_trigger`, `gossip`, `kill_boss`, `wait`, `done` or `failed`) and repeats it until the driver calls `Complete` (or `Fail` with a reason);
it asks a `World` (position, game objects in view, hostiles within a radius; optionally `UnitWorld`, for the unit waits) and decides by itself that a walk is not needed when the
player stands at `to`, that a wait is over when its condition holds, and that a wait failed when it timed out. A `use_object`
step whose object is out of reach gives a `walk` to the object first; such a step fails after `MaxStepWalks` (4) walks or
`MaxStepSeconds` (120 s), so a door that cannot be reached ends the plan instead of looping.

`tools/dungeongen` writes a plan skeleton from the spawns of one map: `tools/dungeongen/export.sh <map> <dir>` reads the
creature, game object and entrance rows of the world database with SELECTs in a read-only session (the database of the docker
stack on the server host, over ssh; the password is expanded inside the container and never printed or stored) and writes three TSV
files (creatures, game objects, entrances; a creature row carries the `auras` it spawns with, from its template and its `creature_addon` row); `go run ./tools/dungeongen -dir <dir> -name uldaman -map 70 -bosses 6906,6910,7228 -out skeleton.json` turns them into legs (one
per boss, packs of spawns within `-cluster` yards or in one `creature_groups` group, doors and altars near a leg in its notes; friendly
factions, NPCs with `npc_flags` and rares are not packs, rares go to `skip`; a pack whose members all spawn with a stealth aura gets `"ambush": true`). It is repeatable: the same files and flags give the same
bytes. `go run ./tools/dungeongen -format dungeons/uldaman.json` checks a plan and prints it in the form the files are kept in.
`tools/dungeongen/testdata/uldaman` is the export the Uldaman plan was written from.

`go run ./tools/dungeongen -spec tools/dungeongen/plans.json -out-dir dungeons -table docs/PLANS.md` writes the **draft plan of every dungeon and raid** the spec lists (46 plans) and the table of
[docs/PLANS.md](docs/PLANS.md). The spec says per plan: the map, the entrance trigger (its target is the plan's `entrance`, its position on the outer map the `portal`, both from `portals.tsv`/`entrances.tsv` of the export), the
bosses in the usual clear order (`route` instead orders them by a nearest-neighbour walk from the entrance, `last` stays last; a boss written `{"entry": E, "first": [..]}` has those entries killed first and they
are no pack), `bbox` boxes for a map that holds several wings (Scarlet Monastery, Dire Maul, Maraudon, Stratholme, Blackrock Spire: packs outside the boxes are left out), the level `band` (default: the packs'
median level - 2 up to their 90th percentile), `size` of a raid, the hired party, the teleports (`entrance_tele` and `outside_tele` are only written when the `game_tele` row lies within 80 yd of the entrance), `scripted` bosses (NPCs with
`npc_flags` or faction 35 until a script or a dialogue makes them hostile: no `-check` warning) and `special`, what the instance needs besides killing (events, rituals, gossip, keys), which goes into the plan's notes as
`Needs: ...` and into the table. Exports are `tools/dungeongen/testdata/map<id>` (`export.sh <map> <dir>` writes the five files: `portals.tsv` from `areatrigger_template`, `movement.tsv` the `creature_movement` waypoints of the map's patrolling spawns); `uldaman` and `zulfarrak` are
hand-written and live-checked and `ragefire_chasm` is hand-written with an offline route but still a draft (not mapped), none of them regenerated. The tests regenerate every draft and compare it byte for byte with `dungeons/`, load it, run the creature check (`-check`, with `-scripted`) and compare the table; the
navmesh test for the drafts (`TestPlansOnNavmeshWhereTheMachineHasIt`) logs how many plan points are on the mesh for every map whose `<map>.mmap` and tiles are present and lists the maps that are not.

`go run ./tools/dungeongen -check dungeons/uldaman.json -dir tools/dungeongen/testdata/uldaman -team alliance -friendly-factions 122 -awakened 2748,4857 -strict` lints a plan against
the export (`creatures.tsv`) and prints a warning for each boss, `boss.first` entry or pack member that has `npc_flags` (a quest giver, vendor or trainer is no
mob) or a faction named friendly to the leader's team; what is gated to the other team is not looked at, and the flags of a part gated to a team are not repeated.
dungeongen cannot tell friend from foe by itself: that is in `FactionTemplate.dbc`, which the export does not carry, so only 35 and the factions of
`-friendly-factions` count as friendly, and a faction that creatures with `npc_flags` also use is noted as possibly friendly. Faction 122, for one, is
the Ironforge dwarves': friendly to the Alliance, and nothing says it is hostile to the Horde but the data of the users of 122 (Bael'dun soldiers are Horde kill targets).
`-awakened` names creature entries that spawn friendly (faction 35, stoned) and that the encounter's script turns hostile, so a plan that fights them is not warned about as if it
asked the party to kill a friend: Uldaman's Stone Keepers (4857, woken one by one by the Altar of the Keepers) and Archaedas (2748, woken by the Altar of Archaedas); their `npc_flags` are
still looked at. `-strict` makes any warning exit status 1, for CI. Generating a skeleton puts `WARNING:` lines in the leg notes for a boss with `npc_flags` or a faction of `-friendly-factions`.

`internal/dungeon`'s `TestUldamanOnNavmesh` plans a `path_to` route through every waypoint of `dungeons/uldaman.json` in the order
a run goes and asks for status `complete`; `TestUldamanPlanMatchesTheWorldExport` checks that each pack member, boss and door
in the plan is a spawn in the export. The navmesh test needs the map 70 files (`070.mmap` and the three `070*.mmtile`) in
`$TURTLE_AGENT_MMAPS`, `~/tb-ci/mmaps-turtle070` or `~/tortoise-wow-server/data/mmaps`, and a cgo build; it skips otherwise. The navmesh
has no doors, so `path_to` walks through a closed one: the door steps are checked in the game, not by that test.

### Plan check

`turtle-agent plan-check --mmaps DIR zulfarrak` (and the `plan_check` method) walks a plan on the navmesh before anything runs, in the order the driver goes, and prints
every failing point; the exit status is 11 when a point fails. A `from_leg` or `from_pull_group` the plan does not have is an error, and `from_pull_group` without `from_leg` is refused by the command and the method (`dungeon_run` takes it as leg 0, as documented for starting anywhere).

**Keys.** The check also reports, with the reason `key` and no navmesh involved, a `use_object` on an object whose lock is known to need an item (`objectLockItems` in `internal/dungeon/plan_keys.go`: the Troll Cages 141070-141074 need the Executioner's Key 8444, Uldaman's Keystone 124371 the Staff of Prehistoria 7733) without that `requires_item`, and a `requires_item` of a `use_object` or `use_item` that no earlier step, `boss.loot_item` or `optional` leg makes (zf-r5: the stairs leg killed the Executioner, nothing looted his key and the cage open was refused "no-item"). `TestShippedPlansProvideTheirKeys` holds every shipped plan to it.

**The gate.** `dungeon_run` in **commander** mode (`remote`) runs the same check first (from `from_leg` / `from_pull_group` on, on a scratch planner so a run going keeps its tiles,
with the look of the run's tunables file). Leader (group) mode, observe mode and pull tests are not checked: the check models the commander driver's walks. What a finding does depends on the plan's `status`:

- **live-checked**: a failing point, a plan that cannot be checked (no `mmaps_dir`, no navmesh files for the map, an error in the check) or a `from_*` the plan lacks refuses the run
  ("plan check failed" / "could not run"), unless `force` is given, which runs it and records `forced past plan check ...` in the result's `plan_check`.
- any other status (`draft`, `mapped`, none): the check runs, the findings go in `plan_check` as a warning (`warning (the plan is draft, not live-checked, so it runs): ...`), and the run goes on.

A second `dungeon_run` while one is going is refused for that, before any check.

What it checks:

- the walk from the previous leg's end to every `via` point and to `to`: mesh floor under it and a complete `path_to` (`off the mesh`, `unreachable`);
- every `gm_cross` point: mesh floor under it (`off the mesh`); no path to it is looked for, it is listed as exempt;
- every `direct` hop: within the module's limits (25 yd across, 8 up or down: `too far`), ground under its end, and a clear walkability ray from its start (`no line of sight`:
  a direct step walks the straight line);
- every pull group, numbered as `dungeon_status` shows it ("pull group 9 of 12"), and the boss: the path from where the party stands must reach it
  (`unreachable` when there is no path), and the point the lead bot looks from must be within the view (`too far`; also when no point of the path gets within the view of the pack, because the driver does not chase a partial path toward a pack it cannot see). That look point is `LookPoint`: the
  standoff (`look_standoff`, 21 yd) back from the path's end, moved up the path to the first point within `min(standoff, 25 - look_arrive - 1)` of the pack (21 yd by default;
  the lead bot may stop `look_arrive`, 3 yd, short of it, and frames from beyond 25 yd do not count; on a path shorter than the standoff the lead bot stays put when the pack is in view and otherwise moves up the path the same way; the moved point is put on the navmesh floor by the driver and the check alike). The positions of a group are looked at in order, each look starting where the one before ended, and the next group goes on from the last; a position within 25 yd of the party needs no look. No ray is cast to a pack: the navmesh ray is only a stand-in for sight and
  called most packs blind that were pulled fine live. A pull group or boss gated to a `team` is checked like the rest (the commander driver looks at it before it skips the fight); its issues name the team;
- a leg `to`, pull group or boss marked `needs_walk` (never snapped to the navmesh: its mapping walk is still to do) is a failing point (`needs a mapping walk`), not a silent skip; Zul'Farrak and Uldaman have none;
- the `stand` points of `use_object`, `loot_object`, `ritual`, `area_trigger` and `gossip` steps (before the boss, and the `after_boss` ones), walked like `via` points;
- an `area_trigger`'s `stand` inside its zone (`outside the zone`: its distance from the `center` must not exceed the `radius`; nobody who stands outside takes the trigger), and, when the leg has a boss, within 30 yd of the boss (`too far`: the trigger would start a fight the party is not at).; the `stand` of a `gossip` must also lie within 5 yd of the NPC's `near` (`too far` otherwise), and a gossip step `by` the agent does not move the party's point for the hops after it.

- the points only the TortoiseBots run leader walks, a pull group's or boss's `approach` points and a `hold`'s `spot`, walked like `via` points from the run leader's point
  before them (the last `via`, `to`, step stand or approach point; part `run leader pull group 3 approach 0`). A separate list, `run_leader` (reason `run leader hop`; never a failure, not a warning of the agent's own run), names every pack (the centre its `pull` row
  gets) and boss that stands more than 50 yd from the run leader's last move row: the run leader sees 60 yd from its lead bot and walks toward anything farther in navmesh hops of its own
  (`kApproachYd`/`kApproachHopYd`), which is where its Uldaman test of 2026-10-16 stopped (a hop point under a low ceiling refused `off-mesh`, and long paths with no path at all). Give such a
  pack `approach` points. The check does **not** model the server's own refusals: `cmd move` snaps a point to the vmap floor found from 8 yd above it (a ceiling 4-6 yd above the floor wins
  and the point is refused `off-mesh`, more than 5 yd from where the path ends), and the server's path finder keeps 2048 search nodes and 256 polygons (here 65535 and 2048) and builds
  a smoothed path of at most 256 points; neither the vmaps nor that path are here, so an approach point is checked against the vmaps offline (`lostool` on the test VM).

A run that starts inside the plan is checked from where the driver puts the party (`Plan.LegStart` for `from_leg`, `Plan.GatherSpot` for `from_pull_group`; the part before is not walked).

A hop the plan itself exempts, `"check_skip": "<reason>"` on a `via` point (Uldaman's two direct hops over the scorpid pit, which the navmesh cannot judge), is listed under `exempt` with the reason and not checked.

A **warning** (not a failure, and it never blocks a run) says a `direct` hop is not needed: the navmesh already joins its two ends with a complete, nearly straight path, so a plain
waypoint does the same and can be sent again from wherever a fight leaves the lead bot. The check is static: it cannot see where a fight drags the lead bot, or a refusal
that only the server's own pathing gives (a tank with no firing position), so a pass is no promise that a run finishes. `TestShippedPlansPassPlanCheck` runs it on every shipped plan whose map
has navmesh files here (`$TURTLE_AGENT_MMAPS`, `~/tb-ci/mmaps-turtle<map>` or `~/tortoise-wow-server/data/mmaps`); the plans that fail today are listed in `planCheckKnownFailing` (issue #187; most are drafts, which only warn);
a listed plan that passes fails the test until it is taken off. The gate itself is tested with a stub navmesh, so it runs in CI without map files.

### Route oracle

`turtle-agent route-oracle --plans dungeons --mmaps DIR --out DIR` compares a plan's **stored route** with what the navmesh **plans live** for the same dungeon, and writes `<out>/<plan>.txt` (a human table) and `<out>/<plan>.json` (the machine report, Stage 1's fixture). It reads plan files and mmaps and nothing else; a plan's status, live or draft, changes nothing (the report is a measurement, not a gate). Without `--plan` it reports the five mapped plans: `wailing_caverns ragefire_chasm uldaman zulfarrak deadmines`.

The walk it plans is the driver's own. Per leg the leg's stored points in the order planexport writes them — the `via` points, `to`, each pull group's `approach` points and its pull point (the positions' centroid, or a hold's spot), the steps' stand points, the boss's approach points and position, the after-boss stands — and every hop between two of them is planned twice: on the plain planner, and on a scratch planner with `SetAvoid` of the plan's avoid boxes. The avoid-aware paths are the live route the metrics use, because it is the one a run plans with; each stored hop is scored against its own navmesh hop (per-segment rows in the JSON, per-leg sums in the table).

A hop that does not plan complete **fails its leg**: the leg takes the failing hop's status (`partial`, `no_path`), the leg's remaining hops are planned on from the point the failed path reached and marked `non_comparable` (that walk is not the plan's, so it scores nothing), and the legs after a failed leg are `indeterminate`. Every aggregate — the mean ratio, the packs on path, the break and redundancy counts — is over the complete legs only; the failed and indeterminate legs stay in the report for reading.

Per leg the report holds: the status, end gap and live length of both runs; the stored hops' length and their ratio to the live length; the most and the mean distance of the leg's stored points to the live route; the route's Z jumps (consecutive samples of the corners and their ground sampling more than 8 yd apart in Z: ground the mesh joins without a climb); the leg's **breaks** — `via` points with `exact`, `direct`, `gm_cross` or `check_skip` — flagged when the live route passes within 6 yd of the foot, or when the failing hop is partial and its path's end stands within 10 yd of the foot; each pull group's corridor membership (one of its positions within the group's radius + 18 yd of the route, no more than 8 yd above or below it); and, for a complete leg, how many of the approach and via points are **redundant**: the navmesh path from the previous to the next stored point passes within 5 yd of the point, so the route would lose nothing without it. The plan's line sums it up, and the report names the SHA-256 of every `.mmap`/`.mmtile` file the planning used.

The report is written under the **validated CLI plan name** (never the name inside the plan JSON — a plan file that says `name: "../escape"` is refused, and the destination must resolve under `--out`). `internal/routeoracle` holds the report; its tests plan over a synthetic mesh (`internal/navmesh/navfixture`) and need no real mmaps.

**Modes.** `--mode walk` (the default) is everything above: the plan's own stored sequence, hop by hop. `--mode bosses` asks a different question, how much of a route the live navmesh finds with the plan's hand data left out: each objective is ONE avoid-aware navmesh plan from where the party stands, using no hand point and skipping no break hop (a `direct`, `gm_cross` or `check_skip` via). It writes `<plan>-bosses.txt/.json`. `--objectives` picks the objectives:

- `--objectives plan` (the default): each stored leg's own objective, its boss, else its `to`, else its last step's point. A leg without a boss is planned to a stored `to` point (a hand-chosen waypoint), so this is not a bare boss list.
- `--objectives bosses` (needs `--mode bosses`): ONLY the chain entrance -> boss 1 -> boss 2 -> ...; a leg without a boss is folded into the next boss's row (its pull groups, stands and `to` are measured against that row's live path; no stored `to` is an objective), and the legs after the last boss are not planned. It writes `<plan>-bosses-only.txt/.json`.

An objective the mesh cannot reach is a failed leg (`partial` / `no_path`): it scores nothing, names the stored break it ran into (a break hop whose foot the mesh reaches and whose landing reaches the objective, or one within 15 yd of the live end or of the line to the objective), and the objective after it is planned from the last point reached and labelled non-comparable (excluded from every aggregate); it is also re-planned from the previous objective (`bridged`), beside the primary numbers. Both modes judge a pack the same way: one of its positions within its radius plus 18 yd of the route, 8 yd of height. A pull group with neither a position nor a hold is skipped and counted (`empty_groups`).

**Output safety.** `--out` is refused when it is, or has a component that is, a symlink (a host where `/tmp` or `/home` is itself a symlink must name the real folder). A report file that already exists as anything but a regular file (a symlink, even a dangling one, a folder) is refused, and each file is written to a temp file in the folder and renamed over the target, so no existing link is followed. Documented limit: the folder is validated by path, so a concurrent rename of a writable ancestor between the check and the open could redirect the writes; this is a local, owner-run tool and the limit is accepted.

### Exporting plans for the TortoiseBots run leader

`turtle-agent plan-export` writes the plan file as rows for the TortoiseBots run leader (`.bot run`; its tables `ai_dungeon_plan`, `ai_dungeon_leg`,
`ai_dungeon_step`) as **one idempotent world migration**: per plan a `DELETE` of its rows, then the `INSERT`s. The plan file here stays the single source: edit it,
export again, never the rows by hand. The mapping is TortoiseBots' `docs/guides/dungeon-run-leader.md` ("From plan v1 to rows"). The plan row carries
the plan's `cc_priority`, `cc_always` and `kill_first` in the columns of the same names (TortoiseBots #1001, migration `20261015200000_world.sql`), each as a comma
list like `entries` (`'7246,5648'`, `''` for none, at most 255 characters; a longer list is refused). `summoned_kill_first` has no column (TortoiseBots #1001 has none; its run leader treats a ward by encounter), so 8179 is exported in `kill_first` only:

```bash
turtle-agent plan-export --out ~/TortoiseBots/data/sql/world/ uldaman              # named: 20261014200000_world.sql (now, UTC)
turtle-agent plan-export --mmaps ~/mmaps --out 20261014200000_world.sql uldaman    # cut hops over 60 yd along the navmesh path
turtle-agent plan-export --all-live --out ./migrations/ --stamp 20261014200000     # every plan whose status is live-checked
```

| Flag | |
| :--- | :--- |
| `--out FILE\|DIR` | the migration, or a folder (the file is then `<stamp>_world.sql`). An existing file is never replaced without `--force`: a migration that is applied is never edited, export a new one |
| `<plan>...` / `--all-live` | named plans of `--dir` (default `dungeons`), or every plan whose status starts with `live-checked`. A `draft` plan is refused unless `--allow-draft` |
| `--mmaps DIR` | a navmesh folder: a hop of more than 60 yd between two moves (the module logs these) is cut into `move` rows along the planned path, at most 50 yd apart. Without it the long hops are listed on stderr and left |
| `--approach-hops` | with `--mmaps`, also cut the hop to a pull, boss or hold more than 50 yd from the anchor before it (the move, or the fight the party stood at) into `move` rows along the navmesh path (comment `approach path`), the last at least 25 yd short of the pack. Without it such a hop is left to the run leader's own navmesh hops (at most 8 of 40 yd, stopping 30 yd short), which a plan of long corridors outruns. Not on by default: it would change the rows of the plans already exported |
| `--skip-legs NAME,...` | leave the named legs out of the rows (each with a warning; the later legs close up, as for an optional leg), for a leg the run leader cannot do while the legs after it can: Zul'Farrak's `bly` (a boss that turns hostile after its gossip) stands between Weegli's gossip, which opens the End Door, and `ukorz` behind it. A name that is no leg of the plan is refused |
| `--gm-cross-direct` | export a `gm_cross` waypoint as a `move_direct` (a straight walk to the landing point) instead of refusing the plan; the run leader has no GM move. Written for Wailing Caverns' ramp to Lord Serpentis (6 yd down over 3.9 yd), which the plan now crosses with a plain direct step and a `check_skip`, so no plan needs it now; the row's comment and a warning say it is not tried live |
| `--rev`, `--stamp` | the commit named in `ai_dungeon_plan.source` (default: `git rev-parse` of the plan folder, `+local` when it has changes) and the file's timestamp (default now) |

Per leg the rows are, in the order the driver does them: the `via` waypoints (`move`, or `move_direct` for `direct`; an `exact` via is a `move` with flags 64, the run leader's exact waypoint: done only once the lead bot stands within 1.5 yd of it, still for 2 s, and only then the direct step after it goes out, TortoiseBots `docs/guides/dungeon-run-leader.md` "Exact waypoints"; that direct step is written as kind `move_hop_exact`, which an older TortoiseBots does not know: its loader skips the row, so a rolled-back binary never sends the hop from short of the foot (it fails closed); the export refuses rows where anything comes between the two, an exact move before a plain `move_direct`, or a `move_hop_exact` not right after an exact move; a via's `check_skip` is the plan check's only and is not written, without a warning), `to` (`move`), each pull group (`pull`: the centre is the
mean of its positions, the radius is its `radius` or the farthest position plus 10 yd, `entries` once each in first-seen order, flags 1 ambush / 2 alliance / 4 horde from `ambush`
and `team`, 8192 with `param` = `clear_yd` from `stand_pull`), the `steps` (`use_object` as a `move` to its `stand` and an `open` whose `item` is `requires_item`; `wait_for.gameobject` as `wait_object` with `param` 0/1/2 for
ready/active/active_alt; `hostiles_gone` as `wait_clear`, centred on its `center` or on the last point of the leg; `loot_object` as `loot` with `item` the `expect_item`; `use_item` with
`item` the `expect_item` and `param` the `requires_item` (the second item the use consumes; the run leader sends the use to the bot that carries both); `ritual`; a `gossip` step with an `option` number and `by` `tank` or `carrier` as a `gossip` row (`entry` the NPC, x,y,z its `near`, `param` = `option` + 1, flags 0, `timeout_s` 0; the lead bot talks, the `stand`, `by` and `retry_s` are warnings, `fail_text` is in the comment); a `gossip` by `text` or `by` `agent`, and a `wait_for` `unit` without a `hold`, are **refused** in the first leg and **cut** the export before a later leg that has one (see **a step the run leader cannot do** below): the run leader has none; `area_trigger` as a `move` to its `stand`, because the run leader has no such step and a bot that walks into the zone takes the trigger itself when the module lists it (`BotAreaTriggers`; `by` `agent` and the `wait_for` `unit` are refused with that reason)), the `boss` (`entries` = `first`, `flags` from `team`; its `loot_item` as a `loot` of the boss entry right after it) and the `after_boss` steps. A pull group's or boss's
`approach` points are `move` rows right before its row (comment `pull group N approach`); a `hold` is a `hold` row in its place (`entry` the instance data, `param` 3 = DONE, `flags` 40 =
done by instance data 8 + encounter states 32, `radius`, `timeout_s`, `entries` the group's, or the boss entry, its `first` and the hold's `entries`); a `move` straight before it to its
own spot is left out (the hold walks there, and the fight that starts with a ritual stays the hold's), and a `wait_for.gameobject` straight after it waits at most 60 s (the event's DONE
opens the door). A `ritual` row carries its `participants` in `param`. With `--mmaps` a long hop is measured from the last pull, boss or hold row when one comes after the last move (the
party fought there), so a leg's first move after a boss is not walked back from the move before the fight. Every `run leader hop` warning of the plan check is printed too. **Avoid boxes**: the run leader does not know a plan's `avoid` boxes (the rows have no column for them), so with `--mmaps` and a plan that has boxes the export checks every hop between two anchors (the entrance, move, pull, boss and hold rows) on the plain navmesh, the one the server walks: a hop whose path enters a box gets the corners of the avoid-aware path as `move` rows (comment `path (keeps out of an avoid box)`, hops of at most 25 yd, refined until each hop's plain path is clear), and a hop that cannot be made safe fails the export. Ragefire Chasm needs none (its plain paths already keep out of the basin).
`skip`, `rest` and `watchdog` are not carried over, as the guide says. **Nothing is dropped silently**: every plan attribute the rows cannot carry is a warning on stderr, one per attribute, naming each place it stands and what the run leader does instead (`skip`, `leave`, `leftover_entries`, a leg's `rest`, `needs_walk`, `patrol`, `boss.tactics`, `boss.companions_loot`, a dropped `companions_loot: false`, `fleers`, `no_cc`, `summoned_kill_first`, `hire_composition`, `level_band`, `raid`, `watchdog`, the entrance and teleports, `avoid` without `--mmaps`, a `stand` of a `loot_object` or `ritual`, an object's `spawn` or `range`, a `loot_object` `batch`, an `area_trigger`'s expect check and timeout); the migration's header should list them. Not warned: what is no run attribute (name, version, status, kind, map, notes, a via's `check_skip`, a pull group's `equiv`, which only the pull-test scenario reads). A **leg's `companions_loot: false`** cannot be carried either (the rows have no column for it, so the run leader's bots would loot the Zul'Farrak graveyard's chests): such a plan is **refused**, naming the leg, unless `--drop-companions-loot` says to export it without. With `--carrier-chests` (which needs `--drop-companions-loot`) such a leg's `loot_object` steps are in the rows instead: one chest per triple, a `move` to the step's `stand`, a `loot` row at the chest's point (its expected item; flags 256, gone is done: a chest of `spawntimesecs` 7200 despawns once looted, so a run resumed after a wipe at one of these rows takes the gone chest as done) and — when the leg has `leftover_entries` — their pull at the chest's point, radius 15, comment `sweep` (the chest's linked trap raises its adds on the opener in the same server call the loot ACK answers, and round 3's eleven idle leftovers aggroed the walk to the next chest: a batch of 3+ chests wiped twice, so `loot_object batch` stays unexpressed and one chest a row keeps the adds two at a time); the run leader's `cmd loot` runs itself, and the four companions' looting still has to be turned off for the leg in the setup, or a module with a trapped-chest rule has to skip them — the warned `companions_loot false` text says so: the setup is looting off for every bot, party loot method free-for-all, the Executioner's Key 8444 on the lead bot, at least 16 free bag slots on the lead bot and a fresh instance (a looted grave respawns after 2 h). The loot row's flags 256 need a TortoiseBots module with the gone-is-done step flag: an older module ignores them and pauses on a despawned grave after a wipe (then a .bot run skip skips the row). Without the option the steps are left out as before. An `area_trigger` is exported as a `move`, and its comment says `needs AiPlayerbot.BotAreaTriggers = 962 (TortoiseBots #1002)`. A row the module's loader would skip (radius over 100, timeout over 600, more than 16 entries, a
step without its entry) is refused here. The written SQL is read back and compared with the rows before the file is written. Uldaman exports 148 rows (14 legs, 133 steps, two
of them holds); with its approach points and the hop measured from the last fight no move is more than 60 yd from the party, so `--mmaps` cuts nothing (the export of dd05724 cut 7 hops into 220 rows, walking the party back from the leg before's last move).

### The Ragefire Chasm plan

`dungeons/ragefire_chasm.json` (Ragefire Chasm, map 389, level band 13-18) is **mapped, not live-checked** (`"status": "mapped (not live-checked)"`): written offline from the world export in
`tools/dungeongen/testdata/map389` (`export.sh 389`; `movement.tsv` holds the patrol paths), the map 389 navmesh and the core's `creature_spells`, `creature_ai_events` and `creature_groups` tables, then corrected by a GM-invisible mapping walk on 2026-10-15 (slot 5, nothing attacked; `tests/walk-rfc/FINDINGS.txt` in the audit folder: the portal and trigger 2230 land at the entrance, every leg and pack and boss reached by `walk_path` with 0 replans, every static mob within 3 yd of the plan, the four bosses stationary where the plan has them, the patrol periods measured) (the core has no instance
script for this map, and the map has no door, lever, key, gossip, item or event: no leg has a step). Nine legs, 55 pull groups of 86 mobs, 1541 yd of corridor from the entrance through the four bosses in the order the navmesh
leads: `tunnel` and `tunnel_end` (Earthborers, one Molten Elemental), `trogg_cave` (the cave's north strip, first pass), `oggleflint` (a side cave up a ramp; the Shaman and the Trogg that stand with him join his pull through `creature_groups`; every boss's `boss.first` is empty, since a `first` entry is matched by id anywhere in view and drew a pull of a terrace Shaman after the boss group was dead), `trogg_west`
(second pass to the cave's south-west corner and up the long ramp), `molten_cave` (seven CC-immune Molten Elementals), `taragaman` (the Searing Blade wing, Taragaman over the lava), `jergosh` (corridor packs, Jergosh and his
two Cultists) and `bazzalan` (the raised ledge and the hall, the last boss; the add on his left is the last pull group, pulled from a stand 30 yd from him). The pull radii are the largest that keep every other pack's mob out of range (`tools/dungeongen` `TestNoMobStandsInAPullGroupsRangeUnlisted`; the patrolling Enforcer 48696 is listed in the first Searing Blade pack it stands in). Packs linked by `creature_groups` (flags 2, aggro together) are one pull group; the plan carries `fleers` (Ragefire Shaman 11319 and Searing Blade Warlock 11324
flee at 15 %), `cc_priority`/`cc_always` (Shaman, Warlock, Cultist) and `kill_first` [11319] (the Shaman is sheeped once and the other Shamans are killed first: rfc-live-2), six `patrol`s (spawns 48648, 48655, 48675, 48698, 48696 and 48707, with their `creature_movement` paths and the periods measured on the walk: the Molten Elemental 48698 walks at 4.0 yd/s and its round is 63.5 s, not the 98 s the walk speed would give; the Trogg 48648 walks the ledge of the cave opening and is on the cave's first pack (the last tunnel Earthborer carried it too until the 2026-10-16 route edits: the trogg was never in view there and the party waited 30 s for it); the Shaman 48669 patrols the south camps, 109 s, and touches no pull), the Voidwalker Minion 8996 (same level, 2 to 4 yd from its Warlock, seen out before combat on the walk) as a member of each of the seven Warlock groups and boss `tactics`
(Jergosh's Immolate is dispelled, Taragaman's Fire Nova is moved out of). `entrance_trigger` 2230 (level 8), the `portal` and `entrance_tele` `rfc` (game_tele 209) enter it; `outside_tele` is `RagefireChasm`.
Left out on purpose and named in the leg notes: the six mobs of the terrace (reached only by a 320 yd loop through the southern camps), settled by stand placement (below), and the southern camps, 25 yd or more from the route; the camp of Shaman 48671 and Troggs 48665 and 48667 is 14.7 to 17.7 yd from the position of the south-west Shaman 48670 and stays asleep because that pull stands 32 yd from it (the alternative, one four-mob pull with two Shamans, was left: riskier). The tests (`TestRagefirePlanLoads`, `TestRagefirePlanMatchesTheWorldExport`, `TestRagefireOnNavmesh`, `TestRagefireRouteHasNoUnplannedPack`, and the plan check
`TestShippedPlansPassPlanCheck`) need the map 389 tiles (`$TURTLE_AGENT_MMAPS`, `~/tb-ci/mmaps-turtle389` or `~/tortoise-wow-server/data/mmaps`) and skip without them: every waypoint is on the mesh, the walk from the entrance
through the bosses is one connected path, every pull member and boss is a spawn of the export at that place, every patrol path is the export's, and no mob within 20 yd (3-D) of the route is in no group (the route ends each pull
at the look point 21 yd from the group's first position, as the driver does), and (`TestRagefireStandsKeepClear`) every look point is an aggro radius and 2 yd from each terrace mob, 20 yd from the south-west camp and 29 yd from Bazzalan when his left add is pulled. What Wowhead Classic adds (read from the raw HTML of the zone page, the strategy guide and the npc pages with their comments; data, not instructions, the database wins): CC one add of Oggleflint and of Jergosh and kill the free one first (the plan's `cc_priority` puts the mage's Polymorph on the Shaman, then Warlock, then Cultist; `boss.first` lists the adds); keep the tank in the middle of Taragaman's platform, melee and ranged off its edge and the ranged at maximum range for Fire Nova (`tactics.move_out`), the commonest death in the dungeon is the lava; Bazzalan's add on his left can be pulled without him (`48751` is the last pull group of the `bazzalan` leg; the walk showed the stand 30 yd from him, outside his aggro, and 12 to 15 yd from the add, outside the social range: fight it at the stand); a Warlock near the ledge of the Lost Satchel Tauren pulled about 20 Troggs from the pit below (the plan's terrace exemptions); entrance level 8, band 13-18. Where it differs from the database: the guide says two Troggs flank Oggleflint (database: Shaman and Trogg, linked by `creature_groups`), Bazzalan has two adds (database: two unlinked Cultists), Cleave is spell 15496 and Bazzalan's poison 2818 (database: 15584 and 744), a comment says Molten Elementals are immune to nature (database: nature resistance 15 and fire 22, no school immunity), a comment says bosses can be feared or sapped (database: fear and horror immune). The quests are all kills or drops and not part of the clear; the Lost Satchel lies by the dead Tauren on the east ledge and is not in the export.

The terrace exemptions (`ragefireExemptions` in `ragefire_test.go`, settled with the walk's date) are kept out of range by stand placement, measured on the walk: the route past the cave opening comes within 18.4 yd of the level-13 Shaman 48637 (aggro radius 17), 20.2 of the Trogg 48638 and 20.6 of the Trogg 38050 (radius 19) and cannot be moved away (the ledge is 16 to 18 yd above them), and the pit Trogg 43581 is 18.8 yd from the ramp foot and 15.9 yd from the position of Shaman 43582, so the pack at the ramp foot is pulled from (-166 3 -48), 25 yd from all three pit mobs. A party spread of 3 yd would put two of the opening mobs in range, and line of sight is not known (no RPC gives it): a live aggro test with a visible character at the opening (-165.7 -33 -40.7) and the ramp top is the next check, then three live runs.

**Route edits of 2026-10-16** (from the round-3 traces; the evidence is in the plan notes of each leg): `antusul_a4` is gone, its Shadowcasters are the last group of `antusul_a3`, and a Shadowcaster at 1465 834 and the linked pack of three at 1613-1619 892-897 are groups of their own; the Shadowcaster camp at 1660 901 and the pack at 1662 930 are pulled in `antusul`, before the pair at 1642 902; `velratha` is split at the pack of five at 1636 1105 into `velratha` and `velratha_b` (north stand 1645 1144); `theka` starts by two vias at 1812 700 and 1812 716; the Witch Doctor at 1759 730 with the Shadowhunter beside it is a group of `antusul_b`. Not done: the Shadowhunter / Sandcrawler pair at 1565 1019 (its look point passes within 20 yd of a static pack off the mesh, see the `velratha` notes), the roaming single mobs without a spawn row near 1675 848, 1712 812 and 1710 1157, and the packs seen at 1811-1816 940-947 and 1820 910 in the graveyard leg.

### Zul'Farrak events: Gahz'rilla, the courtyard and Zum'rah

**The pool stairs (`velratha` pull groups 5 and 6, 2026-10-20).** In the full observe run (`zf-full-19z16`, RUN-LESSONS) the run leader stopped at the pack of four at 1601-1610 1104-1111 and then stood at 1629.2 1099.0 z 9.7, a navmesh **island** at the foot of the stairs: a path from 1595.5 1092.4 to it is `partial` and ends 8.8 yd short at 1621.3 1102.9. Three followers and the healer reached the upper level instead (1629-1636 1070-1099, z 31-46.8, 46 yd from the lead, "reach spell" at a mob 37 yd below) and the gather took 6 min to summon them back. The two groups now carry `approach` rows — 1594 1011, 1598 1028 and 1606 1040 for the five-mob pack (the last point moved from 1582 1070 on 2026-10-21 because that spot was 10.8 yd from the Witch Doctor and inside the patrol's reach, and from 1584 1050 / 1603 1043 the same day because those stood 7.4 and 12.9 yd from the lone Shadowhunter 81610, now on the leg's `leave` list), 1588 1080 and 1584 1088 for the four-mob pack (moved from 1593.6 1089.9, 16.6 yd from a Shadowhunter); the distances are in the plan note — so the run leader's party walks to a stand on the lower floor west of the stair mouth (path corners of the 209 mmaps; not live-run yet). The refused pullback of the same run (144 s) is the run leader's own retry, not the plan.

**The pyramid phase in the plan (2026-10-21, feat/zf-full-module-plan).** The stairs event's phase is instance data **1** (`EVENT_PYRAMID`, 0-8 in `zulfarrak.h`: 3 = `PYRAMID_WAVE_1`, 7 = `PYRAMID_WAVE_3`). The agent's own driver still cannot read it: its `wait_for` holds on units, objects and hostiles, so `stairs_top` waits for the proxy (Nekrum 7796 seen at the foot). For the TortoiseBots run leader the waits carry a `hold` (`wait_for.hold`, the wait's run leader form: plan-export writes the hold row in place of the wait) with a `value` (`hold.value`: the instance data value that ends the event, written as a script value, flags 8 without the encounter flag 32): the cage leg's Weegli wait holds the stairs top to 3, `stairs_top`'s Nekrum wait to 7. The run leader compares for equality, so a value must last (3 and 7 last a whole wave; 2 lasts one instance update). A value is refused on a pull group's or a boss's hold and when it is 0.

**Plan format compatibility (2026-10-21).** `wait_for.hold` and `hold.value` are new fields of plan format version 1; the version number is unchanged (every plan file would have to change with it). The plan reader refuses unknown fields (`DisallowUnknownFields`), so a turtle-agent binary built before feat/zf-full-module-plan (f11df6d) refuses `dungeons/zulfarrak.json` from that branch on (`unknown field "hold"` / `"value"`): run Zul'Farrak with a binary built from this branch or later. Other plan files do not use the fields and load in either build.

**Gahz'rilla (PR A, stacked on PR B).** The optional `gahzrilla` leg sits between `velratha_b` and `courtyard`: it walks to the gong stand (1652.84 1168.99 10.67, 3.5 yd from the gong), pulls the eight packs on and near the walk Gahz'rilla takes out of his pool, then the `use_object` step with `via_item` rings the
Gong of Zul'Farrak (141832, spawn row 27140 at 1650.91 1171.88 10.9, lock 459): a bot that carries the Mallet of Zul'Farrak (9240) gets `cmd use 9240 <bot> entry:141832@1650.9,1171.9,10.9` (the Mallet is used ON the gong: its on-use spell 11434 is an open-lock effect aimed at a game object; round 3 of the live test sent `cmd open` of the gong, which did nothing and answered nothing); a `wait_for` waits up to 30 s for a living Gahz'rilla (7273) in the bots' view, and the boss fight kills him at 1702.58 1224.19 8.88. Without a Mallet in the lead bot's or the carrier's bags the leg is skipped (see `optional` above); a test gives it with `give_items`.
What the world database says (tortoise-wow-cbspin 4f133ab): the gong is a goober with lock 459 and event 2488, the client shows it usable, so the Mallet is checked on the server when it is used. Event 2488 respawns the other gong object (guid 209001, entry 20901, a Turtle decoration copy with no lock and no event; the plan never targets it) and 2 s later summons Gahz'rilla (7273, level 46, faction 107, no spawn row)
at 1664.65 1187.67 -2.89 for 3600 s: the pit under the pool, 22 yd from the stand. His AI casts spell 33007, which fires event 2489: six moves one second apart, 1672.66 1195.93, 1681.74 1204.13, 1686.09 1207.3, 1690.54 1211.53, 1692.57 1213.66 and, 6 s after the summon, 1702.58 1224.19 8.88, which is where he stands (event 2489 has no summon: the "six trolls" of the first draft were those six rows).
**The Mallet's source (tw_world, 2026-10-20, SELECT only).** `Lock.dbc` row 459 is type 1 (item required) with 9240, so the gong needs item 9240 and no skill or key. Item 9240 is not a quest reward, vendor item, chest or loot row anywhere: it is created by using item **9241 Sacred Mallet** (bind on pickup), whose on-use spell 11435 "Create Mallet of Zul'Farrak" (Effect 24) puts 9240 in the bags. 9241's only source is `creature_loot_template` entry 7996 at 100 %: **Qiaga the Keeper**, level 50 elite, faction 37, one spawn on map 0 at -284.792 -3447.45 188.426 (The Hinterlands); 9239 (the mace of the same name) has no source either. A ZF plan starts inside map 209 with hired level 46 bots and has no step kind for leaving the instance, travelling to map 0, killing a level-50 elite and returning, so **no bot run can obtain the Mallet**: the leg is skipped honestly, and only a test's `give_items` (`additem 9240`) reaches it.

The packs: the Velratha leg clears only the strip (and the pack of five at 1712-1720 1181-1190); eight groups remain within about 45 yd of his walk, each member checked against the export: [5649 5649 5650] at 1621-1625 1192-1197, [5650] 1643.6 1200.1, [7246 7246 8095] 1649-1654 1210-1215 (radius 30: the Sandcrawler patrols), [5649] 1652.1 1219.9, [5649] 1667 1233.6,
[5649] 1699.8 1233.8, [5650 7246] at 1700.3 1248.3 and 1713.2 1247.8 and [5649] 1714.4 1218.6 (the one the tank found no firing position for twice in the Velratha leg's batch-2 runs). The four casters at 1662-1667 1254-1260 (47 yd from the walk) are left out; the design list had them as two Shadowhunters and a Witch Doctor, the export has three Shadowhunters (rows 81594, 81592, 81595) and the Witch Doctor 81590.
The gong stand, every pack position and the boss position are on the navmesh and reachable (`TestZulfarrakOnNavmesh`, `TestZulfarrakPlanCheck`, `TestZulfarrakRouteHasNoUnplannedPack`). **Not yet run live with the Mallet.**

PR B of the Zul'Farrak events design ([TortoiseBots #984](https://github.com/Sagiroth/TortoiseBots/issues/984)): the two legs after Velratha, so that a commander run kills Witch Doctor Zum'rah (7271) too. `courtyard` is seven pull groups east
of the gong strip (the pyramid courtyard, `to` 1822 1177): the Soul Eater patrol and the Blood Drinker at 1821-1849, a Witch Doctor at 1863 1200, a Shadowhunter at
1878.7 1159.7, the four at 1900-1912 1134-1166, the Shadowhunter / Sandcrawler / Witch Doctor group at 1865-1879 1110-1120 (two patrollers, radius 30), the three at 1913-1920 1194-1199 and the three at 1885-1899 1211-1222. Every member is an
export spawn to the decimal (`TestZulfarrakPlansMatchTheWorldExport`), and the patrolling members' groups have the road packs' 30 yd radius (no patrol path: the export has none for these three). Two groups of the first cut are dropped (route edits Z6 and Z10, 2026-10-16, from the round-3 traces): the Witch Doctor at 1926 1131 was 52 yd off the route and a ~90 yd round trip of 23-25 s each run, and the lone Soul Eater at 1757 1141 aggroes on the walk and the look for it cost 26 s. `zumrah` goes by 1859 1058 to 1885 1012, 27 yd west of him, with `companions_loot: false`,
the `area_trigger` step above (trigger 962, centre 1909.27 1015.11 11.5, radius 10, `stand` 1903 1014 11.8: 6.4 yd from the centre, 9 yd from him and clear of the Campfire that a stand on the centre would be in, `by` `auto`, `expect` `unit 7271 hostile`) and the boss
with the ward 7785 in the **plan's** `kill_first` and `summoned_kill_first` (the design of #190: the ward is taken as part of a fight only with encounter evidence), and in the boss's `tactics.kill_first` for a leader-mode brain. Live 2026-10-15 (the audit folder's `tests/zf-probe-events` and `zf-probe-zumrah`): trigger 962 sent from 1909.3 1015.1 turns him from faction 35 to 37 and he yells; he keeps `UNIT_FLAG_IMMUNE_TO_PLAYER` (0x100) until combat, but aggroes a bot at 27 yd within about 2 s on his own and
the flag clears in combat; in combat he summons Zombies (7286), Dead Heroes (7276) and a Ward (7785). The route from the courtyard to him walks through the graveyard of Shallow Graves (the leg's `companions_loot` is why the bots do not loot the chests). The straight line from 1859 1058 to 1885 1012 passes 0.3 and 0.8 yd
from two grave traps, so the leg goes by the detour 1859 1058, 1854 1055, 1860 1037, 1885 1012 (owner decision 2026-10-15; 8 yd more, at least 3.5 yd clear of every grave chest and trap). Not run live yet with the bots: `by` `auto` needs TortoiseBots #1002 (`AiPlayerbot.BotAreaTriggers = 962`) on the server, else the agent takes the trigger after 5 s.

**Graves in batches** (`loot_object.batch`, remote runs): consecutive `loot_object` steps with a `batch` id are opened in batches. The loader refuses a batch of fewer than 3 or more than 6 chests, a chest more than 15 yd from the batch's centre (a size of the run's own, `graves_batch` or `graves_ramp`, holds 8 yd) (the mean of the `near` points), and an id that comes back after other steps. Per batch the party is walked to the centre, gathered and held there (`cmd stay`; only the carrier walks, with its own open order), the area spells are turned **off** (`.bot behavior <bot> aoe off`; the module has them on by default and persists the switch), the carrier opens every chest back to back (what comes out is not fought by the driver meanwhile: the bots defend themselves), then `cmd follow`, the driver waits for the tank's threat lead (the tank at the top of the threat of 4 in 5 of the engaged hostiles, at most `ThreatWait`, 6 s), turns the area spells **on** and fights everything out; the rest gate runs after the fight. The area spells are turned on again for every bot that a batch left off by a defer of the run **before the unbind** (a failure, a cancel, a panic or an unrecovered wipe included; a bot that refuses twice makes the run degraded). `dungeon_run` can set the size itself: `graves_batch` (fixed, 1-20) or `graves_ramp` (`{"start":2,"step":1,"max":0}`: one more chest after each batch without a wipe, no limit but the chests that lie together). At run time a batch of the plan keeps the loader's 15 yd and a batch of the run's own size is cut where the next chests are not within 8 yd of its centre and the note says `cut_by_radius=yes`. **The last recovery slot of the leg is the boss's**: once the leg has used all but one of its `MaxWipeRecoveries` (default 2; a wipe earlier in the leg counts), the graves go one chest at a time, and a wipe in a grave batch is not recovered (the run fails with "wipe in the graves: not recovered", the party is dead and cannot go on; start the run again at the leg, the opened graves are skipped). With a larger cap the ramp stops growing at the first wipe and runs at the last clean size. **Wipe counters.** `wipes` (in `dungeon_status`'s report, the dashboard and the end record) counts every wipe of the run: whole-party wipes, stops at three dead bots and wipes after a boss's kill; `party_downs` and `remote.wipes_after_kill` say how many of them were those, and `wipe_recoveries` how many were recovered from (zf-r4-s4: a recovered party down read `wipes: 0`). **A pack out of view** (`confirmGone`): when the approach to a pack's look point leaves the lead bot out of view (a path that ends short of a pack off the mesh), it is walked straight toward the pack, up to 3 times, before the run gives up with "could not get a view of the pack". **Mana gate:** a healer that ends a grave batch under 20% mana gets a rest gate before the next batch, with the healer-only cap of the leg given back; and every batch waits (`cmd rest`, at most 300 s, fights fought out first) while the healer is under `graves_healer_mana_pct` (60). **Leftover summons** (`leftover_entries` on the leg, remote runs): the summons a grave brings that do not fight the party (Zul'Farrak's Zombies 7286 and Dead Heroes 7276; round 3, slot 4: eleven stood idle after step 16 and all aggroed at once when the carrier went to the next grave, 5 alive to 2 in 20 s) are swept: before a batch (around its centre), after a batch's fight-out (before the rest gate) and before an `area_trigger` step (around its stand point) the driver pulls the nearest idle hostile of those entries within 35 yd (`pullAhead`: the party gathers and fights it with what stands around it), again until none is left, at most 8 rounds; what cannot be pulled is noted (`graves-batch: leftover sweep ...`; `remote.leftover_pulls`). No `leftover_entries`: no sweep. **Who opens the graves** (`graves_open_by`, `dungeon_run`, commander mode): `carrier` (the default) is the carrier bot's `cmd open`, which the module refuses while the carrier is in combat ("The bot is in combat and stopped"; zf-r4-s5: about 20 s of retries per chest). `agent` has the agent's own character open the graves of a batch back to back: `.appear` to the lead bot for the first chest of a batch, then `.go xyz` to each chest's stand point (a GM assist, one note and one `setup_shortcuts` line per open, `run_gm_assists`) and its own Opening cast at the exact spawn (`remote.agent_opens`), no order to a carrier; after the last chest `.go xyz` back home, then the party fights as before. Home is where the agent stood before the batch when that is on the plan's map, else the party's safe spot (where the lead bot stood when the batch began, not the batch's centre). Every other way out (a failed `.go xyz` or Opening cast, a wipe, a cancel, the end of the run) brings it home too, with a 30 s context a cancel does not end; a way home that fails makes the run degraded. A cast that takes no item and no money is not counted as opened. It is refused before anything is sent when the game gives the agent no hands, the account is no GM, or the setup does not list `gm visible off` (the summons would take a visible GM for a target). Each batch note carries `open_by=`. Each batch is one note `graves-batch: n= leg= mode= wanted= size= opened= skipped= adds= fight_s= deaths= wipe= tank_min_pct= healer_min_pct= healer_mana_end_pct= aoe= threat_wait_s= tank_held= cut_by_radius= radius_yd=` and a record in `remote.grave_batches`.

**The graves (owner order 2026-10-15).** The Zum'rah leg opens the graveyard before it wakes him. Core research (`src/scripts/dungeons/zulfarrak/zulfarrak.cpp`, `sql/base`): the graveyard holds 40 Shallow Graves, all chests (type 3). 31 are entry 128403, each with a linked trap 128972 (radius 0: only the chest's use fires it; its spell 10247 *Summon Zul'Farrak Zombies* summons one Zombie 7286 and one Dead Hero 7276 at the grave); 9 are entry 128308 (script `go_shallow_grave`: the first use summons a Zombie with 65 %, a Dead Hero with 10 %, nothing with 25 %). Zum'rah's in-fight *Awaken Zul'Farrak Zombie* (spell 10731, every 18 s) activates a grave of entry 128403 within 100 yd of him that is spawned and still unopened (conditions 122 and 123), so every grave opened beforehand is one summon fewer in the boss fight. All 31 graves of entry 128403 are `loot_object` steps (the 28 more than 30 yd from him in batches g1 to g7, the three 22-28 yd from him, which are safe to open because he is friendly until the trigger, as batch g8 after them; owner: at most 1 or 2 graves may be left for him, none are) of the `zumrah` leg (range 2 yd on that exact spawn, a navmesh stand within 4 yd of it, in walking order from 1859 1058), before the trigger step: the carrier bot opens one (`spawn` names the exact grave: the open goes by its GUID, so a neighbour is never opened in its place), the party fights out what comes out (engaged hostiles fought until quiet, the loot wait, the rest gate only when a rest line is crossed) before the next grave, and a resumed run asks `cmd state <guid>` and skips graves that are opened or gone. The nine 128308 graves are not opened: spell 10731's `spell_script_target` names entry 128403 only, and his AI script 727103 casts nothing else. `TestZulfarrakGraveSteps` fails when more than two graves he can awaken (entry 128403 within 100 yd of him) are left unopened before the trigger. `TestZulfarrakGraveSteps` checks every step against the export; the plan check wants the stand of a `loot_object` within the use range (4.5 yd) of its chest. `plan-export` leaves such steps out of a leg whose `companions_loot` is false (with a warning, and only with `--drop-companions-loot`). Zombies and Dead Heroes that stand in the graveyard are not spawn rows (the export has none): they are what Zum'rah's spell and the graves summoned earlier, and stay. Not run live yet: that the Opening spell on a grave fires its trap, and how many mobs one opening brings.

### Zul'Farrak: the stairs event, Bly and Ukorz

The six legs after `zumrah` were written from the instance script (`src/scripts/dungeons/zulfarrak/instance_zulfarrak.cpp` and `zulfarrak.cpp` of the core), the world export and, for tactics, the Wowhead Classic guide and NPC pages (data; the script wins where they differ, and the differences are in the legs' notes), and have not run in the game. A step that fails ends the run (there is no per-leg `optional`), with the earlier legs' results kept; `from_leg: "stairs"` starts at them.

- **`stairs`** (bots' looting off, the way out of the graveyard): the walk back by the courtyard waypoint, the Sandfury Executioner (7274) that stands by the cages, then `use_object` on the Troll Cage 141073 from 3.6 yd (the script's `go_troll_cage` frees the five crew members whichever cage is used) and a unit wait for Weegli (7607) at the top of the stairs, where he starts the event by arriving.
- **`stairs_top`**: the party holds 1887 1274 42, the spot the climbing trolls are sent to, and a `wait_for` `unit` 7796 (`seen`, 600 s) ends when Nekrum is in view. A wait does not fight: the bots defend themselves, and no formation (tank on the stairs, healer behind) can be asked of them. Waves 1 (23 trolls) and 2 (24) come up in groups of 2, 3, 4, ... every 10 s; wave 3 spawns the instant wave 2 is dead. The Wowhead Classic comments say the same: stay on top of the temple while the waves climb.
- **`stairs_base`**: wave 3 (Nekrum Gutchewer 7796, Shadowpriest Sezz'ziz 7275 and five trolls, none with a spawn row) is not sent up: Bly's crew goes down to the foot of the stairs and the party goes down too. `boss` Sezz'ziz with Nekrum first, then an `after_boss` `hostiles_gone` over the foot of the stairs, which is what ends the event (`PYRAMID_KILLED_ALL_TROLLS`). The order is the Wowhead guide's (burn Nekrum down first, he has the fewer abilities and the lower level; a melee interrupts Sezz'ziz's Heal; she also has Renew, Psychic Scream and Shadow Bolt); the script gives none, and the bots cannot be told to interrupt. If her Heal keeps him up, swap the two (boss 7796, first 7275).
- **`weegli_door`**: a unit wait for Weegli at his place (1877.5 1199.6; for the run leader it carries a `hold` on instance data 1 = 8, `PYRAMID_KILLED_ALL_TROLLS`), the `gossip` by the tank (`option` 0: "Will you blow up that door now?", the only line of his menu, offered only after the event; plan-export writes it as the gossip row with param 1), then a `wait_for` the End Door (146084) `active`. He walks to 1858.6 1146.4, plants a barrel, runs back to 1863.8 1177, the door is used and he despawns at 1827 1184. The plan talks to Weegli before Bly, as the Wowhead comments advise ("talk to the goblin first"): he is gone and the door open before the crew turns hostile, so the fight cannot kill him (a dead or fighting Weegli opens nothing, and the event cannot be restarted). The script has no preference (both orders run the same `DestroyDoor`); the wait for him to be gone, the first step of the `bly` leg, keeps Bly's later `DoAction` from planting a second barrel. If Weegli died in the waves nothing opens the door and the first wait fails.
- **`bly`**: a unit wait for Weegli `gone`, a unit wait for Bly at his place (1883.8 1200.8), the `gossip` by the tank (`text` "settled": his only option is "That's it! I'm tired of helping you out. It's time we settled things on the battlefield!"; a menu that lacks it is asked again for 30 s), a unit wait for him hostile (the script turns him, Raven, Oro and Murta hostile 10 s after the option), then `boss` Bly with Murta, Oro and Raven first. Wowhead: Oro's area damage and Murta's healing make the fight hard, so they die before Bly; Murta is a priest (Holy Smite, Renew, Heal, Power Word: Shield), Oro a warlock (Shadow Bolt, Immolate, Rain of Fire, Curse of Weakness), Raven a rogue (Poison, Backstab, Gouge); Bly has 18k health, Revenge and Shield Bash and no loot but the quest's Divino-matic Rod. Differences: a comment says the crew uses Hearthstones and leaves if the party rests, and that talking to Weegli first then waiting leaves Bly to fight alone; the script has nothing of the kind.
- **`ukorz`**: from the doorstep up the spiral ramp (193 yd, the plan check walks through the door because the navmesh has none), the Shadowhunters, Guardians and Abominations of the ramp as pull groups, then `boss` Chief Ukorz Sandscalp (7267) with Ruuzlu (7797) first. Wowhead: Ukorz has Cleave, Wide Slash, Enrage and Berserker Stance, Ruuzlu Cleave and Execute; face them away from the group, melee behind them, kill Ruuzlu first, both are immune to CC and the tank holds both; a comment says Ukorz runs back to his spawn and heals at about 6 %. The bots cannot be told to stand behind them. The comments say the serpents at the top need not be killed; the plan still pulls the two Abomination groups (8120) because the ramp passes within their aggro range.

The event is not restartable in an instance (the script keeps its phase), and the door stays open for the later entries of the same instance (`EVENT_END_DOOR` is saved).

**A step the run leader cannot do** (a `gossip` by `text` or `by` `agent`, a `wait_for` `unit` without a `hold`, an `area_trigger` `by` `agent`): `plan-export` exports the plan up to, not including, the first leg that has one, and says so: a note on stderr (`plan zulfarrak: exported legs 0-14 (weegli_door) only; cut before leg 15 (bly), which has a step the run leader cannot do: steps[0]: wait_for.unit has no run leader step (unit 7607 gone); that leg and the 1 after it are not in the rows`), a `CUT:` line on stdout, and the end of the plan row's comment in the SQL. `--all-live` goes on with the other plans (exit 0). A plan whose first leg has such a step is refused, as before. The legs of `bly` and `ukorz` are therefore not in the rows the run leader reads; they run only from the agent's `dungeon_run`, unless the export is made with `--skip-legs bly` (Bly's crew turns hostile after his gossip, which the run leader cannot wait for): then `weegli_door` (the hold on phase 8, Weegli's gossip, the End Door wait) and `ukorz` follow `stairs_base`.

### The Uldaman plan

`dungeons/uldaman.json` runs the whole dungeon, 14 legs, from the main entrance to the last boss. Legs 0-3 (the west wing) were run end to end by a remote run
(run 5, 22.4 min); legs 4-13 were written from the world database (map 70 spawns, templates, EventAI, the C++ scripts of the core) and the map 70 navmesh, and compared with a live
map walk of the whole route (a GM walker, nothing fought or used; `tests/uldaman-map/MAP.md` of the audit repo: same spawns at the same places, no SQL difference in the visited
area). They have not been run as a party. Walk lengths on the navmesh: legs 0-3 819 yd, legs 4-13 2714 yd, 3534 yd in all (about 9 min of walking at 6.6 yd/s). The plan holds 63 pull
groups, 8 bosses and the four Stone Keepers; from the pace of run 5 (22.4 min for 18 groups and 3 bosses) legs 4-13 are a rough 50 to 80 min.

| Leg | Name | Boss (entry) | What the leg does |
| --- | --- | --- | --- |
| 0 | `baelog` | Baelog (6906), with Eric (6907) and Olaf (6908) first | the west rooms, Baelog's Chest (Gni'kiv Medallion); the Horde fights Baelog |
| 1 | `revelosh` | Revelosh (6910) | the Shaft of Tsol, combined with the Medallion into the Staff of Prehistoria |
| 2 | `keystone` | none | the Keystone (needs the Staff), the Seal of Khaz'Mul |
| 3 | `ironaya` | Ironaya (7228) | |
| 4 | `scorpid_cave` | none | back out of Ironaya's room (a dead end) and east to the lip of the Cleft Scorpid pit: a bat and the nest of ten |
| 5 | `sentinel` | Obsidian Sentinel (7023), two Relic Hunters (4847) first | over the one hole of the navmesh (16 yd hop), Venomlash, a second scorpid nest, the Basilisk, the Relic Hunter camp |
| 6 | `ancient_stone_keeper` | Ancient Stone Keeper (7206) | the central hall's Earthen guards |
| 7 | `galgann` | Galgann Firehammer (7291), Geologists (7030 x2) first | the scorpid nest, Shadowforge groups, the Galgann room |
| 8 | `grimlok` | Grimlok (4854), his Brawler, Geomancer and Basilisk first | the Stonevault corridor |
| 9 | `keepers_door` | none | the Stonevault rooms down to the Hall of the Keepers, opens its door (124368) |
| 10 | `keepers_altar` | none | the three Earthen squads of the hall, then the Altar of the Keepers (130511) and a wait for it to be active |
| 11 | `keepers` | none | the four Stone Keepers (4857), woken one after the other; waits for the Temple Door 124367 (600 s) |
| 12 | `descent` | none | the ramp down to the Vault level, the squads on it |
| 13 | `archaedas` | Archaedas (2748) | the door 141869, the Altar of Archaedas (133234) and a wait for it, the fight, the Vault door 124369 |

The legs follow the navmesh route, not the order of the TortoiseBots guide (which lists the Altar of the Keepers before the Ancient Stone Keeper): the Hall of the Keepers lies past
Grimlok and the Ancient Stone Keeper is a dead-end side room of the central hall. Ironaya's room is a dead end as well; the way on is back south and east. The notes of each leg in
the file hold the mechanics read from the data (EventAI rows, the Keepers' and Archaedas' scripts, the ritual of the altars, the doors). What a run still needs from the driver:

- **Both altars are summoning rituals of three participants** (`gameobject_template` type 18, `data0` 3). Legs 10 and 13 use the plan's `ritual` step: in a remote
  run the carrier and two more bots walk to the altar, use it in turn and stand still until it is active and its 5 s cast has run (`cmd ritual`, TortoiseBots); the final answer is the step's
  result, and nothing waits for the altar to read `active` (it is `ready` again once the bots move). Leg 11 waits for the door the Keepers' death opens, leg 13 for Archaedas to show in
  the feed. The core remembers the first player ever to use an altar: if anyone outside the party clicked it before, the order ends `failed` ("did not make it a participant"). **Not yet
  seen live**: whether the module's order wakes the Stone Keepers and Archaedas the way a player party does. **Group mode** (the agent plus its own companions) cannot take the step yet:
  a group run of the Uldaman plan stops at the first altar with "remote mode only".
- **The navmesh has one hole**, at the Cleft Scorpid pit: the west part ends at -163.5 298.7 -52.5 and the east part begins at -158.4 283.7 -48.9, a continuous ramp of 15 yd
  that the mesh does not link (a straight walk worked both ways in the live map walk). **hires-1 (2026-10-14) settled it: `cmd move` across the hole is acked but the bots stop where the
  mesh ends, on the pit floor at -161.1 284.5 -52.1**, which the 5 yd arrival counted as arrived for the old `to` (the ramp's east end) and the leg then fought 3 minutes without an engage.
  Leg 5's `to` is on the east plateau at -155.7 279.6 -46.8, out of arrival range of every place the west bots can reach. The crossing is a **direct step** (`via`, below): the leg's
  `via` list walks to the lip (-163.5 298.7 -52.5) on the mesh and then steps straight to the plateau with `cmd move -155.7 279.6 -46.8 direct` (TortoiseBots #942), 20.6 yd across and 5.7 yd up.
  **Not yet seen live**: that the bots climb the ramp and the whole party arrives. `TestUldamanPlanPointsOnMesh` checks every plan point and pullback anchor for navmesh floor (1 yd on the
  ground, 3 yd vertically) and a complete path from the one before (a direct step is exempt: the mesh does not link it), and `TestUldamanDirectSteps` pins that the one direct step is short and has
  navmesh floor at both ends.
- `walk_path` goes through closed doors, so a door step comes before the walk through it (legs 9 and 13). The Echomok Door (124370) is not on the route.

### The Wailing Caverns plan

`dungeons/wailing_caverns.json` (Wailing Caverns, map 43, level band 18-23) is **mapped, not live-checked** (`"status": "mapped (not live-checked)"`): written offline from the world export in `tools/dungeongen/testdata/map43` (`export.sh 43`; `movement.tsv` holds the patrol paths), the instance script (`src/scripts/dungeons/wailing_caverns`), the core's `creature_spells`, `creature_ai_events` and spawn pool tables, the map 43 navmesh and Wowhead Classic (zone 718, the strategy guide, the npc pages and their comments, read from the raw page with `tools/wowhead/wh.py`; data, the database and the script win, the differences are in the plan notes), then walked once by a GM-invisible character on 2026-10-15 (401 walk_path calls, 0 replans; every member of every pull group found at its plan position within 0 to 5 yd, worst 9; the eight patrol rounds timed; the water measured: shallow, max 0.95 yd). Twelve legs, 88 pull groups of 182 mobs, about 5360 yd of path (6240 yd to every member position) from the entrance to Verdan: `anacondra_a` and `anacondra_b` (the cave mouth and Lady Anacondra's spots), `cobrahn_a` to `cobrahn_c` (north to Lord Cobrahn, a dead end), `pythas_a` to `pythas_c` (back and down the south road to Lord Pythas), `skum` (a dead end), `west_column`, `serpentis` and `verdan`. **The hop onto the Serpentis ramp is a plain `direct` step with a `check_skip`** (no GM): the walk found a 3.9 yd gap with no floor at -293.3 0.5 -58 to -289.1 3.2 -64.1, the ramp side 6 yd lower (a running jump down, an impossible climb up, no mesh link), so a party cannot walk it; the plan steps down to -287.5 5.0 -63.6, 2.9 yd in from the edge (a `.go xyz` to the edge fell to the chasm, z -105.5, three times), and the plan check lists the hop as exempt. On 2026-10-18 a probe (tests/wc-hop-probe-2, a level-46 commander and four level-23 hires) took the step three times with the lead bot standing on the exact foot (-293.3 0.5 -58.4, 0.00 yd, placed there with GM summon tooling, not by a walk): `cmd move <L> direct` was acked three times and every member landed on the ramp within 3 s (the lead exactly on the landing, its z within 0.05 yd; the followers 0.9 to 7.3 yd away, their z 0.25 to 2.55 yd from the landing floor, the 2.55 one first sample of Jina at 14:38:27 and 0.25 at the next; no deaths, nobody below z -70, no GM move for the crossing). The foot is an `exact` waypoint (the driver change of PR 235): a run holds the lead bot within 1.5 yd of it with a fresh 2 s stationary proof, backing it up along the incoming path when the last stretch is too short for the module to carry a move out, and fails the step without sending a direct order when it cannot prove the stand. Still unproven: no live run has used the driver on this hop (the follower regather after a direct step, 5 yd / 1.5 yd up or down, is untested there); the TortoiseBots run leader side (the exported `move_direct` row, its own arrival tolerance and the SQL mirror re-export at the new landing) is Sagiroth's step; the probe is one group; and its instance question is open (the commander's `.gps` read 177 while the feed listed the party in 176). Lady Anacondra has four spawn rows in one pool (one is up per reset), so she is a member of the pack at each of the four spots (the walk found her at spot 2, in the pack the plan has there), and her kill order is in `kill_first`; the adds that stand with Cobrahn and Pythas are the boss's `first`. Kresh (neutral), the critters and the rare Deviate Faerie Dragon are in every leg's `skip`; the eight patrols the route meets are `patrol`s of the group they pass with their measured `period_s` (a patrol whose period is only an estimate carries `period_estimate`, see above); `fleers` are the Druids of the Fang and Skum, `cc_priority` the Druids (Sleep, Healing Touch) and `kill_first` Lady Anacondra (a kill_first entry is never crowd-controlled), the boss tactics carry the interrupts (Sleep 700, Druid's Slumber 8040, Lightning Bolt 9532, Healing Touch, Chained Bolt, Naralex's Nightmare). Each pull group's radius is cut to stay short of the nearest mob that is not a member. The Disciple of Naralex event (gossip once the four Fanglords are dead, escort, the waves, Mutanus) is in `dungeons/wailing_caverns-extra.json`, a record that is not for running: it needs the gossip step (pull request 194), a way to follow an escort NPC, a wait for a time or a summon, a boss with several spawn spots (its first leg starts with a `gm_cross` to the Disciple, the way back from behind Verdan). The tests (`TestWailingCavernsPlanMatchesTheExport`, `TestWailingCavernsNoUnclaimedMobNearAWalkPoint`, `TestWailingCavernsExtraPlanLoads`, `TestWailingCavernsOnNavmesh`, the general `TestNoMobStandsInAPullGroupsRangeUnlisted` in `tools/dungeongen`, which fails for a hand-written plan with a mob of another pack, a boss or a forgotten spawn within a group's radius + 3 yd that the group does not list, and the generic plan tests) need the map 43 tiles (`$TURTLE_AGENT_MMAPS`, `~/tb-ci/mmaps-turtle043` or `~/tortoise-wow-server/data/mmaps`) for the navmesh ones and skip those without them. Next: the first run with a party.

### The Deadmines plan

`dungeons/deadmines.json` (map 36) is the plan of all seven bosses, written offline (owner order 2026-10-15, step 1 of `ALL-DUNGEONS-PLAN.md`). Its status stays **`draft (not mapped, not live-checked)`** (`dungeon_run` refuses it without `allow_draft`; the plan check only warns). The GM mapping walk of 2026-10-15 (`tests/walk-dm/FINDINGS.txt` of the audit folder: all 16 legs walked, 206 paths complete, 5112 yd, 0 stuck, 249 of 252 spawns seen at the export's places with no entry mismatch, Smite, VanCleef and the Blackguards to 0.1 yd, the gunpowder radius 45 holds exactly the 15 spawns of `cannon_a` and `cannon`) corrected the patrols, the Companions and the doors, but it cannot settle the three catwalk Overseers (a plan point is inside their aggro radius: `deadminesExemptions` stays open, so `TestDeadminesStatusNeedsSettledExemptions` keeps the status a draft; stand placement was tried on 2026-10-16: the look points of the hall packs are 13.9 to 20.8 yd from the Overseers in 3-D where 20 yd are wanted (aggro radius 18 for a level-18 mob and a level-20 party, and 2 yd), a clear look point exists within the 21 yd view of the packs harvester:1 to :6 only by steering each approach, which the plan format cannot place (the look point is the standoff of the path), and none exists for harvester:7 (-63.8 -559.3, 4.7 yd from 2583061) or the Harvester (-66.9 -570.0, 15.3 yd from 2583061 and 15 yd from the end of the line of 2583051), so all three stay open: live aggro test pending) and it cannot prove the doors that scripted deaths open (the navmesh and the walk go through shut doors); no run has checked the plan. It was written from the world database export in `tools/dungeongen/testdata/map36` (spawns, game objects, entrances, and `movement.tsv`, the `creature_movement` rows of the patrols), the instance script (`src/scripts/dungeons/deadmines`: `instance_deadmines.cpp`, `deadmines.cpp`, `boss_mr_smite.cpp`), the EventAI and `creature_spells` rows, `Lock.dbc` and the map 36 navmesh, and compared with Wowhead Classic (zone 1581, the strategy guide 7786 and the npc pages with their comments). `TestDeadminesOnNavmesh` walks every waypoint (about 3850 yd of path with every pack position approached), `TestDeadminesPlanPointsOnMesh` puts every point on the mesh, `TestDeadminesPlanMatchesTheWorldExport` ties every pack member, boss, object and patrol path to the export, `TestDeadminesRouteHasNoUnplannedPack` asks that no hostile spawn within 20 yd of the walked route is left out (three catwalk Overseers are exempt in `deadminesExemptions`, which `TestDeadminesStatusNeedsSettledExemptions` makes settle before the status may change), `TestDeadminesDoorsAndEvents` pins the door and cannon steps, `TestDeadminesPatrolPeriodsAreMeasured` pins the 26 timed periods and the one estimate left (the catwalk Overseer), and `TestDeadminesStatusNeedsSettledExemptions` keeps the status a draft while an exemption is open or a patrol is still an estimate. The generated tables of `docs/PLANS.md` list it as hand-written (`handwritten` in the spec), and `offlineMapped` in `plans_all_test.go` lets a draft carry steps.

| Leg | Boss (entry) | What the leg does |
| --- | --- | --- |
| `mine_a`, `rhahk_zor` | Rhahk'Zor (644), the two Defias Watchmen (1725) first | the upper mine from the entrance; his death opens the Factory Door 13965, the wait is an `after_boss` step |
| `factory` | none | the factory floor; clicks the Heavy Door at -262.7 -482.4 and waits for it |
| `sneed` | Sneed's Shredder (642) | every Goblin Woodcarver of the room (Terrify feared players into them), the Shredder; after it a wait for the hostiles to be gone (Sneed 643 is ejected, no spawn row) and for the Heavy Door at -290.3 -537.0 that his death opens |
| `west_area` | none | the Strip Miner cave, then clicks the Heavy Door 17154 at -243.0 -578.6 |
| `hall`, `harvester` | Masterpiece Harvester (61963) | the goblin hall (z 51), the side branch to the Harvester (Turtle's Foe Reaper replacement) |
| `gilnid` | Gilnid (1763), a Goblin Craftsman first | back to the top of the ramp, down the spiral to the foundry pit; his death opens the Heavy Door at -168.5 -579.9 |
| `cannon_a`, `cannon` | none | the strip miner galleries; `cannon` loots the Defias Gunpowder (17155, item 5397), waits for the Overseer it summons to die, fires the Defias Cannon (16398, needs the item) and waits for the Iron Clad Door 16397 to be `active_alt` |
| `dock`, `mr_smite` | Mr. Smite (646), two stealthed Blackguards (636) as an `ambush` group | the dock and the gangway |
| `deck_a`, `deck_b`, `captain_greenskin` | Captain Greenskin (647), his Pirate first | the lower deck, the stern stair, the upper deck |
| `edwin_vancleef` | Edwin VanCleef (639), two stealthed Blackguards as an `ambush` group | the end of the dungeon |

The plan has 136 pull groups, 45 of them with a `patrol` block (the paths are the `creature_movement` rows; the periods of 26 patrols were **timed on the walk** (24 within 3 % of the 2.5 yd/s estimate; the miner 79144 walks 1.65 yd/s, 107.5 s; the Wizard 79284's pack of the hall walks 312 yd in 130.5 s, and is the patrol of hall:1 and hall:2, not the Taskmaster 79283's 51 s) and carry no `period_estimate`; the catwalk Overseer 2583051, which patrols two points along y -584 and was out of every sampling range, keeps its calculated 22 s and `period_estimate: true` on harvester:3; a pack that two patrols pass keeps the nearer one). The nine Defias Companions (3450, level 14-15, made by the server with a Pirate, no spawn row) are members of the groups of eight Pirates and Greenskin's `first` The pull radii are the largest (2 to 30 yd) that keep every unlisted mob out of range of a group (`TestNoMobStandsInAPullGroupsRangeUnlisted`: radius + 3 yd, 3-D), and packs whose members stand within 6 yd of each other are one group (the patrol of a Defias Overseer and Evoker pair walks through the mine's packs) and `fleers` for every entry whose EventAI flees at 15 % health. Cookie (645), Miner Johnson (3586, a rare), Jared Voss (61961) and the laboratory are on no leg. The route follows the navmesh, not the usual guide order: the navmesh reaches the Harvester's plateau before the foundry, so the Harvester is taken between Sneed and Gilnid (a side branch, about 350 yd of walking extra).

**Doors and the event** (instance script, `gameobject_scripts`, `Lock.dbc`):
- Rhahk'Zor's death opens 13965 (`active`). Sneed's death (not the Shredder's) opens spawn 26185 (the Heavy Door 17153 at -290.3 -537.0), Gilnid's death spawn 26182 (-168.5 -579.9). The levers 101831, 101832 and 101834 open the same three doors for 15 s from the far side and are not used.
- The Heavy Doors at -262.7 -482.4 (spawn 26183) and -243.0 -578.6 (spawn 26184) have **no script and no lever**; their lock 86 is Quick Open with no key, so the plan clicks them (`use_object`, then `wait_for ... active`). **Verified on the walk (2026-10-15)**: both start shut (state ready, flags 32), one click opens each (state active, flags 33) and they stayed open for more than 50 minutes.
- The cannon: the Defias Gunpowder (a chest, lock 57) gives item 5397 and, once per reset, summons a Defias Overseer that runs to -115.3 -617.4; the Defias Cannon (a goober, lock 83 = item 5397) sets the Iron Clad Door to `ACTIVE_ALTERNATIVE`, so the wait says `active_alt`. The lever 101833 does nothing until the door is open and is not used.
- The 12 "detour" warnings the #192 plan check gave the generated draft (packs whose way is much longer than the line: other floors, other tunnels) are gone: only packs that the navmesh reaches in at most twice the line plus 18 yd are in a leg, and the plan check of #192 (`feat/zf-zumrah`, built on yulon-ubuntu2) reports none for the new plan.

**What the driver cannot do yet** (no step type for it; nothing was invented):
- kill a named unit that has no spawn row (Sneed 643; the Overseer the gunpowder summons): the plan waits for `hostiles_gone` after the boss or the loot and relies on the party fighting what attacks it. The wait is a snapshot: radius 45 is chosen so the Overseer (36 yd from the gunpowder when summoned) is inside it;
- a door that opens by a script after a boss is a `wait_for`; one that needs a click is a `use_object` that cannot tell an open door from a shut one;
- the Smite phase changes (the 10 s party stun, the walk to the chest), VanCleef's adds at 50 % and the Defias Companion of Greenskin's Pirate: no field for adds that come out of a fight; `kill_first` has the Blackguards (636) and the Engineers (622) and the CC lists the casters, nothing more;
- a unit's stealth: the Blackguards are `ambush` groups, the driver sneaks through the positions and fights what shows itself.

**Wowhead against the server** (the server wins; differences found): the guide puts an Engineer beside Gilnid and says the Craftsman summons the golem too: the database has a Goblin Craftsman (spawn 79211) beside him and the Remote-Controlled Golem only on the Engineer (622, `creature_spells` 6220; the Craftsman has Melt Ore). VanCleef's Allies (5200) are cast once at 50 to 36 % in the EventAI (the guide says 50 %, comments say 50 and 25 %). The two scripted patrol waves after Rhahk'Zor and Gilnid (`instance_deadmines.cpp`, respawn delays 43199 and 43201) have no spawn rows in this database, and the mine's own patrols are in the plan. The guide's level range is 18-23, the plan's band is 18-22. Taken from Wowhead: clear the Woodcarvers before Sneed's Shredder, keep the pull to Gilnid and his add, kill the Blackguards before Smite's 66 % stun, kill Greenskin's Companion and Pirate first and keep his Squallshaper sheeped, the first Blackguards of VanCleef before him.

**Risks for the first runs**: the doors that scripted deaths open (the walk cannot prove them); the Overseers on the catwalk over the goblin hall (z 57.8, three spawns, not pulled; 2583051 patrols along y -584; the route passes 6.6 to 10.8 yd below them in 3-D, aggro unverified, and no mesh path links them to the floor); the many lone miners that flee at 15 % (the fleer wait); the hall's golems are immune to most CC; Greenskin's loop (48 s a lap) passes the stair Shipbuilders (captain_greenskin:0) at 8.8 yd in 3-D: pull them when he is more than 40 yd away; the lower-deck packs are 21.6 yd or more from his loop; Sneed's Shredder patrols a 15-point loop (72 s a lap, the boss search is 80 yd); the Pirate 79341 at -19.0 -830.3 (stern, on no leg) has a Companion of its own that the plan leaves out.

## Dungeon runs

`dungeon_run {"name":"uldaman"}` makes the logged-in character the **dungeon leader**: it carries out a [plan](#dungeon-plans)
with the party it leads, the way a player would, with no help from the server beyond the companions' normal orders (issue #73).
The call returns at once; the run is a goroutine that ends in a report. `dungeon_status` returns the state and the report
(also while it runs), `dungeon_stop` ends a run and returns the final report. One run at a time; a second `dungeon_run` is refused
until the first has ended. Plans are read from `dungeons_dir` (settings; default `dungeons`). `dungeon_status` is one snapshot: the end
event (`dungeon_done` or `dungeon_failed`) is emitted before the report says `done`, `failed` or `stopped`, so a final state is never read
before its event; a state that is not final always has `running` true; `running` stays true for a moment after a final state while the
run's goroutine lets go of the brain.

Before a run: the leader is in a group (`invite`, or hired companions), is in the world, and stands on the plan's map: either
already inside the dungeon, or `"teleport": true`, which GM-teleports it to the start (the `entrance` for leg 0, else the place the
previous leg ended), summons every companion (`.bot summon`) and waits up to 60 s for them to arrive. `from_leg` starts at another
leg, by number or by name (`"from_leg": "keystone"`). **`from_leg` assumes the legs before it are cleared**: without `teleport` the party
walks from where it stands (from the entrance too) to the leg, and fights the packs of the earlier legs on the way as walk aggros (the
second live run: `from_leg keystone` from the entrance was a 167 s walk with 7 such fights); the report says so in a note. To start clean
at the leg, run with `"teleport": true` next to `from_leg`: the leader is GM-teleported to the leg start (the place the previous leg ended),
the party is summoned, and the run is flagged `teleport_to_leg_start` in `gm_shortcuts`.

**Resume** (`"resume": true`; off by default until a live check). A run started with the party already inside a leg (a restart halfway through it) would otherwise walk back to the leg's first point. With `resume` the leg's route is the straight lines from where the leg starts (the entrance, or where the leg before ended) through its `to`, the first position of each pull group and the boss; when the leader stands within 80 yd of it, past `to`, the walk to `to` is skipped. A pull group behind the leader is skipped only if a party member stands within 25 yd of each of its positions (the same `ClearSeeRadius` the remote driver uses to believe an empty pack) and no hostile is listed there (the unit list and the feed hold only what someone sees, so a pack nobody has looked at proves nothing); from the first group that does not qualify on, every group is kept, and an empty pull passes quickly. The report has `resumed` (`leg`, `at`, `skipped_walk`, `skipped_groups`) and a note. A leader before `to` (a run from the entrance) skips nothing. Not verified live: the route is straight lines between plan points, not the navmesh path.

**GM or player.** A GM leader gets `.gm visible on` first (a hidden GM changes what mobs and companions do) and may use the GM
shortcuts. A **player account works too** (issue #80 step 3a): `"gm": false` makes the driver send **no GM command at all**: no
`.gm visible on`, no `.go` teleport, no `.revive` of a member, no `.aura`. `teleport` and `buffs` are then refused with a clear error
(at the RPC, before anything is sent), the setup (standing at the entrance, the companions' levels) is done by hand and may be
listed in `setup`. `"gm": true` takes the leader for a GM with no probe, of level `gm_level` (1 to 6, only with `gm: true`;
developer, 3, when omitted). With `gm` left out the run **probes** once: it sends `.account`
(a command any account may run, Chat.cpp:77) and reads the reply "Your account level is: N" (0 player, 1 observer, 2 moderator, 3 developer
...). Only that reply counts: any other system line in the 2 s window (a combat message, "This command is not available to you.") is
ignored. With the server option `PlayerCommands = 0` a player's `.account` is said aloud instead, which is read as level 0. **No answer
(or a probe error) is taken for a player**: the server answers every account one way or the other, so silence means the probe broke
(lag, the event log), not a GM; the run sends no GM command, and the note and any refusal say to pass `gm: true` (and `gm_level`) to
override. A probe on a player account costs one `.account` line and no `say`; pass `gm: false` to send nothing.
The level then gates **each GM command on its own**, by the level the core's command table gives it:

| Capability (`gm_caps`) | Command | Level |
|---|---|---|
| `teleport` | `.go xyz` (Chat.cpp:234) | 1 observer |
| `visible` | `.gm visible on` (Chat.cpp:218) | 2 moderator |
| `revive` | `.revive` of a dead member at a rest gate (Chat.cpp:887) | 3 developer |
| `buffs` | `.aura` (Chat.cpp:874) | 3 developer |

Below a capability's level it is off from the start: `teleport` and `buffs` are refused before anything is sent, `.gm visible on` is
not sent (noted), and a dead member is not revived by GM: with `recover_wipe` it runs back to its corpse, without it the run ends
naming `recover_wipe`. The report has `leader_gm` (level above 0), `gm_caps` and `account_level` (when probed), and
`dungeon_run` answers `leader_gm` `true`, `false` or `probe`.

What the driver does for each action of the plan (the stepper decides which):

- **walk** is `walk_path`, tried up to 3 times (it replans after a server move by itself); a walk that cannot arrive fails the run.
- **use_object** sends the use, waits 1.5 s, and the plan's `wait_for` step watches the door or altar change state. Walks inside a step are bounded (see above).
- **loot_object**, **use_item**, **boss loot**: the game must offer them (the agent's session does; a test game without them fails the step with "this game cannot open objects").
  Each holds the brain's move lock around the cast, the result is a `dungeon_step` event, and what was taken goes in the report's `notes` ("took 1 x item 7740").
- **pull of a stealthed group (ambush).** Companions never attack a unit they cannot see, so `.bot action pullback` on a stealthed mob
  draws no fight (the Uldaman Stonevault Cave Lurkers, aura 5916 *Shadowstalker Stealth*). A pull group is therefore not pulled when
  the members in view all carry a stealth aura (`auras` on the unit, by spell id only: 5916, Sneak 22766, Stealth 1784-1787 and 8822, Prowl 5215, 6783 and 9913, Shadowmeld 20580; a
  *Stealth Detection* aura, which many ordinary spawns have, is none), or when none is in view and the plan marks the group `"ambush": true` (`tools/dungeongen` sets it when every member spawns
  stealthed). A member in view without the aura is pulled as usual even in a flagged group, and in a mixed pack the visible member is
  the target. The driver instead says `.bot action follow`, walks the leader to each of the group's positions in turn and stands
  6 s at each, fighting whatever reveals itself (`.bot action attack` on the nearest attacker until nothing has hit the party for 3 s).
  The group counts as cleared when none of its members is left alive in view **or after the party passed it with no fight**; the report
  notes which (`stealthed group [4850 4850] passed with no fight; counted as cleared`, or the fights and members left) and the leg's
  `ambushes` counts the groups. A stealthed member that was left behind aggros on a later walk and is fought there (next item). A
  stealthed member that is not fighting never holds a normal fight open.
- **walk aggro.** A walk is watched (every 0.5 s) for a pack that attacks the party: the walk is stopped, the party fights it
  (`.bot action attack` on the nearest attacker, again every 10 s, until nothing has hit the party for 3 s, then the usual wait for
  loot and adds), and the same walk is made again. It does not use up a walk try, counts as an extra pull of the leg (`pulls`, and
  `walk_aggros` for how often), and is noted. More than 12 such fights in one walk, or a fight of over 300 s, fail the run.
- **pull** targets the nearest living unit of the group's entries that stands within 25 yd of one of the plan's positions (the group's optional `radius`
  replaces the 25: the two rows of Ambushers in the Uldaman Keystone room use 10 so that each group takes its own) or is
  fighting the party (a pulled mob leaves its spawn), walking up to within 38 yd of it first when it is out of 45 yd, along the navmesh
  path. **A mob of the same entry in another pack, farther from the positions, is not a member**: a group whose own mobs are all dead
  (an earlier walk aggro killed them: a corpse within the group's radius of a position says so, a corpse elsewhere does not, and with
  nothing of the group in view the party walks to the positions first) is "taken as already dead" and nobody is sent at the distant one (second live run: a Rockchewer 42 yd
  away that the tank could not reach). The leader says `.bot action pullback` in party chat: the tank pulls and
  brings the mob back, the others join after 5 s. About every 10 s of fighting (and at once when the mob it last sent the party at died) the leader targets
  the pack member the tank holds best, not the nearest (**Tank first**, below), and says `.bot action attack`. The fight is won when every pack member in sight is dead; units of `skip` are never part of it.
  A pull that draws no fight in 25 s, or whose mobs stop fighting for 12 s (evade), is tried again (`.bot action follow` first), 3 pulls
  at most; a pull in which a member died is progress and does not use up a try (stationary mobs with no group link come one or two at a
  time: the six Stonevault Ambushers of the Uldaman Keystone room). A mob that drew no fight or evaded with nothing dying (a zero-kill `noEngage`/`evaded` pull) is passed over for
  the next try when the group has another member; that is this group's alone, a later group whose area holds the mob still pulls it.
  Mobs the bots refused (below) are skipped by the whole run. When the tries are used up and the group made progress, it counts as cleared with a note (the leftovers
  aggro on the walks after); with no progress at all the run fails. Hostile units still hitting the party after the fight (adds) are
  fought too, for up to 15 s.
- **tank first.** The leader targets what the party attacks, so the same rules as in remote mode apply to its choice (README "Tank first (remote)"), read from the units since
  a 1.12 client has no threat list. The tank is the party member of class warrior, paladin or druid (in that order) whose target is a living hostile mob in combat, else the
  first of them in view. A mob scores 2 when it hits the tank and 1 when the tank targets it; the mob the tank holds best is targeted first, the nearest after, and the mob
  fought stays while it is on the tank. A mob that carries polymorph, sap, shackle, hibernate, banish, a freezing trap, turn undead, sleep or seduction (by the spell's mechanic
  or its name), or the moon raid icon (from `MSG_RAID_TARGET_UPDATE`), is left alone unless it is the last (`cc_respected`). A mob that has hit a party member that is not the
  tank for 3 s within 40 yd gets the tank: the leader targets it and says `.bot action pull 0` (the tank walks up and keeps it; the module briefly holds the other bots for the order, see the remote rule), then targets the party's
  mob again; 8 s apart for the same mob, 3 for the fight (`loose_add_orders`). The first attack order at a mob the tank holds waits until the tank has held it for 2 s (the brain
  has its own 2 s opening delay; the pull's `pullback` join delay of 5 s is the bots'). `report.tank_first`: `tank`, `tank_threat_share` (the mean share of the fighting mobs that
  were on the tank: their targets, not threat), `tank_threat_samples`, `loose_add_orders`, `cc_respected`, `attack_resends`, `target_switches`. Absent without a tank in view. The fake game
  and the fake feed test each rule; nothing is seen live yet (`docs/LIVE-CHECKS.md`).
- **refusals.** The driver reads what the bots answer to its orders (`TBM:ACTION_ERR|<command>|<code>|<message>` in the event log, and the
  whisper `<mob> is evading` / `is dead` of an attack order; that whisper is read from the module source, not yet seen in a capture) and keeps them in the report's `refusals[]` (`intent`, `code`, `message`,
  `leg`, `count`, `first`). `pullback`/`pull` `unreachable` (the tank cannot start moving to a firing position) or an evading mob: the
  mob is skipped for the rest of the run (noted), the next member of the group is pulled, and a group with nothing left to pull counts as
  cleared with a note after the bounded tries. `dead-target`/`no-target` on a pull: the target is picked again (3 times at most, not a
  try). The same on an assist `attack` order: sent again at once, twice; a bot that keeps refusing is then asked on the usual 10 s
  cadence. The whisper names no command, so it counts as the answer to the last order the driver gave, and only from a party member. `no-tank` and other codes are only recorded.
- **kill_boss** does the same for each entry of `boss.first`, then for the boss; the whole encounter counts as one fight, so
  brothers that join are fought and a boss already dead in the fight before is taken as killed. A boss is never taken as dead unseen:
  when it is not in view the leader walks toward its position (up to the standoff, three looks), and only its corpse in view counts
  (noted "dead already: its corpse is in view"); a boss neither alive nor dead there fails the run. (Live run 4's "taken as already
  dead" came after a 150 s rest gate during which the party had killed Revelosh, who had joined the last pack's fight: the corpse was
  there.)
- **rest gate**, at the start of each leg and before every pull and boss. It starts only when the party is out of combat: while a fight
  goes on it waits (ordering the attack on the nearest attacker like a fight does), and its 60 s count from the end of the fight (a
  fight of over 300 s fails the run, and so does a gate that has gone on for 60 s plus 2 x 300 s in all, whatever keeps interrupting
  it, such as a mob that flickers in and out of range or keeps its combat flag). Then `.bot action rest`, and wait (health and mana as `party` reports them: the unit fields for a member in view, see the `party` method) until every living member has at least 90% health, every healer
  (a paladin, priest, shaman or druid by the `class` of its unit row) at least 50% mana and every other mana user at least 35% (mana only for mana users),
  and is within 40 yd of the leader, for up to 60 s. These are targets, not "full": a leg's `rest` in the plan overrides them and the cap
  (`health`, `healer_mana`, `mana`, `max_s`). **Drink nudge:** a member short of its mana target whose mana has not risen at all for 15 s (it is
  not drinking) gets one order of its own, `.bot command <Name> u drink` (the owner-or-GM `u <item>` command with the item text `drink`; the bare word `drink` is no chat command of the module and would be ignored; sent as party chat, like the other orders), once per gate, reported as `drink_order_result: "sent (unverified)"` until a live run shows the bot drinks on it; and when TortoiseBots refuses the party
  `rest` order (`rest`/`failed` "No scoped bot accepted the mature action", TB #916) the members that are short get that order at once (`drink` for
  mana, `u food` for health) instead of waiting. The reply is not read: a refusal of the order comes back as a TBM:ACTION_ERR and is listed in `refusals[]`.
  A caster below its mana target whose mana has not
  risen by 5% of its maximum for 30 s (it is not drinking) no longer holds the gate once everything else is rested: the gate passes
  and notes (not for a healer below 30% mana: the party would go
  on without its heals, so that gate waits its 60 s out and the run goes on with the usual note) `rest gate passed with Wishe not drinking (mana 49%); mana did not rise in 30 s` (a companion behaviour to report to
  TortoiseBots, not to work around). After the cap the run goes on with a note if everyone has 50% health, else it fails. **Report:** `rest_seconds` (the run's total, and per leg),
  and `rest_gates[]`, one per gate: `leg`, `seconds` waited, `max_s`, `result` (`rested`, `stalled` = passed with non-drinkers, `gave_up` = the cap came with someone short, `failed`),
  `short[]` (who was short when it ended, `Wishe mana 29%`), `drink_orders[]`, `drink_order_sent`, `drink_order_result`, `party_rest_refused`,
  `skipped` and `refills[]`. In an observe run the run leader's own `rest` phases count as rest gates (`result` `run_leader`, and the `rest` trace events carry `"source": "run_leader"`), so `rest_share` is real there too; the time a run spent paused was not counted as rest (a paused status still names the phase it paused in). **Lighter gates (both modes):** a gate that finds everyone at the targets on its first frame costs no time (`skipped: true`, result `rested`);
  the gate that opens a leg whose first step is a walk of 60 yd or more lets the party go when everyone is over the walking floors (health 70%, healer mana 40%, other
  mana 25%) instead of holding: result `walking`, `skipped: true`, a note, and the party rests on the way; the gate before the first pull still waits for the targets.
  A leg's `rest` overrides it: `walk_yd` (`-1`: always hold, 1-5000), `walk_health`, `walk_healer_mana`, `walk_mana`. The report totals them as `rest_gates_skipped` (of which
  `rest_walk_passes` walking ones) and gives `rest_share` (percent of the run spent in rest gates, also in the `dungeon_done`/`dungeon_failed` summary, the trace's end counters
  and the `trace-score` `rest_share` check, which still sums the `rest` events' seconds). **Refills (remote mode):** the preflight sends `.bot maintenance <bot>` once and the module
  buys only 6-7 drinks a bot. At a gate where a mana user is short of mana, the driver sends `.bot maintenance <bot>` again, at most once per bot per leg, when its drink count
  (`.bot inv`, read afresh once per bot per leg) is under 3 (`low-drinks`), its drink nudge fired (`nudge`), or the feed says its last action was a drink that failed for want
  of one (`drink` `F` `no-item`: at once) or a drink that failed in two different frames within 10 s (`drink-failed`; TortoiseBots reports item-action failures as `drink,F,-`, with no reason), then reads `.bot inv` again and orders the party to rest anew. `remote.refills[]` has `bot`, `leg`, `why`, `before`, `after`
  (drinks, `-1`: not read) and `maintenance`; each is a `refill` trace event and a note, and `refills` is in the summary. With maintenance off on the server (the preflight's
  `disabled`) or a game that cannot send `.bot` lines, none is sent. **Automatic maintenance (remote mode):** the party also gets `.bot maintenance` on a schedule, so a long run keeps
  its reagents, ammo and drinks. The tunable `maintenance_every_legs` (default 3; 0: off, 0-20) sends it to every bot at the start of legs N, 2N, ... (leg 0 is skipped: the preflight
  just did it), before the leg's rest gate. Never in combat: when the party is in combat or a bot is dead at that moment, it is tried again at the next calm moment of the same leg
  (the end of a rest gate, which runs before every pull) and given up when the leg ends. After every recovered wipe (`recover_wipe`) the party is maintained once as soon as
  nobody is in combat and everyone is alive; this is apart from the per-leg slot (one round covers both when both are due). Each bot is asked with the preflight's 6 s ACK; the verdict
  `disabled` turns it (and the refills) off for the rest of the run, like the preflight's. The refills above are unchanged. `remote.maintenance_runs` counts the rounds,
  `remote.maintenance_skips` the rounds not done by reason (`combat`, `disabled`, `no bot lines`); each round or skip is a `maintenance` trace event (`leg`, `reasons`, `verdicts`) and a note.
  A dead member is
  revived by GM (`.revive <name>`), flagged in the report, unless `no_shortcuts` is set or the leader is no GM (then the run fails,
  naming `recover_wipe`; with `recover_wipe` the member gets a minute for a healer's resurrection, then `.bot action release` and
  `.bot action corpse run`, and the gate waits for it up to 12 min). **Before any of that, a leader whose brain can resurrect gets up to 120 s**
  (`Config.BrainRezWait`) to do so: see [The holy paladin and resurrecting the party](#the-holy-paladin-and-resurrecting-the-party); after a revive the party
  list is polled for up to 5 s for the dead flag to clear before the member is judged, and a second revive goes out only after that
  (two revives at most, then the run fails).
- **buffs** (optional, `dungeon_run`'s `buffs`): a **GM boost**, off unless asked for. `buffs` is a spell id, a preset name or a list of
  both (`"buffs": "world"`, `"buffs": [22888, 15366]`, `"buffs": ["world", 24425]`; duplicates are dropped, at most 20 spells). The
  preset `world` is the classic world buffs that work at any level: Rallying Cry of the Dragonslayer 22888, Songflower Serenade 15366,
  Spirit of Zandalar 24425, Warchief's Blessing 16609, Fengus' Ferocity 22817, Mol'dar's Moxie 22818 and Slip'kik's Savvy 22820 (each id
  and name checked in the core's `spell_template`). The leader selects each living companion and sends `.aura <spell id>` (a GM command
  of developer level; below it the server puts the aura on the leader itself), at the start of the run, again after any GM revive
  (death strips the auras), and at the start of a leg for a companion in view that lost one (at most once per leg and member; if the
  buff does not stick the run only notes it). **Every application is a `buff` entry in `gm_shortcuts`** (`detail` `<spell> on <member>`), the
  spells are listed in the report's `buffs[]` and in the `dungeon_done`/`dungeon_failed` totals (`data.buffs`), so a buffed run is never
  mistaken for a player's run. The server answers a refused `.aura` only in chat, so the driver looks at the companion's auras a moment
  after the first batch: a buff that is not there ends the run with `buffs: GM .aura on <name> had no effect ... the leader must be a GM
  with developer security`. Each `.aura` goes out together with the selection of its companion as one session operation, so a `target` call or the addon
  host cannot move the buff onto another unit mid-batch. The first `.aura` of each companion is checked: the companion must be in the leader's object store and within 80 yd before it
  is sent (right after a teleport it may not be: all are waited for up to 10 s together; a companion that never shows gets nothing and is
  noted, and if none shows the run fails with `could not check the GM .aura`), and it must then show the aura within 3 s. If it does not
  (the second live run: the first `.aura` of the 4th companion landed on the leader, whose own aura list gained it), the selection is made
  again and the command is sent once more; a second miss stops the batch and fails the run (`... the aura landed on the leader itself,
  so the selection of <name> did not hold`) once the 10 s (counted from the first command) are up. "Landed on the leader" is judged against
  the leader's auras from before the batch, so an aura the leader already had is no misfire. An aura on the leader never counts as
  success; every resend is a `buff` entry too.
  A companion whose 32 positive aura slots are full cannot take more, which looks like a refusal: use fewer spells (the list holds at
  most 20 and the bot has buffs of its own). Recommended setup: hire the companions 3-5 levels above
  the dungeon (a boost on top of an under-levelled party only hides it) and list that in `setup`.
- **the leader's death** is not a wipe while a member lives: the driver pauses its orders and gets the leader back (see [Death and the
  corpse run](#death-and-the-corpse-run)), gathers the party (`.bot action follow`) and rests, then the run goes on from where it was.
- **wipe**: every online member dead (the leader too, or not) ends the run at once (`state` `failed`, `error` `wipe`, `wipes` 1),
  unless `recover_wipe` is set: then the members are told `.bot action release`, the leader does its corpse run, the members are told
  `.bot action corpse run` (again every 30 s while any is dead), and when all live the run gathers, rests and goes on from the current
  step. At most 2 recoveries a run and 12 min each; the next wipe ends the run. `.bot action release` and `.bot action corpse run` only reach
  companions the leader owns or hired (TortoiseBots' `CanControlBot`); bots that are only in the group but not its own ignore them,
  and the recovery then ends with "members are still dead" after 12 min. Every wipe counts in `wipes`, recovered or not.

Events (`events` kinds `dungeon_leg`, `dungeon_step`, `dungeon_failed`, `dungeon_done`, and `dungeon_leader` for the leader's death): `dungeon_leg` at the start and end of
each leg (`data.phase`, `result`, `seconds`, `deaths`, `pulls`, `rest_seconds`); `dungeon_step` when an action finishes (`data.action`
walk, use_object, pull, kill_boss, or wait when a wait step begins; `ok`, `seconds`, `error`); `dungeon_failed` when the run ends
without finishing (`data.error`: `wipe`, `stopped`, or the failed step); `dungeon_done` with the totals.

The report (`dungeon_status.report`): `plan`, `map`, `state`, `from_leg`, `started`, `ended`, `seconds`, `current` (the action in
progress), `error`, `wipes`, `leader_gm`, `gm_caps` (`visible`, `teleport`, `revive`, `buffs`), `account_level`, `leader_deaths`, `resurrections` (accepted), `corpse_runs`, `seconds_dead` (per leg too), `deaths[]` (`name`, `leg`, `at`), `legs[]` (`leg`, `name`, `seconds`, `result`, `deaths`, `pulls`,
`pull_retries`, `rest_seconds`, `walk_aggros`, `ambushes`, `leader_deaths`, `resurrections`, `corpse_runs`, `seconds_dead`; a leg before `from_leg` has `result` `"skipped (from_leg)"`), `start` and `pulltest` (remote mode: [Start anywhere and the pull test](#start-anywhere-and-the-pull-test)), `notes[]`, `refusals[]` (see **refusals**), `gm_shortcuts[]`: every GM action the run took instead of
playing it, with `kind` (`teleport_to_entrance`, `teleport_to_leg_start`, `revive`, `buff`), `leg`, `detail` and `at`, `buffs[]` (the spells asked for, `id` and `name`), and
`setup_shortcuts[]`: the setup the caller did by hand, as `dungeon_run`'s `setup` parameter listed it (`"setup": ["go entrance",
"levelup -15", "tbm summon all"]`; at most 40 entries of up to 400 characters; empty when none were given). Nothing checks or sends
them: they are there so that a report shows what a run was started with. A run with any `gm_shortcuts` or `setup_shortcuts` is not a run
a player could have done.

Limits of this driver: no CC marks, no loot orders beyond the `companions_loot` pair (the companions loot on their own), no stuck detection beyond
the bounds above and the [run watchdog](#run-watchdog), and stealthed packs are walked through, not pulled. The code is
tested against a fake game and clock (`internal/dungeon/driver_test.go`) and the agent's RPC rig; the first live run is listed in
`docs/LIVE-CHECKS.md`.

### Run watchdog

Issue #73, #83. A run must never sit stuck without making progress (the live Uldaman runs waited on a chest order, a rest gate and a
silent feed for minutes). Both drivers (group and remote) feed one watchdog (`internal/dungeon/watchdog.go`) and ask it at every wait.

**Progress** is any of: the step or leg index advanced; a pull started or ended; a kill; a fought mob's health at a new low, 1 % of
its maximum or more below the lowest it has been since the stall began (each mob on its own, in a pull, at a rest gate and on the way: a
long boss fight that wears the boss down never stalls, while a mob that evades and comes back, or is healed back up by an add, is progress
only once); the lead bot (remote) or the leader (group) got more than 5 yd farther from where the stall began than it had been (net
displacement: a bot running to and fro between two points keeps nothing alive; a steady walk counts every 5 yd; in a fight it counts only
until the recovery); an order was ACKed, *if it is not one of the last 16 orders ACKed* (the same `cmd move` or `cmd rest`, or two orders
alternating, sent again and again does not keep a stuck run alive; remote orders only, a chat order has no ACK); a rest gate finished; an
object step ended. **After the recovery has fired**, what a stuck run can fake by repeating itself (a pull started or ended, an order ACKed,
a rest gate passed, and movement anywhere but in a walk) is not progress any more; only a kill, a new health low, a step, an object step or
a walk getting on is. A walk is every walk of the lead bot or the leader that is not inside a fight: the plan's walks and those of a pull
step (to a standoff point, through a stealthed pack, to a group's position to look at it); the fight itself (the `fight` phase: a pull's
fight, from its pull order on, a fight at a rest gate, a fight on the way) is judged by kills and health. So a recovery that re-pulls a boss
that stays stuck at full health, running the tank to the commander (`cmd follow`) and back, cannot keep the run alive.

**`fail_s` is a ceiling** for the time without progress the watchdog counts. Some waits have a limit of their own and pause the clock
(the time held is not counted, the stall before it is): a rest gate while the party is out of combat (at most the gate's `max_s` or
`RestTimeout` plus 2 x `FightTimeout` for the whole gate; a fight inside the gate runs the clock), a plan's `wait_for` (its `timeout_s`), a
leader's death (a resurrection wait `ResWait`, the release, a corpse run of at most `CorpseRunTimeout`, 10 min; at most `MaxLeaderDeaths`,
5, per run), a wipe or dead members with `recover_wipe` (the corpse runs within `WipeRecoverTimeout`, 12 min; at most `MaxWipeRecoveries`,
2, per run) and a remote revive of dead bots (its wait and `WipeRecoverTimeout`). Every other wait is on the clock and fails at `fail_s`.

**Levels**, seconds without progress, `watchdog {warn_s, recover_s, fail_s}` in the plan or in `dungeon_run` (defaults 120/240/420; a
negative field turns that level off; `dungeon_run`'s field wins over the plan's; levels out of order are refused). Each fires once per stall;
progress ends the stall and the levels can fire again.

| Level | What happens |
| --- | --- |
| warn | a `stall` trace event and a report note: the time, the last progress, the phase and plan step the run waits on, the last order and its answer |
| recover | one bounded recovery attempt, by phase (below); noted, and a `stall` event with `recovery` |
| fail | the run stops cleanly: `error` is `stalled: <phase> <step> for N s, last progress <what>`, `state` is `failed`; the end-of-run fight handling and the unbind still run (the watchdog is off by then) |

Recoveries. Remote: **walk** re-plans from the lead bot's position with a shorter leg (`cmd move` at most 40 yd on), **object step** cancels the carrier order's retries so that the step's retry or fallback takes
over, **fight** (a pull's) sends `cmd follow` and ends the fight so that the target is picked again; a fight at a rest gate or on the way sends
nothing (its own `FightTimeout` bounds it), and a walk's recovery sends no `cmd move` while the party fights; **feed silent** (no frame for `FeedStale`)
sends `commander view on` again. The feed is also switched on again once, at half of `FeedLost` silence, before the run is failed for it.
Group: **fight** (a pull's) orders `follow` and picks again, an **object step** gives up its wait for the item; a
group **walk** has nothing to cancel (each try plans again from where the leader stands). A phase with no recovery of its own uses the
enclosing phase's; with none the stall is noted and the failure comes at `fail_s`.

The watchdog is looked at from the drivers' own waits, never from a goroutine: a single game call that blocks is bounded by its own
timeout and is judged at the next wait. A player order that reported `ACTION_PENDING` waits for its final answer at most 90 s (`cmd enter`: 130 s), whatever budget the
server named (`tbm.MaxPendingWait`).

**RPC.** `dungeon_status` has `last_progress_at`, `stalled_for_s` (0 while the clock is paused) and `stall_level`, and the same with the limits
and totals in `report.watchdog`: `warn_s`/`recover_s`/`fail_s` (0: off), `last_progress_at`, `last_progress` (what it was), `stalled_for_s`,
`stall_level`, `waiting`, `warnings`, `recoveries`, `longest_stall_s`. A poller needs only `stall_level`: `none` is a run that moves,
`warn` and `recover` a run that is being looked after, `fail` a run that has been stopped.

Tests: `internal/dungeon/watchdog_test.go` (the watchdog on a fake clock: each level fires once per stall, progress resets it, the recovery is
attempted once, the failure text, the pause) and `watchdog_driver_test.go` (both drivers: a rest gate holds the clock, a stalled pull recovered once and then failed at `fail_s`, a long fight that wears its mob down never stalls, a mob whose health flips 100/90 fails at `fail_s`, a walk in a pull step that gets on after the recovery, a fight on the way or at a rest gate that wears its mob down, no `cmd move` while a fight on the way stalls, a walk re-planned, a silent feed, the carrier's retries cancelled); the RPC
fields in `cmd/turtle-agent/dungeon_methods_test.go`. Not verified live: see `docs/LIVE-CHECKS.md`, "Run watchdog".

## Run traces and checks

Issue #73, data-collection step 1. Every `dungeon_run` (group and remote) and every brain session has a **run id**, and a dungeon
run writes a **trace**: a JSONL file with what the agent saw and did, which a pure scorer turns into pass / fail / n/a verdicts. The aim is to
score many runs and compare pass rates per build, instead of watching one run and guessing. The format is shared with the TortoiseBots
side (a server recorder is the next step, and a TortoiseBots dashboard reads the summary files), so the two producers' files join on the
run id. The ideas (one trace schema, oracles that are pure functions of the trace, re-scoring old traces) are those of mod-dungeon-clear's
Pull Lab; no code or data of it is used.

**Run id.** `r-<utc yyyymmddThhmmss>-<4 hex>`, e.g. `r-20261014T093012-a1b2`, made by the agent, or the caller's own `run_id` (letters,
digits, `.`, `_`, `-`, at most 64, starting with a letter or digit; it names the file, so nothing else is allowed). It is in the
`dungeon_run` and `brain_start` results, in the report (`run_id`), in the data of every `dungeon_*` and brain event (`data.run_id`), and
in the trace's header and file name. `run_meta` (`{"model", "effort", "run", "server_trace"}`, e.g. `{"model": "haiku", "effort":
"max", "run": "zf-ab-2-s5"}`) names the run's AI driver: `dungeon_run`'s wins when it names a driver (it replaces the slot default
whole; an explicit but empty `{}` counts as absent, so the slot default still applies), else the settings file's `run_meta`. It is
written into the trace header (`run_model`, `run_effort`, `run_label` beside `trace_meta`) and into the run report. By default
nothing of it goes to the server.

**Server trace (opt-in).** With `run_meta.server_trace: true` (off by default; refused without a model, effort or run) a remote
run, driven or observed, also stamps the run on the server: at run start it sends `.bot trace start <run_id> ...
driver=<model>:<effort>:<run>` (TortoiseBots #986), so the server's summary and the agent's trace join on one id, and it ends that
trace with `.bot trace stop` whatever way the run ends (done, failed, stopped, a panic: the stop is deferred right after the
acknowledged start, bounded to 5 s). It needs a TortoiseBots build that has `.bot trace` (the `driver=` field lives on
feat/986-trace-summary-fields). A group run never sends it: party chat carries no answer; the report notes it. The run owns the
server trace from an acknowledged start until the server answers its stop: a stop with no answer, a transport error or any refusal
other than `not-running` or `forbidden` is tried again (3 tries, then again at the end of the run, each retry after a `.bot trace
status` shows the trace running is still this run's id); when every try fails the report says the trace may still be running and
to stop it by hand. A refused start sends no stop. A start with no answer may still have been applied: no stop is sent for it, and
the report notes `server trace state unknown (start not acknowledged): stop it by hand if one is running`.

The server's `.bot trace stop` is not scoped: it takes no run id and stops whatever trace is running (the server records one at a
time). Two windows cannot be made safe from the agent's side: a start the server applied whose acknowledgement was lost leaves a
trace running that the agent does not stop, and between a retry's `.bot trace status` and its `.bot trace stop` someone else's
trace could start and be the one stopped. Enable `server_trace` only on boxes where nobody starts traces by hand. A run-id-scoped,
atomic stop on the server (`.bot trace stop <run_id>`, refused unless that run is the one recording; TortoiseBots #986) would close
both. and into the run report. `trace_meta` (up to 16 `key: text` pairs, e.g. `{"tb_sha": "...", "build": "19h", "scenario": "uldaman-1"}`) is copied
into the header: the agent cannot know the server's build itself.

**Where.** `<traces_dir>/<run_id>.jsonl` and `<traces_dir>/<run_id>.summary.json`. `traces_dir` is a settings key (a leading `~/` is the home
folder); without it `$TURTLE_AGENT_TRACES_DIR`, else `traces` under the working directory. On by default for `dungeon_run` and `brain_start`;
`"trace": false` writes no file (the run id stays). A trace that cannot be opened (the file exists, the folder is not writable) never
stops the run: the report's `trace.error` says why. The folder keeps the newest 200 traces of generated ids (older ones and their
summaries are deleted when a new one opens; ids you chose yourself are never deleted). The report has `trace_file` while the run goes and, once it
ended, `trace`: `path`, `summary`, `frames`, `events`, `bytes`, `truncated`, `verdict` (`fail` if any check failed, `pass`, `na`), `failed[]` and
`checks[]`.

**Size.** One frame about every second (a feed that runs a little faster is not halved; past half the size limit one in three is
taken). At 32 MiB the frames stop, with a `trace_limit` event in the file; events go on for a quarter more, then stop (`trace_limit`
again); the end record and the checks are always written. `end.dropped` counts what was left out, and `truncated` is in the summary.

### Schema `bt-trace` v1

The format is the one TortoiseBots' server recorder writes (`docs/guides/run-trace.md` there): one scorer and one dashboard read both
producers' files, and the two files of a run join on the run id. Every line is a flat JSON object that starts with the same five fields:
`v` (1), `k` (the kind), `run` (the run id), `src` (`agent` here, `server` there) and `t` (milliseconds since the run started). GUIDs
are strings, `0x` and lower-case hex without padding (`0x2a`). A reader ignores kinds, fields and events it does not know, and refuses
a file whose `v` is higher than it reads. Adding a field or an event name does not change `v`; changing the meaning of one does.

| `k` | Written | Fields (besides the five) |
| --- | --- | --- |
| `header` | once, first | as the server's: `schema` (`bt-trace`), `scope` (`party`), `scope_name` (the lead), `requester` (the agent's character, group runs), `start_unix`, `interval_ms`, `max_bytes`, `map`, `instance` (0), `caps`. The agent's own: `agent_version`, `agent_sha`, `source` (`dungeon_run`, `brain`), `mode` (`group`, `remote`), `plan`, `from_leg`, `start` (RFC 3339), `lead`, `tank`, `brain`, `options` (`teleport`, `shortcuts`, `recover_wipe`, `setup[]`, `buffs[]`, ...), `meta` |
| `party` | per member, when first seen | `g`, `name`, `class`, `role` (`tank`/`heal`/`dps`; only where known), `level`, and the agent's `lead` |
| `mobs` | per hostile, the first time it is in a frame (at most 1024) | `g`, `entry`, `name`, `level` (group runs) |
| `frame` | about 1 Hz | `bots[]`, `mobs[]`; the agent's `via` (`session`: its object store and party list, `feed`: a TBM:VIEW frame), `seq` (the feed's), `cut` |
| `event` | as it happens | `ev` and flat fields: the server's `a`/`b` (guids), `s`/`s2` (text) where they fit, the agent's own besides, see below |
| `end` | once | `reason` (`done`, `failed`, `stopped`), `error`, `seconds`, `ended`, `counters{}` |
| `oracle` | after the end, one per check | `id`, `name`, `verdict`, `count`, `first_ms`, `detail`, and the agent's `value`, `threshold`, `unit`, `evidence[]` (the server keeps its verdicts in the summary only) |

`caps` says what the producer records, so that a scorer answers `na` instead of passing what it cannot see. The server's:
`facing`, `moving`, `interrupts`, `in_fire`. The agent lists `facing` (bots have `fa`), `moving` (bots have `mv`; group runs only: the
feed has no movement flag), `threat` (mobs have `thr`; remote runs only), `orders`, `walks` and `partylist` (hp and power come from the party
list). A group run has no `threat`; a remote run has no `moving`; neither has `interrupts` or `in_fire`.

Frame bots (`bots[]`, keyed by `g`; the name is in the party record): `al` alive, `cb` in combat, `st` engine state (`c` combat, `n` non-combat,
`d` dead, `r` reaction), `hp`/`mhp`, `pw`/`mpw` power and its maximum, `pt` power type (0 mana, 1 rage, 2 focus, 3 energy), `x` `y` `z`, `mv` a
movement is active, and when set `tg` target, `fa` facing (degrees 0-180 between where the bot faces and the direction to its hostile target in view), `cs` the
spell it casts (feed), `act` its last action (feed; the module cuts the name to 16 bytes), `res` and `why` the verdict on that action and the reason a failed check did not run (feed, with TB #924; absent from older modules and traces), plus the agent's `np` (position unknown: x, y, z are 0) and `lead`. Frame mobs (`mobs[]`):
`g`, `hp` percent, `x` `y` `z`, `cb`, and when set `tg`, `cs`, `ev` evading, `cc` crowd-controlled, `thr` the top of the threat list as
`[guid, percent of the top]` pairs (feed only). A group run's mobs are the hostile units in combat or within 60 yd (at most 40), without
threat; a remote run copies the feed's party, hostiles and threat records. `end.counters` has the writer's `frames`, `events`, `party`,
`mobs`, `records`, `bytes`, `dropped` (with `dropped_frames`, `dropped_events`) and `truncated`, as the server's does, and the run's
totals (`deaths`, `wipes`, `gm_shortcuts`, `pulls`, `rest_seconds`, `refusals`, and for remote runs `orders_sent`, `orders_refused`,
`frames_received`, `frames_missed`, `feed_stalls`, ...).

Events (`ev`; fields beside `ev`, a field named like an envelope field is written with an `x_` prefix):

| `ev` | Meaning |
| --- | --- |
| `leg`, `step`, `leader` | the `dungeon_leg`, `dungeon_step` and `dungeon_leader` events of the run, with their data and `text` |
| `order` | an order sent: group `via: "chat"`, `intent` (`pullback`, `attack`, `rest`, `follow`, `command`, ...), `text`; remote `via: "cmd"`, `verb`, `text`, `reply` (`ack`, `err`, `timeout`), `code`, `message`, `ms` (the answer's latency) |
| `refusal` | group mode: a bot's whisper or `TBM:ACTION_ERR` refusing an order: `intent`, `code`, `message` |
| `pull_start`, `pull_end` | a pull: `target`, `entry`, `guid`, position; the end has `outcome` (`won`, `no_engage`, `evaded`, `refused`, `retarget`, `error`), `kills`, `seconds` |
| `rest` | a rest gate ended: `seconds` waited, `leg`; an observe run's run leader rest has `source` `run_leader` |
| `instance` | the lead bot's dungeon instance id, once, when the feed shows one: `instance`, `map` (the summary's `instance` comes from it when the header has none) |
| `stall` | the [run watchdog](#run-watchdog) fired: `level` (`warn`, `recover`, `fail`), `stalled_s`, `last_progress`, `waiting` (phase, step, last order), `phase`; a recovery has `recovery` (what it did) and `recovery_error`, a failure `error` |
| `walk_start`, `walk_end` | a walk of the plan: target `x y z`; the end has `ok`, `error`, `seconds` |
| `death`, `wipe` | a party member died (`a` its guid, `name`); the whole party was dead (a wipe, recovered or not) |
| `combat_on`, `combat_off` | a member's combat state changed between two frames (`a`, `name`), as the server's |
| `gm_shortcut` | a GM command taken instead of playing: `kind` (`teleport_to_entrance`, `teleport_to_leg_start`, `revive`, `buff`), `detail` |
| `stuck`, `unstuck` | a member that should be moving stood still for 5 s, with its position (`a`, `name`, `x y z`, `seconds`, `lead`, `dist_lead`); it moved again or stopped being expected to move (`seconds` is the total) |
| `rez` | remote: a dead bot was left to a living caster's resurrection: `leg`, `bot`, `raised`, `seconds`, `why` |
| `rejoin_tp` | remote: the commander sent `cmd summon <bot>` to bring a far bot to the lead bot: `leg`, `bot`, `reply`, `other_map`, `dist` (yd), `code` when refused |
| `silent_death` | remote: a bot died out of combat at 80% health or more one frame before (lava, slime, a fall: the feed has no line for it): `name`, `leg`, `x y z`, `map`, `hp_pct`, `spot` (`avoid_box`, `lone` or `deadly`: a second such death within 6 yd made a deadly spot, no summon near it for 5 min) |
| `lead_off_deadly` | remote: before a summon, the lead bot stood in an avoid box or on a deadly spot and was sent to a safe floor (`try` 1-2) or, after two tries, walked back to where it last stood and lived (`fallback`): `leg`, `x y z`, `to_x to_y to_z`, `why` |
| `feed_stall` | remote: no new feed frame for the stale limit |
| `roster` | remote: the party names in the first feed frame |
| `note` | the report's notes, as they are made |
| `brain`, `brain_state` | the leader's brain: a decision (`action`, `trigger`, `result`, `spell_id`, `target`, `via`, `error`), and its start and stop |
| `trace_limit` | the size limit was reached (`dropping`: `frames` or `events`) |

A group run takes hp and power from the agent's own party list (so a list that has stopped updating shows up as stale data, see the check)
and positions, facing, targets, combat and movement from the object store, for members in view. A brain session started by hand writes a
thin trace (header, a party record, brain decisions, end); a brain a dungeon run started adds its events to the run's trace. In a remote run the
party records have no class or level (the feed does not carry them) and `role` is only `tank` where `tank` was given.

Summary file `<run_id>.summary.json`: the server's shape, so a dashboard lists both. Written when the run starts (`status: "running"`) and again,
whole and atomically, when it ends and is scored: `v`, `schema`, `run`, `src`, `status` (`running`, `complete`), `end_reason`, `scope`,
`scope_name`, `requester`, `trace_file`, `start_unix`, `end_unix`, `duration_ms`, `interval_ms`, `map`, `instance`, `counters{}`, `oracles[]`
(each check: `id`, `name`, `verdict`, `count`, `first_ms`, `detail`, and the agent's `value`, `threshold`, `unit`, `evidence[]`), `verdict`
(`fail` if any check failed, `pass`, `na`), `scored_unix`, `scorer`, `trace_complete`, `bad_lines`. The agent adds `agent_version`, `agent_sha`, `source`,
`mode`, `plan`, `result`, `error`, `party[]`, `meta{}`, `failed[]` (ids), `bytes` and `truncated`. The report's `trace` has the same verdict and checks.

### Checks

`turtle-agent trace-score [--json] [--summary] [--set check=threshold]... <trace.jsonl>...` reads trace files and prints each check's verdict with
its measured value and the threshold; `--json` prints one object per trace; `--summary` also writes `<run_id>.summary.json` beside the
trace; `--set rest_share=30` changes a threshold. Exit code 11 when any check failed. The scorer reads nothing but the file, so a trace
can be scored again after a threshold or a check changes. A dungeon run scores its own trace when it ends and appends the `oracle` records.

Each check has an `id`, a `name`, a `verdict` (`pass`, `fail`, or `na` when the trace lacks what it needs), the `count` of times it
happened, `first_ms`, a `detail` sentence, the measured `value` against the `threshold` (with `unit`) and the `evidence` times. The ids
`threat_off_tank`, `deaths`, `facing` and `stuck` are the server scorer's (`tools/trace_score.py`), with the agent's reading of them;
the others look at what only the agent sees (orders, rest gates, walks, the feed).

| `id` | Fails when | Threshold (default) | n/a when |
| --- | --- | --- | --- |
| `rest_share` | the rest gates took more than this share of the run | 40 % | the run is shorter than 60 s |
| `stale_party` | a mana user's power did not change for this long while someone else's health or power did (the party data stopped updating); full mana and dead members are ignored | 60 s | no frames with a mana user |
| `walk_stops` | the lead stood still (at least 1.5 s) in the middle of a walk of the plan more often than this per 100 yd walked; the last 3 s of a walk are arriving | 4 stops / 100 yd | under 100 yd walked |
| `threat_off_tank` | a mob in combat is on a party member other than the tank — its `tg` is that member, or the member ties the tank's threat — during a pull for longer than this; a dead tank, a mob with no target (polymorphed, fleeing, evading) and a mob out of combat do not count; the tank is `header.tank` (an observed run names its lead), else the `tank` role, else whoever holds the top threat most often; the detail gives the spans per tank-combat-hour | 4 s | no threat data (group runs have none) |
| `deaths` | any death or wipe | 0 | never |
| `order_refusals` | more than this share of the orders was refused (`err` replies and `refusal` events) | 20 % | fewer than 3 orders |
| `feed_stalls` | the longest gap between two feed frames is over this | 10 s | no feed frames (group runs) |
| `gm_shortcuts` | the honest-run flag: the run used any GM shortcut or was set up with `setup` commands, so a player could not have done it | 0 | never |
| `stuck` | a member that should be moving (the lead in a walk of the plan, or a member more than 10 yd from the lead in one; alive and out of combat) did not move 1 yd for this long; the detail lists where, for a per-dungeon stuck-spot map | 5 s, any stall | no walk of the plan, or no frames |
| `facing` | a bot in combat stands (no movement) within 6 yd of its hostile target for longer than this while its `fa` is over 90 degrees (the server rule, without its casting case: the agent does not see casts) | 2 s | the header lacks the `facing` capability, or no frame has a bot in melee range of its target |
| `spells` | **report only, never fails**: what each class cast and did, as readings of 1 Hz samples, not facts. Cast episodes per class from `cs` (a run of frames with the same spell is `ceil(duration / base cast time)` casts, at most one per frame; a spell with no known cast time counts once per run, so that number is a lower bound), action mix, verdict letters and reasons, idle share, power at death, taunts, and two proximity/observation readings: **interrupts** (each interrupt action is matched to at most one enemy cast, from 1 s before its first frame to its last frame, and the interrupter's target must be the casting mob when both are known; an action in the frame right after the cast's last is counted apart, because an interrupt that worked and a kick that came too late look the same at 1 Hz; "confirmed cut short" is a cast seen for under 80 % of its cast time) and **crowd control** (an observation, not a success rate: some mob shows a new cc flag within 4 s of a single-target action; it is not tied to the caster or the spell, and a new flag on the caster's own target is counted apart), **taunt latency** (seconds from a hostile's top victim becoming a party member that is not the tank to the tank's taunt or the hostile going back to the tank; the frames are the resolution, about 1 s), **cc hold** (a cc episode is a run of consecutive frames with the mob flagged: only an episode that starts within 4 s of a single-target cc action is joined, and only long crowd control is joined (polymorph, sap, shackle undead, freezing trap, banish, hibernate, entangling roots, seduction, blind, fear, turn undead: the short stuns and disorients are left out); a joined episode under 5 s that ends with the mob still in view and unflagged, alive, was broken; the party's targeting of the mark is not measured) and **pet threat** (the share of mob-frames whose top threat holder's guid starts `0xf140`). A verdict is *unknown* where that bot sent no verdict letter (`res`) within 10 s: it is left out of the idle share and not counted as executed, so an older or mixed trace says `unknown`/`not recorded` instead of guessing. `trace-score` prints the tables under the checks; `--json` has them as `spells` | warnings only: `idle_share` over 15 % of engaged mana-user frames with a verdict and no executed (`O`) action for 4 s; `interrupt_cover` under 30 % of enemy casts (from 5) with an interrupt action during or just after them (the favourable reading is held to the limit); `taunt_latency` over 3 s median (from 3 episodes); `cc_hold` under 50 % held (from 3 episodes); `pet_threat` over 5 % of mob-frames (from 30); `--set idle_share=20`, `--set interrupt_cover=20`, `--set taunt_latency=4`, `--set cc_hold=40`, `--set pet_threat=10`. The uncorrelated crowd-control flag observation has no limit | no bot frame has a `cs` or an `act` |

Not verified live: the traces were built and tested against the fake game, the fake feed and hand-made traces only. Open: whether a
group run's party list stays fresh in a long fight (`stale_party` is there to find out), the feed's facing values, and what the frame
rate costs on a long run.

## Unit positions

Every `units` row carries where the unit is, as far as the server has said:

- `x`, `y`, `z`, `o` come from the unit's create or movement block, the movement packets the server relays for
  players (`MSG_MOVE_*`, heartbeats every 500 ms while they move), and the splines it sends for creatures, pets and
  bots (`SMSG_MONSTER_MOVE`, also inside `SMSG_COMPRESSED_MOVES`).
- While a unit runs a spline, `moving` is true and the position is the spline's current point: the route (a linear
  path's points rebuilt from their packed offsets, or a smooth path's points) taken at an even speed over the
  spline's duration, as the core times it. `o` faces along the current leg, and at the end the spline's final
  angle when it set one. A create block for a unit mid-spline carries on from how far along it the unit was.
- `pos_age_ms` is how long ago the server last said where the unit is (that block, relay or spline). For a player
  standing still it grows, as the server sends nothing more; for a unit on a spline the position is current
  although the age grows. A unit known only from a values block has no position: `pos_age_ms`, `distance` and
  `distance_2d` are -1, it sorts last and `within` never matches it.
- `distance` (3D) and `distance_2d` are measured from the player's own position (`status` x/y/z), so they are
  as good as that: a `z_unknown` player has a guessed height, and `distance_2d` is then the one to trust.

In the captured runs, a spline that the server cut short started its next one within 0.75 yd of the point the
agent had (90 % of about 100 cases). Wandering and pathing mobs in those runs reached their ends 1-1.7 s before
the duration the server sent; until it runs out the agent still has them that far short of the end.

## Unit state

Each `units` row (and `unit`) also says what the unit is doing:

- `in_combat` (`UNIT_FLAG_IN_COMBAT`) and `target`, the guid it has selected (absent when none).
- `class`, the class id from the low-order class byte of `UNIT_FIELD_BYTES_0` (1 warrior, 2 paladin, 3 hunter, 4 rogue, 5 priest, 7 shaman, 8 mage,
  9 warlock, 11 druid); absent when the server has not sent it. The dungeon driver uses it to tell a healer.
- `stand_state`: `stand`, `sit`, `sit_chair` (any chair height), `sleep`, `dead` or `kneel`, from the low byte of
  `UNIT_FIELD_BYTES_1`, named as the `stand_state` event names them. Eating and drinking sit the unit down.
- `auras[]` in slot order (never null): `slot`, `spell_id`, `name`, `stacks` (proc charges times stacks for an aura with charges, as the core counts
  them), `harmful` (slots 32-47), and
  `remaining_ms` for the player's own auras only: the server sends aura times to their owner alone
  (`SMSG_UPDATE_AURA_DURATION`), and `unit` is the way to see the player's own row. A unit eating shows the
  `Food` aura, one drinking `Drink`.
- `casting`, null when idle: `spell_id`, `name`, `channel`, and `ends_in_ms` for a cast with a cast time, from its
  `spell_start` until its `spell_go` or a failure (pushback on the player's own cast moves the end). A cast whose
  `spell_go` never came stops showing 2 s after its end. A channel (`UNIT_CHANNEL_SPELL`) has no end: the server
  sends its duration to the caster alone.

Spell names come from the core's `spell_template` (`sql/base/tw_world_spell_template.sql`), packed into
`internal/spells/names.tsv.gz` by `make spellnames CORE=<tortoise-wow checkout> DBC=<the server's dbc folder>`. A spell
the server's database added later has no `name`.

## Agent state

What the agent knows about its own character's spells, for a brain (issue #80) and for `spells` and `cast_state`:
`internal/spellstate`, kept up to date by the session loop from the packets below. All packet layouts were read from the
core (`Player.cpp`, `Spell.cpp`, `Unit.cpp`); `SMSG_INITIAL_SPELLS`, `SMSG_SPELL_START`/`SMSG_SPELL_GO` and `SMSG_CAST_RESULT`
are also in the committed captures. The rest is built from the core only: see `docs/LIVE-CHECKS.md` for what has not
been seen on a live server yet.

- **Known spells.** `SMSG_INITIAL_SPELLS` at login (u8 0, u16 count, count x (u16 spell, u16 0), then the cooldowns),
  `SMSG_LEARNED_SPELL` (u32), `SMSG_REMOVED_SPELL` (u16) and `SMSG_SUPERCEDED_SPELL` (u16 old, u16 new: the spellbook
  shows the highest rank alone, so the old one leaves the set). Spell ids are u16 on the wire in all but the learned
  packet; the core writes them truncated. **Never hard-code a rank's id**: `State.Highest(name)` is the highest known
  rank of a name (the N of "Rank N" in the spell's subtext, case-insensitive), and `spells` marks it `highest`.
- **Cooldowns and the GCD.** The server sends no cooldown packet for a spell the player casts (only for a pet, a school
  lockout after an interrupt, or a script: `Unit.cpp:11900-11907`), so the agent does what the client does and starts
  them from the spell data when its own `SMSG_SPELL_GO` comes: the spell's cooldown, or its category's when it has
  none, and the category lock (`Spell.cpp:3888`, `Unit.cpp:11838-11930`). The global cooldown starts at the player's own
  `SMSG_SPELL_START` (a direct cast always sends one, `Spell.cpp:3647-3651`), for the spell's `StartRecoveryTime` and
  in its `StartRecoveryCategory` group; a cast that fails gives it back (`Spell.cpp:8443-8458`). What the server does
  send is applied on top: the cooldowns in `SMSG_INITIAL_SPELLS` (u16 spell, u16 item, u16 category, u32 cooldown ms,
  u32 category cooldown ms; `0x80000000` is the month-long "infinity" kept for a few spells), `SMSG_SPELL_COOLDOWN`
  (guid, then u32 spell, u32 ms pairs), `SMSG_COOLDOWN_EVENT` (u32 spell, guid: start the cooldown now, as when an
  aura that blocked the spell ends), `SMSG_CLEAR_COOLDOWN` (u32 spell, guid) and `SMSG_COOLDOWN_CHEAT` (guid: all).
  `State.Ready(spell)` and `State.Remaining(spell)` (the longest of the spell's cooldown, its category lock and the
  GCD of its group, with the reason) answer for any spell. They err on the side of waiting: talents and items that
  shorten a cooldown are not known; the server rounds a cooldown to whole seconds, so it can be up to a second shorter
  than the agent's; haste does not shorten the GCD here; an item's own cooldown (a potion) is in the item prototype and
  is not tracked; Shoot and the wand (categories 76 and 351) have no cooldown in the template, the core uses the ranged
  attack time (`Unit.cpp:11888`), so they read ready; the core stretches a 1.0-1.5 s GCD with cast speed
  (`Spell.cpp:8420-8434`), so a slow can make it 1.5 s where the table says 1.0 s. A spell held back while its aura is up
  (`cooldown_on_fade`: Stealth, Cold Blood, Presence of Mind, Lightning Shield) reads on cooldown from its go until
  `SMSG_COOLDOWN_EVENT` starts the real one (`SpellAuras.cpp:6892`, `:7011-7016`). A cast the server refuses as
  `not_ready` holds the spell back for one second whatever the numbers say.
- **Cast state.** The player's own `SMSG_SPELL_START` (casting, with its cast time), `SMSG_SPELL_GO` (done),
  `SMSG_SPELL_DELAYED` (pushback), `MSG_CHANNEL_START` (u32 spell, u32 ms: channelling) and `MSG_CHANNEL_UPDATE` (u32
  ms left, 0: ended). A failure is `SMSG_CAST_RESULT` (u32 spell, u8 2, u8 `SpellCastResult`, with a few u32 more for
  some reasons) or the `SMSG_SPELL_FAILED_OTHER` interrupt broadcast. A `SMSG_CAST_RESULT` only records the failure
  (`last_failure`): a refusal of a new attempt (`spell_in_progress`, `not_ready`, `out_of_range`, ...) comes from a fresh
  spell object that is not the running cast (`Spell.cpp:3545-3551`), while a failure of the running cast is always
  preceded by `SMSG_SPELL_FAILED_OTHER` (`Spell::SendInterrupted`, `Spell.cpp:3704`, `:3786`, `:3828`), which alone ends
  the cast and gives its GCD back (`Spell.cpp:3699`, `:8443-8458`). The core never sends `SMSG_SPELL_FAILURE`
  (`Spell.cpp:4926-4935`), so it is not handled.
- **Combo points** are in the player's own update fields: byte 1 of `PLAYER_FIELD_BYTES` (index 0x4C6) and the guid in
  `PLAYER_FIELD_COMBO_TARGET` (0x2CA, two words); the core leaves the old guid in place when the points go to 0, so
  the agent reports a target only with points (`Player.cpp:20942-21002`).
- **Spell data** (`internal/spells/meta.tsv.gz`): per spell its rank, level, cost (power type and amount, or percent of
  base mana), cast time, range, cooldown, category and category cooldown, GCD group and length, school, dispel type,
  mechanic, aura duration, three effects (effect, aura, base points, dice, die sides, points per level, points per combo
  point), reagents and flags (`passive`, `ranged`, `next_swing`, `channeled`, `combo_points`, `harmful`, `not_in_combat`,
  `stealth_only`, `move_interrupts`, `interruptible`, `auto_repeat`, `cooldown_on_fade`, and by effect `damage`, `heal`,
  `aura`, `dispel`, `interrupt`, `resurrect`, `energize`, `taunt`). `make spellnames` writes it from the core's
  `spell_template` with the later `REPLACE INTO` and `UPDATE` statements of `sql/database_updates/world` applied, plus
  `SpellCastTimes.dbc`, `SpellRange.dbc` and `SpellDuration.dbc` from the server's DBC folder (the template only holds
  indices into them). A rage cost and the rage held are both in tenths.

`spells` result (a list, sorted by name and rank; passives only with `passive: true`): `spell_id`, `name`, `rank`,
`highest`, `meta` (false for a spell the table lacks: only id and name are known), `level`, `passive`, `flags[]`,
`cast_ms`, `range_min`/`range_max`, `power` (`mana`, `rage`, `focus`, `energy`, `health`), `cost`, `cost_pct`,
`cooldown_ms`, `category`, `category_cd_ms`, `gcd_ms`, `school`, `dispel`, `mechanic`, `duration_ms`, `effects[]`,
`reagents[]`, `ready`, `remaining_ms`, `why` (`cooldown`, `category`, `gcd` or `server_refused`) and `affordable` (the
flat cost against the power held; absent for a cost in percent of base mana).

`cast_state` result: `cast` (`state` `idle`/`casting`/`channelling`, `spell_id`, `name`, `target`, `cast_ms`, `elapsed_ms`,
`ends_in_ms`), `last_cast` (`spell_id`, `name`, `ago_ms`), `last_failure` (`spell_id`, `name`, `reason` the
`SpellCastResult`, `reason_name` such as `out_of_range`, `no_power`, `interrupted`, `ago_ms`), `failures` (count since
login), `gcd` (`active`, `remaining_ms`, `total_ms`), `combo_points` and `combo_target`. `status` and the player's own
`unit` row carry `combo_points`, `combo_target` and `gcd_ms` too. A unit that plays on another power than mana (a druid in Cat Form: energy,
in Bear Form: rage) shows that power in `power`/`max_power`; its mana pool is in `mana`/`max_mana` (`UNIT_FIELD_POWER1`, left out when
the unit plays on mana or has none). `base_mana` and `base_health` are `UNIT_FIELD_BASE_MANA` and `UNIT_FIELD_BASE_HEALTH` (left out when
0): the core takes a spell's percent cost of these, not of the maximum pool (`Spell::CalculatePowerCost`), and the brain does the same, so
Summon Voidwalker (100%) is affordable at a few hundred mana whatever the maximum is.

| Event | `source` / data |
| --- | --- |
| `spell_removed` | the player; `spell_id`. SMSG_REMOVED_SPELL |
| `spell_superseded` | the player; `spell_id` (the old rank), `by` (the new one). SMSG_SUPERCEDED_SPELL |
| `cooldown_start` | the player; `spell_id`, `ms`, `source` (`cast`, `server`, `event`), and `category`, `category_ms` when the spell has one. Not sent for the GCD or for the cooldowns at login |
| `cooldown_end` | the player; `spell_id`. The spell's own cooldown ran out (inferred from the clock, on a 100 ms tick; a category lock and the GCD end without an event) |
| `cooldown_clear` | the player; `spell_id`, or `all` and `spells` for SMSG_COOLDOWN_CHEAT |
| `channel_start` | the player; `spell_id`, `ms` |
| `channel_end` | the player; `spell_id`. MSG_CHANNEL_UPDATE with nothing left |
| `combo_points` | the player; `target` (the unit), `points`, `was` |
| `spell_fail` | now also `reason_name` (`reason` stays the number, or `interrupted`) |

## Run modes

One option picks who plays the dungeon (`dungeon_run {"mode": ...}`, `tools/run-uldaman.sh --mode ...` or `MODE=`; design: TortoiseBots-audit
DESIGN-DUNGEON-MODES, phase P2). The plan, `dungeon_run.prepare`, the report, the trace and `trace-score` are the same in every mode.

| Mode | Who does what | State |
| :--- | :--- | :--- |
| `commander` (older name `remote`) | The agent stays outside the group and leads bots through the TortoiseBots remote commander ([Remote leader](#remote-leader)). The bots fight; the agent's own class does not matter. | as before; `lead_bot` or `prepare.party "hire"` required |
| `leader` (older name `group`) | The agent's **character is in the party**, leads it by its plan (walks, pulls, rests, loots, as in [Dungeon runs](#dungeon-runs)) and **plays**: the [bot brain](#bot-brain) is on during the fights. Without `brain` the run takes the preset of the character's class, `role` `dps` unless it says `tank` or `healer`: warrior `arms_warrior`/`tank_warrior`, paladin `ret_paladin`/`holy_paladin`, hunter `marksman_hunter`, rogue `combat_rogue`, priest `shadow_priest`/`holy_priest`, shaman `enhancement_shaman`/`resto_shaman`, mage `frost_mage`, warlock `affliction_warlock`, druid `feral_cat_druid`/`bear_tank_druid`/`resto_druid`. `rotation` swaps the class rotation for a [rotation file](#rotations-as-data); the plan's [boss tactics](#boss-tactics) apply to the character. | the party is the one the character is in (no hiring or binding yet) |
| `observe` | A TortoiseBots run leader (`.bot run`) clears the dungeon, the agent prepares the party, starts it and **watches** ([Observe mode](#observe-mode)). | prepare, bind and enter as in commander mode, then `.bot run start <plan>` and no order after it. Needs a server with the run leader (TortoiseBots PRs #962 and #963); not checked live |

An omitted `mode` is the older group mode (the brain is optional there); `mode: "leader"` always plays. The options of one mode are refused in the
other (`lead_bot`, `bots`, `prepare`, `pulltest`, `tune`... go with commander; `brain`, `rotation`, `role`, `teleport`, `buffs`, `gm` go with leader).
The result has `run_mode` (`leader` or `commander`) next to the older `mode` (`group` or `remote`). `tools/run-uldaman.sh --mode leader
[--brain PRESET] [--rotation NAME] [--role ROLE] [--from-leg N]` builds the leader run's params; its commander-only options are refused with it.
Not checked live: a leader run in a party with bots (`docs/LIVE-CHECKS.md`, "Run modes, rotations and boss tactics").

## Observe mode

`dungeon_run {"mode": "observe"}` lets a **TortoiseBots run leader** clear the dungeon while the agent watches and scores it (TortoiseBots
`.bot run`, its `docs/guides/dungeon-run-leader.md`; PRs #962 and #963). It is the third [run mode](#run-modes): the plan, the report, the trace
and `trace-score` are the same, but the walking, pulling, boss kills and object steps are done by the bots on the server, from a copy of the plan in
its database ([Exporting plans](#exporting-plans-for-the-tortoisebots-run-leader)).

The server needs `AiPlayerbot.DungeonSelfClear = 1` and `AiPlayerbot.RemoteCommander = 1`, the plan's rows in `ai_dungeon_plan`/`_leg`/`_step`
(a restart after the migration) and a bound party inside the plan's map. Nothing of this has run against a real server yet (`docs/LIVE-CHECKS.md`).

**Flow**

1. **Probe.** Before anything is prepared the agent sends `.bot run list` (it needs no binding). A server that does not answer is refused with
   "observe mode needs the TortoiseBots run leader ..."; a server that does not list the plan is refused with the list it has; `disabled` names the
   two switches. No bot has been hired or bound at that point.
2. **Prepare, bind, enter.** Exactly the commander mode's: `prepare` (hire or named bots), `commander bind`, `view on`, the enter phase, the kit
   check. `lead_bot`/`bots` or `prepare.party "hire"` are required; `preflight`, `enter`, `on_end`, `keep_on_fail`, `team` and `ignore_entry_limit` work as there.
   **`.bot maintenance` is off in observe mode** unless `observe.maintenance` is true — in the kit check and in the hire check's top-up of
   a reused hire or one short of drinks or ammo: maintenance walks each bot to a vendor, bank or mailbox, and a party scattered over the maps
   right after the hires' autogear pauses the run leader ("the lead bot is not on the plan's map", #253). The inventory and talent reads still
   run; a hired party already gets its autogear in `prepare`, and a hire still short of supplies is reported (and fails the step).
   The start also undoes a boss `loot off` a crashed run left in place (`.bot behavior <bot> loot on` for every bot) unless
   `observe.keep_loot_setting` is true: then the bots' corpse looting stays as the operator set it, as a dungeon whose legs need looting off
   does. A driven run always sends the undo.
3. **Start.** `.bot run start <plan> [from ...]` (the plan's file name in lower case, or `observe.plan`). A run of the same plan that is already going (`busy`) is
   adopted and watched; another plan's, or a finished one, is refused. **Partway** (`from_leg`, `from_pull_group`, `from_step`, #248): the run leader begins at
   that point of its own plan instead of step 1, for a party already standing at that point in the instance (the run leader checks
   the map and that the party is together; the steps before are neither done nor checked). `from_leg` maps to the leg's number in the exported rows
   (`from <n>`; an optional leg has none, and plan leg names with an apostrophe make the number necessary), `from_pull_group` to the step of the group's
   first row (`from step <n>`) when the step count the server lists (`.bot run list`, at the probe) matches the rows the agent rebuilds with the exporter's
   option sets (plain; cut hops; approach paths; avoid-box routes). When no set matches — the deployed rows were exported with other options, which shift the
   step numbers — the run is refused at the probe, before a bot is hired, and `from_step` carries the number `.bot run status` shows (from 1) instead.
   **Checked before and after the start, fail closed.** A leg whose name is one word the command grammar takes and is unique in the plan is sent as
   `from leg <name>` (else its number). The start must be **proven**, not trusted: first by the run leader's own start line (`The run starts at step X of
   Y (leg NAME)`) — the reported step must be the requested one and the leg the expected one (the leg asked for, or the requested step's leg from the plan
   rows); only a start the module says it moved back to open a scripted event may differ, and it is noted. Any other step or leg means the deployed start
   is not the plan's: the run is stopped and the watch refused. If no start line comes (an ACK-only reply, changed text, a dropped line), `run status`
   right after the start must report the requested step (and its leg);
   if neither source proves it, the newly started run is stopped and the watch refused — no unverified run is watched, and the failed run's unbind leaves
   no hire bound. The party's place is checked before `run start`: the lead bot must stand within 80 yd of the start step's point (the step's coordinates,
   or the leg's first point), and a start whose point or whose lead position is not known is refused too, since the place cannot be checked;
   `observe.allow_far_start` turns each of these refusals into a note. Adopting a run that is already going (`busy`) happens only when its status proves it
   is at the requested step (or in the leg, for a leg sent by name); a leg sent as a number is never adopted.
4. **Watch.** From here the agent sends **no order**: any `.bot cmd` order but `state`, and any `.bot action`, pauses the run leader. It reads the
   `TBM:RUN|<plan>|<state>|<step>/<steps>|<leg>|<kind>|<phase>|<note>` line the module pushes on every change (through the view feed), asks
   `.bot run status` every `poll_s` (15 s) as a safety net, and ignores a line of another plan. Per change the report, the trace (`run_leader`
   event), the dungeon events (`dungeon_leg`, `dungeon_step`, mode `observe`) and the card take in state, step, leg, kind, phase and note.
5. **End.** `done` at the last step ends the run `done` (the agent then sends `cmd stay`, as in any run, and unbinds). `done` before the last step, or with
   a note "stopped...", ends it `failed` with the module's note. When the watch ends any other way (a failure, a ceiling, `dungeon_stop`) a run that is
   still alive on the server gets `.bot run stop` first.

**Bounded waits** (a zero or omitted `observe` field takes the default; past a ceiling the run is stopped and fails with the reason):

| Field | Default | Ends the watch when |
| :--- | :--- | :--- |
| `max_run_s` | 3 h | the whole run, from `run start`, takes longer (a longer dungeon sets it: Uldaman needs more than 3 h). The stop it sends is `.bot run stop time limit reached (<N>h)`, so the server logs the reason (TortoiseBots #1070) instead of "stopped by .bot run stop"; the other endings send the plain `.bot run stop` |
| `stall_s` | 10 min | a `running` run shows the very same status line this long (the module's longest own ceiling is a boss at 7 min) |
| `paused_wait_s` | 10 min | a `paused` run is not resumed this long. The run leader pauses itself at 3 dead, an unsupported step (`ritual`) or a ceiling and never goes on alone |
| none | 20 s | no `TBM:RUN` line came after the start, or the module says "no run" |
| none | | the binding ended: the view feed's end-of-binding line fails the run as in commander mode |
| `maintenance` | false | not a ceiling: sends `.bot maintenance` to every bot at the kit check (off: it walks them to vendors and can scatter the party) |
| `allow_far_start` | false | not a ceiling: lets a partway start go on although the lead bot stands more than 80 yd from the start step's point, or the point or the lead's position is not known (a note says so; off: the run is refused before `run start`) |
| `keep_loot_setting` | false | not a ceiling: leaves the bots' corpse looting as the operator set it (the start sends no `.bot behavior <bot> loot on`, the undo of a boss `loot off` a crashed run left); a driven run always sends the undo |

**Controls.** `dungeon_stop` stops the run (`.bot run stop`, then the unbind). In an observed run `dungeon_pause`, `dungeon_resume` and `dungeon_skip` send the run leader's own
`.bot run pause|resume|skip` on the watch's next look; the answer is in `report.remote.observe.controls`. The `commander` method takes `{"op":"run","verb":"status"}`
for a hand-sent run command. A pause the module makes for a player's order (`PauseForPlayer`: the observer's own `cmd summon`, a `move`, ...) is listed in
`report.remote.observe.player_orders`, one entry per pause (later orders during the same pause are not listed separately), with the step and the order (`step 2/5 (first): cmd summon Healy`); `controls` also records
`player order: ...` and `resumed after the player order at step N` when the run goes on again, also when a person resumed on the server (a run that stops instead of going on ends the pause with no resume entry). The pause itself is the module's: after a `cmd`
order the run waits for `dungeon_resume` / `.bot run resume`.

**Report.** `report.remote.mode` is `observe`; `report.remote.observe` has `plan`, the newest `state`, `step`, `steps`, `leg`, `kind`, `phase`, `note` and a
plain-words `text`, `started`, `lines`, `polls`, `pauses`, `controls`, `player_orders`, `ended` and `changes[]` (the newest 300 status changes with their time). `report.wipes`
counts the watched party's wipes: one rising edge of "every bot dead" in the feed is one wipe, the latch re-arms only once fewer than
3 members are dead (a single member revived in place for a poll, dead again the next, does not count a second wipe). A wipe also counts
when every member has died within 90 s of each other, each death newer than the last counted wipe, even if the lead bot revived in
between (no frame shows them all dead; the RFC run: five dead within 63 s): that `wipe` event has `"window": true`. Each counted wipe
also goes to the trace as a `wipe` event with `"observe": true`, so the tracecheck deaths oracle sees it too (the run leader stops and
recovers on its own; the watch only counts). In an observe run `report.leader_deaths`, `report.resurrections` and `report.seconds_dead`
(in all and per leg) count the lead bot's deaths and revives from the feed, each death also a `leader_death` trace event; a revive here
is any return to alive, the run leader's own revive included, not only an accepted resurrection. `report.legs[]` has one
entry per leg the run leader entered, with its seconds. `report.current` is "run leader: sentinel · pull 7/41 · pulling". `report.instance` is the lead bot's numeric instance id, read from the feed
(absent until a frame shows one); the trace has one `instance` event (`instance`, `map`) and the summary's `instance` is filled from it when
the header has none. The orders counters stay at zero but for the end's
`stay`. Not in observe mode: `tune`, `live_reload`, `cc`, `carrier`, `loot_restore`, `object_fallback`, `tank`, `healer`, `recover_wipe` and the start options that
move or equip the party (`start_here`, `have_items`, `pulltest`, `give_items`) are refused, since the agent steers nothing; `from_leg`, `from_pull_group` and
`from_step` only say at which step of the run leader's plan it starts.

**Dashboard.** An observed run's card (`turtle-agent dashboard`) shows the run leader's state in plain text, in the "Now" line and as `run.leader` in
the card JSON: `Run leader: Sentinel · pull 7/41 · pulling`, `Run leader paused: Sentinel · boss 12/41 · 3 dead: regroup ...`, `Run leader: finished all 41 steps`.

`tools/run-uldaman.sh --mode observe [--hire] [--no-fresh] [--keep-loot-setting]` builds the params of an observed Uldaman run.

## Remote leader

Issue #83. `dungeon_run {"name":"uldaman","mode":"remote","lead_bot":"Tanky"}` leads a bot group **without being in it**, through the
TortoiseBots remote commander (TortoiseBots #884: `.bot commander`, `.bot cmd`, the `TBM:VIEW` perception feed;
[player-controls.md section 1a](https://github.com/Sagiroth/TortoiseBots/blob/dev/docs/guides/player-controls.md)). The agent can stand in
another zone or outside the instance; it needs no fighting brain, and the bots do the fighting. The module needs the build with the
remote commander and `AiPlayerbot.RemoteCommander = 1` (off by default: the first command answers `disabled`).

**Perception: `TBM:VIEW`.** While bound with the view on, the module sends a frame about once a second as system lines
`TBM:VIEW|v1|<seq>|B|<lead>,<party>,<hostiles>,<threats>[,<truncated>]`, then `P` (party records: guid, name, health, power, position, map,
target, spell, dead, state, last action and, with TB #924, that action's result letter and failure reason: `PartyRec.Result`/`Reason`, e.g. `drink` `F` `no-item`), `H` (hostiles the bots fight or see: guid, entry, position, health %, target, flags `s` stealthed,
`e` evading, `c` crowd-controlled; then, from TortoiseBots #943, reaction `h`/`n`/`f`/`-` and kind (any of `k` critter, `v` civilian, `g` guard, `u` not attackable, or `-`), in `bot_view` as `reaction`/`kind`, absent from an older module), `T` (threat: per hostile the top three `guid:pct`) and `E|<lines>`. `internal/tbm` assembles them by
`seq`: a frame is published when its `B` and `E` are in, the line total matches `E` and the record counts match `B`; frames that lost lines are
dropped when a newer one completes, a late frame behind the newest is dropped as stale, a `seq` that goes back by more than 3 is a new binding,
a line of another version (`v2`) is counted and skipped, a malformed line is counted and skipped. The counters (`lines`, `frames`, `missed`,
`stale`, `invalid`, `incomplete`, `orphans`, `unsupported`, `malformed`, `truncated`, `restarts`) are in `bot_view.stats` and the run report.
A `truncated` frame (the module dropped records to fit; the optional fifth field of the B line, any further fields are ignored) is flagged in the frame and counted; the driver never counts a mob gone from one. The feed follows the event log, so it needs no
registration: it reads every `TBM:VIEW` system line (or addon line) as it arrives. `bot_view` returns the newest frame (with `after_n` and
`wait_ms` it waits for a newer one). The events log gets a throttled `bot_view_frame` (the first frame, then every 5 s, or sooner when a bot
dies or the party enters or leaves combat, never faster than once a second): `data.seq`, `lead`, `party[]` (name, hp_pct, power_pct, dead,
state), `hostiles[]` (guid, entry, hp_pct, flags, threat_holder), `engaged`, `in_combat`. The `TBM:VIEW` lines go to the feed only and are **not** written to the event log (about five lines a second while bound would push chat,
dungeon and brain events out of its ring): the log's intercept (`events.Log.SetIntercept`) hands them to the assembler. The unbind ACKs the
module sends stay in the log. For debugging, `bot_view {"log_every": n}` also logs the lines of every nth frame (0 turns it off); `bot_view`
reports `view_lines`, the lines read.

**Orders.** `commander {"op":"bind","lead_bot":"Tanky","bots":["Healy"]}` (also `unbind`, `status`, `view` with `on`) and `commander {"op":"cmd",
"verb":"pullback","args":["0xF130000C5E0012AB"]}` send `.bot commander ...` / `.bot cmd ...` as say chat and wait for the `TBM:ACTION_ACK` or
`TBM:ACTION_ERR` with intent `commander <verb>` / `cmd <verb>`; a reply that does not come in `wait_ms` is `kind: "timeout"`. Only these verbs are
sent, and an argument may hold letters, digits and `_ . , : + -` (not starting with a dot): nothing can ride along on the line.

**The run.** `bind` (the lead bot and `bots`; none: the lead bot's party as it is; a bind refused as `already-bound` is reused when `commander status` shows the same lead bot (and the same number of bots, which the first frame's names then confirm), else the run is refused with a message naming the other group and leaves its binding alone, with the feed this run switched on put back off), `commander view on` (the feed is off at bind in newer
builds), a wait of up to 20 s for the first frame, then the plan with the same stepper, plan format, pull groups, stealth packs and rest gates
as a group run, but everything comes from the feed and goes out as an order:

| Plan action | Remote mode |
| --- | --- |
| enter (before leg 0) | only when a bot of the first frame is not on the plan's map; see **Entering the dungeon** below |
| walk | `.bot cmd move x y z` on the lead bot, in legs of at most 120 yd (the server takes 150; legs follow the navmesh path when the agent has one, else the straight line), then a wait for the lead bot's position in the feed to come within 5 yd of the last point. **Look-ahead:** while the lead bot runs towards a waypoint that is not the walk's end, the next `move` goes out as soon as it is within 10 yd of it, or about 2 s from it at its measured speed (6.6 yd/s until seen), at most one `move` a second, so it does not stop at every waypoint (`look_ahead_moves`; `walk_stops` and `walk_stopped_s` count the times it stood still in the middle of a walk and for how long). The final point and a pull still wait for arrival. A lead bot that gained no ground in 10 s gets the move again; a refused move (`too-far`, `off-mesh`, `unreachable`) is retried with a shorter leg; `lead-unavailable` is waited out for up to 30 s |
| pull group / boss | the nearest hostile of the group's entries **as the bots see it** (not the agent's view), `.bot cmd pullback <guid>`; out of range, the lead bot is moved up to 38 yd first; `too-far` moves it closer and retries; `evading` skips the mob; **`unreachable`** (the tank found no firing position) is not a way to clear the group: the party is walked to 20 yd of the mob, then 8 yd and the pull is sent again from each; still unreachable, the step fails with the mob's position (`... is unreachable for the tank ...; the group is not cleared`), so a pack with a living mob is never counted cleared. Every `unreachable` answer, to a pull or to a `cmd move`, is a note (`unreachable pull|move to x y z (...)`), a trace event `unreachable` and counts in `remote.unreachable`, so the plan's positions can be corrected; **`tank-busy`** (the only tank is in combat, it cannot start a pull while it fights) waits up to 10 s for the tank to leave its fight (or for the mob to come to the party by itself) and sends the pull again, up to 3 times, never an attack order in its place (`tank_busy_waits`; still busy after that, the pull counts as one that drew no fight); `no-tank` (no tank-capable bot in the party at all) falls back to `.bot cmd attack`; **a group the bots do not see is not "already dead"**: it counts as cleared only with evidence for each of its positions (its mobs died there in a won pull of this run, listed dead, a living bot within 25 yd of the position and a whole frame that lists none, a pull of a mob that stood there failed and is named in the notes, on a First add's step the boss seen standing there with no add in view, or the lead bot walked to the position, once per position, and two whole frames in a row showed none; `pack_looks`, `packs_seen_empty`), else the lead bot walks up to 21 yd short of the position (within 3 yd of that spot: out of the pack's aggro range, still within the 25 yd), and the group is pulled when it shows. "Of the group" is always the entries the step picks its target from: the boss standing at its position is not its First add. A won pull does not end the group (the fight tracks only the pulled mob and what joins it): the driver picks again, so a group's other positions are pulled or looked at as well. Only a won pull's dead mobs mark their spots; an evaded pull marks none. Giving up (mobs that drew no fight or were refused, or the pulls used up) still needs every position seen, else the step fails (`... were never seen: not counting it as cleared`). A boss counts as killed only when it died in this run (in its own pull or a First add's it joined, or in any other fight of the run: **the kill ledger** follows every hostile the feed shows fighting the party, whichever pull or walk fight it came to, and counts one as dead when it is listed at 0 health, or is absent from two whole frames after being seen fighting at 20% health or less, not walking home, with a living bot within 25 yd of where it was last seen; a wipe clears the counts; a boss seen alive again is alive again, one that left view healthier, unfought, evading or out of every bot's sight is not dead) or is listed dead, or its corpse (health 0 or the dead flag, the boss's entry, within 80 yd of the step's boss position; a step without positions takes none) is in the **agent's own object store** (a boss killed before this driver ran, as on a resume; the agent is brought near the party first, see **Keeping the agent near**), never because its adds died. A pull's fight tracks every hostile that joins it, not only the step's own entries, so a boss that comes into a pack's fight and dies there is that fight's kill (run 1: Revelosh died inside a 4852 pull that ended `evaded`). The ledger proves the boss dead only; the group positions are still cleared only by a won pull's dead mobs (`killedAt`). A position the lead bot cannot get within 25 yd of fails the step instead of passing it. The resume rule believes an empty view on the same 25 yd terms and wants every position of a group seen |
| boss not at his spot | (issue #190: Theka and Velratha patrol, and the step failed at once with "no living [7795] seen") when the kill-boss step finds no living unit of the boss's entry in range after the look walk, it waits up to `boss_search_s` (60 s); the first living unit of the entry the feed shows is then approached and pulled, that exact unit, when it stands **within 120 yd of his plan spot** and the navmesh path from the lead bot to it is **complete** (ends within 8 yd of him; with no planner the straight line) and **passes no uncleared pull group** (a group of a later leg, or a group of this leg that the feed still shows a living member of, within the group's radius of a position: a pull on the way would drag the party through uncleared packs); else the wait goes on (noted once: `but not taken: ...`) and the step fails with `boss <entry> not found`. The party walks to him **hop by hop along that path and checks it again before each hop** (a changed path, a pack in the way or a boss gone stops the walk and fails the step), then the pull follows with its own checks. **No search** with `boss_search_s` 0, when the boss is already known dead (this run's kill, the ledger or his corpse in view: checked before the search and at every look of it), and when the run began inside this boss's leg from a later start (`from_leg` above 0, `from_pull_group`, `start_here`, or a `resume` that skipped the walk: he may have been killed before this run, so his absence is expected and the step fails at once as it always did); the wait is counted in `boss_searches` and noted. He is **never counted dead because he is absent**: only a kill of this run, the kill ledger (seen fighting, then dead or gone) or his corpse in view says so. After the wait the step fails with `boss <entry> not found: no living [<entry>] seen by the bots (...)`. Once per step |
| team gate | `boss.team` and a group's `team` work as in a group run: the party's team is `team`, else the agent's own (the feed carries no race, and the owned bots share the account's faction); the skipped steps are in the notes (`skip_team` events) and `remote.team` says which team ran. The gate is **data-driven**: a part gated to the other team is skipped only if none of its mobs is hostile (listed alive now, or a unit seen alive earlier in the run, by GUID, and not killed since) **and** the party has looked: the feed lists only what the bots see, so the part is skipped on the evidence a pack needs to count as cleared (a living bot within 25 yd of each of its positions and a whole frame that lists none of its mobs, else the lead bot walks up to look, once per position; `... : skipped (looked: ...)`), and a position that cannot be looked at fails the step rather than skipping blind; if the mobs are hostile it is fought like a pull group (a note, `team_override` event: `... its mobs are hostile to the party: fought`). Uldaman's Baelog, Eric and Olaf are `team: horde` in the plan (the world database has them friendly to the Alliance), but were hostile to an Alliance party in remote run 2 (the pull of the last pack aggroed them), so an Alliance run skips them only while they are not in the feed. Group mode keeps the plan's gate as written. With no team known nothing is gated (a note says so) |
| kit check (`preflight`) | before the first step: `.bot maintenance <bot>` for each bot in the first frame (not in an observed run unless `observe.maintenance`, #253) (plain `.bot` lines; the module refreshes consumables, reagents, ammo and repairs when `AiPlayerbot.OwnedBotMaintenanceEnabled = 1`; `disabled` or a refusal is recorded and the run goes on), then `.bot inv <bot>`: drinks for every mana user (10; 20 for the `healer`), arrows or bullets for the shot of a ranged weapon in the ranged slot (100; a gun with arrows is named), and a shield in the off hand of the `tank` (only when named). Entries come from the core's item table (`tools/itemkinds`, `internal/dungeon/itemkinds_gen.go`); a drink or ammo the table lacks reads as missing. Before the bags it reads the **spent talent points** of every bot with `.bot command <bot> talents`, **all bots at once** (the lines go out together and the answers, each in its own bot's words, as a whisper or party chat, "My current talent spec is: ... (a/b/c)", are read in one wait of 5 s: it was 6 s per bot one after another, 31 s at every start of the 2026-10-15 ZF runs, whose second asking got no answer from the server at all; the one `.bot command` line the agent sends, and the plain-line check refuses any other word after `talents`): a bot with none spent (a respec that was reset and never applied again, a 0/0/0 tank) or no answer is a warning. Whether the server answers a second asking is not settled: with no answer the check costs 5 s, not 31. The role is the `tank`/`healer` the run names (the module has no read for it that the agent may send). It also warns when the tank's max health is below the plan's `tank_min_hp`, and when a bot's race is of the other team than the party's (read from the object store; bots out of view are skipped). Result in `remote.preflight` (`bots[]` with `maintenance`, `inventory`, `talents` (`"0/5/31"`), `drinks`, `ammo`, `shield`; `warnings[]`; `ok`). `warn` goes on; `strict` fails the run before any order; `off` sends nothing |
| end of run | **while the party is in combat the binding is kept** and the fight logic (tank first, attack orders) keeps running, on any end (done, failed, stopped) and with its own context, for at most 90 s until the party is out of combat (`remote.end_fight`: `out of combat after N s` or `still in combat after 90 s; gave up`); unbound bots would die in the pack that aggroed them (run 2). Then `cmd stay` before the unbind (`remote.end_stay`), because the restored followers would run after the agent, however far away it is; `on_end: "logout"` then sends `.bot logout <bot>` for each bot after the unbind (`remote.logged_out`); `"none"` is the unbind only |
| fight | the feed's hostiles, targets, health and threat; `.bot cmd attack <guid>` at the engaged mob the tank holds best every 10 s (**Tank first**, below). A mob is dead when its health reads 0, or when it was seen fighting (engaged or hurt) and is then absent from **two frames in a row**; an absence in a `truncated` frame counts for nothing. A pull whose mobs all leave the feed without ever being seen fighting is `drew no fight`, not a win. `evaded` when they walk home |
| cc | `cc` option: `[{"entry":4863,"mark":"moon","bot":"Magey"}]` sends `.bot cmd cc <mark> <guid> [bot]` for mobs of that entry around the pull target, before the pull; such a mob is not picked as the pull target while another is there |
| big packs | (owner decision 2026-10-14) a pull whose pack is **`big_pack` (5) or more** living mobs the bots see within the group's radius of the pull target (`big_packs`) gets one member crowd-controlled before the pull: `.bot cmd cc moon <guid> <cc bot>`, the cc bot being the `cc_bot` option, else the party's mage: a hire sets it, and a party named by `lead_bot`/`bots` (a resume, a kept binding) gets its first mage from the class the agent learned of the hires in any earlier run of this process (the report's `ready` block; kept whatever became of the binding), else from what the run that kept the hires read, else from `.bot roster` at the start of the run (it lists none of the bound hires: ZF-2 run 4 resumed seven times with no Polymorph), else from the spells the bots cast (the party record's last action is a mage spell), tried again at each big pack (note `cc bot: none given; <name>, the mage of the party, ...`); no mage, no CC: the pack is pulled as it is with a note (ZF-2 run 3i: a resume without `cc_bot` pulled nine mobs with no Polymorph). A pack with a member of the plan's `cc_always` entries gets the CC whatever its size, on that member (a Zul'Farrak Shadowhunter). The mark goes on a member of the plan's `cc_priority` entries first, else on the member farthest from the pull target, never on the pull target itself and not when a `cc` rule already marked one; an accepted mark is counted in `big_pack_cc` and the party leaves that mob alone until it is the last of the fight (as for a `cc` rule). A pack is linked, so it is never split; the CC is the cap. The pack is counted the same way for every pull: the target and the living hostiles within the group radius (the walk's pulls on the way use `pack_radius`) and `ahead_max_dz` of it, not fighting yet; a pull on the way that counts as big is gathered for, CCed and pulled back like a planned one (`ahead_big_pulls`, with a note). A CC counts as holding only while the feed shows the mob crowd-controlled or for a few seconds after its order: a pack pulled again after its first mob died (Uldaman hires-3 group 4) gets a new CC if the first ran out. Not verified live yet |
| patrols and reactive CC | (Zul'Farrak hires-3, 2026-10-14: every wipe on the road at x 1650-1690 came from mobs that joined a pull of three 10-25 s after it) **Reactive CC**: during a fight (a pull or a fight on the way) a mob that joins it gets the cc bot's Polymorph at once when it is a `cc_always` entry, or when the fight has `big_pack` mobs and none is crowd-controlled; it goes on the `cc_always` member first, then by `cc_priority`, the member farthest from the tank among equals; never on the tank's target, the mob the party is sent at, a mob below 30 % or one already crowd-controlled; one order per 4 s, at most 3 per mob (`reactive_cc`). The cc bot holds one CC: a CC that holds stays unless it is on a mob that is not `cc_always` and a `cc_always` one is free (the Polymorph moves). A CC that lapses while the fight goes on is put on again (`cc_renews`). **Patrols**: before a pull of a group with a `patrol`, the members of the patrol the bots see on its path (15 yd) are looked at (a unit of its entries seen standing within 3 yd of one spot for 6 s is a mob that stands on the path, not a member): one that stands, or by its path and pace (`period_s`) will stand, within `patrol_aggro` (25 yd) of the pull target or the lead bot within `patrol_window_s` (40 s) is a danger. One that passes within `patrol_pull_yd` (40 yd) of the lead bot with no member of the pack within `patrol_aggro` of it is pulled alone first, as its own pull with the pack CC (a member that is not `cc_always` is the target; at most 2 per group, `patrol_pulls`); otherwise the party waits for it to walk away (`patrol_waits`, `patrol_wait_s`), at most `patrol_wait_s` (90 s) per group over all its pulls (rule 3), then pulls with the patrol counted in the pack (`patrol_timeouts`, noted). **No member in view is not a patrol that is far away** (ZF-2 run 3i: the Shadowhunters were out of view when the pull of 1642 902 began, joined it 30 s later and wiped the party): when none of the patrol's entries is in view (a unit standing still on its path does not count) and none was seen walking in the last 45 s, the party watches for it for up to `patrol_locate_s` (120 s, and never longer than the patrol's `period_s` + 10 s) before the pull (`patrol_locates`, `patrol_locate_s`, noted); once a member walks into view the rules above apply, and a look that sees nothing is noted and the pull goes. A patrol member within `patrol_aggro` of a pull target counts in its pack for the big-pack CC, planned pulls and pulls on the way alike. A patrol that came to the party during the wait is fought as walk aggro. Not verified live yet |
| tiny moves | the module carries out no `cmd move` shorter than about 6 yd, measured on the ground plane (a hop of 6.3 yd on a slope but 5.3 yd flat was acked three times and never walked in rfc-live-2; ZF-1 run 4: a last stretch of 5.2 yd was ignored five times and failed the walk). A walk's stretch shorter than `min_move` (6) is sent as a move to `min_move`+1 yd along the same line; the lead bot walks through the point and the arrival counts within the overshoot (`tiny_moves`). Direct steps are sent as they are. Not verified live yet |
| rest gate | party health and mana, and (before a pull or an ambush walk, not at a leg's start) everybody near the lead bot, from the feed, against the same targets and cap as a group gate (90% health, 50% healer mana, 35% other mana, 60 s; a leg's `rest` overrides them); `.bot cmd rest` (again every 45 s); the feed has no class, so the bot named by `healer` gets the healer mana target and, without one, every caster does. The commander has no order for a single bot: a member whose mana has not risen at all in 15 s gets one more `.bot cmd rest` (recorded in `rest_gates[]` as a drink order). A dead bot is first left to a living caster: while one is alive out of combat (the `healer`, else any bot with mana) the gate waits up to 30 s of calm for the resurrection (fights meanwhile are helped and do not count; `rez_waits`, `rezzed`, `rezzes`, a trace event `rez` per dead bot with `raised`). The module's own AI raises a dead party member out of combat (priest Resurrection, paladin Redemption, shaman Ancestral Spirit) and the commander has no cast order, so the driver only leaves it the time; a caster counts only with a body within `rez_reach_yd` (30 yd) of it, a body farther off is released at once, because `cmd revive` releases the spirit and a released body cannot be raised in place; then, or at once when no caster is left, `.bot cmd revive <name>` (owner or GM; refused: the run fails) |
| wipe / death | from the feed: every bot dead is a wipe, and so are **three bots dead** inside the dungeon (`wipe: party down: 3 of 5 bots are dead`, counted in `party_downs` and in `wipes`; ZF-1 walked the lone tank back into the pack after four deaths): the leg stops there, the survivors are not sent on; with `recover_wipe` a revive order for each dead bot and the step is tried again with the whole party. The three-dead stop is said once until fewer than three are dead, so the revive and the end's fight are not stopped again |
| party whole | (ZF-2 run 3b: the module dismissed two hires from the group, `dismissed (removed from group)`, and the run walked on with three until the three-dead stop) the run starts with `1 + bots` bots (the feed's party when it names none) and keeps that number: when the feed lists fewer for 5 s the run stops at the next calm moment (a fight is fought out first, noted once) with `party short: the party has 2 of 5 bots, <names> left the group`; and a walk or a pull never starts out of combat with a bot of the party dead (`party short: ... bots are dead (<names>)`; the rest gate before a pull revives the dead first). A failed run keeps its binding with `keep_on_fail`: bring the bot back (`.bot hire`/invite) and resume |
| careful walk | inside the dungeon (a leg begun) a walk moves in **hops of at most 25 yd** (the walk to the portal does not). Before each hop, and before each pull, the party **gathers**: every living bot within 12 yd of the lead bot, a wait of at most 12 s that sends nothing (`gathers`, `gather_wait_s`; one that runs out is noted with who was still away and counted in `gather_timeouts`; a bot that came no closer during the whole wait is not waited for again in that leg, by the gathers or by the rest gate, until it is back within 12 yd by itself, `gather_gave_up`). Before each hop a living hostile the bots see within **22 yd of the hop** (the line from the lead bot to the hop's end, and no more than 8 yd above or below it) that is not fighting yet is **pulled before the hop** (`cmd pullback`, the fight to the end, `ahead_pulls`; at most 12 per walk) unless a step of the plan owns it: a boss or First add of any leg (by entry), an entry any leg skips (Uldaman's dormant Stone Keepers and statues), or one with an entry of a pull group of the current or an earlier leg standing within that group's radius of its positions (those are pulled in the plan's order, each from a gathered party; a team-gated one, of any leg, may not be pulled at all). A pack of a **later** leg standing in the way is pulled like one the plan does not list: ZF hires-2 run 6 walked leg 0 through Velratha's packs of leg 2, stopped 15 yd from ten mobs and wiped. The same goes for a hostile within **30 yd of the hop's end** (`gather_clear`), where the party stops and gathers, even when it stands more than 22 yd off the line. One that draws no fight, evades or is refused is passed over for the rest of the leg and never counted cleared. Only a hostile is pulled on the way: the hostile record's reaction and kind (TortoiseBots #943, the fields after `flags`: reaction `h`/`n`/`f`/`-`, kind any of `k` critter, `v` civilian, `g` guard, `u` not attackable) leave out neutral and friendly units and every critter, civilian, guard or unattackable unit; a record without them (an older module) counts as hostile. A neutral that attacks the party is fought by the walk's fight branch like any other. So the walk pulls what the plan does not place there, such as a patrol away from its group or a pack the plan does not list. A won fight's dead mobs mark their spots as any won pull does. Not verified live yet |
| `loot_object`, boss `loot_item`, `use_item`, `use_object`, `ritual`, `wait_for` a game object | done by the **carrier** bot (below), on the per-bot player orders of TortoiseBots (#928): `cmd open`, `cmd loot`, `cmd use`, `cmd ritual` (the carrier and the bots it takes along), `cmd lootmethod`, each waited for through its `ACTION_PENDING` to the final ACK or ERR, and `cmd state` for the waits; objects are named by entry and point when the module takes that ([Targets by entry](#targets-by-entry-the-agent-need-not-be-in-view)). A game that cannot give those orders (a fake) still fails the step with `remote mode: object step needs the agent nearby` |

**Object steps: the carrier.** The agent's character is not in the group, so one bot does what a player does at a chest, a corpse, a lever and in the
bags. `carrier` (a run parameter) names it; omitted, it is the first living bot that is not the lead bot, the tank or the healer (a DPS; else any
bot but the lead and the tank). It is not the lead bot by default, because a follower leaves the formation for the walk and goes back to following the
lead bot after it (the lead bot holds its spot meanwhile, so the party waits where it stands). Every order waits for its final line: a refusal is
one `TBM:ACTION_ERR`, an order that walks answers `TBM:ACTION_PENDING|cmd <verb>|bot:<Name>|<budget s>|<guid>` and then the final ACK or ERR, and the
wait is the budget plus 5 s. After the order the carrier is waited for until it is within 25 yd of the lead bot (30 s at most; `cmd follow` is sent
after 10 s if it is not).

| Plan step | Orders (`<c>` is the carrier) | Checks |
| --- | --- | --- |
| `loot_object` (Baelog's chest) | the approach (below), `cmd lootmethod ffa`, `cmd open <chest guid> <c>`, `cmd lootmethod <loot_restore>` (default `group`; the module cannot be asked for the old method) | `expect_item` in the ACK's loot list, else in `.bot inv <c>`; already in the bags before: the step is skipped |
| boss `loot_item` (Revelosh's Shaft) | before the fight, if `companions_loot` is false: `.bot behavior <bot> loot off` for every bot but `<c>` (back on after); after the kill: `cmd lootmethod ffa`, `cmd loot <corpse guid> <c> <itemId>`, `cmd lootmethod <loot_restore>` | the item in the ACK or `.bot inv <c>`; already carried (or its product, the Staff): skipped. The corpse GUID is the one the feed showed die, else a dead creature of the entry within 80 yd of the boss's position in the agent's own unit store; a corpse that is in no view fails the step (`boss N died out of sight ... its loot can't be reached`), and one the server answers `invalid-target` fails it as gone (called stale when the agent's copy is over 300 s old) |
| `use_item` (the Medallion combine, spell 9594) | `cmd use <itemId> <c>` | before: the item, and `requires_item` (the Shaft), in `.bot inv <c>`, else the step fails naming the bot and the item; after: `expect_item` (the Staff) in `.bot inv <c>` (looked at up to 4 times, the cast being over a moment before the bags change) |
| `use_object` (the Keystone, `requires_item` the Staff) | the approach, `cmd open <go guid> <c>` | `requires_item` in `.bot inv <c>` first; the ACK `opened` |
| `ritual` (the altars of Uldaman) | the approach, then **one** `cmd ritual entry:<id>@x,y,z <c> <bot> <bot>`; ordered again (at most 3 orders in all) only after `interrupted`, or after a fight that kept the bots from starting, **and** only when `cmd state` says the altar is not active: a ritual that has fired is never sent again, and `in-combat`/`interrupted` with an active altar ends the step as done (the Keepers woke into a fight) | the final line `TBM:ACTION_ACK\|cmd ritual\|party\|3\|done` is the result (the bots did their part, not the object's script; the altar reads ready again after the release, so no wait on it); `not-enough`, `busy`, `not-found` and the others as for the other orders. Needs entry targets and TortoiseBots with `cmd ritual`; the agent's character cannot take the step over |
| wipe after the kill / dead outside the dungeon / alive outside | **The cap of recovered wipes (`max_wipe_recoveries`, 2) counts per leg**, with a run-wide ceiling of five times that: a wipe in each of three legs is recovered (live: the third, in the Zum'rah leg, hit a run-wide cap and ended `failed: wipe`). **In a boss fight** the attack order is sent again after 3 s (the pack fights' 5-10 s let the boss go to the wrong bot), a summoned `kill_first` unit (the ward) is never held for the tank's threat, the adds a boss leaves are fought out before the leg ends, and a run that starts in a leg whose area trigger was taken already (the unit hostile or dead) goes to the trigger step at once (`resume: the area trigger ... was taken already`). **After a wipe's revives** the run waits up to 120 s for every member to be alive and with the lead bot (summoning the far ones on a GM account) before the next pull; a bot that dies in that wait gets `cmd revive` too (at once with a live hostile within `revive_safe_yd` of the bodies, else after 20 s; rfc-live-2: the rogue lay dead until the wait ran out); a lead alone outside the dungeon is party down by itself. **A rest gate** that waits only for the healer's mana stops after 120 s and the later gates of that leg do not wait for it. **Pull floors** (`rest_pull_tank_pct` 80, `rest_pull_healer_mana_pct` 60, `rest_pull_extra_s` 90; held down to a leg's own lower `rest` targets): a gate that would pass below its targets (its cap, the healer cap, a caster that does not drink) keeps waiting for the tank's health and the healer's mana to reach the floors, for the extra time past the cap, and then goes on as before (`rest_gates[].pull_floor_wait`). **A failed start** (enter, prepare) is not retried by the driver, and every entry counts toward the server's 5 new instances an hour: the operator stops at the first failure. **The healer** that is among the stragglers at a gather's first timeout is summoned (`cmd summon`, a GM assist, `rejoin_tps`) instead of being given up on. The plan check **warns** of a pull group that stands within 45 yd in a line but is more than twice that plus 25 yd away by the path (`detour`: the tank finds no firing position); it never fails a plan, and Zul'Farrak has none.  **A party that is alive on another map** (spirit-healed at a graveyard outside: live, Antu'sul) counts like dead ones: three dead or outside for 8 s is party down (`bots are dead or outside the dungeon`), and the recovery brings the lead in first (see below). **A `cmd enter` run's agent stands outside, at the door** (zf-r5, Velratha wipe: the tank died last and was spirit-healed on map 1, the others followed it out): no `.summon` into the instance is possible then, so the recovery (`remote_outside.go`, GM account) sends `cmd summon <bot>` for every bot that is outside (GM assists: `run_gm_assists`, `rejoin_tps`, `setup_shortcuts`) to the commander at the door, requires the ACK of `cmd follow`, and enters again with the run's entry method (`cmd enter`, the walk or the tele) before the step is retried. Without a GM account the party is waited for outside for `offMapWait` (120 s), then the recovery fails and names the bots. **A GM agent left inside the dungeon** by an earlier run's `.appear` is moved out to the plan's portal with `.go xyz` before the enter phase (a start shortcut; any other account is refused with the reason), and the `cmd enter` phase's 240 s ceiling does not count the time the party spends fighting on the way to the door.  **A wipe in a leg whose boss is dead** (the party died to the adds he left: Zum'rah's zombies and Dead Heroes) is no failure of the boss step: it is recovered like any wipe (also with `recover_wipe` off), the leg goes on, and the report counts it apart as `remote.wipes_after_kill` (not in `wipes` or `party_downs`); the kills of a fight that ended in a wipe are kept, so the boss still counts dead. **A bot dead outside the dungeon** (a ghost at a graveyard on another map: `cmd revive` is acknowledged and does nothing for it) is revived where it stands with `.revive <bot>` on a GM account (a GM assist: `run_gm_assists`, `setup_shortcuts`; up to 3 times, 20 s apart; `dungeon.BotReviver`), and the rejoin (`cmd summon`) takes it into the instance; any other account leaves the ghost to run back through the entrance for the recovery's own time limit, with no failure after 3 orders | the recovery and the report's counters |
| `area_trigger` (Zum'rah, trigger 962) | no carrier order: the lead bot walks to the step's `stand` (`move`, the party follows) and the module takes the trigger for it; 5 s after, the agent's own character (`.appear`, a walk into the zone, `session.AreaTrigger`, a walk back) | `expect` (a unit wait) holding, read from the feed and the agent's store; see the step above |
| `gossip` (Weegli's End Door, Bly's fight; Zul'Farrak) | `by: carrier` or `tank`: the approach, then `cmd gossip <npc entry> <bot> <option>`; `by: agent`: the agent's own `gossip_hello` / `gossip_select` next to the NPC | the ACK (bot) or `SMSG_GOSSIP_COMPLETE` (agent); a refusal or a menu without the option is tried again for `retry_s` (default 15 s), then fails with the menu id or the code; an unknown-verb refusal (or no answer) hands the step to the agent once. What the option did is for the next `wait_for` to check |
| `wait_for` a game object (the Seal) | none; with entry targets `cmd state entry:<id>@x,y,z` every 2 s | with entry targets the module's answer (`state:active`, or the plan's state), whether or not the agent sees the object; without them the state in the agent's object store when the agent sees the object, else the wait is taken as met after 10 s and a note says so |

**The approach.** Before a `cmd open` or `cmd loot` (the chest, the Keystone, a boss's corpse; not `cmd use`) the party is brought to the object, because the
module's open order fails while the carrier is still walking in (remote Uldaman runs 2 and 3: `cmd open` sent with the carrier ~20 yd away, an ERR
`failed (spell check 255)` four seconds later; even standing still, the bots' own Opening cast was interrupted). The lead bot is walked to the object's stand point (the
step's `stand`, else the point 3 yd short of the object on the navmesh path from the lead bot), `cmd stay` goes to the party, and the order goes to the carrier only when
the carrier is within 5 yd of the object and its position is the same in two frames of the feed in a row. A carrier that has not settled 20 s after the stay gets the order
anyway (its own walk is the fallback), with a note; a fight, a stalled feed or a carrier missing from the feed ends the wait the same way. After the step `cmd follow` sends the
party back to the lead bot. What it did is `object_steps[].approach` and the trace event `object_approach`. A step that is skipped (the carrier already holds the item) walks nobody.

**The agent as the fallback** (`object_fallback`, `agent` by default for a remote run; `off` fails the step as before). With the game able to act as the agent (`dungeon.AgentHands`:
the agent implements it), a refusal of the carrier's `cmd open` or `cmd use` (any ERR, including `failed (spell check ...)`; not a timeout, a lost binding or a wipe) is sent once
more; if that fails too, the agent's own character takes the step over, with the group-mode code:
- `loot_object`: the agent is brought within reach of the chest (a GM that the run's `setup` made invisible is `.appear`ed to the carrier; anyone else walks the navmesh), casts its own
  Opening spell (`LootObject`, twice at most) standing still, takes the loot into its own bags, then hands the step's `expect_item` to the carrier with the trade RPC (#112, within 10 yd:
  the agent walks, or `.appear`s, to within 8 yd of the carrier first). `.bot inv <carrier>` must show the item, else the step fails.
- A GM that `setup` lists `gm visible off` for is invisible, and a trade needs both sides to see each other: `.gm visible on` goes out just before the trade and `.gm visible off` right after it
  (also when the trade failed), each one a GM assist during the run (`run_gm_assists`, `setup_shortcuts`, a note; `gm_commands` stays what it was). A fight holds the trade, so the
  visibility, and the opening: the fallback waits up to 60 s for the party to be out of combat and fails the step if it is not.
- `use_item` (the Medallion): if the agent's own bags hold both the item and the one the use consumes, it uses the item itself and trades the product (the Staff) to the carrier.
- `use_object` (the Keystone): if the agent's bags hold the `requires_item` (the Staff, which the agent got by combining) and no later step of the plan uses that item or requires it on a bot,
  the agent opens the object itself; the same when the carrier lacks the Staff and the agent has it. There is no trade.
- A boss's corpse (`loot_item`) is not covered: the agent is not in the party.

The report: per object step `carrier_result` (how the carrier's last order was answered), `fallback_used` and `fallback` (`reason`: the carrier's error, `did`, `items`, `gm_assists`,
`seconds`, `result`, and `trade`: `result` (`completed` or why not), `verified`, `in_bags` (the `.bot inv` check), `items`, `visible`, `seconds`), and the trace events `object_fallback`,
`object_fallback_end` and `object_trade`. Fake commander only; not verified live (see `docs/LIVE-CHECKS.md`).

### Targets by entry: the agent need not be in view

TortoiseBots #933 lets the commander name a game object (or, for `cmd loot`, a corpse) by its **entry** and a point instead of a GUID, and adds `cmd state`
(player-controls.md section 1a, "Targets by entry"). The agent's character is not with the party, so its object store seldom holds the chest, the door or the boss's corpse of a step and a
spawn GUID is what it could not give; the plan's entry and position are known. A remote run whose plan has object steps (or a boss `loot_item`) probes this once at its start with one
`cmd state entry:<id>@x,y,z` on the first plan object with a position. An ACK, `not-found` or `too-far` proves the module knows the verb (**on**); `invalid` (an older module), any other
refusal, or silence turns it **off**, and everything below is then as it was (GUIDs from the agent's store, keep-near, the wait that passes unchecked after 10 s). The probe, the verdict and the
per-wait polls are in `remote.entry_targets` (`on`, or `off: <why>`), `remote.state_polls`, the notes and the `state` count of `remote.orders`.

With entry targets on:
- `cmd open|loot|use` of an object step say `entry:<id>@x,y,z` (the plan's entry and `near`, else its `stand`; without either, `entry:<id>` alone: the search is around the carrier). The server picks the
  nearest spawned object of that entry within 30 yd of the point. The `TBM:ACTION_TARGET|cmd <verb>|bot:<Name>|<guid>|<entry>` line it sends before the final answer is parsed into the order result and
  the step report (`object_steps[].target_guid`, `orders[].target`). If the agent happens to see the object, its GUID is the **fallback**: sent once when the entry order answers `not-found`.
  The agent's own-hands fallback (`object_fallback`) uses the GUID in the ACTION_TARGET line, or the object once the agent stands at it.
- `not-found` fails the step with a message that names the entry and the point ("no spawned game object or corpse of entry N within 30 yd of x y z, within the bots' reach"). `too-far` (the point is beyond
  `ReactDistance` from the carrier) walks the party toward the object in legs and retries **once** (a GUID order gets two walks).
- A boss's `loot_item` is `cmd loot entry:<boss>@x,y,z <carrier> <item>` at the place the kill was seen (else the plan's boss position); it no longer needs the corpse in anyone's view. The corpse GUID,
  when the feed or the agent's store has one, is the retry after `not-found`.
- A `wait_for` a game object with an entry polls `cmd state entry:<id>@x,y,z` every 2 s and ends when `state` is the plan's (`active`, `ready`, `active_alt` = the module's `alternative`).
  `not-found` is "not there yet", `too-far` is noted; the step's own `timeout_s` ends the polling with "waited N s for ..." plus `last cmd state answer: ...`. The watchdog does not count the wait.
  A `cmd state` refused as `invalid` in the middle of the run turns entry targets off from then on.
- **Keep-near is off** when every object step of the plan has an entry (no `.appear`, no walk behind the party before object steps and boss steps; `run_gm_assists` is 0, `agent_walks` 0 for them). A boss
  step still brings the agent up once (`.appear` or a walk, a counted `run_gm_assists`), but only when this run has no evidence the boss died (no kill, no ledger entry, no corpse in the agent's view): a boss killed before the run (a resume) can be shown dead only by the agent's view. A read-only check would need a `cmd state` for creature entries (follow-up in TortoiseBots). With a step that names no entry (a position only) it stays on, with a note.

Fake commander only; not verified live (see `docs/LIVE-CHECKS.md`).

A refusal that cannot help ends the step with the code and what it usually means (`no-item`, `locked`, `denied`, `unavailable`, `invalid-target`,
`unreachable`, `timeout`, `failed`). `busy` is sent again (4 times), `cancelled` too (twice), `in-combat` waits for the fight to end (60 s), `too-far`
(the object is more than 150 yd from the carrier) walks the lead bot toward the object (stand point or object position) and sends the order again (twice),
`lead-unavailable` waits as any order does. The loot method is put back even when the step fails, and when its `cmd lootmethod ffa` order was refused, timed out or cut short by a cancel (the order may have changed the method anyway). Every remote run starts by turning corpse looting on for each bound bot (`.bot behavior <bot> loot on`), since a run that ended in a crash may have left a boss's `loot off` in place; the report notes it. Not verified live yet.

**The agent must be within view of the object.** The GUID of a game object is its spawn's, and the bots have no listing of them: it comes from the agent's
own object store (`gameobjects`). So an object step needs the agent's character to be near enough to **see** the object (it need not be in the group, but
it has to be on the dungeon's map, in the instance, with the object loaded: a character in Ironforge sees nothing). If the store has no such object the
step fails before any order with `the agent must be within view of game object <entry> near <x y z>`. An agent that is out of the instance cannot
do the leg's object steps; run them from a character that stands in or beside the instance, or use group mode for those legs.

**Bringing the agent into view of each object step** (remote mode, with a game that can move the agent). Before any step that needs an object's GUID
from the agent's store (`use_object`, `loot_object`, a `wait_for` on a game object) the driver first brings the agent toward the lead bot as above, then,
when the step's own object (its `near`, else its `stand`) is still not in the store, looks for it for up to 12 s (and 12 s again after a walk): after 4 s (at once when the agent did not move
for the lead bot) the agent walks to 20 yd short of the object's position (`walk_path`, counted in `agent_walks`, 40 s ceiling, out of combat, never when a
hostile the bots see, or one in the agent's own view, stands within 25 yd of the way, which is checked every 10 yd along it, not only at its corners). The lead bot can stand more than the view range from a door or chest of the step (uldaman-remote-4: the Seal, 41 yd). If the
object stays out of view, a `use_object`/`loot_object` step fails with `... within view of game object <entry> near <x y z>, the agent stands <n> yd from it, after
looking for it 12 s ...`. A `wait_for` on a game object goes on instead, after the usual 10 s, **unchecked**: the feed carries no game-object state, so the
wait is passed as a last resort with a note that says where the agent stood, and the party's next walk is the only check. If that walk fails, its error repeats the object (`...gained no ground ...; the earlier wait for game object <entry> near <x y z>
passed unchecked (the agent could not see it): a door that stayed closed would fail this walk`), so a closed door can be told from a stuck bot; a walk that works clears it. Before this, a wait that held in the same step of the Stepper as the next leg's walk was wrongly failed as an object step out of view;
only the actions of an object step count now. The approach to an object also waits for **three** feed frames within 0.3 yd (was two within 0.5) before `cmd open`.

The report's `remote.carrier` and `remote.object_steps[]` give, per step: the carrier, the orders sent with their replies (`command`, `result` `ack` or
the ERR code or `timeout`, the final line, whether it reported `pending`, the seconds), the items gained, the seconds of the whole step and `result`
(`done`, `skipped` or `failed`), plus `approach`, `carrier_result`, `fallback_used` and `fallback` (below). `orders` and `refused` count the orders by verb as for the others.

**Threat decides when the party goes in.** After a pull the party is sent at a mob (`.bot cmd attack`) once the tank **holds its threat**: the top
of the mob's `T` record is the tank and the mob is hitting it. The tank is `tank` (a name) or, when not given, the first bot seen holding a
pulled mob (a rage user when the party has one). A mob that is not on the tank holds the party back for up to 6 s (`threat_waits`), then it goes in
anyway (`threat_overrides`); `threat_holds` counts the mobs that were on the tank in time, `threat_warnings` the bots that came within 90 % of the
top threat. Naming the `tank` is the reliable way.

**Tank first (remote).** The party follows the tank's threat (after mod-playerbots' `ThreatStrategy`, `DpsTargetValue` and `TankTargetValue`; the behaviour only, no code):
- *Target.* The attack order goes to the engaged mob where the tank's share of the top threat (`T` record: 100 when the tank tops the list, 0 when it is not among the top
  three) is largest; the tank's own target comes before the others, then the nearest. The party stays on the mob it fights while the tank holds at least 50 % of it, and below that
  until another mob leads it by 25 points, so two mobs the tank holds alike do not flip it back and forth; the `cmd attack` for a mob goes **once**: it is sent again only when the mob changed, the module refused the order, or `assist_every` (10 s, at least 5 s) has passed and the tank is not holding the mob near it (`attack_resends` counts these, the first five are noted), because an attack order at the same mob every second after a pull made the tank drop it for "return to pull position" and back (Uldaman group B, tank wipes); it is sent at the next mob at once when the one before died (`target_switches` counts the times it left a
  mob that still lived).
- *Opener.* A mob the party was not sent at yet waits until the tank has held it (top of its `T` record, the mob hitting it) for `OpenerDelay` (2 s, the existing threat gate
  extended; `threat_waits` counts it too), or until `ThreatWait` (6 s) has passed. The feed gives shares, not absolute threat, so the delay stands in for "the tank has N threat".
- *Loose adds.* A mob that has been hitting a bot that is not the tank for 3 s (and whose list the tank does not top) gets `.bot cmd attack <guid> <tank>` (TortoiseBots #928: with a
  bot name only that bot of the party goes, so the tank alone is sent at the add). It was `.bot cmd pull <guid> 0`, which the module refuses `tank-busy` while the tank is in
  combat, which is exactly when an add is loose (the first remote Uldaman run: 19 refusals, 11 threat overrides); `cmd pull` stays for opening pulls. The 3 s count starts at the
  look that first showed the mob on that bot. At most one order per look, `AddCooldown` (8 s) apart for the same mob, 3 for the fight (`loose_add_orders`). The tank stays on the add it was last sent at for 5 s (`tankSwitchHold`) while that add is alive and still off the tank before it is sent at another one: with several loose adds it was otherwise sent at a different mob every second, walked between them and never landed a Taunt (ZF hires-2 run 3b). A mob that hits the healer (the named one, or any mana user when none is named) breaks the hold and gets its order at once. A refusal is noted and
  counted in `refused` (`attack:<code>`); it does not end the fight. **No tank order goes to a tank that cannot take it**: one that is dead or released (a ghost shows dead), on another map, or more than 60 yd (horizontal; the module's sight distance) from the mob is not ordered; the order waits for the rez or the tank's run back (`tank_orders_held`, one note per absence). Seen in Zul'Farrak hires-3: the tank released and stood 433 yd off at the graveyard, and 11 orders were refused "That bot did not accept the attack". A `too-far` answer to a tank order pauses the tank orders for 10 s. A pull answered `too-far` while the named tank (not the lead bot) is dead, released or away from the lead bot waits for it, at most 90 s (`tank_away_waits`), before the lead bot is walked at the mob; after that wait the pull is refused and the group is not counted cleared.
- *Crowd control.* The feed's `c` flag is stunned, confused, fleeing or rooted (`UNIT_STAT_*`): it cannot tell a sheep from a stunned or rooted mob. So only a mob a `cc` order
  was accepted for is spared, while it carries the flag or no bot is fighting it: it is not attacked, not picked as the pull target while another candidate is, and not sent the tank
  at, unless every engaged mob is like that (the last one is fought). The mob the party fights and the tank's own target are never spared. Each spared mob is counted once in
  `cc_respected`. Crowd control the bots start by themselves, and the moon mark, cannot be seen: the feed has neither.
- *Runners and bodies* (remote_flee.go; Uldaman A run 6, 2026-10-14). A mob the party fights that is at `flee_pct` (20) of its health or less and moves away from the party
  (1.5 yd further from the nearest living bot than in the frame before), or that the plan lists in `fleers` and the feed shows with the `c` flag at up to 15 points more health,
  is a *runner*. While it is in view and within 35 yd the damage dealers are ordered at it (`cmd attack <guid> <bot>`, the tank and the healer keep their work; the whole party
  when the tank is unknown or the module takes no bot name), every 2 s, up to 6 times (`flee_orders`). A runner that leaves the feed alive (two frames) is not a kill: not in the
  fight, not in the kill ledger (`goneMeansDead`), so the pull does not count it dead. It is a *fled* mob (`fled`), unless it was nearly dead (a few percent, or no more than it was
  losing in 1.5 s) with a damage dealer ordered at it and a bot in spell range, or a bot within 10 yd: that is a kill. Every pull (`engage`) first waits where the party stands for the fled
  mobs that were last seen within `flee_near_yd` (40) of the lead bot, at most `flee_wait_s` (30) from the moment each was lost and 90 s in all: whatever engages in the meantime
  (the runner back at low health, its friends) is fought there with the usual tank-first rules; the waits are `flee_waits` and `flee_wait_s` in the report. The same
  gate is the dead bots' rule: a dead bot is left to a living caster's resurrection (`rez_wait_s`) only when no live hostile (not a critter, guard or civilian) stands within
  `revive_safe_yd` (30) of the bodies; else the wait is skipped, the note says so, and the bots are released with `cmd revive` (they run back from the graveyard; `unsafe_rez_skips`).
- *Counters* (`report.remote`): `tank_threat_share` (the mean over the frames of a fight of the tank's share on the fighting mobs that have a `T` record, in percent; the
  deploy-19h run had it at 15-25 with the tank leading), `tank_threat_samples`, `loose_add_orders`, `cc_respected`, `target_switches`.

**`keep_on_fail`** (ZF-2, off by default): a FAILED run keeps its binding instead of unbinding, so the hires stay bound and in their instance (`cmd stay` is still sent), `report.remote.unbind_via` says `kept ...`, and the next run's bind finds the same group and re-enters the same instance instead of a new one (the account's hourly new-instance limit). Without it the group is never left bound (driver rule 4). While a binding is kept, a bound hire does not answer `who`, so the next hired-party prepare takes class and level from what the run that left it read; `dungeon_stop` with no run going sends the `commander unbind` that lets a kept binding go. To halt a run you mean to resume, use `dungeon_pause`, never `dungeon_stop` (a stop unbinds and the instance is lost). A finished or stopped run, `on_end: "logout"` and a failed prepare always unbind.

**Always unbound** (also failed, unless `keep_on_fail`). The unbind (`commander unbind`) is deferred inside the run: done, stopped (`dungeon_stop`) or a panic, it goes out with
its own context before the report says the run ended. A panic in a remote run is recovered and reported as a failed run (`internal error: ...`,
with the stack in the notes); group mode has no such guard. A bind the module refused is no binding (nothing to unbind); one that got no answer is
unbound anyway. The module can end the binding itself (the lead bot logged out or left, the party disbanded, a player joined, the switch turned
off): its `commander unbind` ACK or a `not-bound`/`disabled` answer fails the run with `the commander binding ended: <reason>`.

**Entering the dungeon.** A dungeon's instance bind belongs to the *group*, and a bot that leaves its group inside the dungeon is sent to its
homebind about a minute later (deploy-19i: the bots were ejected twice that way). So the party that fights has to be the bots' own group from
the first step in, and the agent stays **outside and ungrouped**. When a bot of the first frame is not on the plan's map (and `enter` is not
`off`), the run does, after the bind and the first frame:

0. **Checks the bound party** (a run that names `bots`): after a 3 s settle it reads, and re-reads every second for up to 10 s until it matches, `commander status` (lead, bot count, and the listing line
   `You command X's group: ...`) and the feed's party and compares them with the named lead bot and bots, **exactly**. A bot that dropped out
   after the bind (run 6b: the hunter was in the bind's ACK and listing, and gone one second later; an earlier `.bot command <bot> leave` is only
   forwarded and can land after the bind) is invited again by one `commander unbind` + `commander bind`; a bot still missing after that, a
   refusal of the second bind (`hired`, `no-bot`, `member-grouped`, `bot-bound`, ...), or a group that holds a bot the run did not name, fails the
   run **before** anything is teleported or summoned, naming the bot and the reason. The run never enters with a partial party. Not done when
   `enter` is `off` or the party is already inside (the group is not changed in the dungeon), nor for a run that names no `bots`.
1. The agent's character leaves its group if it is still in one (a bind that keeps the bots-plus-commander party, TortoiseBots #929, removes
   the agent itself; the bound bots do not leave with their master). It is refused when the agent stands on the plan's map.
2. It picks the way in. `auto` asks the server for the account level once (`.account`): a developer-level account (3; `.tele name` needs it)
   with an `entrance_tele` in the plan teleports the lead bot; anyone else whose plan has a `portal` and an `entrance_trigger` is walked to the
   door and ordered through the trigger with `cmd enter` (TortoiseBots #939); a plan with a `portal` only walks. `gm`, `cmd` and `walk` force one.
   - **gm:** `.tele name <lead bot> <entrance_tele>`, a wait of up to 30 s for the lead bot to show on the plan's map (it goes into *its group's*
     instance, and the first entry binds the instance to that group; the agent never enters), then `cmd summon <bot>` for each other **named**
     bot that is not on the plan's map, 6 s apart, **by name**: a bot the feed does not list (it left the group, or the feed dropped it) is
     summoned too (the module answers `no-bot` for a bot outside the group, and the run fails saying so).
   - **cmd:** `cmd move` legs to the portal on the outer map as for the walk, then up to 8 s for the other bots to stand within 30 yd of it, then
     `.bot cmd enter <entrance_trigger>`. With `cmd enter` the lead bot is at the door already when it stands within 30 yd of the portal on the
     ground plane and no more than 12 yd above or below it: a door in a cave or on a ledge stands above or below the walkable ground, so the
     3D 5-yd arrive check of the plain walk would never pass. Without `cmd enter` the lead bot must reach the portal point itself. The module answers `TBM:ACTION_PENDING|cmd enter|party|<budget s>|<trigger>` and one final line:
     `TBM:ACTION_ACK|cmd enter|party|<n>|entered` (the party is inside; the run waits for the lead bot on the plan's map) or
     `TBM:ACTION_ERR|cmd enter|<code>|<Bot>: <msg> [(n of m got in)]`. The wait for the final line is the budget plus 8 s (the module ends the
     order with its own `timeout` about 6 s after the budget), at most 130 s (its worst case is 60 + 30 + 30 s and 6 s). Retries are bounded: `busy` 3, `cancelled` 2, `too-far` 2 (a wait for the
     party in between), `in-combat` for 45 s; the whole phase fails after 4 minutes. The ceiling counts from the start of the walk. Before any retry, an answer with the lead bot already on the plan's map (or `n of m got in`, n above 0) goes on to the summons and `cmd enter` is not sent again. `not-found`, `requirement`, `instance-limit`, `unreachable`, `unavailable`, `denied`, `no-bot`, `failed` and `timeout`
     fail the run with the cause. A module without the order (no `ACTION_PENDING` and no answer, or an unknown-verb refusal) leaves the walk
     behaviour: the lead bot gets 8 s to be taken in, then the run fails saying it needs `cmd enter` (`remote.enter.cmd_enter.fallback`).
     The agent's own character does not enter: alone it would be given an instance of its own, not the bots' group's.
   - **No walkable path to the portal (cmd and walk):** the hires spawn at the commander, wherever it stands (the first Ragefire start had them in
     Tanaris, the portal in Orgrimmar's Cleft of Shadow: "refused 8 times ... unreachable: The lead bot has no walkable path to that point"). The
     walk asks before it spends its tries. If the first move that is refused `unreachable` aimed at a point the agent's navmesh planned (or at the
     portal itself) and nothing was accepted before it, or the agent's own measured navmesh path to the portal is longer than 300 yd, or the lead bot
     is on another map than the portal, a **GM account** (level 1, `.go xyz`) moves the party: the commander goes to the portal point with `.go xyz`,
     dead bots are revived with `cmd revive` (not the run's recovery, which would bring the party in), the lead bot is brought with `.bot summon <lead>` and held with `cmd move` (as the start phase
     does), the other bots with `cmd summon <bot>` (GM assists, `run_gm_assists`), and every configured bot counts as apart until the feed shows it
     alive within 15 yd of the portal (missing, dead or on another map: summoned again, 2 rounds, then the phase fails naming it). Then one `cmd follow`
     must be acknowledged (busy, cancelled, in-combat or no answer: asked again, 3 tries; any other refusal fails the phase before `cmd enter`): the
     summons made the commander the bots' master, and with the commander outside the instance they would be pulled out after the entry. Then
     "enter: party moved to the portal by GM, N bots" (`remote.enter.party_moved`) and `cmd enter` as before. One GM move per run. A refusal of a
     straight-line point (no planner or no tiles of the outer map) says nothing about the portal: those halve the leg and replan like `off-mesh` and
     `too-far`, and only a whole budget of `unreachable` gives the portal up (GM move, else the failure). Without a GM account the failure is
     "the lead bot has no walkable path to the portal ... hire the party near it or use a GM account"; a long but walkable path is walked as before.
   - **walk:** `cmd move` legs along the navmesh to the portal on the outer map, then a wait of 8 s for the lead bot to be taken in by the
     area trigger, then `cmd summon` for those still outside. A bot never sends `CMSG_AREATRIGGER` itself and the module takes a trigger only
     from the master, so this fails on a module without `cmd enter` (the run says so); it is what is left for a plan without `entrance_trigger`.
3. Every bot's feed map must be the plan's within 30 s of the entry. A bot still outside then is summoned once more, by name, and waited for
   another 30 s (`remote.enter.resummoned[]`); one that is still outside fails the run naming it (the group is **not** changed to fix it). So the
   phase has a ceiling of two waits.
4. From the first entry order on, the group is left alone: no leave, invite or bind until the run ends (the unbind is the only thing left).

**Instance limit.** The core refuses a 6th *new* instance per account per hour (`MAX_INSTANCE_PER_ACCOUNT_PER_HOUR`, the player sees
`TRANSFER_ABORT_TOO_MANY_INSTANCES`; re-entering one on the list is free, and a mangosd restart clears the list). The owned bots share the
agent's account, and a GM teleport counts too. Every run that enters a dungeon is one new instance, said in the report (`remote.enter`:
`entries`, `entries_last_hour`, `entry_limit`, `warning`) and in the notes. The agent keeps the entries of the last hour per account in a small
file next to the settings file (`instance-entries-<hash>.json`, unix times only) and **refuses the 6th** within the hour, saying when the oldest
leaves the list; `ignore_entry_limit` tries anyway (after a server restart, say). Entries made outside this agent (by hand, a game client)
are not in the file.

`remote.enter`: `method` (`gm`, `cmd` or `walk`), `cmd_enter` (`trigger`, `orders`, `answers[]`, `entered`, `fallback`, `seconds`), `account_level`, `agent_left_group`, `tele`, `summoned[]`, `resummoned[]`, `arrived` (seconds from the phase's start,
per bot), `seconds`, and the entry numbers above. A party already inside the plan's map has no `enter`. `from_leg` past the first leg and `from_pull_group` now bring the party to the start
by the commander's own GM move: see [Start anywhere and the pull test](#start-anywhere-and-the-pull-test).

### Prepare: one step before the bind

A tester used to spend about 11 minutes per remote run on setup: the agent rebuilt, bots logged in and maintained one call at a time, `.instance
unbind all` per bot, the bind, a tele, the summons. `dungeon_run`'s optional `prepare` block (remote mode only; group mode refuses it) does the
part before the bind in one go, then the kit check and the enter phase run as before, back to back:

```json
"prepare": {"online": true, "fresh": true, "gm_setup": ["gm visible off"], "online_wait_s": 45}
```

1. **`online`:** `.bot roster` is read; `.bot add <Name>` goes out for each of `lead_bot` and `bots` the roster does not show `online` (one that is
   `starting` is only waited for; the agent must own the character or be a GM, and the reply is read: `not found`, `may only control` and the like fail
   the run naming the bot). The roster is then read every 2 s until all are online, at most `online_wait_s` (default 45, at most 180) after the
   logins; a bot still not online fails the run. A bot the server answers `already online` for but the roster does not list (a hired bot:
   `.bot roster` lists the account's own bots only) is taken as online on the server's word and counted in `already_online`, with a
   warning (ZF-1: five hires waited the whole 45 s for nothing). A hired party is better brought with `"party": "hire"`.
2. **`fresh`:** a new instance, for a developer-level GM account only (the step reads the level with `.account`; a player account, or one the server
   does not answer for, is refused with a message before anything is sent). In this order: the account's hourly entry ledger is checked (the 6th new
   instance is refused here, before anything moves; `ignore_entry_limit` goes on); `.tele name <bot> <outside_tele>` for each bot and for the agent
   (a bot is not unbound from the map it stands on), then the step waits (at most 25 s) until the agent is off the plan's map and has every bot in view;
   then, **from account level 4**, `.bot command <bot> leave` for each bot (a group keeps its own instance bind; the module only forwards the
   command, so the step reads `.bot roster` until the bots show ungrouped, 10 s at most, a warning after that) and `.instance unbind all` **for each bot by name**: the step selects that bot's
   GUID (from the roster), waits until the server shows that selection (the target field of the agent's own object; 3 s, else nothing is sent), and sends the line as one operation of the session, so it never runs on whatever the
   agent had selected (its reply goes to the unbound player, so it is reported as *sent*, `unbound[]`). **Below level 4** the core
   (`HandleInstanceUnbindCommand`) unbinds the *sender* whatever is selected, which would clear the agent and no bot; there the order is
   `commander bind` (the bots-only group with the lead; `commander status` must list exactly the lead and the bots, else one unbind + bind
   invites the missing one, else the step fails naming it), then `.instance groupunbind all` with the lead bot selected (every in-world member of
   its group is unbound and the group is disbanded; the line is sent only once the server shows the lead bot as the agent's selection), the sender's reply read (`Group unbound. Disbanding.`, which names nobody, is accepted only if `commander status` then answers `not-bound`: disbanding the bound group ends the binding, else the run stops with "the unbind hit another target"; `<name> is not in a group.` naming the lead, or no reply, fails the step; a reply naming the *agent* means the line ran on the sender and is sent again, three sends at most, each a counted GM line; a reply naming any *third* player stops the step at once and nothing is sent again), then `commander bind` again
   (the group was disbanded), checked the same way. No `leave` goes out on this path unless the bind refuses a bot as `member-grouped`/`bot-bound`
   (it is in some other group): then the bots are told to leave, the roster is read until they show ungrouped, and the bind is made again. The binding stays for the run's own bind
   to reuse (`already-bound`, the same lead and count: "reusing the binding"); a step that fails after it unbinds. Last, `.instance unbind all` for the agent itself. **Needs the plan's `outside_tele`.**
   `fresh` always resets: the agent cannot read from outside whether the old instance is cleared.
3. **`gm_setup`:** `.gm visible off` / `.gm visible on` lines (a leading dot and any spacing or case are fine) and nothing else: any other text is refused when the run is
   started. It needs account level 2.

4. **`give_items`** (a run option of its own, not a field of the `prepare` block; remote mode only, and it runs last, after the hire and `gm_setup`): `"give_items": [{"bot": "lead" | "carrier" | "<name>", "item": 9240}]`
   (at most 4; `bot` defaults to `lead`; `name` is optional, for the setup line: it defaults to the plan's own name for the item, else `item <entry>`). For each entry the step reads the bot's bags with `.bot inv <bot>`
   (a bot that carries the item already gets none and nothing is booked), selects the bot by GUID (the agent's own group or view for a hired bot, the roster for a named one; the selection is confirmed by the server first and the line goes out as one session operation, like `.instance unbind all`),
   sends **`.additem <entry>`** and reads `.bot inv` again until the item shows (at most 8 s, else the run fails saying the line may have run on another player or the bags are full). `lead` is the lead bot, `carrier` the run's `carrier` option, else the bot the object steps use (the first bot that is not the lead, tank or healer).
   It **refuses without a GM account of level 3** (`.additem` is a developer's command; the account level is read with `.account`, and the run stops before it sends or binds anything). The line is a setup shortcut: `additem 9240 on <bot> (test setup: Mallet of Zul'Farrak)` in `setup_shortcuts`,
   so an honest-run check flags it. `give_items` is refused in group and observe mode. Before anything is hired, logged in or sent, the run checks that the agent's game can do what its options need (`give_items`: `.additem` and `.bot inv`; the prepare block; a hired party; an optional leg's bag read) and fails naming the missing ability, so a run that cannot give items does not pay for a hire first. The `.additem` line (like `.instance unbind all`) acts on the selected player and is sent as the chat packet alone: the unit is selected, the server's echo of the selection is awaited (an echo that could answer an older select of the unit, with another unit's select sent after it, does not count, nor the target field rewritten by combat or a stun, which leaves the selection unknown; when a new echo could not be told from an old one, or would not come because the field already shows the unit, a neutral selection (none, or the agent itself) is made and confirmed first; reselecting the unit already confirmed needs no echo; `.aura` world buffs and `.respawn` go the same way), and the line goes out only while that confirmed selection still stands (the core handles a selection and a GM chat line on different threads, so a selection sent right before the line would not be in force); if another request changed the selection meanwhile, nothing is sent and the unit is selected and confirmed again (three tries). It is how a test gets the Mallet for the optional gahzrilla leg below.

Every GM line the step sends is a **setup shortcut**: it is in `setup_shortcuts` (for example `.tele name Lcwarra uldaman (prepare fresh)`,
`.instance unbind all on Lcwarra (prepare fresh)`, `gm visible off`) and is a `gm_shortcut` event of kind `setup` in the trace, so `trace-score`'s
`gm_shortcuts` check fails an honest-run flag for it, as it did for the old `setup` list. They are not in `remote.gm_commands` (which stays the
enter phase's one `.tele name`).

Report: `report.prepare` = `steps[]` (`name`, `seconds`, `detail`), `seconds`, `already_online[]`, `added[]`, `teleported_out[]`, `left_group[]`,
`unbound[]`, `group_unbound[]`, `not_grouped[]`, `gm_setup[]`, `gm_commands`, `warnings[]`. `report.setup_s` is the whole setup: the seconds from the start of the run to its first step (prepare, bind,
enter phase and kit check; `remote.preflight.seconds` and `remote.enter.seconds` are the last two). Nothing in the block waits longer than its
own ceiling (`online_wait_s`, 25 s for the teleports, 6 s per `.bot` line). `tools/run-uldaman.sh` runs all of this for Uldaman; see
[docs/LIVE-CHECKS.md](docs/LIVE-CHECKS.md) "Remote Uldaman in one command".

### Hired party (prepare party "hire")

The owner's rule (2026-10-14): every commander group is **five hired bots at the instance's level, the commander outside the group** (TortoiseBots
#935: `commander bind` accepts the commander's own hires and the commander ends outside the bound party). The hand-made alt party (`Lc*`) is the
`online` + `fresh` path above; this is the other one, and it needs no character to exist before the run:

```json
{"name": "uldaman", "mode": "remote", "prepare": {"party": "hire", "hire": {"composition": ["warrior:tank", "priest:healer", "hunter", "rogue", "warlock"], "level": 43, "rehire": false}, "gm_setup": ["gm visible off"]}}
```

`lead_bot`, `bots`, `tank` and `healer` stay out of the call (the step names them: the lead bot is the hired tank); `online` and `fresh` do not go
with it (a new hire has no saved instance and is online). `hire` is optional and every key of it too:

- **`composition`:** `"class"` or `"class:role"` each (role `tank`, `healer` or `dps`; a warrior is the tank and a priest the healer unless said otherwise, and
  paladin, shaman and druid need a role). Two to five bots, exactly one tank and one healer. Default: `warrior:tank`, `priest:healer` and three damage
  dealers, the first three of hunter, rogue, warlock, mage.
- **`level`:** default the middle of the plan's `level_band` (43 for Uldaman); a plan without a band needs `level`.
- **`rehire`:** end the old binding and dismiss every current hire first. Without it the step **keeps** the hires that match (same class and level, a
  build that gives the role: read with `command <bot> talents`) and the standing binding, if it already is the wanted party, to save the hire time;
  the rest are dismissed and replaced. A kept hire keeps its saved instances: `rehire` is how a run gets a clean instance. A hire the agent cannot see
  (no class in its view) does not match.
- **`no_fix`:** report what is wrong and fail, instead of `.bot autogear` and `.bot maintenance`.
- **`wait_s`:** the longest wait for a hire to show in the group (default 30, at most 120).

**What the step does,** in order, all with the commander's own `.bot` commands (no GM line):

1. **Current hires.** The members of the commander's group and of the bound group that `.bot roster` does not list are its hires (a hire is a pool
   character, not one of the account's). An owned bot in the commander's own group fails the step (it is not a hire and would be bound with them); an
   owned bot in an old bound group is left as it is. What each hire is comes from the bot's **own answer** to `.bot command <bot> who` (class, level and
   talent points, e.g. `protection (0/0/36) warrior (43 lvl)`), not from the agent's view, so a bound hire inside an instance is recognised.
   `commander unbind` ends an old binding that is not the wanted party. **A hire is deleted by `.bot logout`, so it is dismissed only when its own answer
   shows it is the wrong version of an empty slot** (same class, another level or a build for another role), or when the caller said `rehire`. A hire that does
   not answer `who` (offline, teleporting, in its logout grace) and a hire with no empty slot of its kind (surplus) are never dismissed: the step stops and
   names them. The step never sends a hire `leave` (that deletes it too), not even when the bind refuses a bot as `member-grouped`.
   **Hires left outside the group and the binding still count against `AiPlayerbot.HireMaxBotsPerPlayer`** (and the agent cannot list the ones that
   are neither in the group nor in the bound group): when a hire is refused with "maximum number of companions", `--rehire` / `hire.rehire` dismisses the
   commander's current hires on purpose; one that is out of reach still has to be dismissed from its own account or by the grace running out.
2. **Hire.** `.bot hire <class> <role> level=<n>` for each missing slot, in composition order. The level goes only when it differs from the commander's own.
   A reply "Convert to a raid" (the 5th hire) is answered with `convert_raid` and one more try. A server without TB #940 either refuses `level=` (the
   reply is read: `Bad level`, `Unclear number`, `Give only one level`) or ignores it (the answer carries no `Level n.`): the step then hires at the
   commander's level and **accepts that only inside the plan's level band**; a server that lowers the level to the commander's limit says so and is
   treated the same. Any other refusal (`recruiter`, `cannot afford`, `maximum number`, `party is full` after the conversion) fails the step with the
   server's words.
3. **Wait** (at most `wait_s`) for each hire to show in the commander's group with a level and health.
4. **Ready check, per bot** (the standard of `TEST-BRIEF.md`; every problem is collected and the step fails once, naming the bots):
   - *level and class* as hired; not dead;
   - *gear:* the worn items are asked for with `CMSG_ITEM_QUERY_SINGLE` (what a client does for a tooltip; cached, the agent is a GM so the core answers
     for any item). No epic (quality 4) that is out of band (an epic is fine when it is for the bot's level: required level <= its level and item level <= its level + 5; owner rule 2026-10-14), head, chest, legs, feet and main hand filled, a shield on a warrior or paladin tank (an armor of the shield
     subclass), a ranged weapon on a hunter. Else `.bot autogear <bot> blue` once, then read again; still wrong fails, except that a head, chest, legs or feet slot that is still empty after autogear (at level 16 autogear has no blue head piece for plate, leather or mail) is only a warning (`head empty after autogear at level 16`); an empty main hand, a missing shield or a hunter's missing ranged weapon still fails, and with `no_fix` only empty armor slots are a note. `ilvl` is the average item
     level worn (shirt and tabard left out), `quality` the best worn. An item the server does not describe is a warning;
   - *supplies:* the drinks (a healer 20, any other mana user 10) and 100 shots of the kind the ranged weapon takes (as the preflight judges them);
     a shortage, and every kept hire, gets `.bot maintenance <bot>` and is read again; a shortage after it fails (`ready.bots[].fixed` has the numbers);
   - *key spells* from the bot's own list (`.bot command <bot> spells <filter>` is answered by a whisper burst: `=== Spells ===`, then **one spell per
     line**, `[Name]` links, also when nothing matches; the step reads the whole burst, ending it 0.4 s after the last line, 3 s at most): warrior
     Battle Stance, and for the tank Defensive Stance, Taunt, Sunder Armor and Shield Block; priest Resurrection, for the healer Renew and Dispel Magic;
     hunter Tame Beast, Call Pet, Mend Pet; warlock Summon Imp or Voidwalker; mage Polymorph; paladin Redemption; shaman Ancestral Spirit (each from its
     level). A missing spell fails ("lacks X"): **the step learns nothing** (`.learn` is a GM command on a character that is not the commander's). A
     question that gets no header within 3 s fails the bot as "no answer from <bot>", not as a missing spell;
   - *talents:* `command <bot> talents` must show points spent and a build whose role is the slot's (warrior protection tank; priest discipline or holy
     healer; paladin holy healer, protection tank; and so on). A feral druid cannot be told as bear or cat: a warning;
   - *pet:* a hunter's or warlock's pet is out in the agent's view within 10 s (a bot the agent cannot see is a warning).
5. **Bind** `commander bind <tank> <others>` (the shared bind code of `fresh`, checked with `commander status`), then wait (10 s) for the commander to be
   out of every group; still in one means a server before #935 and fails. The run's own bind then finds the binding (`already-bound`, the same lead
   and count) and reuses it. The hires stay in the commander's raid afterwards (`unbind` returns them), so a later run finds them.

Every wait has its ceiling (`wait_s`, 8 s per hire reply, 6 s per `.bot` line, 3 s per spell question, 60 s for the pet (a bot with no pet at 20 s is told once to `summon voidwalker`, a hunter to `call pet`), 12 s for dismissals, a hire of a **kept binding** (a resume into the group the run before left: it is bound, so it answers no `.bot` line) is not checked again for gear, spells, talents or pet (a warning each; what the first run read is kept in the hire memory), 240 s for the
whole step); no loop is extended by a retry (a hire line is sent at most three times: the raid conversion and the level fallback are one try each).

**Report.** `report.ready`: `party` (`hire`), `level`, `level_note` (why the level differs from the asked one, when it does), `hired[]`, `reused[]`,
`dismissed[]`, `seconds` (the hire step), `setup_s` (the whole setup of the run, the same as `report.setup_s`), `ready`, `warnings[]` and `bots[]`: `name`,
`class`, `role`, `level`, `source` (`hired` or `reused`), `max_hp`, `ilvl`, `quality` (`rare` is blue), `talents`, `drinks`, `ammo`, `shield`, `pet`,
`spells[]` (the key spells seen), `fixed[]` (what `autogear` and `maintenance` changed, with the numbers), `warnings[]`, `problems[]`, `ready`. A failed
step leaves the block in the report with the bots checked so far. `remote.lead_bot`, `bots` and `tank` carry the hired names, and the trace has a
`hired_party` event (its header was written before the hires existed, so its `lead`/`tank` are empty). `report.prepare.gm_commands` is 0 and
`setup_shortcuts` has no entry from this step: hiring, `autogear` and `maintenance` are player commands. `max_hp` is recorded, not judged: there is no
known-good number per role and level to compare with.

`tools/run-uldaman.sh --hire [--rehire] [--level N]` runs it for Uldaman. **Not verified live** (tested against a fake server only): the exact text of the
hire reply on a build with TB #940, that `.bot command <bot> who` and `spells <filter>` are answered in the formats above when forwarded as a whisper to a hire, the item prototypes for every
slot, pets in the agent's view while the hires stand around it, and whether a kept hire's talents read back as the premade.

**GM.** The only GM command the remote run itself sends is that one `.tele name` (`remote.gm_commands` is 1 then, else 0): no `.revive`, `.aura` or
`.gm visible`. The optional `prepare` block sends more, all booked as setup shortcuts (above). `teleport`, `buffs`, `brain`, `gm`, `gm_level` and `wait_sickness` are refused with `mode: "remote"`, `leader_gm` is `false` and
`gm_shortcuts` is empty. The kit check's `.bot command <bot> talents` is a bot command (the owner's), not a GM command.
**Keeping the agent near** (only when the module does not take objects by entry, see [Targets by entry](#targets-by-entry-the-agent-need-not-be-in-view): with entry targets none of this runs and `run_gm_assists` stays 0). The bots walk and the agent's own character does not, but the object steps (and the carrier's `cmd open|loot` orders) need it in view of the chest, the corpse or the door. So, in remote mode only, after each leg's walk and before an object step the driver looks at how far the agent stands from the lead bot (the agent's position against the feed's) and, over 40 yd (or on another map): a **GM** account (the `.account` level reaches the teleport level, 1) sends `.appear <lead bot>`, but never during a fight, and only when the caller's `setup` lists `gm visible off` (run it first and pass it, so the mobs cannot take the arriving agent for a target; otherwise the run notes it and does not `.appear`); any other account walks its own character (`walk_path`) to a point 30 yd short of the lead bot, only out of combat, never when a hostile the bots see stands within 25 yd of the way or the stand point, and it stops at once when a fight begins or after 60 s. **Each `.appear` is a GM assist during the run**, not part of the party's play: it is counted in `run_gm_assists`, noted, and listed in `setup_shortcuts` as `.appear <lead bot> (GM assist during the run)`. `gm_commands` does not count it (it stays the enter phase's), so a "0 GM commands" target has to read `run_gm_assists` too. `agent_walks` counts the walks. A failed `.appear` or walk is noted and does not fail the run. An object step is tried once per step, and so is a boss step (before its kill is judged). Not verified live (run 1 needed `.appear` by hand once; Glacienne stayed ~110 yd behind).

**Bringing a far bot back (`rejoin_tp_dist`).** A bot that died, was released (or revived by the recovery) and came up again stands at the entrance, or outside the instance, and would run the whole way back alone while the gathers wait for it and give up. In remote mode, inside the dungeon (a leg begun), when a **living** bot of the run (never the lead bot, never a dead one, only a bot `bots` names, else the bound party) stands more than `rejoin_tp_dist` yd (default 60; 0 turns it off) from the lead bot or on another map, the party has been out of combat for 4 s and the account is a **GM**, the commander sends `.bot cmd summon <bot>` (the module's existing order: owner or GM, the bot is teleported to the lead bot across maps within 3 s, refused while the bot is dead or in combat; TortoiseBots `HandleSummon`, `RequestSummon`), no new server command. It runs before each hop of an in-dungeon walk and in each rest gate (after a single death as well as after a wipe's recovery, once the bot shows alive), so the run does not walk on while a bot is far: it waits up to 15 s for the bot to show near the lead bot (90 s at most for the whole call), then sends `cmd follow` once (the module makes the commander the bot's master and puts it on follow when it summons: a summon with the commander outside the instance is why the *gathers* never use `cmd summon`, Uldaman hires-3 run 1; the `cmd follow` puts the party back on the lead bot at once; **not verified live**). A bot is summoned at most once per `rejoin_tp_gap_s` (60 s); a refused summon (`busy`, dead, in combat) or one that does not land is noted and left to the gathers. A non-GM account, or a game that cannot tell the account level, does nothing but note it once. **Each summon is a GM assist during the run**: counted in `rejoin_tps` and `run_gm_assists` (not `gm_commands`), listed in `setup_shortcuts` as `cmd summon <bot> (GM assist during the run)`, noted, and a trace event `rejoin_tp`. It never names anything but a bot of the run.

**Report.** `dungeon_status.report.remote`: `tune_changes` and `plan_reloads` (see [Live tuning](#live-tuning)), `mode`, `lead_bot`, `bots`, `tank`, `bound`, `unbound`, `unbind_via` (`ack`, `already unbound`, `no
answer`, ...), `ended_by_module`, `gm_commands`, `run_gm_assists`, `rejoin_tps`, `agent_walks`, `frames_received`/`frames_missed`/`frames_invalid`/`frames_stale`/`frames_truncated`/`last_seq`
(since the bind), `feed_stalls`, `orders_sent`/`orders_refused`/`orders_unanswered`, `orders` (by verb), `refused` (by `verb:code`; the details are
in the report's `refusals[]` with intent `cmd <verb>`), `move_stalls`, `walk_aggros`, the threat counters above, `pull_fallbacks`, `tank_busy_waits`, `cc_orders`,
`revives`, `rez_waits`, `rezzed`, `rezzes`, `unsafe_rez_skips`, `fled`, `flee_orders`, `flee_waits`, `flee_wait_s`, `pack_looks`, `packs_seen_empty`, `gathers`, `gather_wait_s`, `gather_timeouts`, `gather_gave_up`, `direct_gathers`, `direct_gather_wait_s`, `direct_gather_timeouts`, `direct_gather_moves`, `direct_gather_stays`, `tank_orders_held`, `tank_away_waits`, `ahead_pulls`, the tank-first counters (`tank_threat_share`, `tank_threat_samples`, `loose_add_orders`, `cc_respected`, `target_switches`), `walk_stops`, `walk_stopped_s`, `look_ahead_moves`, `fight_holds`, `object_steps_ahead`, `team`, `enter`, `preflight`, `end_stay`, `end_fight`, `unreachable`, `logged_out`. Once a fight is over for good (the loot wait past and nothing hitting the party) the lead bot is sent a `move` to the place it stands (`fight_holds`), unless it stands within 3 yd of the point of its last `cmd move` (its stay spot is already here, and the server cannot path to where a bot stands: it was refused `unreachable` about 23 times in a Uldaman run): `cmd move` sets its stay spot (BotCommands.cpp CommanderMove), and the module's "return to stay position" would otherwise walk it back to the last point it was sent to before the rest gate and the next walk. It is not sent earlier, because it resets the bot's movement and the followers' formation; the bot may therefore walk back a little during the loot wait (to be seen live). The `dungeon_step`/`dungeon_leg` events carry `data.mode: "remote"`; `dungeon_done` and `dungeon_failed` add
`remote`, `lead_bot`, `frames_received`, `orders_sent`, `orders_refused`.

**Limits.** The feed has no names for mobs (entries only), no class for bots (so a caster below 30 % mana always holds a rest gate, healer or not; `healer` names the bot that gets the healer mana target),
no loot or dead flag for hostiles (a hostile seen fighting that leaves the feed for two frames is counted dead), and the bots see only what they see: a pack
nobody has in sight is not counted dead: the lead bot walks up to its positions and looks (see the pull group row). A feed that goes quiet for 6 s makes the fight loops wait
(`feed_stalls`); one quiet for 45 s fails the run. `resolveTeam` still assumes the agent's own team for the team gate (the feed carries no race);
the preflight's faction warning only reports a hire of the other team.

### Start anywhere and the pull test

Both are remote mode, owner-approved 2026-10-14 to make a combat fix testable in minutes instead of a 40-minute run. They are for an instance
whose earlier part is already dealt with (a run that was stopped, a GM-cleared instance, a pull that is being tuned); nothing before the
start is done or checked.

**`from_leg` and `from_pull_group`.** The party is brought to the start point by the commander's own GM move, then the run goes on from there
with the ordinary per-leg logic (the rest gate at the leg's start, walks, pulls, object steps):

1. `.appear <lead bot>` when the agent's character is not on the plan's map near the lead bot (a GM enters the lead bot's instance that way; a
   `.go xyz` into the map from outside would open an instance of the agent's own);
2. `.go xyz <x> <y> <z> <map>` (the agent's own character; `session.Teleport`, which waits for the teleport and lands the agent on the navmesh);
3. `.bot summon <lead bot>`, a player command of the commander's account: `cmd summon` fetches a bot to the **lead bot**, so the lead bot has to
   come first;
   `.bot summon` makes the commander the bot's master and puts it on follow, so the lead bot would chase the GM: right after it lands the
   agent sends `cmd move <the point it landed on>` (the module's hold-this-spot order; `cmd stay` as the last try) and watches the feed for 4 s
   to see the bot stay within 4 yd of it (three tries, then the run fails);
4. `cmd summon <bot>` for every other living bot that is not with the lead bot, and a wait until all stand together.

**The commander's z (issue #292).** `.go xyz` takes the z it is given and never snaps to the ground, so a point with a wrong z put the
commander under the terrain (or in the air) and the party was brought to it. After step 2 the agent selects its own character, waits
for the server to show the selection, and sends the plain GM line `.gps` (the reply's lines are read as `KindSystem` text) and reads `GroundZ` (the top surface) and `FloorZ` (the floor under the commander's z). A
commander more than 0.5 yd from `FloorZ` gets one more `.go xyz X Y FloorZ+0.3` (booked as a GM start line) and `.gps` is read again. The
start is refused, before the lead bot is touched, with the target z, `GroundZ`, `FloorZ` and the commander's z when it still stands more
than 0.5 yd off the floor, or when `GroundZ` and `FloorZ` differ and `FloorZ` is more than 3 yd from the target z (the floor under the
commander is another layer than the point). The check is best effort: a failed or unanswered `.gps`, a reply without its X/Y, `GroundZ` or
`FloorZ`, a reply about another unit (its X/Y more than 20 yd from the commander) or one with no height data (-200000) leaves the start as
it was, with one note per run saying the z check could not be made.

The two GM lines are the only GM commands; neither is aimed at another player. They count in `remote.gm_commands` and are listed in
`setup_shortcuts` as `... (GM start point)` and in `start.gm_commands`. A party that is at the start point already (the lead bot within 20 yd,
the others within 25 yd of it) sends nothing (`start.method` `already there`); a lead bot that stands within 60 yd of one of the leg's own points (its `to`, vias, step stands, pull group positions, boss), or, with `resume`, within 80 yd (`ResumeMaxOff`) of the leg's route from the previous leg's end through its `to`, its pack positions and its boss, with the party together also sends nothing (`already in the leg`: a resume into the Zum'rah room is not sent back to the courtyard; rfc-live-2's resume into leg 4 was sent back to the leg's first point and the party summoned again, 127 s). The account must be a GM (`.go xyz` is observer level): with a
player's account the run fails before it sends anything, naming the level. Every wait has a ceiling (30 s each: the commander's arrival, three
`.bot summon` tries of a third of it each, two rounds of `cmd summon`).

- **The start point of a leg** is where the leg before it ended (its boss's position, else its `to`; the entrance for leg 0), as in the group
  driver's `teleport`.
- **The start point of a pull group** is its *gather spot*: 55 yd short of the group's first position on the navmesh path from the leg's `to`,
  moved back along the path (70, then 85 yd) until no other pack or boss of the plan is within 30 yd of it; `start.warning` says when none was.
  The run then starts at that pull group (the leg's walk and the earlier groups of the leg are skipped).
- **An unsafe start in a fresh instance is refused** (commander mode; #193, zf-g9-6cd0b13 gathered 21 yd from a live pack and wiped there
  twice): a `from_leg`, `from_pull_group` or `pulltest` start whose start point (the gather spot on the same navmesh path the move takes, or
  the leg's start point) is under 30 yd from a pack or boss of a step the run skips. The instance counts as fresh with `prepare.fresh`, and
  with `prepare.party "hire"` (new hires have no saved instance) unless the hire takes over a binding an earlier run kept and `rehire` is not
  given. The run fails before anything is sent (no prepare, bind, entry or GM line; the report's `error` says why) unless `force_start`
  is given, which runs it with a note. **In practice a fresh `from_leg` past a leg with a boss needs `force_start`**: its start point is
  where that boss stands, alive ("the boss of leg N is alive at the start point"); start at a pull group of the leg instead
  (`from_pull_group`, whose gather spot is checked the same way: on the plans of 2026-10-18 about half of the first pull groups and
  two thirds of all pull groups are refused fresh, the packs of a leg stand close together), or in a cleared or kept instance. `force` is the plan check's and does not pass
  this gate, and `force_start` does not pass the plan check. A start that is not fresh (`prepare.fresh` false, a resume, a kept hired
  binding) only warns in the report's notes: the party may have fought its way there. `start_here` moves nobody and is not checked. The
  gate covers commander mode only: a leader-mode pull test runs where the party stands, and a leader-mode `from_leg` moves only with
  `teleport`, which is not checked.
- **A step that needs something a skipped leg makes fails the start at once**, before anything is sent: the plan is read for steps from the
  start leg on that need an item (`use_object.requires_item`, `use_item` and its `requires_item`) which only a leg before the start makes (a
  boss's `loot_item`, a `loot_object`'s or `use_item`'s `expect_item`). Uldaman: from leg 1 the Gni'kiv Medallion (7740) is missing, from leg 2 the
  Staff of Prehistoria (7733). The error names the item, the step and the leg; `have_items: [7733]` (script: `--have-items 7733,7740`) says the
  party already carries it. A game-object wait whose opener is in a skipped leg (a door) is not detected: it fails at its own `timeout_s`.
  The skipped legs are not in the `legs` totals of the summary event and the trace counters.
- **What is skipped is reported as skipped, never as cleared** (driver rule 1): `report.legs[]` lists every leg before `from_leg` with `result`
  `"skipped (from_leg)"`; `start.skipped[]` has the texts, including `pull groups 0-N skipped (from_pull_group)`. The report's `start` has the
  start point, its kind (`entrance`, `leg_start`, `pull_gather`), the map, the GM lines, whether the lead bot was summoned, the bots `cmd summon`
  fetched and the seconds. A start past something the plan needs (a key, a door opened by an earlier step) fails at the step that needs it.
- `start_here: true` keeps the old `from_leg`: no move, the run starts where the lead bot stands (legs before it still reported skipped).
  `from_pull_group` does not combine with `start_here` or `resume`.
- `tools/run-uldaman.sh --from-leg N [--pull-group M] [--force-start]`; the same with any plan over the RPC. The script asks for a fresh
  instance unless `--no-fresh`, so an unsafe start is refused there: `--force-start` passes `force_start`.

**The pull test.** `dungeon_run` with `pulltest: {"leg": 2, "pull_group": 1, "tries": 3, "reset": "auto"}` (`leg` a number or a name; `tries`
default 3, at most 10; it names its own start, so `from_leg`, `from_pull_group`, `start_here` and `resume` stay out) starts at that pull group as
above and runs only that one pull, `tries` times: gather (the rest gate) -> `cmd pullback` -> the fight -> the rest gate that precedes the next
pull. Between tries the pack is brought back (`reset`):

| `reset` | What happens |
| :--- | :--- |
| `auto` (default) | an equivalent pack first, then `.respawn`; when neither is possible the scenario says `one try per spawn: <why>` and stops with the tries it has |
| `equivalent` | the next pack of the leg that has the same `equiv` label as the tested one (a `pull_groups[]` field of the plan, up to 40 characters) and was not tried yet; the party walks to its gather spot |
| `respawn` | the party walks back to the gather spot, then the commander sends its own `.respawn` (developer level; Commands.cpp `HandleRespawnCommand`: with nothing selected, it respawns **every dead or despawned creature and game object within the map's visibility distance of the commander**: other corpses, a boss the party killed, looted chests; the agent clears its selection first and waits for the server to show it cleared). A GM assist during the run: `remote.run_gm_assists`, `setup_shortcuts` (`.respawn (GM assist during the run: pulltest reset)`) and `pulltest.gm_commands`. The commander must be within 80 yd of the pack (it is: it stands at the gather spot). **It is refused** (`one try per spawn: respawn would revive boss N of leg L`) when a boss of the plan lies within 250 yd of the commander (the largest instance visibility distance; the default is 170) and the bots do not see it alive; prefer `equivalent` packs or `none` where bosses are near |
| `none` | one try |

A try is scored only when something was killed: a reset that brings nothing back ends the scenario with the try marked `no_pack` and not
scored. A wipe ends it too (the run fails with the wipe; the try is kept as `wipe`). A creature that respawns keeps its GUID, so the driver's
per-GUID memory (engagement times, openers, give-ups, kills) is cleared before each new try. The report's `pulltest` (and, for a traced run,
`<trace folder>/<run id>.pulltest.json`) has per try:

`try`, `pull_group`, `reset` (`first spawn`, `equivalent pack`, `respawn`), `outcome` (`killed`, `no_pack`, `wipe`, `failed`), `kills`,
`time_to_kill_s` (the pull order to the last kill), `seconds` (the whole try), `deaths` (+ `death_names`), `threat_overrides` (mobs the party was
sent at without the tank holding them), `off_tank_s` (mob-seconds the engaged mobs spent on a bot that was not the tank, crowd-controlled mobs
excluded) with `off_tank_mobs` and `max_off_tank_s`, `adds` (engaged mobs that were not of the pack: not of its entries, or first seen away from
its positions) with `add_entries`, `healer_min_mana_pct` (the `healer` bot, else every mana user; -1 when not measured) and `rest_s`; and a
`summary`: `tries` (scored), `killed`, means of time to kill / off-tank / rest over the killed tries, sums of deaths / overrides / adds, the
lowest healer mana.

`turtle-agent pulltest <result.json>` prints a result as a compact table; `turtle-agent pulltest --compare a.json b.json` prints b against a
(each summary number, the change, better or worse) and warns when the two are not the same pull or have fewer than 3 scored tries (the A/B rule
of the auto-improve loop: the same pull, the same party, 3+ tries each). A result file may also be a saved `dungeon_status` answer.
`tools/run-uldaman.sh --pulltest LEG:GROUP[:TRIES[:RESET]]` runs one and saves `pulltest.json` in its output folder.

**The pull test in leader mode.** `dungeon_run {"mode":"leader","brain":"frost_mage","rotation":"...","pulltest":{"leg":2,"pull_group":1,"tries":3,"reset":"auto"}}`
(or `tools/run-uldaman.sh --mode leader --pulltest 2:1:3 --rotation NAME`) scores the **agent's own character**, so `pulltest --compare` can A/B two
rotations on the same pull ([Rotations as data](#rotations-as-data), `docs/ROTATIONS.md`). The party and the character stand in the instance at the
pack (the run does not walk the plan to it; `teleport`, `resume`, `from_leg` stay out). Per try: the rest gate, the ordinary pull of the group
(the leader approaches, the party's `pullback`, the fight, with the brain playing), the rest gate. `reset`: `equivalent` walks nowhere (the pull approaches the next
pack as it does any pack); `respawn` is the leader's own `.respawn` (developer level, within 80 yd of the pack, refused when a plan boss within 250 yd is not seen alive,
counted as a GM assist); `none` one try. A brain is required (`mode: "leader"` picks the preset of the character's class). The result has the commander's
per-try numbers (`time_to_kill_s` is the first pull order to the last fight's end; `rest_s`; `deaths`, the party's and the character's) and, in `mode`, `brain` (the preset
with `+rot:<name>@<digest>`) and `character`, the character's own score per try (`tries[].leader`) and in `summary.leader`:

`damage_done`, `healing_done` (the combat log's melee, spell damage and heal events with the character as the source; the heal amount includes overhealing),
`dps`, `hps` (over the time to kill), `power_spent` (the primary power it lost in combat, in the power's own unit: the sum of the drops between brain ticks,
so regeneration hides a little) and `damage_per_power`, `healing_per_power`; `actions` and `action_counts` (what the brain sent, by action id), `errors` (game calls that failed),
`casts_failed` and `casts_failed_by_reason` (casts the server refused: `out_of_range`, `no_power`, `line_of_sight`, `not_infront`...); `combat_s`, `idle_combat_s`
and `idle_pct` (in combat with nothing running, not casting, not walking: waiting for a cooldown, power or the tank); `mob_on_me_s` and `mobs_on_me` (the threat it
took off the tank: seconds with a mob on it, and how many different mobs); `leader_deaths`, `min_power_pct`. The summary's means are over the killed tries.
`pulltest --compare` prints both sides' brain and then these rows (damage and healing per second, per power, actions, casts failed per try and per reason, idle,
mobs on me, deaths, lowest power) next to the party's; it warns when one side is a commander run, and notes when both name the same brain (that is the noise).
A try the character did not play (no brain) is noted and has only the combat log's numbers. Not checked live.

Not verified live (see docs/LIVE-CHECKS.md "Start anywhere and the pull test"): `.appear`/`.go xyz` into the lead bot's instance, `.bot summon`
of a bound lead bot, `.respawn` in an instance and the visibility distance it reaches.

### Live tuning

Fixes on the spot during a live run, without a restart: the remote driver's thresholds are named, bounded **tunables**, the plan file and a
tunables file are read again as the run goes, and every change is a trace event, so a fix made at the keyboard becomes a PR afterwards.

**Where.** The driver looks at the files and at the `tune_set` requests **once at the start of the run** (`boundary` `run_start`, before the
enter phase and the preflight, so the entry walk reads them too) and then at two places only: **before a leg starts** and **before a pull group
starts** (also the first group of a leg, after the leg's start gate). What it finds applies to the next step; nothing changes in the middle of
a walk, a fight or a wait. A file is read when its modification time or size changed. `live_reload: false` (a `dungeon_run` parameter) turns
the two file reloads off; `tune_set` still works.

**The tunables file.** JSON, one object of key to number, `<plan>.tune.json` beside the plan file (`dungeons/zulfarrak.tune.json`), or the file named
by `dungeon_run`'s `tune` or the login's `--tune FILE`. It is optional; a file named by `tune` must exist and be good when `dungeon_run` is called (an invalid one is refused there, with every problem listed; the login's `--tune` file is checked the same way when it exists). Keys that start with `_` are
comments. A key left out has the value the run was made with, so taking a key out of the file puts it back.

```json
{"_why": "the second pack patrols", "pull_standoff": 30, "pull_range": 40, "gather_radius": 25, "rest_health_pct": 95, "assist_every_s": 6}
```

Every value is checked against hard limits (rule 3 of the driver rules: no tunable can make a wait unbounded; the longest, `boss_timeout_s`, is
30 minutes, and the watchdog's levels are not tunables). One bad value, an unknown key, a number that must be whole, a file that is not JSON or a
broken rule between two values (`pull_standoff` at most `pull_range`, `rest_min_health_pct` at most `rest_health_pct`, `move_arrive` and
`look_ahead` at most `move_leg`) refuses the whole file: the old values stay, the reason is in the report's notes and in a `tune_change` event
with `ok: false`, once for that version of the file. A file that goes away changes nothing.

**Order of precedence.** `tune_set` over the file over what the run was made with. A `tune_set` stays until `tune_set key "default"`.

| Key | Unit | Limits | Default | What |
| :-- | :-- | :-- | :-- | :-- |
| `move_leg` | yd | 10 to 150 | 120 | longest single move of a walk (the server takes 150) |
| `move_arrive` | yd | 1 to 15 | 5 | how near a point the lead bot has arrived |
| `look_ahead` | yd | 2 to 40 | 10 | how near its waypoint the lead bot must be for the next move to go out |
| `look_ahead_eta_s` | s | 0.5 to 10 | 2 | the look-ahead by time |
| `move_gap_s` | s | 0.2 to 10 | 1 | least time between two move orders |
| `move_stall_s` | s | 3 to 60 | 10 | a lead bot that gained no ground this long gets the move again |
| `move_retries` | count | 1 to 10 | 4 | stalls and refusals of one walk before it fails |
| `walk_attempts` | count | 1 to 8 | 3 | refusals of one walk (on top of move_retries) before it fails |
| `lead_wait_s` | s | 5 to 120 | 30 | how long an order waits out lead-unavailable |
| `pull_range` | yd | 10 to 80 | 45 | a target farther than this from the lead bot is approached first |
| `pull_standoff` | yd | 5 to 70 | 38 | how far from the target the lead bot stops when it approaches (at most pull_range) |
| `pack_radius` | yd | 5 to 60 | 25 | how far from a pull position a pack member may stand (a group's radius wins) |
| `pull_attempts` | count | 1 to 6 | 3 | failed pulls of one group before the run fails |
| `engage_timeout_s` | s | 5 to 90 | 25 | a pull that draws no fight in this long has failed |
| `evade_grace_s` | s | 3 to 60 | 12 | a fight with nobody in combat this long is over |
| `assist_every_s` | s | 3 to 60 | 10 | how often the party is re-ordered onto the nearest mob |
| `aggro_quiet_s` | s | 1 to 15 | 3 | a fight on a walk is over when nothing has hit the party this long |
| `post_fight_s` | s | 1 to 30 | 4 | wait after the last pack member died |
| `adds_wait_s` | s | 1 to 90 | 15 | longest wait for loose hostiles after a fight |
| `fight_timeout_s` | s | 30 to 900 | 300 | one pull group |
| `boss_timeout_s` | s | 60 to 1800 | 600 | one boss fight |
| `boss_search_s` | s | 0 to 600 | 60 | a kill-boss step that sees no boss at his spot waits this long for him to walk back into range (a patrolling boss), then fails with `boss <entry> not found`; **0 turns the search off** |
| `look_standoff` | yd | 5 to 24 | 21 | how far short of a pack's position the lead bot stops to look for it (under the 25 yd of the clear evidence) |
| `attack_hold_yd` | yd | 10 to 80 | 45 | how near the mob the tank must be for its threat to count as holding it |
| `tank_busy_rounds` | count | 1 to 6 | 3 | waits of one pull for a tank that stays in combat |
| `add_orders` | count | 1 to 6 | 3 | tank orders at one loose mob |
| `fallback_tries` | count | 1 to 4 | 2 | tries of the carrier's order before the agent takes an object step over |
| `threat_wait_s` | s | 1 to 30 | 6 | longest wait for the tank to hold a mob's threat |
| `opener_delay_s` | s | 0 to 15 | 2 | how long the tank must have held a mob before the party is sent at it |
| `add_cooldown_s` | s | 2 to 60 | 8 | least time between two tank orders at the same loose add |
| `tank_busy_wait_s` | s | 2 to 60 | 10 | one wait of a pull the module refused as tank-busy |
| `rez_wait_s` | s | 5 to 120 | 30 | how long a dead bot waits for a resurrection before it is released |
| `revive_safe_yd` | yd | 10 to 80 | 30 | a dead bot is raised in place only when no live hostile stands this near the bodies; else it is released and runs back |
| `flee_pct` | pct | 5 to 50 | 20 | a fought mob at or below this health that moves away from the party is a runner: it is killed first |
| `flee_wait_s` | s | 5 to 120 | 30 | how long a pull waits for a runner that got away to come back |
| `flee_near_yd` | yd | 10 to 80 | 40 | a runner last seen within this of the lead bot is waited for before the next pull |
| `rest_health_pct` | pct | 30 to 100 | 90 | rested: every member's health at least this |
| `rest_mana_pct` | pct | 10 to 100 | 35 | rested: every other mana user's mana at least this |
| `rest_healer_mana_pct` | pct | 10 to 100 | 60 | rested: every healer's mana at least this |
| `graves_healer_mana_pct` | pct | 10 to 100 | 60 | the next grave batch waits while the healer's mana is under this |
| `rest_timeout_s` | s | 5 to 300 | 60 | longest rest gate |
| `rest_min_health_pct` | pct | 20 to 100 | 50 | after the timeout the run goes on only with this health (at most rest_health_pct) |
| `rest_pull_tank_pct` | pct | 30 to 100 | 80 | a rest gate that would give up waits (up to `rest_pull_extra_s` more) for the tank to have at least this health |
| `rest_pull_healer_mana_pct` | pct | 10 to 100 | 60 | a rest gate that would give up waits (up to `rest_pull_extra_s` more) for the healer to have at least this mana |
| `rest_pull_extra_s` | s | 5 to 300 | 90 | how long past the cap a rest gate waits for the pull floors |
| `rest_walk_yd` | yd | 10 to 500 | 60 | the walk length from which the start gate lets the party rest while it walks |
| `rest_walk_health_pct` | pct | 10 to 100 | 70 | health floor for resting on a walk |
| `rest_walk_healer_mana_pct` | pct | 5 to 100 | 40 | healer mana floor for resting on a walk |
| `rest_walk_mana_pct` | pct | 5 to 100 | 25 | mana floor for resting on a walk |
| `maintenance_every_legs` | count | 0 to 20 | 3 | `.bot maintenance` for the whole party at the start of every Nth leg and after a recovered wipe (0: never) |
| `gather_radius` | yd | 8 to 80 | 40 | the rest gate before a pull also wants every living bot this near the lead bot |
| `careful_hop` | yd | 5 to 60 | 25 | the longest single move of a walk inside the dungeon |
| `gather_wait_s` | s | 1 to 60 | 12 | how long the party is waited for to gather before a hop or a pull |
| `gather_near` | yd | 4 to 40 | 12 | every living bot within this of the lead bot counts as gathered |
| `gather_progress` | yd | 0.5 to 10 | 2 | how much nearer a bot must have come during a gather's whole wait to be waited for again |
| `aggro_guard` | yd | 8 to 40 | 22 | a hostile this near the next hop is pulled before the hop |
| `gather_clear` | yd | 10 to 60 | 30 | a hop's end (where the party gathers) stays this far from a hostile no step of this leg owns |
| `big_pack` | count | 3 to 20 | 5 | a pack of this many mobs gets one member crowd-controlled by the cc bot before the pull |
| `patrol_aggro` | yd | 10 to 50 | 25 | a patrol member this near a pull (its target or the lead bot) is a danger to it |
| `patrol_window_s` | s | 5 to 180 | 40 | a patrol is a danger to a pull when its path brings it within patrol_aggro this soon |
| `patrol_wait_s` | s | 0 to 180 | 90 | the longest wait of one pull group for its patrol to walk away |
| `patrol_locate_s` | s | 0 to 300 | 120 | the longest look of one pull group for a patrol none of whose members is in view (0: none; at most the patrol's own round plus 10 s) |
| `patrol_pull_yd` | yd | 10 to 60 | 40 | a patrol this near the lead bot, and patrol_aggro clear of the pack, is pulled alone first |
| `min_move` | yd | 0 to 10 | 6 | the shortest move the module carries out; a shorter last stretch is sent as a step to min_move+1 yd along the line |
| `ahead_max_dz` | yd | 2 to 30 | 8 | how far above or below the hop a hostile may stand to be pulled before it |
| `max_ahead_pulls` | count | 1 to 30 | 12 | pulls of hostiles near the way, at most, in one walk |
| `look_arrive` | yd | 1 to 10 | 3 | how near its stand point the lead bot must get to look at a pack |
| `party_down_dead` | count | 1 to 5 | 3 | dead bots that stop the leg |
| `rez_reach_yd` | yd | 10 to 100 | 30 | a dead bot is left to a living caster's resurrection only when its body lies this near the caster; a farther one is released at once |
| `rejoin_tp_dist` | yd | 0 to 500 | 60 | a living bot farther than this from the lead bot, or on another map, is brought to it by the commander (`cmd summon`, GM account only); 0 turns it off |
| `rejoin_tp_gap_s` | s | 10 to 600 | 60 | least time between two summons of the same bot |
| `respawn_settle_s` | s | 1 to 30 | 6 | pulltest: wait after .respawn for the pack to show |

Not tunable on purpose: the evidence radii of the clear and boss rules (25 yd, 20 %, two whole frames: rules 1 and 2 of the driver rules;
`look_standoff` stays under that 25 yd), the watchdog levels, the party, and what only runs once before the first leg (the start phase of
`from_pull_group` / `start_here`: its gather spots and waits, and the `.respawn` reach of the pull test, a server limit). A pull test reads the
tunables file before its first try and before each later try (the plan is the test, so it is not reloaded); a change between two tries is in
the report, but an A/B comparison should keep them still.

**`tune_set`.** `{"key": "pull_range", "value": 40}` (RPC `tune_set`; `"default"` or `null` puts the key back, `tune_list` shows the keys with limits, the
value now and a queued request). It is checked at once with the same limits as the file and answers `old`, `new` and where it applies; it is
taken at the next leg start or pull-group start like a file's change, with the same events. For one-off changes from the run worker.

**The plan file.** At the same two places the plan file the run was loaded from is read again when it changed. It must parse as a plan and keep
the run's name, map and number of legs, else the old plan stays (report note, `plan_reload` event with `ok: false`, once per version). What a
reload takes: at a **leg start** the whole of that leg and of the legs after it (points, `via`, pull groups, `radius`, steps, boss, `rest`, `skip`,
notes), the leg then begins again from its first point on the new plan; at a **pull-group start** the current leg's groups from the one about
to start, its `skip`, steps, boss, `after_boss`, `rest` and notes, and the legs after it. What it never takes (rule 1): legs already done, the
groups of the current leg that are cleared, the current leg's `to` and `via` once walked, the plan's header (entrance, tele, watchdog).
Groups of a leg are matched by position in the list, so edit only the groups from the current one on. The ignored paths are listed in the
`plan_reload` record's `ignored`, so a change that was not taken is not a surprise.

**Events and the report.** Each change is a trace event `tune_change` (`key`, `old`, `new`, `source` = `file`, `rpc` or `reset`, `at`, `leg`,
`boundary` = `run_start`, `leg_start` or `pull_group_start`) and a line in `remote.tune_changes[]`; each reload is a `plan_reload` event (`ok`,
`file`, `at`, `leg`, `boundary`, `ops` as JSON patch operations with the old value, `ignored`, or `error`) and a line in `remote.plan_reloads[]`.
Both also leave a note. A refused file is a `tune_change` or `plan_reload` event with `ok: false`.

**`turtle-agent tune-diff <trace.jsonl>`** turns the events of a run into JSON patches (RFC 6902) for the files: `tune_patch` has one operation per
key whose value at the end of the run is not the one it began with (`add` sets it, `remove` puts the key back to the run's own), `plan_patch` has the
reloads' operations in order (apply them to the plan file as it was when the run began), `refused` lists the files and requests the run turned
down. A live fix is then a PR: apply the patches, commit.

*Not verified live:* this was tested against the fake game and the session rig only; a run on the server has not been done with it.

## Death and the corpse run

Issue #80 step 3a. The leader is a player: when it dies it comes back the way a player does, with **no GM revive or teleport**. The
packets are the ones a 1.12 client sends (layouts checked against the core, `internal/game/death.go`); `death_status`, `accept_resurrect`,
`release_spirit`, `corpse_query`, `reclaim_corpse` and `area_trigger` are the RPC calls, the driver uses the same session calls.

1. **Resurrection first.** A resurrection spell cast at the dead leader arrives as `SMSG_RESURRECT_REQUEST` (an event `resurrect_request`
   and `death_status.resurrect`); the driver answers with `CMSG_RESURRECT_RESPONSE` (guid, 1). After the leader's death it waits up to
   60 s for that while a living member could cast one (a healer class by its unit row, or a member whose class is not in view). A
   healer bot resurrects party members by itself.
2. **Release.** Else `CMSG_REPOP_REQUEST` (no body): the player becomes a ghost (health 1, `PLAYER_FLAGS_GHOST`) at the nearest graveyard
   and gets `SMSG_CORPSE_RECLAIM_DELAY` (30 s, more after repeated deaths). `status` and `death_status` show `ghost`.
3. **Corpse run.** `MSG_CORPSE_QUERY` gives the corpse. On the ghost's map the driver walks the navmesh to it (`walk_path`; a ghost walks
   as anybody), waits out the reclaim delay and sends `CMSG_RECLAIM_CORPSE` within 34 yd (the server takes 39 yd): the player lives at 50%
   health and mana with **resurrection sickness**, which is noted in the report (`wait_sickness` waits it out, up to 11 min). A corpse
   **inside a dungeon** is reached through the entrance portal: the query then names the entrance on the outer map, the ghost walks
   there and sends `CMSG_AREATRIGGER` for the plan's `entrance_trigger` (Uldaman 286); the core revives a ghost that enters the map of
   its corpse (`Player::TeleportTo`), at the dungeon's entrance. A plan without `entrance_trigger` cannot do this and the run fails with
   that reason (the id is `areatrigger_teleport.id` in the world database). A resurrection offered on the way is accepted at once.
4. **Back.** The driver says `.bot action follow`, then runs a rest gate (the brain eats and drinks there), and the run goes on from the
   step it was on; a walk the death interrupted is made again and does not use up a try.

| Event | `source` / data |
| --- | --- |
| `resurrect_request` | the caster; `caster`, `name` (when the server sent one), `sickness` (accepting costs resurrection sickness: a spirit healer), `timer`. SMSG_RESURRECT_REQUEST |
| `corpse_reclaim_delay` | the player; `ms`. SMSG_CORPSE_RECLAIM_DELAY |
| `dungeon_leader` | the run; `phase` (see below), `leg`, and per phase `how`, `seconds`, `error`, `trigger`, `ms` |

Bounds: a corpse run longer than 10 min fails the run (`corpse run: gave up after 10 min`), the leader may die 5 times a run (every death
counts, also one during a wipe's recovery or while dead members are brought back), and an
offer that does not revive it is tried 3 times. Spirit healers are not used. The report counts `leader_deaths`, `resurrections`,
`corpse_runs` and `seconds_dead`, in all and per leg, and the `dungeon_leader` events (`data.phase`: `died`, `resurrect_accepted`,
`released`, `corpse_run`, `waiting_reclaim_delay`, `reclaimed`, `portal`, `back`, `gave_up`, `wipe_recovery`) tell the story. The
behaviour follows mod-playerbots' dead strategy (ideas only; its code is GPL and this repo is MIT).

## Bot brain

The brain (issue #80) lets the agent **play its own character** while it leads: a small trigger -> action engine in
`internal/brain`. It is off until `brain_start` (or `dungeon_run`'s `brain` option). It runs in its own goroutine, ticks
every 200 ms, and acts only through the session calls an RPC client has (`cast`, `face`, `target`, `attack`, `use_item`, and for a pet `pet_action`):
**it sends no packet a real client would not send.** It reads only what the agent knows of itself and what a 1.12 client
sees: the spell state ([Agent state](#agent-state)), the object store (units, their targets, flags and auras) and the party
list. A 1.12 client has no threat data, so the brain does not pretend to have any (see "Threat" below).

**The engine.** Every tick takes one snapshot of the game, then:
1. every active **rule** whose **trigger** is true asks for actions at a **relevance** (the band values follow the constants of mod-playerbots, GPL-2.0+; ideas and numbers only, no code: idle 0,
   default 5, normal 10, high 20, move 30, interrupt 40, dispel 50, raid 60), and each strategy's defaults ask every tick;
2. every **multiplier** scales a candidate's relevance; 0 is a **veto** (it is reported as `vetoed`, with the multiplier's name);
3. the candidates are tried by relevance. An action runs only when it is **possible** (a cast: the spell is known, off
   cooldown and the global cooldown, paid for, not backed off, and the target is alive, hostile when the action is harmful,
   and within the spell's range from the spell data) and **useful**. When it is not possible, its **prerequisites** (`Pre`:
   things to do first, such as turning to face the target) are tried, then its **alternatives** (`Alt`: a lower-rank
   buff stands in for one the mage has not learned);
4. the first action that runs ends the tick. A cast waits 300 ms for the server's `SMSG_SPELL_START` before another goes out,
   and nothing but an interrupt-band action goes out while the player casts or channels.

Values (`Value[T]`) are named, cached computations (per tick or for a TTL) that triggers share. The engine is code, the
strategies are data: Go literals naming actions and spells (`"Frostbolt"`, never an id: the highest known rank is cast).
Another class is a new strategy and a preset line (`internal/brain/mage.go` is the template), not an engine change.

**Events.** `brain_decision` for every tick that decided: `source` the player, `spell_id`, `target_guid`, `data.trigger`,
`action`, `relevance`, `result` (`ok`, `not_possible`, `vetoed`, `error`), `via` (`pre` or `alt` when the action ran as a chain
member), and `why` or `error`. An action that ran is always logged; a refused or vetoed top candidate once, then again after 3 s
if it persists. `brain_idle` (`data.reason`: `no_trigger`, `vetoed:<multiplier>`, `not_possible:<action>:<why>`,
`not_useful:<action>`, `not_ready`, `dead`) when a tick did nothing, at most once per reason per 10 s. `brain_state` on start
and stop (`data.state`, `strategies`, `role`). A rule whose action can never run is not reported at every try: an action whose spell is not
in the spellbook (a talent the character never took: Stormstrike, Death Coil, Seal of Command) is mentioned as `not_possible ... unknown_spell`
once per 5 min and is never cast; the healthstone and health potion rules are active only while the bags hold one; Battle Shout is asked for
only while the rage pays for it. `rest_drink` is off with no drink in the bags and says `no_drink` once per rest (the first tick
of a low-mana rest at a rest gate), not on every tick; a feral cat asks for Rip and Ferocious Bite only from their combo points,
and a shapeshifted player asks for no meal (it leaves its form first when it has food).

**Back-off.** A refused cast (`last_failure.reason_name`) holds its spell back for a moment so the brain does not spam it:
`out_of_range` 1.5 s, `too_close` 1 s, `no_power` 2 s, `line_of_sight` 3 s, `not_ready` 0.5 s, `not_infront` 0.4 s (and the brain turns
to face the target first), `interrupted` 0.3 s, `moving` 0.5 s, `immune` 5 s, any other reason 1 s. `bad_targets` of a cast aimed at the player itself holds 10 s (a wrong target stays wrong). A failed game call holds the
action for 1 s. The holds are in `brain_status.holds`.

**Threat.** The mod-playerbots threat multiplier reads the server's threat list, which a 1.12 client does not have. The brain's
stand-ins: (1) *Opening delay*: a harmful action waits until the fight has gone on for 2 s (the fight starts when the tank's mob
is first seen in combat; a fight is over once no mob has been there for 3 s). A mob that attacks the leader is fought at once.
(2) *The mob's target*: the brain fights the tank's target. The tank is the party member `tank` names, else the first of warrior,
paladin and druid (by the `class` of their unit row) in view whose target is a living hostile mob in combat; without one, a mob
that attacks the leader, then the leader's own selected target in combat. (3) *Area spells* (Cone of Cold) are vetoed unless every
hostile mob near the leader (12 yd) is on the tank or on the leader. (4) *The hard signal*: a mob whose target is the leader. The mage
answers it by relevance: Ice Block below 20% health (a talent: only if the spellbook has it), Blink out of melee below 50% health
when Frost Nova is not available (see below), Frost Nova when the mob is in melee (existing), a step back out of reach once the mob is
rooted, and, while the mob is still coming (over 9 yd away, not rooted), a run toward the tank so that the tank can take it (to
4 yd from the tank; not if the tank is within 8 yd already).

**Positioning** (`ranged_position`, `avoid_ground`; the walks go through `walk_path`, so they follow the navmesh). The leader stays
**20 to 28 yd** from the fight's mob (Frostbolt reaches 30, the top leaves room for the server's reach; the band is only left past
1 yd of slack), **with sight of it** and **outside the front of the mob** (a 90 degree cone in front of the mob's facing). When one of
these is wrong and no mob is on the leader, the brain looks for the nearest standing spot that meets all of them: candidate points
on rings of 22, 24 and 27 yd round the mob, each snapped to the navmesh (within 2.5 yd), reachable in a straight line over the
mesh, within 24 yd of the leader, and at least 15 yd from any mob that has not noticed the party (8 yd from one that is
fighting), then it walks there in steps of 2.5 s and decides again. A step runs in the background, so the brain keeps ticking while
the leader walks: an action at the interrupt band or above (Counterspell, Blink, Ice Block, a potion, leaving a ground effect)
cancels the step and acts in the same tick; everything else waits until the step ends (no casting on the move, no second walk). No spot is looked for more than every 0.6 s (1.5 s after a miss).
It never walks while casting (a cast of its own comes first), only an interrupt-band action may break a cast. **Ground effects**:
a dynamic object (Blizzard, Rain of Fire, a fire patch) of an enemy (its caster a hostile mob in view; or the source of a periodic
damage aura the leader carries; or, with its caster out of view, a harmful spell) that covers the leader makes it walk out along the
mesh to a point 2 yd past the edge, at relevance 42, which also breaks a cast. Effects of the party's own casters (a bot's
Blizzard, a Consecration) and of the leader are ignored.

**Line of sight.** The agent has no vmaps (the server's collision geometry), so sight is a **navmesh raycast**: Detour's walkability
ray over the polygons a player on foot may stand on, from the leader to the target. A wall, a pillar or a drop is where the mesh
ends, so the ray stops there; what the mesh does not show (a bridge over the ray, a ceiling) is not seen, and a gap or a cliff counts
as a block. The ray reaches the target if it stops within 1.5 yd of it (a unit stands on the mesh's edge as often as inside it).
The answer is cached per target for 250 ms, and until the leader or the target moves 1 yd. If the mesh cannot say (no `mmaps_dir`, a
missing tile, the leader off the mesh) the brain takes the line as clear and does not move for it.
The server's own refusal beats the mesh: after a `line_of_sight` cast failure the fight's mob counts as **known blocked** for 5 s (a
further refusal runs that on), as long as the leader stays within 3 yd of where it was refused, so `reposition` fires and looks for a
spot at least 4 yd from here, instead of the mage casting into the wall again when the 3 s back-off ends.

**Move lock.** The dungeon driver and the brain must not walk at once. `brain.MoveLock` is the arbiter: the brain takes it for each
step (and `walk` checks `no_move` and the driver's hold once more itself); the driver calls `Controller.HoldMove(ctx)` (the `dungeon.Brain`
interface's `HoldMove`) around **every call that moves the leader** and lets go right after: `walk_path` (the plan's walks, the approach to a
target, a use_object or area trigger stand point, the ghost's corpse run), the GM teleport to a leg's start, the release of the spirit
(the graveyard teleport) and the dungeon portal's area trigger; a `use_object` is held with its settle wait, the teleport across the wait for the arrival and the release across the wait for the ghost. The waits under a hold only wait: the look at the party (a dead leader's recovery) is made after the lock is let go. A hold stops a step of the brain's that is under way and keeps the brain
from walking until the release (`brain_status.move_held`); holds nest. The driver holds it for one call at a time and never across a wait,
so while a fight is fought (and between a walk and its retry) the lock is free and the brain positions the leader: `ranged_position`,
`avoid_ground`, `step_away`, `run_to_tank`, `mage_defense` all work in a run, and **a dungeon run's brain moves** (it no longer starts with
`no_move`). `HoldMove` waits for the walker to stop the step (the driver waits at most 5 s; a step that does not stop fails the walk like a
refused one), so it must not be called from inside the session's do loop: the driver calls it from its own goroutine only.
A step the brain cancels and whose walker is slow to answer leaves the lock taken for a moment: an emergency in that moment (leave a ground
effect) is answered "the brain's earlier step is still stopping" and is **not** backed off, so the next tick retries it; "the dungeon driver is
walking" means the driver does hold the lock. `Controller.Stop` waits (up to 2 s) for a step under way, so a restart finds the lock free.
A `line_of_sight` refusal marks the fight's mob as a wall only when the last cast sent aimed at that mob (the refusal names the spell, not
the target).

**Interrupts** (`mage_interrupt`: Counterspell, relevance 40, which also breaks the leader's own cast). An enemy cast shows from
`SMSG_SPELL_START` (spell, end, the unit it is aimed at). The brain interrupts, in this order: a heal or a crowd-control spell
(fear, stun, sleep, polymorph, charm, horror; by the spell's mechanic) on anyone, then a damage spell aimed at a party member or
the leader, only if the spell can be interrupted, has at least 350 ms left, and its caster is in the spell's range. A cast tried
once is left alone for 3 s; a refused call backs the spell off as any cast.

**Consumables** (`combat_consumables`). Below 25% health in combat: a healthstone, then a health potion (the best in the bags that
the level allows; not while Ice Block is up); below 45% health in combat, with no mob on the leader and no health lost for 4 s: a
bandage (the best in the bags, not while "Recently Bandaged" is up or while moving or casting; the agent does not know the First Aid
skill, so a bandage that started no channel within 1.5 s is left alone for 5 minutes). A mana potion below 10% mana when the mob
looks like a boss (3 or more levels above the leader, or 6 times its health: a 1.12 client sees no creature rank). Required levels
and cooldowns (2 minutes, shared by a group) are the core's `item_template`; the agent does not see item cooldowns, so a used
item is not used again for 2 minutes.

**Wand** (`wand`). Below 15% mana, with a mob to fight in range: `Shoot` (relevance 9, behind Frostbolt, so it takes over when the
bolt cannot be paid for). Shoot repeats by itself, so it is not sent again for 3 s or while the spell state shows a shot in the last
3 s; a server that says no wand is equipped backs it off for 30 s.

**Strategies** (`brain_start.strategies`; the `base` strategy, which stops a second cast while one is going, is always added):
- presets: `frost_mage` = `combat_dps` + `mage_frost` + `mage_rest` + (step 3b) the six below + `rest_eat`; the warrior, rogue, hunter and paladin presets are in "Roles and classes" and the priest presets in "The healer and the priest" below;
- `combat_dps`: the opening delay and the area-spell veto above, and `face_target`;
- `mage_frost`: Frostbolt (normal) at the fight's mob; Frost Nova (high) and Fire Blast (normal + 2) on a mob that attacks the
  leader within 8 yd (no Nova on a rooted mob; Nova hits everything within 10 yd, so it is an area spell for the veto above); Cone of Cold (normal + 5) with 2 or more mobs in combat within 10 yd in a 90 degree
  cone in front;
- `ranged_position`, `avoid_ground`, `mage_defense` (Ice Block, Blink, step back from a rooted mob), `mage_interrupt`,
  `combat_consumables`, `wand`: see above. The preset `frost_mage` is now `combat_dps` + `mage_frost` + `mage_rest` + these six + `rest_eat`.
  Blink lands up to 20 yd ahead of the way the leader faces and the brain has no way to turn on the spot, so it only blinks with the
  mob behind it, 12 yd of walkable mesh ahead and a landing 8 yd farther from the mob;
- `rest_eat` (any class): out of combat, with no mob to fight, and below 85% health, only where upkeep is allowed like `mage_rest`: sit
  and eat the best **food** in the bags that the leader's level allows (a table of the core's `item_template` foods that restore health
  and nothing else, each with its `required_level`; the highest tier first), one step under drinking, so a leader low on both drinks
  first. A mage with no food casts Conjure Food first, in the highest rank whose conjured item the leader's level can use (rank 7
  makes the level 55 Cinnamon Roll: a level 45 mage casts rank 6, whatever it has learned). A piece just used is not used again for 5 s; nothing is sent while the Food aura is
  awaited.
- `mage_rest`, out of combat and with no mob to fight, only where upkeep is allowed (always for `brain_start`; in a dungeon run only
  inside a rest gate, so the leader never sits down mid-walk): Ice Armor (else Frost Armor) and Arcane Intellect when missing or,
  if the server says how long they last, under a minute from ending; Evocation below 20% mana, else drinking below 65% (the best
  conjured or vendor water in the bags that the leader's level allows, from the core's `required_level`; none: Conjure Water first, in the highest rank whose water the leader's level can drink, as for food;
  a drink just used is not used again for 5 s, and nothing is sent while the Drink aura is awaited, so a refusal cannot loop).

**Dungeon runs.** `dungeon_run {"name":"uldaman","brain":"frost_mage"}` starts the brain at the first rest gate of each leg
and stops it when the leg ends; the driver keeps leading and giving the party its orders. The run does not decide whether the leader is a GM: pass `gm: false`
for a player account (see [Dungeon runs](#dungeon-runs)). A brain started by hand is not touched by a run (the run refuses to start while one runs: "a bot brain that was started by hand is running: brain_stop it first"; a run still ending says "the previous dungeon run still owns the brain"), and while a run owns the brain
`brain_start` and `brain_stop` are refused (`brain_status` still answers). The run's brain moves the leader in fights; the driver holds the [move lock](#bot-brain) around its own walks.
The report's `brain` names the strategies.

The leader's death is handled by the driver, not the brain (see [Death and the corpse run](#death-and-the-corpse-run)); a dead brain
idles (`dead`). The other classes: the holy paladin and resurrecting the party are in "The holy paladin and resurrecting the party", the warlock and the pets in their own section; rogue, hunter and retribution paladin are in "Roles and classes", the priest and the healer role in "The healer and the priest", the druid and the shaman in "The druid and the shaman"). Tested against a scripted fake game and clock (`internal/brain`) and the agent's RPC rig; the live check is in
`docs/LIVE-CHECKS.md`.

### Roles and classes (step 3c)

**Roles.** A role is a set of strategies that share a discipline; a strategy names its role, and `brain_start.role` (`Params.Role`) is
checked against the active strategies: left empty it takes the role they play (`dps` when none names one), a role that does not fit
them is refused (`role "tank" does not match the strategies [...], which play dps`), more than one role among the strategies is
refused. `healer` needs the `role_healer` strategy (presets `holy_priest`, `holy_paladin`, `resto_druid`, `resto_shaman`); `tank` needs `role_tank` (`tank_warrior`, `bear_tank_druid`).
- `role_dps` (the name of `combat_dps`): wait out the opening delay, veto area abilities while a mob is loose, fight the tank's mob.
- `role_tank`: the player holds the mobs. The fight's mob is **a loose add first** (a mob in combat within 30 yd that attacks a party
  member), then the nearest mob that attacks the player, then the player's selected target in combat. There is no opening delay. A loose add is answered with the
  class's taunt at relevance 25 (`taunt`: Taunt, Defensive Stance only; Growl, Bear Form only, for the druid; the class table also has Hand of Reckoning for
  the paladin, which has no tank preset yet). **A tank only fights mobs that are already in combat; it never opens a pull** (Charge works in
  Battle Stance only, and nothing in the role walks up to a sleeping mob): the dungeon driver or a companion tank bot pulls, the
  tank player picks the mob up when it is in combat and attacks a party member or the player, or the player has it selected.
- `role_healer`: the player heals the party; see "The healer and the priest" below. Its place in the class tables is the class's `Heals` row (a kit of heal, HoT and
  dispel spell names): the role is the same for every healing class, the class is a row.

**Class tables** (`internal/brain/class.go`). What the brain needs to know of a class that is not a rotation: the power it plays on, the
spells it interrupts with (and the stances those work in), whether it drinks and uses mana potions, whether it conjures its own water
(mage), its taunt. A strategy asks for the leader's class at the tick instead of being written for one class: the interrupt strategy
`class_interrupt` is Counterspell for a mage, Pummel (Battle or Berserker Stance) then Shield Bash (Battle or Defensive Stance) for a warrior; `mage_rest` and
`combat_consumables` take their drink and mana-potion tables from the leader's class (the mage's conjured waters are the mage's row;
a warrior drinks nothing, a priest only vendor drinks). `mage_interrupt` is the same strategy under the mage's name, and `frost_mage`
is unchanged (the existing tests are its proof). An unread class (id 0) acts as a mage. The rogue (Kick), paladin (Hammer of Justice) and shaman (Earth Shock)
rows are used by `combat_rogue`, `ret_paladin` (step 4b) and `elemental_shaman`, `enhancement_shaman` (step 4c); the priest's Silence is used by `shadow_priest`. The brain **does not change stance for an interrupt**: a stance change costs a warrior
its rage (without Tactical Mastery) and Pummel costs rage. It uses the highest rank the stance allows: the core's data lets Pummel 6552 work in
Battle and Berserker Stance and the Turtle rank 13491 in Battle Stance only (`SpellStances`), Shield Bash in Battle and Defensive Stance; a
tank in Defensive Stance has Shield Bash only (it needs a shield: the server's refusal backs it off).

**Melee** (`melee_position`; the swing is in `warrior_melee`). The melee strategies share:
- **Auto attack** (`auto_attack`, relevance 19): `target` the fight's mob and `attack` when the mob is within 8 yd and the swing is not on
  it already. The swing is read off the event log (`attack_start`/`attack_stop`, through the optional `brain.Melee` half of the game,
  like Sight; the first look also reads an attack that began before it); a game without it remembers its last order and repeats it every
  6 s (the server ignores an attack order on a unit already attacked). The order is **never sent twice within 3 s**, whatever the game
  shows: auto attack is a toggle the server keeps on, and an order repeated every tick shut out `face_target` and every ability (a
  live warrior sent 1015 in 205 s). The same holds for `hunter_swing` and the druid's, paladin's, shaman's and rogue's swing. It waits out
  the opening delay like every attack of a damage dealer.
- **A swing the server refused for facing** (`attack_error` `bad_facing`, read through the optional `brain.SwingWatcher` half of the
  game; the session counts the refusals in its event log): the swing is not ordered again, and the rule `swing_refused_facing`
  (relevance 19.5, above the swing) runs `face_target` at once, for 3 s or until the player faces the mob. The server resumes the
  auto attack by itself once the player faces the mob.
- **Positioning** (`melee_reposition`, relevance 30): a damage dealer stays within 6 yd (2D) of the fight's mob and **out of its front**
  (the 90 degree cone it faces: its cleave, breath, tail swipe). When one of these is wrong and no mob is on the player, the brain looks for the
  nearest spot on rings of 3.2, 4 and 4.6 yd round the mob that is **behind it** (at least 100 degrees round from the way it faces), has sight of
  it over the navmesh, is not in a ground effect and 10 yd from mobs that have not noticed the party (mobs that are fighting do not
  keep it away: a melee fighter stands among them), then walks there in steps like the mage's. A mob out of range of the search (over 24 yd) is run at in a straight line. A game with a walker
  but no navmesh gets the point behind the mob without a search (`canWalk` instead of `canMove`: the walker has its own route). It does not walk
  while `no_move` is on or the driver holds the move lock, and it does not walk to a spot when Charge or Intercept is about to fire (below).
- **A tank** (role `tank`) stays within 6 yd and turns the mob **away from the group**: when the mob is on the tank and faces the middle of the party members in view
  (within 60 degrees), the tank walks to the far side of the mob from the group (rings of 2.6, 3.2 and 4 yd, at least 110 degrees round from the
  group's side), so that the mob turns to face away.
- **Gap closers** (`warrior_melee`/`warrior_fury`, relevance 32, above a walk): **Charge** out of combat in Battle Stance, **Intercept**
  in combat in Berserker Stance, at a mob 8 to 25 yd away, only when the tank holds the mob and the opening delay is over, never into an untanked pull
  (the multiplier `gap_closer_needs_tank`; a tanking player may, and so may a player a mob has pulled already: it attacks the player). While one of them is about to fire, the fighter does not walk the distance.

**Warrior** (`internal/brain/warrior.go`). Spells are named; the highest known rank is cast. Rage and rage costs are in tenths.
Stances are cast by id (the spellbook also holds the stances' passive spells under the same names). The abilities are checked against
the core's spell data in `TestWarriorSpellData`, and the stance each ability works in is the core's `spell_template.stances` of every rank
(tortoise-wow 5d79486; `warriorStances`, pinned by `TestStanceGatesFollowTheCoreData`). Thunder Clap, for one, works in Battle **and** Defensive
Stance there, all six ranks, so the tank claps without a stance change. The relevance is the band the rule asks for.

| Strategy | Ability (relevance) | When |
| :--- | :--- | :--- |
| `warrior_melee` | Auto attack (19) | a mob within 8 yd, not swinging at it yet |
| | Execute (18) | the mob below 20% health; Battle or Berserker Stance |
| | Charge (32) / Hamstring (12) | see gap closers; Hamstring on a mob at or below 25% health that is moving, within 7 yd |
| | Bloodrage (13) | a fight mob within 12 yd, below 20 rage and above 50% health |
| | Battle Shout (12 out of combat at a rest gate, 8 in combat) | the aura missing or, if the server says how long it lasts, under a minute left. It costs 10 rage, so out of combat it only goes while some rage is left |
| | Cleave (11) / Heroic Strike (10) | from 50 rage on; Cleave with 2 or more mobs within 8 yd, and the area veto allows it. A sent Heroic Strike or Cleave holds both for 1.5 s (the server queues it for the next swing and the rage is spent then) |
| `warrior_arms` | Overpower (17) | the fight's mob dodged an attack within the last 4.5 s; Battle Stance. The dodge comes from `melee` events and `spell_miss` events |
| | Mortal Strike (16) | on cooldown |
| | Rend (14) | the mob has no Rend and is above 35% health; Battle or Defensive Stance. The server's `immune` refusal (undead, elementals, mechanicals bleed no) backs it off 5 s |
| | Sweeping Strikes (13) | 2 or more mobs within 8 yd, in Battle Stance, area veto allowing it |
| | stance home (12) | back to Battle Stance |
| `warrior_fury` | Bloodthirst (16), Whirlwind (15) | on cooldown; Whirlwind in Berserker Stance, one mob or more (the area veto applies) |
| | Berserker Rage (45) | a fear or horror aura (by the aura spell's mechanic) on the leader; Berserker Stance |
| | Intercept (32) | see gap closers |
| | stance home (12) | Berserker Stance; Battle Stance below level 30, when it is not known |
| `warrior_tank` | Revenge (16) | the leader dodged, parried or blocked an attack within 3.5 s; Defensive Stance |
| | Thunder Clap (15), Demoralizing Shout (13) | 2 or more mobs within 8 yd, some without the debuff |
| | Sunder Armor (14) | up to 5 stacks on the fight's mob |
| | Shield Block (12) | a mob on the leader within reach and health below 90% |
| | stance home (12) | Defensive Stance |

**Stance policy.** Each spec has a home stance and leaves it for nothing: no dance for Pummel, Charge or Intercept. The brain goes home
when it is out of its stance and the change is cheap: out of combat, or with 25 rage or less. A stance cast is not repeated within 2 s.
An ability names the stances it works in; in a stance that does not allow it, it is simply not possible (Fury has no Overpower and no
Rend, a tank no Pummel).

**Presets.**
- `arms_warrior` = `combat_dps` + `warrior_arms` + `warrior_melee` + `melee_position` + `avoid_ground` + `class_interrupt` + `combat_consumables` + `rest_eat`.
- `fury_warrior` = the same with `warrior_fury` for `warrior_arms`.
- `tank_warrior` = `role_tank` + `warrior_tank` + `warrior_melee` + `melee_position` + `avoid_ground` + `class_interrupt` + `combat_consumables` + `rest_eat`.
  A tank needs Defensive Stance (level 10) and a shield; the brain does not check the shield (a Shield Bash or Shield Block without one is refused by
  the server and backed off).
  The tank role changes what the dungeon driver should do (the party's tank bot then has a second tank in the party); this step only makes the
  brain able to tank, it does not change the driver's orders.
The warrior presets end with `rest_eat` (eat below 85% health at a rest gate), as `frost_mage` does.

Examples: `brain_start {"strategies":["arms_warrior"],"tank":"<name>"}`, `dungeon_run {"name":"uldaman","brain":"fury_warrior"}`,
`brain_start {"strategies":["tank_warrior"]}`. In a dungeon run the brain moves too (the driver holds the move lock around its own walks), so the melee
positioning works there as well.

### The healer and the priest (step 4a)

`brain_start {"strategies":["holy_priest"]}` / `dungeon_run {"name":"uldaman","brain":"holy_priest"}` play the leader as a healer;
`shadow_priest` is the damage preset of the same class. The healer role is written once (`internal/brain/heal.go`) and takes its spells from
the class row (`ClassInfo.Heals`, a `HealKit`: direct heals, the quick one, the HoT, the dispels), so the other healing classes are rows
(step 4b). Ideas from mod-playerbots' healing (the `hp% + distance/10` triage, the health and mana bands, dispel by type; GPL-2.0+,
numbers and ideas only, no code). Amounts come from the spell metadata (`internal/spells` effects: the core rolls base+1 to base+sides, so the average is base + (sides+1)/2, or base+1 for an effect with no die; then the level
scaling), without the player's bonus healing, which the agent cannot read.

**Triage** (`role_healer`). Every online, living party member **in view, the player included**, within 38.5 yd and in sight (the navmesh
ray, the 250 ms line-of-sight cache; a server refusal for line of sight is remembered for a few seconds, as for a mob) is scored
`hp% + distance/10`, minus 8 for the tank; the lowest score is healed first. The hp% counts the heals other party members have in
flight on the member (read off their cast), and a member is a candidate below 85% in a fight (the tank below 90%), below 90% at a
rest gate (the dungeon driver's rested threshold), and below 70% (tank 75%, never below 25%) when mana is short. Relevance by band:
under 25% (the tank under 35%) 41, which also cuts a walk short but never the heal that is being cast; under 45% 28; under 65% 20;
above that 10, or 8 at a rest gate so that drinking comes first. Nobody in reach but a member under 70% out of reach: `heal_approach` walks
toward it (the spot search covers 24 yd of walking).

**The spell** is chosen by the deficit: all known ranks of the kit's direct heals (ranks more than 30 levels under the player are left
out, the highest of each name is always in), the **smallest that covers 90% of the deficit**, else the biggest. A member under 45%
gets the quickest that covers it (else the best health per second), under 25% (tank 35%) the biggest Flash Heal. A fast heal is not
used for a member who is not low, nor under 40% mana unless the member is under 35%. **No heal is started** on a deficit under half of
the smallest heal in play, and damage taken in the last 3 s is added to the deficit for the time the heal takes (up to 30% of the
member's health).

**HoT.** Renew on the tank in combat, without it, under 98% health or losing health, with 40% mana or more (relevance 9.5, under every heal).

**Dispels** (`dispel_cc` 25, `dispel_debuff` 11). A harmful aura on a member whose spell has a dispel type the class removes: Dispel Magic
for magic, Abolish Disease (else Cure Disease) for disease; no poison or curse cure for a priest. Crowd control (charm, fear, sleep, stun,
polymorph, horror) first, then the tank, then the lowest health. Urgent dispels need 20% mana, any other 40%. The member must be within **the dispel's own range** (30 yd, less a yard), not the heal's: for crowd control out of
that range the healer walks into it (`heal_approach`), for a minor debuff it does not. One try per aura per
4 s (it may resist), nothing while Abolish Disease is already on the member, and **Mutating Injection is never dispelled** (the do-not-dispel list, as in
TortoiseBots).

**Position** (`healer_position`). In a fight with a tank in view and no mob on the player: 20 to 30 yd from the tank (a 3 yd slack),
with sight of the tank, **out of the fight's mob's front and of ground effects**, and, when the mesh allows, with sight of every other member in
view (else of the tank alone). It ranks at 24 under the low-health heals and walks like the mage's steps (`no_move`, the move lock and a missing
navmesh switch it off). A mob that picks the healer is brought to the tank (`run_to_tank`, as for the mage). The walk to the band is **not taken when it would
take the healer out of reach (34.5 yd) of the member it has work for** (a heal, a dispel, or a member it walked toward): otherwise it would walk back before the heal is cast.

**Sitting** (engine level, `stand.go`, for every class). The core refuses every cast of a seated player (`not_standing`) and does not stand it up. While the Food or Drink
aura is up and nothing is going on, no cast is sent (buffs, top-up heals, dispels wait for the end of the meal). A cast in a fight, or of relevance 28 or more
(a heal under 45% health), breaks the meal: the brain stands the player up (`CMSG_STANDSTATECHANGE`, the optional `Stander` half of the game, `Session.Stand`) as that tick's action
`stand_up`, and the cast follows on the next. A player who sits with no meal to finish (the aura ended) is stood up for any cast. `not_standing` is also a 1 s back-off.

**Rest.** `rest_drink` (the healer drinks below 80% mana at a rest gate; the driver wants 75%; a damage dealer below 65%) and `rest_eat`.
`combat_consumables` drinks a mana potion below 10% mana in a boss fight. Below 15% mana the wand takes over (`wand`).

**Priest** (`internal/brain/priest.go`; spell data pinned by `TestPriestSpellData`).

| Strategy | Ability (relevance) | When |
| :--- | :--- | :--- |
| `role_healer` | Lesser Heal, Heal, Greater Heal, Flash Heal (10 to 41) | triage above |
| | Renew (9.5), Dispel Magic / Abolish Disease / Cure Disease (25, 11) | HoT and dispels above |
| `priest_holy` | Power Word: Shield (26) | a member that lost a quarter of its health within 3 s and is under 85%; 20% mana at least |
| | Power Word: Shield (21) | the tank, once it has a living mob as its target (the pull) or a fight is on, with 50% mana or more |
| | | not on a member with a shield or **Weakened Soul**, and not again for 6 s while the aura is awaited |
| `priest_defense` | Fade (23) | a mob has the priest as its target, in combat |
| | Psychic Scream (43) | 2 or more mobs on the priest within 8 yd, the priest under 70% health, and **no loose mob** within 30 yd (one that is not in combat, or fights someone outside the party: a feared mob runs into it) |
| `priest_buffs` | Inner Fire (13), Power Word: Fortitude on the priest and every member in view (12) | out of combat at a rest gate, 30% mana or more, not in Shadowform (a Holy spell). The shadow priest buffs first and takes the form when nothing is missing (in a fight the form does not wait); the brain cannot leave the form, so a buff that runs out while in it is not renewed |
| `priest_shadow` | Shadowform (16, cast by id 15473) | known and not on; out of combat at a rest gate or in a fight |
| | Vampiric Embrace (15), Shadow Word: Pain (14) | on the fight's mob; not on a mob under 30% / 25% health |
| | Mind Blast (13), Mind Flay (10) | on cooldown (8 s); the filler; Smite (when not in Shadowform) when Mind Flay is not known or out of reach (20 yd) |
| | Power Word: Shield on the priest (24) | a mob on the priest, under 80% health, not in Shadowform |
| `class_interrupt` | Silence (40) | a talent; the class row's interrupt, for `shadow_priest` only |

Presets: `holy_priest` = `role_healer` + `priest_holy` + `priest_defense` + `priest_buffs` + `rez` + `healer_position` + `avoid_ground` +
`combat_consumables` + `wand` + `rest_eat` + `rest_drink`; `shadow_priest` = `combat_dps` + `priest_shadow` + `priest_defense` +
`priest_buffs` + `ranged_position` + `avoid_ground` + `class_interrupt` + `combat_consumables` + `wand` + `rest_eat` + `rest_drink`. The shadow priest stands
14 to 19 yd from the mob (the class row's `Band`; the mage's is 20 to 28) because Mind Flay reaches 20 yd. `dungeon_run`'s `brain` option takes either.
The dungeon driver's own orders are unchanged: a priest leader in a TortoiseBots party that has its own healer bot has two healers; the triage
counts the other's heals in flight, so they do not heal the same member twice. Resurrecting a dead member came with step 4e (`rez`). Not in this step: Prayer of Healing,
Divine Spirit and Shadow Protection, and every other class (step 4b).

### Rogue, hunter and retribution paladin (step 4b)

Three more damage dealers as presets, each a role-`dps` strategy (`combat_dps` discipline: the opening delay, the area veto) plus the shared
parts above. The rotations are ideas from mod-playerbots' rotation tables (GPL-2.0+; ideas and numbers only, no code); every spell is checked
against the core's spell data in `TestRogueSpellData`, `TestHunterSpellData` and `TestPaladinSpellData` (id, name, level, cost, range, power,
flags, cooldown categories, and the aura names the brain looks for). None of these abilities has a stance or form in the core's `spell_template`
(`stances` and `stancesNot` are 0 for every rank, tortoise-wow 5d79486; the data tests pin that the brain gives them no gate and the class table
gives Kick and Hammer of Justice none), so there is no stance table here as there is for the warrior. Energy is in whole points, mana in
points; relevance is the band the rule asks for.

**Rogue** (`combat_rogue` = `combat_dps` + `rogue_combat` + `melee_position` + `avoid_ground` + `class_interrupt` + `combat_consumables` + `rest_eat`).
It stands behind the mob like the warrior.

| Ability (relevance) | When |
| :--- | :--- |
| Auto attack (19) | a mob within 8 yd, not swinging at it yet |
| Slice and Dice (18) | 2 or more combo points on the mob, the mob above 40% health, and the buff missing or with under 1.5 s left (when the server says how long). **Cast at the mob that holds the combo points**, not at the rogue: the core's spell has an explicit enemy target (effect 1 `TARGET_UNIT_ENEMY`, the finisher needs its combo target) and the aura goes on the caster by the second effect; a cast at the rogue's own guid is refused with `bad_targets` |
| Eviscerate (17) | 5 combo points, or 4 when the mob is at 35% or less. The points count only when they are on the fight's mob (`combo_target`) |
| Sinister Strike (15) | the builder, below 5 points (it does not overshoot) |
| Kick (40, `class_interrupt`) | a heal, crowd-control or damage cast within 5 yd |
| Feint (20) | a mob within 8 yd has the rogue while a tank is in view; its own 10 s cooldown spaces it |
| Evasion (43) | a mob on the rogue within 8 yd and health below 50% |
| Vanish (44) | a mob on the rogue and health below 25%. A threat wipe that leaves the rogue stealthed until its next swing or Sinister Strike: the last resort of the ladder, there is no opener after it |
| Sprint (42) and the run to the tank (33) | a mob on the rogue, health below 30%, the tank in view more than 8 yd away: `flee_to_tank` walks to a point 4 yd from the tank |

Not in this step: Stealth openers (they need a walk up to a sleeping mob; a tank or the dungeon driver pulls), Backstab (it needs a dagger, which
the agent cannot tell from the equipment) and poisons (applying one needs a target item; `Game.UseItem` takes none).

**Hunter** (`marksman_hunter` = `combat_dps` + `hunter_marksman` + `hunter_position` + `pet` + `avoid_ground` + `combat_consumables` + `rest_drink` + `rest_eat`).
Ranged, 8 to 35 yd; the first 8 yd are the dead zone. Auto Shot is spell 75 started like the mage's wand: one `cast` at the mob, and the server ignores a
second start while it runs but restarts the
ranged timer at 500 ms, so the brain does not repeat it: it takes Auto Shot as running while it sees a shot (the spell state's last cast, within 6 s)
and sends it again after a walk, a refusal, a feint, a new mob, or when no shot was seen for 6 s.

| Ability (relevance) | When |
| :--- | :--- |
| Auto Shot (19) | a mob in range, and Auto Shot not running (see above) |
| Hunter's Mark (18) | the mob has none and is above 50% health |
| Multi-Shot (17) | 2 or more mobs in combat within 10 yd of the target, 20% mana or more; the damage dealer's area veto and a veto of its own (a mob near the target that is not on the tank or the hunter) hold it back |
| Serpent Sting (16) | the mob has none, is above 40% health, 20% mana or more |
| Arcane Shot (15) | on cooldown, 20% mana or more |
| Concussive Shot (32) | a mob on the hunter, moving, 9 to 30 yd off, not slowed yet |
| Feign Death (41) | a mob on the hunter within 15 yd while a tank is in view. The auto-repeat spell is switched off first (`CMSG_CANCEL_AUTO_REPEAT_SPELL`, the optional `AutoRepeatCanceller` half of the game; the server keeps Auto Shot through the feint and the next shot would end it). The shots and the swing wait 6 s, then Auto Shot is sent anew |
| Swing (19), Raptor Strike (16) | a mob on the hunter within 6 yd (no tank, or Feign Death not ready); Raptor Strike is held 1.5 s like Heroic Strike |
| Aspect of the Hawk (12) | no aspect up (a Cheetah or Pack in combat is replaced); Cheetah and Pack are never cast and left alone out of combat |
| Aspect of the Viper (20) | in combat below 20% mana, back to the Hawk from 60%. Turtle has it from level 56 (`45651`); without it the hunter drinks out of combat (`rest_drink`) |

`hunter_position` (relevance 30) keeps the hunter between 9 and 32 yd (2D) from the fight's mob with sight of it and out of its front within 14 yd;
the standing spots are searched 12 to 29 yd from the mob like the mage's band. A mob on the hunter is the aggro response's business (Concussive
Shot, Feign Death, `run_to_tank`, the melee kit). A server refusal for ammo (`no_ammo`, `need_ammo`) holds the spell back for 30 s
(`brain_status.holds`, the decision log says `held:no_ammo`); the ammo in the quiver is not in the backpack list, so there is no count to warn on.

**The pet** is the shared `pet` strategy, described under [Pets](#pets) below; the hunter's preset carries it. A game that cannot drive a pet (no `PetGame`
half) plays the hunter without it, as before step 4d.

**Retribution paladin** (`ret_paladin` = `combat_dps` + `paladin_retribution` + `melee_position` + `avoid_ground` + `class_interrupt` + `combat_consumables` + `rest_drink` + `rest_eat`).
It stands behind the mob.

| Ability (relevance) | When |
| :--- | :--- |
| Seal (18) | none up and the fight's mob within 15 yd (also during the opening delay: it is a buff). Seal of Command, else Righteousness; Seal of the Crusader instead when the mob has no Judgement of the Crusader and is above 60% health and the paladin has 50% mana or more; Seal of Wisdom instead below 30% mana unless the mob has Judgement of Wisdom. A seal that is up is never swapped |
| Auto attack (19) | a mob within 8 yd |
| Judgement (17) | a seal is up, the mob within reach; its own 10 s cooldown. It spends the seal, so the seal is cast again |
| Crusader Strike (16) | on cooldown |
| Consecration (14) | 2 or more mobs within 8 yd, 40% mana or more, and the area veto allows it |
| Hammer of Justice (40 as `class_interrupt`, 42 as an emergency) | a cast to stop within 10 yd; or a mob on the paladin in melee, health below 45%, the mob not stunned already |
| Divine Protection (44) | a mob on the paladin and health below 35%. It pacifies the paladin: attacks wait while the aura lasts |
| Lay on Hands (50) | in combat below 20% health |
| Mana floor | below 30% mana only a Seal of Wisdom is judged (its judgement gives mana back); below 15% no seal but Wisdom is cast and Crusader Strike waits. The swing and the free abilities go on; `rest_drink` and the mana potion are the way back |
| Aura (13) | Retribution Aura, else Devotion Aura, when the aura is not up. A companion paladin's Devotion does not count for the Retribution (they stack); out of combat only at a rest gate |
| Blessing (12) | out of combat at a rest gate, on the paladin itself: Blessing of Might, else Kings, when neither is up, or the one it carries when under a minute is left. The buff's caster is not visible, so a blessing from a companion paladin counts as ours and is not cast over |

The companion paladins judge too: the brain never judges over their debuffs on purpose (the Crusader and Wisdom seals above are the only ones
that leave a debuff, and both look at the mob first). Drinking is `rest_drink` (any mana class: the vendor drinks of the class table, below 65%
mana, out of combat at a rest gate). Not in this step: Exorcism and Holy Wrath (undead and demons only, and a 1.12 client sees no creature
type), blessings for the party, Redemption, Righteous Fury and Hand of Reckoning.

**Warlock** (`affliction_warlock` = `combat_dps` + `warlock_affliction` + `warlock_upkeep` + `pet` + `ranged_position` + `avoid_ground` + `combat_consumables` + `wand` + `rest_eat` + `rest_drink`).
Ranged: the class row's band is 18 to 26 yd from the mob (Drain Life and Fear reach 20 yd, Shadow Bolt 30). The rotation is ideas from mod-playerbots' warlock
behaviour (GPL-2.0+; ideas and numbers only), the spells are pinned to the core's data in `TestWarlockSpellData`; no warlock spell has a stance or form.
A cast with a cast time (Shadow Bolt, Corruption, Drain Life, Drain Soul, Fear) waits until the warlock stands still.

| Ability (relevance) | When |
| :--- | :--- |
| Corruption (16) | the mob has none and is above 30% health |
| Curse (15) | **one curse per mob, and never over another**: a mob with any `Curse of ...` aura (the aura does not say whose it is, so another warlock's counts) gets none. Curse of Agony on a mob above 40% health; Curse of the Elements instead on a mob above 60% when two or more other casters (mage, warlock, priest, shaman, druid) are in the group |
| Drain Soul (14) | the mob is at 25% or less, the bags hold fewer than 5 soul shards (item 6265), and a kill of the mob still gives experience (a grey mob gives no shard: the 1.12 gray-level formula) |
| Shadow Bolt (10) | the filler |
| Drain Life (18) | the warlock is below 45% health and the mob above 15% (within 20 yd, so inside the band's near half) |
| Life Tap (17) | in combat below 25% mana with 60% health or more; out of combat below 40% mana with 85% health or more, before the drink (`rest_drink`) |
| Death Coil (43) | a mob on the warlock and health below 40% |
| Fear (42) | the same mob and health below 35%, **only with no tank in view** and no unengaged mob within 30 yd: a feared mob runs into the next pack, so in a dungeon group it is never cast |
| Shadow Ward (41) | a mob in combat is casting a shadow spell at the warlock |
| Demon Armor (12) | out of combat at a rest gate when it is down (Turtle names the old Demon Skin ranks 1 and 2 "Demon Armor"; "Demon Skin" is tried second) |
| Create Healthstone (11) | out of combat at a rest gate, 40% mana or more, when the bags hold no stone of the best tier the warlock can make or a better one, and two shards (one stays for the demon; one is enough with the demon out) |

The demon is the `pet` strategy (below): Summon Voidwalker when the spellbook has it and the bags hold a shard, else Summon Imp (no shard). The strategy
`pet_imp` in place of `pet` asks for the Imp first, which is how a spec that wants Firebolt and Blood Pact picks it; the one preset so far is
affliction, which takes the Voidwalker. Not in this step: Immolate, Siphon Life, Health Funnel, Soulstone, Banish, Enslave, the other demons (the Succubus and
the Felhunter need a shard each and have no rules), soul shard use beyond the demon and the stone.

## Pets

The player's pet (a hunter's pet, a warlock's demon) is controlled the way a client does it, through the pet's action bar. Packet layouts are verified
against the core (tortoise-wow 5d79486, `src/game`; the cites are in `internal/game/pet.go`).

**What the agent knows.** The pet is the unit the player's `UNIT_FIELD_SUMMON` names (`Unit::SetPet`, `Objects/Pet.cpp:401`; the field is public,
`Player::InitVisibleBits`, `Objects/Player.cpp:5107`); `Store.SummonOf` reads it and `Store.PetOf` falls back to a pet-class creature in view that names the
player as its summoner, creator or charmer (`Store.Owners`). The pet's health, power, target and combat flag are its ordinary unit row; a hunter's pet plays
on focus and carries its **happiness** in `UNIT_FIELD_POWER5` (0 to 1,000,000; a third each for unhappy, content and happy, `Pet::GetHappinessState`) and its
loyalty in byte 1 of `UNIT_FIELD_BYTES_1`. The **action bar** comes from `SMSG_PET_SPELLS` (`Player::PetSpellInitialize`): ten buttons (attack, follow, stay,
four spells, aggressive, defensive, passive), each a u32 with the action in the low 24 bits and the type in the top byte (`0x07` command, `0x06` reaction,
`0xC1` spell with autocast on, `0x81` autocast off), the pet's reaction and command state, and its cooldowns; `SMSG_PET_MODE` switches the bar on or off (a
mount turns it off). The core does not repeat the state when an order changes it, so the agent applies a sent order to its copy and the next
`SMSG_PET_SPELLS` corrects it. A pet that is gone clears the bar with an empty `SMSG_PET_SPELLS` (`Player::RemovePetActionBar`).

**Events.** `pet_summoned` (source: the pet; `data.entry`, `data.dead`), `pet_died` (health reached 0: a hunter's pet stays as a corpse until revived, a demon is
gone in 15 s), `pet_revived`, `pet_dismissed` (the summon field was cleared: dismissed, replaced, unsummoned or despawned; `data.was_dead`), `pet_error`
(`SMSG_PET_CAST_FAILED`: `spell_id`, `data.reason`; or `SMSG_PET_ACTION_FEEDBACK`: `data.feedback` `pet_dead`, `nothing_to_attack`, `cant_attack_target` or `no_path`).

**RPC.** `pet` and `pet_action` (the table above). `pet_action` sends `CMSG_PET_ACTION` (u64 pet, u32 button, u64 target) for the commands, the reactions and a
spell cast from the bar, and `CMSG_PET_SPELL_AUTOCAST` (u64 pet, u32 spell, u8 state) for the autocast actions; a cast uses the button type the bar has, so
a spell with autocast on goes out as `0xC1` and one with it off as `0x81`. Examples: `pet_action {"action":"attack","target":"0xF130..."}`,
`pet_action {"action":"cast","name":"Growl","target":"0xF130..."}`, `pet_action {"action":"autocast_off","name":"Growl"}`. Not sent: `CMSG_PET_ABANDON`
(it deletes a hunter's pet) and the dismiss command; `CMSG_PET_CAST_SPELL` is not needed (the bar's button casts).

**The `pet` strategy** (`internal/brain/pet.go`; the hunter's and the warlock's presets carry it; a game without the `PetGame` half has no pet and every rule stays
quiet). All of its orders cost no cast and no global cooldown.

| Rule (relevance) | When |
| :--- | :--- |
| Attack (20) | the pet is out, alive, not told to stay, and the fight's mob is in combat and **held**: it attacks the tank (the fight's tank), the player or the pet, or no tank is in the fight. The damage dealer's opening delay (2 s, counted from the start of the fight) holds the order like it holds the spells, so the pet never goes in before the tank has the mob, and a mob that is not in combat is never ordered at: **the pet does not pull**. One order per mob (the pet's own target shows it, and the order is not repeated within 2.5 s). A pet order is no cast: the `busy` veto (a cast in progress, the settle after one) and the wait for a walk do not hold it (`Action.Free`), so it goes out within a tick of the opening delay's end (a live hunter's order was vetoed `busy` through its fights) |
| Recall: `follow` (16) | the fight is over (no mob to fight, the player out of combat) and the pet still has a target. Not repeated within 3 s. A pet told to `stay` is left where it is: an explicit order beats automation |
| Recall: `follow` (32) | the leader is fleeing: moving in combat with the fight's mob (or the one the pet chases) more than 40 yd off. While that holds no attack order goes out either, so the pet is not sent back in; it is sent again once the leader stops. A hurt leader with a mob on it is not fleeing: the pet stays on that mob |
| Defensive (13) | in a group, a pet on `aggressive` is put on `defensive`, once per pet (a player who sets it back is not fought). A pet on `passive` or `defensive` is not touched, and a solo player's pet keeps its setting |
| Taunt off (13) | with a tank in view (the class order warrior, paladin, druid, or `Params.Tank`), the autocast of the pet's Growl, Torment, Suffering is switched off, once per pet and spell. Solo it stays on. It is not switched back on |
| Summon (14) | **hunter**: no pet out, out of combat at a rest gate, Call Pet (a refusal `no_pet`, a hunter without a pet, backs it off for a minute). **warlock**: Summon Voidwalker (needs a soul shard) else Summon Imp. Not again within 12 s of a cast |
| Revive Pet (15) | the hunter's pet is dead, out of combat at a rest gate (a 10 s cast costing the whole base mana; it is not cast in a fight) |
| Mend Pet (22) | the hunter's pet is within 20 yd and below 50% health in combat or 70% out of it, and not mending |

Not in this step: **Feed Pet.** The item-targeted cast exists now (`game.BuildCastSpellOnItem`); what is missing is the pet's diet: the food has to match the pet's
creature family, and the agent has no food-by-diet table, so it cannot pick the item. `pet` shows `happiness_state`, and the hunter feeds by hand. A pet that goes
unhappy deals less damage and, at the lowest loyalty, can leave. Also not: Growl on purpose when no tank is there (it stays on by default), the pet's own spells
(Cower, Dash, Firebolt, Torment are the pet's autocast or the owner's `pet_action cast`), Dismiss Pet, Tame Beast, and a pet in a fight against a crowd-controlled mob.

Examples: `brain_start {"strategies":["combat_rogue"],"tank":"<name>"}`, `brain_start {"strategies":["marksman_hunter"]}`,
`brain_start {"strategies":["affliction_warlock"]}`, `dungeon_run {"name":"uldaman","brain":"ret_paladin"}`.

### The druid and the shaman (step 4c)

Both classes come as presets over the roles that exist: `resto_druid` and `resto_shaman` are `role_healer` with their class's kit
(`ClassInfo.Heals`, `internal/brain/heal.go`), `bear_tank_druid` is `role_tank`, the other four are damage dealers. The rotations are ideas from
mod-playerbots' druid and shaman behaviour (GPL-2.0+, ideas and numbers only, no code); every spell name, level, cost, range, cast time, form mask and totem slot is checked
against the core's spell data (tortoise-wow 5d79486) in `TestDruidSpellData`, `TestFormGatesFollowTheCoreData`, `TestShamanSpellData` and
`TestTotemSpellsFollowTheCoreData`. Spells are named and the highest known rank is cast, except the shapeshift forms and the totems, which are cast by id (their
names are also those of passives and of the auras the totems cast).

**Kits.** What the healer role takes from the class row. Two small additions to the kit serve these classes: `DirectOnly` (Regrowth is chosen by what it heals *at once*; its
heal over time is left out of the amount) and `Multi` (Chain Heal is a candidate only while two or more members in reach are hurt, and then wins over a single-target heal that covers
the deficit equally).

| Class | Direct heals (fast) | HoT | Dispels |
| :--- | :--- | :--- | :--- |
| Druid | Healing Touch, Regrowth (Regrowth) | Rejuvenation on the tank, as a priest keeps Renew | Abolish Poison (leaves its cure on the target 8 s, as Abolish Disease), Cure Poison, Remove Curse |
| Shaman | Healing Wave, Lesser Healing Wave (Lesser Healing Wave), Chain Heal with 2 or more hurt | none (the 1.12 shaman has no heal over time) | Cure Poison, Cure Disease |

**Druid forms** (`internal/brain/druid.go`). A shapeshift form is an aura, like a warrior's stance. The core's rules (`SpellEntry::GetErrorAtShapeshiftedCast`) are what the presets follow, and
they are in the data: every heal, cure, Mark of the Wild and Innervate carry `SPELL_ATTR_NOT_SHAPESHIFT` (barred in every form); Moonkin Form (stances bit `0x40000000`) is additionally barred for the heals by `stancesNot`;
Wrath, Moonfire, Starfire, Insect Swarm, Thorns and Hibernate carry the flag that lets them be cast without a form and `stances` `0x40000000`, so they work in caster form and in Moonkin Form; the feral abilities need their form: Claw, Shred,
Rake, Rip, Ferocious Bite and Tiger's Fury Cat (`0x1`), Maul, Swipe, Demoralizing Roar, Growl, Enrage, Frenzied Regeneration and Bash Bear or Dire Bear (`0x90`), Faerie Fire (Feral) any of the three (`0x91`).
The abilities name their forms (`druidAbilityForms`, pinned by `TestFormGatesFollowTheCoreData` against the `stances` of every rank), and an ability in the wrong form is simply not possible.
**The core also refuses every item used in a form** (`SPELL_FAILED_NO_ITEMS_WHILE_SHAPESHIFTED`, `SpellHandler.cpp`): food, drink, potions, bandages and healthstones.

- *Leaving a form* is `CMSG_CANCEL_AURA` for the form's aura (`Session.CancelAura`, the optional `brain.AuraCanceller` half of the game, like `Stander`); a game without it never leaves a form. It is not a cast: no mana, no global cooldown, and the action holds itself for 2 s while the aura goes.
- *Entering a form* is a cast of the form's spell **from caster form** (Cat Form 768, Bear Form 5487, Dire Bear Form 9634, each 35% of the base mana): from another form the unit's `power` is energy or rage, so the brain could not tell whether the mana is there. The brain reads the mana pool of a shifted druid off `Unit.Mana`.
- *`druid_caster`* (resto, balance): in any form, `leave_form` at relevance 42, above an emergency heal (41), and a multiplier vetoes every cast while the aura is there (`vetoed:caster_form_only`).
- *`druid_form_cat` / `druid_form_bear`* (feral, bear): `enter_form` (22) when a fight is on, or out of combat at a rest gate when nothing else is to be done (not at a meal, no Mark of the Wild to give, not hungry or thirsty); the bear takes Dire Bear Form when it has it. **In combat the druid never leaves
  its form**, whatever its mana or health, and a multiplier vetoes the item actions (`eat`, `drink`, `healthstone`, `health_potion`, `bandage`, `mana_potion`) while it is in one. Out of combat, at a rest gate, it leaves the form (`leave_form`, 14) to **drink** (mana under 50% and a drink in the bags), to **eat**
  (health under 85% and food in the bags) or to **buff** (Mark of the Wild missing on someone and mana 60% or more, because the shift back costs a third of it), and shifts back after. A form that is not its home form (Travel, Aquatic, Moonkin, Tree, or Bear for a cat) is left out of combat.

| Strategy | Ability (relevance) | When |
| :--- | :--- | :--- |
| `druid_buffs` | Mark of the Wild (13) on the druid, then on each member in view | out of combat at a rest gate, caster form, 30% mana or more; the aura missing or under a minute left (Gift of the Wild counts) |
| | Thorns (12) | the same, on the tank only. `druid_buffs_feral` (cat and bear) has no Thorns: it is barred in a form |
| `druid_innervate` | Innervate (23) | on the druid itself in a fight, below 20% mana, caster form (barred in every form); level 40, a 6 minute cooldown |
| `druid_balance` | Moonfire (15), Insect Swarm (14, if known) | on the fight's mob when it lacks the aura and is above 35% / 40% health; instants, so also on the move |
| | Starfire (11) | not on the move, 35% mana or more, the mob above 30% health; else Wrath |
| | Wrath (10) | the filler, not on the move |
| | Entangling Roots (26) | a mob on the druid within 10 yd, the druid under 80% health, the mob not rooted; **outdoors only** (`SPELL_ATTR_OUTDOORS_ONLY`: indoors, in a dungeon, the server refuses it and the refusal backs it off 30 s). A rooted mob that stays on the druid is stepped away from (`step_away`, as for the mage's Frost Nova) |
| `druid_cat` | Auto attack (19) | as the warrior's |
| | Rip (18) | 4 combo points or more on the mob, no Rip on it, the mob above 40% health |
| | Ferocious Bite (17) | 5 combo points, or 3 or more on a mob at or below 35% health |
| | Faerie Fire (Feral) (16) | the mob lacks it and is above 50% health (it costs nothing) |
| | Tiger's Fury (15) | energy 60% or more, the mob above 40% health, not up |
| | Rake (14) | the mob lacks the bleed, is above 50% health, under 5 combo points |
| | Shred (13), Claw (12) | builders; Shred only from **behind** the mob (more than 100 degrees round from the way it faces, a margin inside the half plane the server wants), Claw instead otherwise or when Shred is out of energy |
| `druid_bear` | Auto attack (19) | |
| | Swipe (15), Demoralizing Roar (13) | 2 or more mobs within 8 yd (Roar: some without the debuff) |
| | Enrage (12) | rage under 10, health above 50%, a mob in reach |
| | Maul (10) | from 40 rage, once per swing (held 1.5 s) |
| | Frenzied Regeneration (43) | health under 35% and 10 rage or more |
| `role_tank` | Growl (25) | a loose add; Bear Form only |

Combo points are the player's own (`combo_points`/`combo_target`), counted only when they are on the fight's mob. **Never used:** Prowl, Pounce and Ravage (no stealth play), Hibernate (a sleeping mob that wakes pulls the pack), Moonkin Form, Tree Form, Travel Form and Aquatic Form, Rebirth, Tranquility, Gift of the Wild, Mangle, Bash and Challenging Roar.

Presets: `resto_druid` = `role_healer` + `druid_caster` + `druid_buffs` + `druid_innervate` + `rez` + `healer_position` + `avoid_ground` + `combat_consumables` + `rest_eat` + `rest_drink`; `balance_druid` = `combat_dps` + `druid_caster` + `druid_balance` + `druid_buffs` + `druid_innervate` +
`ranged_position` + `avoid_ground` + `combat_consumables` + `rest_eat` + `rest_drink`; `feral_cat_druid` = `combat_dps` + `druid_form_cat` + `druid_cat` + `druid_buffs_feral` + `melee_position` + `avoid_ground` + `combat_consumables` + `rest_eat` + `rest_drink`;
`bear_tank_druid` = `role_tank` + `druid_form_bear` + `druid_bear` + `druid_buffs_feral` + `melee_position` + `avoid_ground` + `combat_consumables` + `rest_eat` + `rest_drink`. The balance druid keeps the mage's 20 to 28 yd band (Wrath and Starfire reach 30).

**Shaman** (`internal/brain/shaman.go`).

| Strategy | Ability (relevance) | When |
| :--- | :--- | :--- |
| `shaman_shield` | Water Shield (13) for a healer that has it, else Lightning Shield (13); 8 in combat | out of combat at a rest gate with 25% mana, or in a fight with 50% when the aura (charges used up) is gone |
| `shaman_imbue` | Windfury Weapon, else Flametongue Weapon, else Rockbiter Weapon (12) | out of combat at a rest gate, 30% mana: **a cast at the weapon in the main hand** (`CMSG_CAST_SPELL` with `TARGET_FLAG_ITEM` and the weapon's guid: `Session.CastOnMainHand`, the optional `brain.WeaponImbuer`). The imbue is remembered and renewed 5 minutes before its hour (the spell's base points are 3599) is up, once at the start of a run; since step 4e the weapon is also read (below), so a weapon swap or a lost imbue is seen. A refusal (`item_gone`: no weapon) is tried again after a minute. The off hand is left alone |
| `shaman_elemental` | Flame Shock (15) | the mob lacks it, is above 40% health, 30% mana or more; instant, also on the move |
| | Chain Lightning (13) | 2 or more mobs within 12 yd of the fight's mob, not on the move, and `combat_dps`'s area veto allows it (no mob near the shaman that is not on the tank or the shaman) |
| | Lightning Bolt (10) | the filler, not on the move |
| `shaman_enhancement` | Auto attack (19), Stormstrike (17) | in reach; Stormstrike on its 8 s cooldown |
| | Flame Shock (15), Earth Shock (13) | Flame Shock on a mob above 50% health; Earth Shock only above 60% mana (the shocks share a 6 s cooldown, so it is the filler between Stormstrikes) |
| `class_interrupt` | Earth Shock (40) | elemental and enhancement: a heal, crowd-control or damage cast aimed at the party, within 20 yd |
| `role_healer` | Healing Wave, Lesser Healing Wave, Chain Heal (10 to 41), Cure Poison / Cure Disease (25, 11) | triage above |

**Totems.** A totem is a self cast that fills the slot of its element and unsummons what stood there: the spell's effects 87 (fire), 88 (earth), 89 (water) and 90 (air) are the four slot effects, and the spell needs the element's **totem tool in the bags**
(Earth Totem 5175, Flame Totem 5176, Water Totem 5177, Air Totem 5178; `SPELL_FAILED_ITEM_GONE` otherwise: the brain looks in the bags first, and a refusal backs the spell off 60 s). There is no Totemic Recall in 1.12, so "recalling" a totem is dropping the next
one. The totem manager (`shaman_totems_*`) keeps its own record of each slot (spell, place, time, and the lifetime the spell's data gives: Searing Totem 30 to 55 s, Mana Spring Totem and Healing Stream Totem 60 s, the earth and air totems 2 minutes) and drops a totem when
a fight is on (the player in combat or a mob to fight), the tank in view is within 30 yd, the mob is above 25% health, the mana is 25% or more, and the slot is **empty, expired, more than 30 yd from the player (the party has moved on), or its creature has gone** (a creature whose name holds the totem's name and "Totem"
within 8 yd of the drop; one whose name is not known yet counts as ours, so a slow name query cannot make the shaman drop the same totem again and again), never twice into one slot within 10 s, one per tick, in the order fire, air, water, earth. The preset decides the totems (the first of each list the spellbook has):
`resto_shaman` Mana Spring Totem (else Healing Stream Totem) and Stoneskin Totem, at relevance 7, under every heal; `elemental_shaman` Searing Totem and Mana Spring Totem, at 11; `enhancement_shaman` Searing Totem, Windfury Totem (else Grace of Air Totem) and Strength of Earth Totem, at 11. **Ghost Wolf is never cast.**

Presets: `resto_shaman` = `role_healer` + `shaman_shield` + `shaman_totems_resto` + `rez` + `healer_position` + `avoid_ground` + `combat_consumables` + `rest_eat` + `rest_drink`; `elemental_shaman` = `combat_dps` + `shaman_elemental` + `shaman_shield` + `shaman_totems_elemental` +
`ranged_position` + `avoid_ground` + `class_interrupt` + `combat_consumables` + `rest_eat` + `rest_drink` (the elemental shaman stands 14 to 19 yd from the mob, the class row's `Band`, because the shocks reach 20 yd); `enhancement_shaman` = `combat_dps` + `shaman_enhancement` + `shaman_shield` + `shaman_imbue` + `shaman_totems_enhancement` + `melee_position` + `avoid_ground` + `class_interrupt` + `combat_consumables` + `rest_eat` + `rest_drink`.
`dungeon_run`'s `brain` option takes any of them. Not in this step: Gift of the Wild, Prayer-style group heals, Earth Shield, Water Breathing, Reincarnation, off-hand imbues, Moonkin Form. Resurrecting a dead member (Rebirth, Ancestral Spirit) and the holy paladin came with step 4e, together with the three follow-ups of this step's review (see there).

### The holy paladin and resurrecting the party (step 4e)

The last healer class, and the resurrection every healer class shares. Spell names, levels, costs, ranges, cast times, reagents and form masks are checked against the
core's spell data (tortoise-wow 5d79486) in `TestHolyPaladinSpellData` and `TestRezSpellData`; the rotation is ideas from mod-playerbots' paladin behaviour (GPL-2.0+, ideas and
numbers only, no code).

**`holy_paladin`** (`internal/brain/paladin_holy.go`) = `role_healer` + `paladin_holy` + `rez` + `healer_position` + `avoid_ground` + `class_interrupt` + `combat_consumables` + `rest_eat` + `rest_drink`.
The kit (`paladinHeals`): **Holy Light** (9 ranks, level 1 to 60, 2.5 s) is the big heal and **Flash of Light** (7 ranks, level 20 to 60, 1.5 s) the quick and dear one, which the
healer role keeps for a member who is low (under 25%, the tank 35%); **Cleanse** (level 42: magic, poison and disease) else **Purify** (level 8: poison and disease) dispel. In this
data Flash of Light heals through a script effect (effect 77, the core's `spell_paladin_flash_of_light`), not a heal effect, so `heal.go` counts it as a heal by name (`scriptHeal`).

| Strategy | Ability (relevance) | When |
| :--- | :--- | :--- |
| `paladin_holy` | Blessing of Wisdom / Might / Kings / Salvation on the paladin and every member (12) | out of combat at a rest gate, 30% mana, the member within 28 yd and in sight. **By role**: the tank gets Kings, else Might (**never Salvation**); healers and mana casters (priest, paladin, shaman, druid, mage, warlock) Wisdom, else Kings, else Salvation; warrior, rogue and hunter Might, else Kings, else Salvation |
| | | **A member that carries any blessing (or Greater Blessing) is left alone**: the caster is not visible, a companion paladin's blessing counts as the blessing and is never cast over. The paladin's own is renewed when the server says under a minute is left (aura rows carry no caster, so a companion's recast of the same blessing is not told apart). A blessing is not sent twice to one member within 6 s |
| | Concentration Aura, else Devotion Aura (13) | in a fight or at a rest gate, when **no paladin aura** at all (Concentration, Devotion, Retribution, Sanctity, a resistance aura) is on the paladin: a companion paladin's aura stays |
| | Lay on Hands (45) | in a fight, on the member or the paladin with the least health under 15%, in heal reach (38.5 yd) and sight |
| | Hand of Protection (44) | in a fight, on a member who is **not the tank and not the paladin**, under 40% health, with a mob hitting it in melee and either two on it or a quarter of its health lost within 3 s; not on one with Forbearance. (The data's name for the Blessing of Protection is Hand of Protection) |
| | Divine Shield (43) | the paladin under 15% health in a fight with a mob on it or a quarter of its health lost within 3 s, no Forbearance. **In this data Divine Shield cuts the damage the paladin does (-61%) and does not pacify or silence**, so the heals go on; the check is `TestHolyPaladinSpellData` |
| | Divine Favor (42) | when the heal of the tick is an emergency and the spellbook has a castable Divine Favor; while its aura is up the big heal is chosen instead of the quick one (`HealKit.NoFast`). **The five Divine Favor ranks of this data (51312 to 51316) are talent passives and are never cast**, so today this is dormant |
| `class_interrupt` | Hammer of Justice (40) | a heal, crowd-control or damage cast aimed at the party within 10 yd: a stun breaks the cast. The class row's interrupt |
| `paladin_judge` | Seal of Wisdom (below 80% mana, else Seal of Light) (7), Judgement (8) | **off by default, not in the preset** (`brain_start {"strategies":["holy_paladin","paladin_judge"]}`): 60% mana or more, nobody needing a heal, the fight's mob held by the tank and within 9 yd (Judgement reaches 10 yd, the healer's band is 20 to 30 yd from the tank, so it fires for a mob that stands near the paladin) |

**`rez`**, the shared resurrection (`internal/brain/rez.go`), in the `holy_priest`, `holy_paladin`, `resto_shaman` and `resto_druid` presets. Priest **Resurrection** (level 10, 75% of the base mana), paladin
**Redemption** (12, 80%), shaman **Ancestral Spirit** (12, 90%): 10 s casts, 30 yd, **out of combat only** (the spells are barred in a fight) and, in the brain, only at a rest gate (or a brain
started by hand) with no one in the party in combat (relevance 14). Druid **Rebirth** (level 20, 85%, 2 s, a 30 minute cooldown, a reagent: Maple Seed 17034 to Ironwood Seed 17038) is the other way round: **only in a fight, only
on a key member** (the tank, a healer class), only when the spellbook has it and the bags hold the reagent, never out of combat (relevance 26, under an emergency heal). The brain bars the spells in the forms the data
bars them in (Resurrection in Shadowform, Rebirth in Moonkin Form: `stancesNot`).
- **Who**: a party member in view that is dead with its body where it fell (`Dead`; a released spirit is not in view of the living, and the module's own resurrection chooses corpses the same way), within 28 yd and in sight; healers
  first, then the tank (the one seen last, the named one, else a warrior when no tank is in view), then the others, the nearest first. A body in view 28 to 40 yd away, or behind a wall, makes the healer walk into reach (`rez_approach`, a band spot, relevance 13; at a rest gate only, and
  not while the driver holds the move lock). **One at a time**: the 10 s cast is the brain's only cast (no second cast starts before it ends), and a member it sent a resurrection to is not offered another for 14 s unless the server refused the cast. A member who stays dead and keeps declining gets **three offers and is then left alone for 5 minutes**.
- **Pending from another healer**: a member for whom anyone in view is casting a resurrection (the cast's target as `SMSG_SPELL_START` names it, read off the caster's unit) is skipped.
- **Mana**: a resurrection costs most of the pool, so while a member waits and the spell is not affordable the healer **drinks to the top** (`rest_drink`'s threshold becomes 100% instead of 80%).
- **What a TortoiseBots bot does with the offer** (checked in the module, not live): a dead companion gets `SMSG_RESURRECT_REQUEST` and the dead strategy's `resurrect request` trigger runs `accept resurrect` (`CMSG_RESURRECT_RESPONSE`, accept) at once, with no
  delay or condition but being dead (`AcceptResurrectAction`, relevance +3 over the dead strategy). A bot that holds the Dungeon activity lease **does not release its spirit** while a living priest, paladin, shaman or druid of the group is on its map, or the crew is in combat (`AutoReleaseSpiritAction`), so
  a healer leader finds the bodies where they fell. A bot that did release (a wipe, or no living resurrector then) is a ghost and out of this brain's reach: that is the driver's corpse run.
- **The dungeon driver** (`internal/dungeon/rez.go`): at a rest gate, when a member is dead and the leader's brain `CanRez()` (its class has a resurrection in the book and it saw a dead member in view a moment ago, **including one whose resurrection is on its way**: the brain's own cast in its 14 s hold, or another healer's in flight), the gate **waits for the brain's resurrection** up to
  `Config.BrainRezWait` (120 s, from the first dead member) instead of a GM revive, the corpse runs of `recover_wipe`, or the "dead and GM shortcuts are off" failure; the rest timeout does not run meanwhile. A brain started at this very gate has not looked yet, so the first "cannot" is given one poll to change. Members the brain brought back get their buffs back as a revived member does. When the wait is over the old answers apply. A leader that is not a healer (or a
  druid, who cannot resurrect at a gate) is unchanged. Only the optional `dungeon.RezBrain` half is read, so other brains are unaffected.

**Follow-ups of the review of #102.**
- A **totem is recorded when its spell goes off** (the spell state's last cast, `SMSG_SPELL_GO`), not when the cast is sent: a refused or lost cast leaves no record, so the slot is filled again after the refusal's hold instead of the 10 s totem gap. A cast that neither went off nor failed within 4 s is given up.
- The **weapon imbue reads the weapon** (the optional `brain.MainHandReader`; `objects.Item.TempEnchant`, the id word of `ITEM_FIELD_ENCHANTMENT` slot 1, `Session.MainHandItem`): it imbues again when the main-hand item is another one than the imbued one, or when the temporary enchantment reads 0
  (10 s after a cast at the earliest: the item's update takes a moment) once it has been seen set. If the field never shows (the server does not send it to the client), the brain falls back to the 5-minutes-before-the-hour timer and still notices a weapon swap.
- **Mark of the Wild on a member it keeps failing on** (out of reach through a wall, a refusal) is dropped after two refusals in a row for 60 s, so a feral druid no longer shifts out of its form at every gate for a buff that does not land. The count goes when the member carries the buff.

Not in this step: Blessing of Light, Sanctuary, Freedom and Sacrifice, Holy Shock, Righteous Fury, auras beyond Concentration and Devotion, Greater Blessings, Divine Intervention, a corpse-object target for a released spirit.

## Vendors, trainers and quests

Every request here is sent at once and answered by events; each method also takes `wait_ms` and then returns the first
event that answers it, as `gossip_hello` does (`event` is null when nothing came in time; read `events` from `since`
for the rest). The NPC must be within reach: the core answers a request from too far away with nothing, or with a
refusal event. These packets follow vanilla 1.12 as the core sends them (`ItemHandler.cpp`, `NPCHandler.cpp`,
`QuestHandler.cpp`, `GossipDef.cpp`).

| Event | `source` / data |
| --- | --- |
| `vendor_list` | the vendor; `items[]` (`index`, `item_entry`, `display_id`, `stock` (-1: unlimited), `price` in copper, `durability`, `buy_count`), `empty` |
| `vendor_buy` | the vendor; `item_entry` (from the oldest purchase asked of that vendor), `count`, `stock`, `slot` (the vendor's own line number; the server skips lines you cannot use, so it is not `index`) |
| `vendor_buy_failed` | the vendor (none when out of reach); `item_entry`, `code`, `reason` (`cant_find_item`, `item_already_sold`, `not_enough_money`, `seller_dont_like_you`, `too_far`, `item_sold_out`, `cant_carry_more`, `rank_required`, `reputation_required`). A full bag is an `inventory_error` instead |
| `vendor_sell` | the vendor; `item_guid`, `item_entry`, `bag`, `slot`, `count` sold, `money` gained in copper. Inferred: the core sends no packet for a sale, so the agent reports it when the item has left its slot and every bag (or its stack shrank) and the character's money has gone up, within 5 s of the request. Nothing is reported while the character's own object is missing from the store (after a teleport to another map) |
| `vendor_sell_failed` | the vendor (none when out of reach); `code`, `reason` (`cant_find_item`, `cant_sell_item`, `cant_find_vendor`, `not_your_item`, `unknown`, `only_empty_bag`), `item_guid`, `item_entry` |
| `trainer_list` | the trainer; `trainer_type` (0 class, 1 mounts, 2 tradeskills, 3 pets), `greeting`, `declared` (only when the core's header counted more spells than it wrote), `spells[]` (`spell_id`, `state` learnable, unavailable or known, `cost`, `level`, `req_skill`, `req_skill_value`, `req_spell_1`, `req_spell_2`, `primary_profession`) |
| `trainer_trained` | the trainer; `spell_id` |
| `trainer_failed` | the trainer; `spell_id`, `code`, `reason` (`unavailable`, `not_enough_money`, `not_enough_skill`) |
| `spell_learned` | the player; `spell_id`. SMSG_LEARNED_SPELL: a spell the server taught (an item's learn effect, such as a bridle's mount spell, or a quest). The #61 run got none for `train` (turtle-agent#62) |
| `quest_list` | the NPC; `greeting`, `quests[]` (`id`, `icon`, `level`, `title`) |
| `quest_details` | the NPC; `quest_id`, `title`, `details`, `objectives`, `can_accept`, `choices[]`, `rewards[]` (`item_entry`, `count`), `money` (signed: negative is what you pay), `reward_spell` |
| `quest_request_items` | the NPC; `quest_id`, `title`, `text`, `money` asked, `items[]` asked, `completable` |
| `quest_offer_reward` | the NPC; `quest_id`, `title`, `text`, `choices[]`, `rewards[]`, `money`, `reward_spell` |
| `quest_accepted` | the NPC; `quest_id`, `slot`, `complete`. Inferred: the core sends no packet for an accept; the agent reports it when the quest appears in the log (within 5 s), and nothing while the character's own object is missing from the store |
| `quest_rewarded` | the NPC the reward was asked of; `quest_id`, `xp`, `money` (signed), `rewards[]` |
| `quest_abandoned` | `quest_id`, when the quest leaves the log (within 5 s; inferred, as for `quest_accepted`) |
| `quest_error` | `error`: `invalid` (the core refused: `reason` such as `low_level`, `already_on_quest`, `inventory_full`), `failed_at_giver` (a hand-in failed: `quest_id`, `reason`) or `log_full` |
| `quest_update` | `update`: `kill` (`quest_id`, `entry`, `count`, `required`; `target` is the victim), `item` (`item_entry`, `added`: the amount picked up just now, not the total held (the core sends the difference); the packet does not name the quest; `quest_log` `counts[]` holds the totals), `complete`, `failed`, `timer_failed` (`quest_id`) |

How to use them:

- **Vendors.** `vendor_list` asks a vendor NPC directly; a vendor reached through a gossip option (`icon` 1) answers
  `gossip_select` with the list. `buy` takes the item entry (the list needs not be read first; the server checks its
  own). `sell` picks the item as `equip` does and refuses a count above the stack. Only one sale can be in flight: a second `sell` fails until the first has produced `vendor_sell`/`vendor_sell_failed` or timed out (5 s), because the core sends nothing for a sale and its money could not be attributed. Money that arrives meanwhile (loot) while a real sale is in flight is still counted in that sale. A refused buy or sale arrives as a
  failed event; a sale the server accepted has no answer of its own (see `vendor_sell` above).
- **Trainers.** `trainer_list` then `train` with a `spell_id` from the list. A `learnable` spell can be bought; the
  server decides the rest.
- **Quests.** A quest giver answers a hello with a gossip menu (its quests in `data.quests`), a `quest_list`, or at once
  with the one quest's panel. The panels follow the client: a quest you are not on is `quest_query` (details), then
  `quest_accept`; a quest you are on is `quest_complete`, which shows `quest_request_items` and, when you hold the
  items, presses "Complete Quest" for you so the `quest_offer_reward` follows; then `quest_reward` with the `choice`.
  `quest_accept`, `quest_complete` and `quest_reward` work without the earlier steps too, since the core checks each
  one. `quest_reward` is checked against the last offer seen for that NPC and quest (a `choice` is needed when it
  has several). `quest_abandon` clears the quest's log slot (`quest_log`). A refusal the core answers with nothing
  (an NPC that does not give that quest closes the window with `gossip_complete`) leaves `event` null.

## Movement

`walk_to` knows no terrain: it walks a straight line of at most 40 yards and 30 s, and the z you give is taken as it
is. `walk_path` walks the server's navmesh instead (see [Navmesh walking](#navmesh-walking)); both send the same
movement packets. The agent answers the server's movement orders the way the 1.12 client does:

- speed changes, root and unroot, teleports and far teleports are acked with the server's counter;
- a knockback is acked with the jump values the server sent, then flown as a ballistic arc (500 ms heartbeats) that
  lands with `MSG_MOVE_FALL_LAND` on the ground (see [Falling and knockbacks](#falling-and-knockbacks)).
  `status.falling` is true in the air, and `walk_to` and `face` are refused (with the time left) until the landing.
  Session events `knocked_back` and `landed` bracket the flight. `walk_to` is refused the same way while rooted or
  moved by the server (fear, charm);
- water walking, hover and feather fall are acked with their apply flag and kept in later movement. Feather fall holds
  a knockback's arc to 7 yd/s up and down, the server's bound under it; applied or removed mid-flight, it restarts the
  arc from where it is, at the speed it has, from the ack on.

Every walk and facing packet is checked against the server anticheat's distance bound for the time since the last one
(run speed across and up or down, so no step can trip `OVERSPEED_Z`); a walk that would break it stops where the server
has the player. The client time never runs backwards and is never 0. It follows the wall clock's pace, not the monotonic clock's
(which runs 3-4% fast on a WSL2 host), and a walk is timed on that same client time, so a walk of any length keeps both the
anticheat's step bound and its clock-desync bound (`ClockDesync`: more than max(5 s, 500 ms a minute) ahead of the server). While a
walk goes the client time may slow down to half the monotonic pace but speeds up by 2% at most (a wall clock that is ahead, after a
suspend or a forward step, is caught up with slowly): the heartbeats stay 500 ms of real time apart, and a loop stall must not put two
moving packets more than 1000 ms of client time apart (`SKIPPED_HEARTBEATS`) or make the leader walk faster than its speed.

`status.z_unknown` is true when the z is a guess: after a GM teleport the agent found no floor under (no navmesh, or
none within reach), after a `walk_to` that changed height, and after a knockback that landed without a navmesh floor.
It sticks until the server moves the player itself: a server spline (fear or charge) or a teleport settles x, y and z
(a teleport's z is again checked against the navmesh), and a reconnect starts from the server's saved position. The
core sends the player no other position of its own. A `walk_path` leg that ends on the navmesh floor, and a fall or
knockback that lands on one, settle it too, as the mesh knows the ground. Keep `walk_to` walks short and on flat
ground, or use `walk_path`.

### Falling and knockbacks

With `mmaps_dir` set (and a cgo build, see [Navmesh walking](#navmesh-walking)), the agent flies a knockback's arc,
and a fall, against the server's navmesh, the floor the server's own pathing uses, as a client flies it against the
terrain:

- the physics are the core's (gravity 19.29 yd/s², terminal speed 60.148 yd/s, 7 yd/s under feather fall,
  `Movement/spline/util.cpp`, `Movement.cpp:874-881`); the arc is sampled every 50 ms against every navmesh surface
  under it (ground, water, steep slopes), and lands where it meets one: sooner and higher up a slope, later and lower
  down one. A surface that appears above the arc, out of a slope's reach from the ground it was over, is an obstacle:
  going up into it (or through it from below) with ground under it is a ceiling, and the flight stops going across and
  drops from rest; otherwise, and for a surface above the arc with none under it, it is a wall: the flight stops going
  across there and flies on up and down;
- a GM teleport (`.go xyz`, `.summon`) that leaves the player above the floor falls to it from rest. The fall opens
  with a heartbeat at the teleport's position, as the server keeps the player's movement flags across a teleport and
  checks the next packet against them. A floor up to 1 yd above or 0.05 yd below the teleport's z counts as standing
  on it. Session event `falling` (with the landing it expects) opens the fall, `landed` ends it;
- in the air the packets carry `MoveJumping` with the jump values, and `MoveFallingFar` too once on the way down; the
  landing carries the time fallen, so the server can deal fall damage as for a client (`Player::HandleFall`: 14.57 yd
  or more below the highest point, at least 1229 ms in the air, no feather fall);
- the heartbeat due within 150 ms of the landing is left out, and a landing step that would still break the
  anticheat's bound (a floor met a little above the arc) waits until it does not, at most until 900 ms after the last
  packet (the core flags a moving client silent for over 1000 ms, `SKIPPED_HEARTBEATS`);
- `landed` says what it landed on: `navmesh` settles z (`z_unknown` false); `water` does not (the agent does not
  swim: it stands on the water's surface, where the mesh has it); `guess` is the old behaviour. `floor_late` is true
  when the floor search did not answer in time (below) and the landing is the guess for that reason.

Without a navmesh (no `mmaps_dir`, a build without cgo, no tile under the point) a knockback lands back at the height
it started at and a teleport stays where the server put it, both with `z_unknown` true. Limits: the mesh has no game
objects and no walls of its own, so the wall rule is a stand-in for collision; a floor is where the mesh has it, within
its detail error (a few tenths of a yard); a `walk_to` that ends above the ground does not fall (it sets `z_unknown`);
a far teleport (`SMSG_NEW_WORLD`) does not fall either; landing in water sends `MSG_MOVE_FALL_LAND`, not the client's
`MSG_MOVE_START_SWIM`.

The floor search never runs in the session loop, so heartbeats, order acks and pings stay on time while it reads a
tile (0.5-3 MB) or waits for the planner. After a knockback (or feather-fall) the ack goes out first, as the server
drops an order not acked within 2 s; the flight then waits up to 300 ms for the trace, sending nothing (the next
heartbeat is then at most 600 ms after the ack). When the trace has not answered by then, the flight goes straight up
and down where it began, on ground the server knows, and lands back there (`floor` `guess`, `floor_late` true): the
plain guess, the arc's x and y at its starting height, would land under the terrain up a slope. `knocked_back` is
emitted once the landing is known, or when a teleport or another knockback ends the flight first. After a GM teleport the player stands still where
it was put until the search answers, then falls or settles z as above; an answer that comes after the player has moved
again (a walk, a knockback, another teleport) is dropped. `path_to`/`walk_path` read their tiles without holding the
planner, so a floor query waits behind a plan's Detour search only, not its disk reads. A missing tile is looked up
once (until the next `path_to`/`walk_path` plan). The 300 ms wait and the late fallback are tested against a planner
that stalls for 3 s; they have not been seen live.

## Rotations as data

A class rotation of the [bot brain](#bot-brain) can be a **file** instead of Go: a priority list of one class and spec for the agent's own character.
A file **replaces** the class strategy of a preset (`replaces`: `mage_frost`, or `warrior_melee` + `warrior_arms`...), so the same preset plays
the Go rotation or the file, and two files can be switched between **by name**: `rotation` of `dungeon_run` and `brain_start`, `--rotation` of
`tools/run-uldaman.sh`. The run's report and trace name the rotation with the digest of its file (`brain: "frost_mage+rot:frost_mage@32c7144315b5"`; the
trace header's `options.rotation`), so two arms of an A/B test are told apart. Without the option nothing changes.

Built in (`internal/brain/rotations/*.json`, `turtle-agent rotation list`): `frost_mage`, `fire_mage`, `arcane_mage`, `shadow_priest`, `arms_warrior`,
`fury_warrior`, `tank_warrior`. `frost_mage`, `shadow_priest` and the three warriors are the **data twins** of the Go strategies (tests play both through the
same scenes and compare the actions); `fire_mage` and `arcane_mage` exist only as files, with the presets of the same names (`brain: "fire_mage"`).
Extra files: the settings' `rotations_dir` (every `*.json` in it is registered at login; a name that is taken, a built-in one too, is refused).
Healer rotations stay Go: the healer triage (`heal.go`) is not a priority list.

```json
{"version": 1, "name": "frost_mage", "class": "mage", "spec": "frost", "replaces": ["mage_frost"],
 "sources": ["mod-playerbots 79bd428 src/Ai/Class/Mage/Strategy/FrostMageStrategy.cpp"],
 "priorities": [
  {"id": "frost_nova", "spell": "Frost Nova", "level": 10, "target": "self", "band": "high", "threat": "aoe",
   "when": {"melee_mob": true, "no_mob_aura": ["Frost Nova"]}},
  {"id": "frostbolt", "spell": "Frostbolt", "level": 4, "target": "fight"}]}
```

The list is read **top to bottom**: within a band an earlier entry beats a later one (the engine casts the most relevant action that can run). Entry fields:
`id` (the action's name in `brain_decision`), `kind` (`cast`, the default; `attack`, the auto attack at the fight's mob; `next_swing`, a spell that
replaces the next swing and is sent once per swing; `stance`, back to a home stance from `stances`), `spell` (a name: the highest known rank is cast) or
`spell_id`, `level` (the character level of the first rank, checked against the spell data; `talent: true` for a talent), `target` (`fight`, `melee_mob`, `self`), `band`
(`low` 5, `normal` 10, `high` 20, `move` 30, `emergency` 43; the engine's relevance bands, so movement and interrupts still outrank a rotation), `threat`
(`single`, `aoe`, `none`: an `aoe` entry is vetoed while a mob is loose), `harmful`, `alt` (other ids to try when this one cannot run), `when`.
`when` is a closed set of conditions that all must hold, each a function of what the agent sees: `state` (`combat`, `rest`, `any`), `melee_mob`,
`mob_in_reach`, `mobs_in_reach_min`, `mobs_in_cone_min`, `mob_distance_min`/`max`, `mob_on_me`, `tank_holds`, `feared`, `mob_fleeing`, `moving`, `in_combat`, `after`
(`dodge`, `avoided`), `combo_points_min`, `stance`, `self_hp_below`/`above`, `power_below`/`above`, `mob_hp_below`/`above` (percent),
`self_aura`, `no_self_aura`, `self_aura_missing` (absent or under a minute left), `mob_aura`, `no_mob_aura`, `mob_stacks_below`, `mobs_lacking_aura`. Unknown
fields are refused. Costs, cooldowns, range and the global cooldown are the spell data's and the engine's, not the file's.

**Checks.** `turtle-agent rotation check <file>...` reads files and checks every spell and aura name against the core's spell data, the level of the first
rank, the ids, the bands and the conditions; `rotation show [--level N] <name|file>` prints the list with the rank cast at level N. The same checks run at load,
and `make test` runs them on every built-in file. A rotation that replaces nothing of the preset (the wrong class) is refused, and so is an action id that another active
strategy also uses.

**Sources.** The files are reimplemented from an understanding of mod-playerbots' class strategies (GPL-2.0+; behaviour, ideas and thresholds only, no code
copied or translated), each file names the donor files in `sources`: `src/Ai/Class/Mage/Strategy/{Frost,Fire,Arcane}MageStrategy.cpp` and
`GenericMageStrategy.cpp`, `src/Ai/Class/Priest/Strategy/ShadowPriestStrategy.cpp` and `GenericPriestStrategy.cpp`,
`src/Ai/Class/Warrior/Strategy/{Generic,Arms,Fury,Tank}WarriorStrategy.cpp`. The spells, levels and stances are the Turtle core's.

**A/B.** The arm of a test is the name. The [pull test in leader mode](#start-anywhere-and-the-pull-test) scores the character on one pull (damage and
healing per second and per power, casts failed by reason, idle in combat, mobs taken off the tank, deaths): run it once per arm with the same pull and party,
3 or more tries each, then `turtle-agent pulltest --compare a.json b.json`. How, step by step: `docs/ROTATIONS.md`.

## Boss tactics

A plan's boss can carry what a player does on that fight beyond the class rotation: `bosses[].tactics` in the plan, i.e. the leg's `boss.tactics`
(`internal/tactics`; checked when the plan loads, so a spell name with a typo is a load error, not a rule that never fires):

```json
"boss": {"entry": 7023, "first": [4847, 4847], "tactics": {
  "kill_first":   [7209],                          "creature entries to kill before anything else while the fight is on",
  "never_target": [9001],                           "entries the character leaves alone (unless one attacks it)",
  "interrupt":    ["Chain Lightning", 15801],      "enemy casts to interrupt first: a spell id or its name",
  "dispel":       ["Fevered Plague"],              "auras a healer dispels first, at any mana",
  "no_dispel":    ["Mutating Injection"],          "auras a healer leaves alone",
  "move_out":     [{"cast": "Fire Nova", "yards": 12}, {"ground": 10132}]}}
```

(The explanations above are not part of the file.) **Leader mode only**: the brain gets the plan's bosses; while the boss, or one of the units in its `first`, is in
view, hostile, alive and in combat, the encounter is ON (`brain_state` event with `state` `encounter` / `encounter_end` and `data.encounter`, `<plan>/<leg>`) and:
- **kill_first** - the fight's mob is the nearest such unit within 40 yd, whoever the tank holds; **never_target** - the tank's or the selected mob is skipped (a mob that attacks the character is still fought);
- **interrupt** - an enemy cast on the list is the first to interrupt (above heals and crowd control), with the class's own interrupt spell (Counterspell, Pummel...); a cast the spell data says cannot be interrupted still is not;
- **dispel / no_dispel** - healer presets dispel the listed aura before anything else (also a slow, at 20% mana) and leave the listed ones alone;
- **move_out** - `cast`: while an enemy casts the spell, a circle of `yards` (default 10) round the caster is a hazard and `avoid_ground` walks out of it (a tanking character holds its place); `ground`: a ground effect of that spell is a hazard whoever cast it.

The `encounter` strategy and `avoid_ground` are added to the brain when the plan has any tactics. A **commander** run carries the block but does not use it
(the bots are TortoiseBots'): the remote driver's kill order comes from the plan's own `kill_first` and `summoned_kill_first` ([#190](https://github.com/pjerra/turtle-agent/issues/190)), which Zul'Farrak's plan fills with the healers and the wards (Zum'rah's Ward 7785, the Greater Healing Ward 8179). A summoned unit counts as part of a fight only with encounter evidence: it stands within 10 yd of a living hostile that fights the party, or it fights the party itself; a unit of the same entry anywhere else is left alone. Shipped: Uldaman Revelosh, Obsidian Sentinel (kill the Obsidian Shard 7209 first) and Grimlok, Zul'Farrak Antu'sul and Velratha
(interrupts) and Zum'rah (kill the Ward 7785 first), with ids from the core's `creature_spells` and EventAI tables. Behaviour after mod-playerbots' dungeon and raid strategies (ideas only). Not checked live.

## Map images

`turtle-agent map` draws a run as a top-down map from what the agent already records, so a run can be read at a glance
instead of from thousands of event lines. It renders nothing live and takes no screenshots: the images are SVG (standard
library only, no new dependency) and an optional single-file HTML replay.

```
turtle-agent map --capture agent.cap --events events.json --report run-report.json \
    --plan dungeons/uldaman.json --out run.svg --html
```

What it needs: `--capture` (a `login --capture` file: the positions of the party and of the hostile creatures come from
replaying it, the leader's route from the movement packets the agent sent, and its events from the replay) and/or
`--botwatch` (wow-play-harness samples, JSON lines with `t` in epoch seconds and `bots[]` with `name`, `map`, `x`, `y`, `z`,
as extra routes; `--members a,b` keeps some of them). `--events` replaces the capture's events with saved ones (the
`events` method's pages, a JSON array or one event per line): the dungeon driver's `dungeon_*` events are in the live log
only, so a dungeon run wants its `events.json`. `--report` is the `dungeon_status` report (the bare report, or the result
holding it): GM shortcuts flag the teleports and revives, deaths and wipes come from it. `--plan` draws a dungeon plan
over the run. `--mmaps DIR` (or `mmaps_dir` of the settings file that `--config` names, default the settings file) gives the
background; without tiles, without cgo, or with `--background=false` the map is drawn on white, and says so.

What the map shows:

- **Background:** the navmesh floor under the run as filled polygons (ground, water, lava and slime, too-steep slopes
  told apart by colour), clipped to the run's extent plus `--pad` yards (default 25), or to `--box x0,y0,x1,y1`.
  `--from` and `--to` (RFC 3339, both inclusive) keep one stretch of a long run: a leg, say, with its report's times.
- **Floors:** where one walkable surface lies at least 6 yd over another (a gallery over a hall), the map is split by
  height, halfway between the two, into one image per floor: `run-f1.svg`, `run-f2.svg`, and so on, lowest first.
  A route, creature or event shows on the floor its height falls on (a stretch between two floors on both). Hills and
  ramps never split. `--floors off` keeps one image, `--floors -30,-10` cuts at the heights given.
- **Routes:** one line per member (the leader thicker, first in the legend), a dot on every route at each time tick
  (every 15 s to 4 h, whichever leaves about a dozen; labelled on the leader's). A member is told apart by its GUID, so two
  units with the same name are two lines; a botwatch bot, which has no GUID, joins the one member of its name that has
  one, when there is only one. A hole of more than 30 s in a member's positions is a dotted line; a leap of 60 yd or more
  (a teleport) a dashed pink arrow.
- **Hostile creatures:** a dot at the place each was first seen, by what became of it: killed (red, from the `kill` and
  `death` events or a corpse in view), evaded (orange: fought, then seen alive and out of combat), fought and left alive
  (purple: a wipe, or out of view in combat), skipped (hollow: seen, never fought). Creatures within 10 yd of each other
  are ringed as a pack. Bosses are stars with their names and the time they died: the plan's boss entries, and any
  creature whose health is at least 5 times the median and 8000.
- **Events:** pulls (a diamond numbered in order: the first combat of the party after 15 s of quiet), deaths (a cross
  and the name), wipes (`dungeon_failed` with a wipe), doors, levers and altars changing state (`gameobject` events, a
  square marked D, L or G), teleports (a triangle, marked `GM` when the report lists a matching shortcut) and the report's other GM
  shortcuts (a revive, a buff) the same way, and the start of each plan leg.
- **Plan overlay:** the plan's walk from the entrance through the legs' waypoints (dashed), its pull group positions,
  bosses and the objects it uses, drawn under the run, to compare plan and reality. Only the part of the plan the run came
  near stretches the image.
- A legend, a scale bar, a north arrow (north is up, west left) and notes (what is missing).

`--html` also writes `run.html`, one self-contained file with no external script, style or font: the same background
and legend, a slider, play and pause, a speed choice (1x to 600x), floor tabs and an event list. The slider moves the
members, draws their routes up to that time, shows creatures from when they were first seen and in their fate colour from
when it was settled, and reveals the events as they happen. `run.html#t=1500` opens at 25 minutes (`#f=1` at floor 2).
Output files are mode 0600 and an existing one is refused unless `--force`; a capture is never overwritten.

`map_snapshot` is the same for the live agent: it keeps the positions of the party and of the hostile creatures the session
shows every 2 seconds from the start (memory bounded: 40000 positions per member, 6000 creatures), and draws them with
the event log and the `dungeon_status` report. `plan` names a plan in `dungeons_dir`; `floor` picks the image, `floors`
lists their names. The navmesh is the one `path_to` uses, so it needs `mmaps_dir`. The background is read into a navmesh
planner of the call's own, closed when the call returns, so a snapshot never unloads the tiles a running plan is walking
on; a `plan`'s bosses mark that image only, not the next one.

Limits: only what the agent saw is drawn (units outside its view distance are missing, and a creature's dot is where it
was first seen, not where it fought); the capture's replay does not follow the agent's flights or walking after RPC calls
except through the movement packets it sent; and there are no textures or models, only the navmesh, which has no doors
(a door closed in the game still shows as open floor). Two full-run samples of the first live Uldaman run
(`docs/LIVE-CHECKS.md`) are in the audit repository, not here: captures are not committed.

## Live dashboard

`turtle-agent dashboard` serves one web page on the laptop that shows every running commander live: where its party is on
the dungeon map, what the run is doing, and the numbers and orders behind it. It is for watching runs, so it is
**read-only**: it calls four RPC methods (`status`, `dungeon_status`, `tune_list`, `bot_view`, the last one without its
`log_every` switch) and nothing else, and the client refuses every other method name. Plain Go standard library, the page
is embedded in the binary (no CDN, no build step). The header's "Refresh every" picker (1 / 2 / 5 / 10 / 30 s / paused, default 2 s —
saved per browser) sets how often the page polls; "paused" freezes the map for a close look.

```
turtle-agent dashboard                                  # http://127.0.0.1:8790/, every agent found in the settings files
turtle-agent dashboard --listen 127.0.0.1:8790 --agents 8765,8766 --tests-dir ~/TortoiseBots-audit/tests
turtle-agent dashboard --listen 100.101.102.103:8790 --allow-host <machine>.<tailnet>.ts.net   # over Tailscale, see below
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--listen` | `127.0.0.1:8790` | where the page is served; a non-loopback address prints a warning, and a specific IP is accepted as `Host` by itself |
| `--allow-host` | none | host names or IP addresses (comma separated) the page accepts in its `Host` header besides loopback: exact names only (no wildcard, port or path). Any other `Host` gets 403 (see below) |
| `--agents` | `auto` | `auto`: the `rpc` field of `settings*.json` in `--config-dir`; or a comma list of RPC ports/addresses (loopback only); `auto,8770` adds one. The list is read again every 10 s, so an agent started later appears |
| `--tests-dir` | `~/TortoiseBots-audit/tests` | the **only** folder whose files `/files/...` serves (traces, summaries, reports); no other path is reachable |
| `--traces-dir` | `~/turtle-agent/traces` | where the agents write run traces: read for the last orders and the binary SHA, never served |
| `--plans-dir` | `~/turtle-agent/dungeons` | plan files for the map (comma list); a plan file that changes (a live plan reload) is loaded again. A plan no folder has is drawn from the copy the binary carries (`dungeons/embed.go`), which can differ from the file a run was started with |
| `--minimap-dir` | `~/turtle-agent-minimaps` | the map images `minimap-extract` wrote; the map draws over the image of its map id when the folder has one (see Minimaps below) |
| `--embed-from` | none | extra origins (`http://host:port`, comma separated) that may show the page in a frame, on top of loopback pages (see below) |
| `--config-dir` | `~/.config/turtle-agent` | settings files and the `rpc-<port>.token` files |
| `--mmaps-dir` | none | the navmesh folder of the server the plans are run on. With it the plan view draws the real route (below); `--edit` needs it. Without it the page draws only the plan file's own straight lines |

**Real route (`--mmaps-dir`).** The plan view (`#/plan/<name>`) draws the path the bots walk, not just the plan file's own points. `GET
/api/plan/<name>/route[?agent=<id>]` (GET only, no `--edit` needed) runs the plan check's walk (`dungeon.TracePlan`: the same navmesh planner,
the same look point as `plan-check`) and answers the stops in run order and the walk to each, as `{stops, segments, summary}` in the page's plane:

- every walk between consecutive points, **including the walk from the end of one leg to the start of the next** (`link: true`) and each pull
  group's walk from the previous stand to the computed look point (the path is cut at the look point; a pack already in view has no walk);
- the stops numbered `1, 2, 3...` across the whole plan: via points, stage points (`to`), packs, steps that have a stand point, bosses;
- a segment the planner cannot path (`ok: false`, with the plan check's reason: `unreachable`, `off the mesh`, `too far`, `no line of sight`)
  is drawn dashed red from its start to its end, with the reason on hover. Each segment is judged from where the plan means the party to stand, so
  one unreachable point does not turn the segments after it red (`plan-check` stays where it was and reports its own issues).
  A `needs_walk` point or a stand outside its trigger zone has no segment of its own: its number turns red. `direct` hops are straight (amber dotted),
  `check_skip` and `gm_cross` hops are dotted violet (not walked on the mesh).

The route is traced once per version of the plan: the answer is cached by the plan file's sha256 and the mmaps folder (at most 48 kept; a
second request for the same plan waits for the first instead of tracing again; one trace runs at a time). With `--edit`, `GET
/api/edit/<plan>/route?ops=<JSON list of ops>` traces the plan with the editor's ops applied (like `snap`'s `prior`) and caches by the sha256
of the edited plan; it writes nothing. The page asks for it after each edit, undo, redo or discarded edit, and draws the old route faded until
the new one is there. A box on the map, "Route legend", has the toggles: **Real path**, **Straight lines (old)** (the plan file's own straight
segments, dimmed while the real path is on), **Packs** and **Numbers**; they are the same choices as the Layers panel. In edit mode the yellow
handles stay on the plan file's own via points and `to` (so the real path is the line and the handles are the dots). Without a navmesh the
endpoint answers 503 and the box says to start the dashboard with `--mmaps-dir`. Run cards do not draw it yet.

**Route editor (`--edit`).** Off by default: without `--edit` the page is GET/HEAD only, exactly as above. With it, the one thing the
page can write is a checked version of a plan's route, through `POST /api/edit/<plan>`:

```
turtle-agent dashboard --listen 100.101.102.103:8790 --plans-dir ~/wt-ta-x/dungeons --edit --mmaps-dir ~/turtle-agent-mmaps
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--edit` | off | turns the editor on |
| `--mmaps-dir` | none, required | the navmesh folder of the server the plans are run on; every version records it (a check on another server's mesh proves nothing); also the mesh of the real route above |
| `--edit-source` | the first `--plans-dir` | the folder whose plan files an edit starts from; the page draws and edits this copy |
| `--edit-dir` | `~/turtle-agent-route-edits` | where versions go: `<plan>/vNNNN.json` (the whole plan, `dungeon.Marshal`), `vNNNN.meta.json` (who, when, base sha256 and git SHA, note, check result, ops, the plan diff) and `vNNNN.diff` (text diff). A refused edit keeps the record and the diff only |
| `--edit-tokens` | `~/.config/turtle-agent/route-edit-tokens` | lines of `label token`, mode 0600, owned by you; the label is the recorded `who` |
| `--edit-trust-local` | off | saves from this machine, or from a Tailscale address you named, need no token typed: the server uses its own (below). Needs `--edit` |
| `--edit-trust-label` | `owner` | with `--edit-trust-local`: the label in the token file the trusted saves are made as (startup fails when it is not there) |

The request carries ops, never a plan (`{"base_sha256", "note", "ops": [{"leg", "kind": "move"|"insert"|"delete", "index", "x", "y"}]}`, game
coordinates; index counts the leg's via points, then `to`). The server applies them to a copy of the base file it reads itself: only the
position of a via point or of a leg's `to` changes, a plain via point is added or deleted; a `direct` or `check_skip` via point, pack
positions, bosses, steps, and legs are locked (so is a `gm_cross` via point). Each new point is put on the navmesh floor nearest its old height (a drag on a multi-level map
stays on its level; more than one floor in range is reported as `floor_picked`), must lie within 200 yd of the plan's own points, and the
result must still load as a plan. Then `dungeon.CheckPlan` runs on the whole plan: an edit is refused (HTTP 422, with a refused record) when
it makes a plan check issue the base plan did not have, or cannot be checked. A version is "checked" only when the edited plan has **no** plan check
issue at all (exempt `check_skip` hops aside). An edit that adds none to a plan that has issues of its own is kept as "unchecked" (the plan file is
kept, so work can go on from it) and is never picked by `latest`, `export` or `promote`.

**The page.** With `--edit` the route is editable as soon as the plan view (`#/plan/<name>`) opens: there is no Edit button. Hover a leg's line
and its points appear (always, on a touch screen); a selected point stays visible. Drag a point to move it (a via point, or the leg's stage
point, orange, where the party gathers); click a line, or drag from it, to add a point there; click a point and press Delete to remove it;
Ctrl+Z / Ctrl+Y undo and redo; Esc cancels a drag. Grey points are locked (direct and check_skip hops). A "How to edit" box says this and what
the colours mean. Every point and line says what it is in words ("Walk point 3 of leg Baelog", "Where the party gathers before Baelog",
"Pack: 2 Stonevault Oracles"; a creature the plan's notes do not name is "creature <entry>") and, on hover, what the bots do there, the walk
length, and the reason when it is red (the planner finds no path, or a plan check issue). Each drop asks the server where the point lands
(`/snap`): a floor picked among several is marked purple, a point off the mesh is refused on the spot. The edits are one list of ops against the
plan file, so saving always sends all of them.

Unsaved work is never silent: an "Unsaved changes" bar (with Save and Discard) is at the top of the box while the edits differ from the last
save, the browser asks before a reload or close, and the edits and note are kept as a draft in the tab's `sessionStorage` (and taken back after a
reload, only on the same plan file; a blocked or full storage just means no draft). Save asks for the editing token (kept in this tab's
`sessionStorage` only, sent in the `Authorization` header only), sends the ops, and shows the result: checked or refused, the new plan check
issues (red marks on the map, the reason on hover), the changes in words ("leg 3 (theka): via 2 moved 6.1 yd east"), and the text diff. The
version list has Diff, Load (put a version's edits on the map) and Revert (a new version equal to an older one; versions are never overwritten).
Hiding a leg hides its handles. The box says why editing is off when the map does not show the file edits start from (`--edit-source`).

**No token typing on your own laptop (`--edit-trust-local`).** Off by default. With it, a save that sends **no** `Authorization` header is made
as the `--edit-trust-label` token's owner when the request's TCP peer is (a) a connection from this machine (loopback, or the `--listen`
address, **and the request's Host is that literal address**: a host name never counts as local) whose client socket belongs to the **same user as the dashboard** (the kernel's `/proc/net/tcp` says whose; Linux only, elsewhere this
never trusts), or (b) a literal Tailscale address (100.64.0.0/10, fd7a:115c:a1e0::/48) that is in `--allow-host`, from another machine, **and** only when the
connection reached a Tailscale address of the dashboard (not a LAN, public or wildcard-listener path that happens to carry that source address).
`GET /api/edit/status` then answers `"local_trusted": true` and the page hides the token field. The token itself stays in the server: it is not in
any answer, page, version record or log, and the browser never holds it. Threat model:

- *What is trusted is the peer address, never a header the caller writes.* A Tailscale source address arriving on the dashboard's Tailscale address is authenticated by
  WireGuard, so another device cannot claim it. A **name** in `--allow-host` says how the page may be opened (the Host header), not who is calling, so it is never
  trusted; nor is a LAN or public address (they are listed at start-up as "not trusted as callers").
- *A remote viewer not on the list still needs the token*, exactly as before: the same token check, the same ten-failures lockout.
- *A request that sends any `Authorization` header is judged by that token alone*, trusted address or not (a wrong token is not forgiven).
- *A local caller must be you.* A relay that connects from this machine (a reverse proxy, a port forward, another user's SSH tunnel) is a
  local socket too, so it is trusted only if it runs as the dashboard's user (which could read the token file anyway); a relay of another user, or
  one the kernel does not show, is not. Requests carrying `X-Forwarded-*`, `Forwarded`, `X-Real-IP`, `Via`, `Tailscale-User-*` or similar are never
  trusted either.
- *DNS rebinding.* A name in `--allow-host` that someone else controls can be rebound to 127.0.0.1 and then serves a same-origin page that talks to a
  local socket of your user. So a same-machine caller is trusted only when its Host is a literal loopback address or the literal address the
  connection was accepted on; by name, only the remote Tailscale branch applies (the owner on another device opening the page by its Tailscale name).
- *A page capability.* `GET /api/edit/status` gives a trusted caller a random per-process nonce (`trust_nonce`, not derived from any token) and a
  trusted save must echo it in `X-Route-Edit-Nonce` (a custom header, so a cross-site request needs a pre-flight the guard refuses); a missing or
  wrong nonce is a 401. A save with the real token needs none.
- *No cross-site saves from the owner's own browser.* A trusted request that has an `Origin` must have the one equal to its Host, and a
  `Sec-Fetch-Site` must be `same-origin` or `none`; on top of that the unchanged guard demands `Content-Type: application/json` (a form cannot
  send it, a cross-site fetch must pre-flight and the guard answers every OPTIONS with 405) and the Host check (DNS rebinding sends a Host that is
  not listed).
- *What it cannot do:* tell a forwarder run by you from you. Anything that runs as your user and relays strangers' requests to the dashboard
  (a `socat` you left open, your own SSH tunnel handed to someone) is you, as far as the dashboard can see; it could also read the token file. A
  save is still only a checked version in `--edit-dir`; it never reaches a slot's plan folder. If the browser reaches the dashboard through a relay
  of another user (WSL's Windows forwarder may be one), nothing is trusted and the token field stays.

Safety: every POST needs `Authorization: Bearer <token>` (a header, so a form on another site cannot send one), `Content-Type:
application/json` and at most 64 KiB; ten wrong tokens from one address lock it out for a minute; the Host check is unchanged. Files are
created with names the server builds from the plan name (`a-z0-9_-`) and a counted version, never overwritten, and every path step is made
relative to a directory descriptor opened with `O_NOFOLLOW` (the edits folder once at start, each plan folder per call), so a folder swapped for a
link cannot redirect a write (Linux; the store does not run elsewhere).
`--edit-source` is held the same way: opened once at start, each plan read with `openat(..., O_NOFOLLOW)` and accepted only as a regular
file; the folder checks compare the held folders' identities (device, inode), run again around the write (a failed one removes the version),
and stop the editor when a held folder's path no longer names it. A save with the same base, ops and note as the latest version returns that
version (`"unchanged": true`, no new number, its stored author). Saves are bounded: 30 a minute per token label and 500 per plan per 24 hours (HTTP 429), and
none that would leave the edits folder's filesystem under 200 MB free, the version's files counted (HTTP 507).
The commander run reloads its plan file at every leg, so anything written into a running slot's `dungeons_dir` would be taken by that run
unchecked: the dashboard refuses to start, and refuses each edit, when `--edit-dir` is, contains or lies inside any slot's `dungeons_dir`
(read from `settings*.json`), or when `--edit-source` lies inside one. The guard fails closed: a settings file that cannot be read or decoded,
a missing config folder, `--agents` other than `auto`, or a slot (a file with an `rpc`) whose plan folder cannot be told (no `dungeons_dir`, or a
relative one: its runs read `dungeons/` under the agent's working folder) stops the editor and each edit, saying which file. A settings file whose
default folder you know is elsewhere is named with `--edit-allow-default-dir settings-3.json`. Also: `GET /api/edit/status`, `/api/edit/<plan>/versions`,
`/api/edit/<plan>/snap?kind=move&leg=&index=&x=&y=` (where a dropped point would land, writes nothing), `/api/edit/<plan>/route?ops=` (the real
route of the plan with the ops applied, writes nothing), `/api/edit/<plan>/vNNNN` and `/vNNNN/diff`.

**Using the edits in a run (`route-edits`).** An agent reads one plain plan file, so an edit reaches a run by copy, never by overlay:

```
turtle-agent route-edits list [<plan>]                        # versions, status, who, when, the check result
turtle-agent route-edits export --to ~/ta-new/dungeons zulfarrak [v0007|latest]
turtle-agent route-edits promote --worktree ~/wt-ta-x zulfarrak v0007
```

`export` copies the newest checked version (or the named one) as `<plan>.json` into a fresh folder (it must not exist or be empty) and prints
`route_edit=<plan>@v0007 sha256=<hex>`. It refuses a refused version (a refused edit has no plan file), an unchecked one (the plan has issues of its own), a file that was changed after it was
written (the record's sha256), and a folder that is, holds or lies inside the `dungeons_dir` of an agent that reports a run in progress (or
whose state cannot be read), since that run reloads its plan file at every leg; it warns when a slot that reads the folder has another
`mmaps_dir` than the one the version was checked on. Point a stopped slot's `dungeons_dir` at the folder and start the run with
`trace_meta: {"route_edit": "<plan>@v0007 sha256=<hex>"}` in `dungeon_run`: the run's trace then names the edit, and the run card says "run uses
route edit v0007" (or warns when the file the run read is another version). `promote` writes the version over `dungeons/<plan>.json` of a
worktree for a PR and prints the commit message (who, when, what moved, what it was checked on); it refuses when the worktree's file is no
longer the one the edit started from. The brief rule for the lead: before copying plans for a run, `route-edits list <plan>`; when a checked
version is newer than the golden or worktree copy, export it into the slot's fresh `dungeons_dir` instead.

**Over Tailscale.** Listen on the machine's tailnet address (`tailscale ip -4`) and open the page from another tailnet device at
`http://100.101.102.103:8790/`: that IP is accepted as `Host` by itself. A name the page is opened by (the MagicDNS name,
`<machine>.<tailnet>.ts.net`) is refused with 403 until it is listed in `--allow-host`. The page stays read-only, and the scrub
and the file limits are unchanged. A wildcard `--listen` (`0.0.0.0`) takes no address by itself: name the hosts you open it by.

**What it reads.** From a settings file only the `rpc` and `dungeons_dir` fields (accounts, passwords and every other field are skipped by the
decoder). Each agent's bearer token comes from its `rpc-<port>.token` file and is used for the calls only. Every 1.5 s each
agent gets the four calls in parallel with a 2 s bound; the answer is built into a card and cached as JSON, so a page
request does no work. The trace of the run (the path in `dungeon_status`'s `report.trace_file`) is followed from the byte it
was last read to, so a poll costs the new lines only.

**Plan view and plan copies.** The select at the top opens `#/plan/<name>`: a plan drawn with no run (every plan of `--plans-dir`
and of the binary), with a checkbox per leg, "only" per leg and, while a run is on the plan, "Current leg + next". The layers
add the computed look and gather points of the packs (straight lines, not the navmesh), area triggers, gossip NPCs and stand
points, and the plan's `needs_walk` points and `check_skip` hops. A run reads its plan from the `dungeons_dir` of its settings
file, which is usually not the file in `--plans-dir`; so a run card draws the slot's own copy when the file is there, and says in
words which file is drawn, which file the run reads, and whether they are the same bytes (sha256). The plan view lists the same
for every agent. `/api/plans`, `/api/plan/<name>[?agent=<port>]` and `/api/plan-copies/<name>` serve this; all are GET.

**The page.** A card per commander (a grid, or tabs with the Grid/Tabs switch; the choice is remembered in the browser):

- Header: character and level, the group label (the plan and the capture folder of the run), the state (phase, leg and name,
  `pull group k of n`; the phase is the first word of what the watchdog says the run waits on: prepare, enter, walk, pull,
  boss, rest, step, ...), minutes in, and the stall timer: the time since the run last made progress, yellow from 2 min,
  red from 5 min (the stale-run rule).
- Numbers: deaths, wipes, party downs, threat overrides, rest share and gates, gathers, walk stops, pulls, GM commands and
  GM assists, agent walks, orders sent and refused, and the binary SHA (from the trace header).
- The party table: name, class and role (from the hire block, else the trace's party record), HP %, power % (the power type
  is in the tooltip), yards to the tank, and the target, from the module's `bot_view` frame.
- The last 10 orders with the module's answer (ACK or ERR with code and message), the last error, the last refused order, and
  the on-the-spot changes (`tune_change`, `plan_reload`, and the tunables whose value is not the base).
- The map: an SVG drawn from the plan file for the plan's map: the route of each leg (via points; `direct` steps dashed), pull
  groups as circles of their radius with the group number, bosses, doors and objects the steps use, the entrance. North is
  up, west is left, one unit is one yard, and the view fits the plan. With `--minimap-dir` the real in-game map of the run's
  map id is drawn under it (a picker in the legend row appears when the folder has several images of that map: the other source,
  or another floor). The current leg's route is bold; legs done and groups
  already pulled are dimmed. On it: the bots (tank gold, healer green, dead grey), the commander (violet, only when its
  character stands on the plan's map), and the creatures of the feed (hostile red, neutral grey, friendly green; a ring marks
  one in a fight; dimmed when its pull group is behind the run). Hovering a dot names it with HP, target and pull group.
- Debug (a fold-out): the raw last `status`, `dungeon_status` and `tune_list` replies, and links to the run's trace, its
  summary and the capture folder's `REPORT.md` under `--tests-dir`.

A mob's kind comes from the module's reaction field (`h` hostile, `n` neutral, `f` friendly; critters, civilians and guards
count as neutral). Its pull group is found by matching its entry and position against the plan's groups (within the
group's radius, the current leg first); the module does not say which pack a creature belongs to.

**What never leaves the server.** Cards are built from a list of fields, not from the raw replies. On top of that, everything
served (the state, the raw panel) is scrubbed: any JSON member whose name matches `pass|token|account|secret|credential|auth|login|cookie|session_key|srp`
is dropped at any depth, and a string that contains the agent's token is replaced. A test walks the card model for such
names and another runs a full poll against a fake agent whose replies are full of them. Files are served only from
`--tests-dir`: dot-dot segments, absolute paths, backslashes, NUL bytes, symlinks that leave the folder and directories are
refused (a 404 that does not say why). Only GET and HEAD are answered, a request whose `Host` is neither a loopback name nor one named with
`--allow-host` (or the `--listen` IP) is refused (DNS rebinding), and the page may be framed only by a loopback page or an origin named with `--embed-from` (the TortoiseBots observability
dashboard's Commanders tab embeds it that way when `TBDASH_COMMANDER_URL` points here; a dashboard on another machine, for
example `--embed-from http://m910q:8095`, must be named).

**Reading and moving the map.** Each card opens on the whole map picture and the whole plan, fitted to a large panel. The mouse wheel
zooms around the cursor, a drag pans, two fingers pinch and pan on a phone, a double click zooms in, and the `+`, `-` and `Fit`
buttons do the same; the view is remembered per map in the browser. **Follow party** (on by default while a run is live) eases the view
to the middle of the party at every update and keeps the zoom (first the party plus about 60 yards around it); dragging the map pauses it
(a yellow "Following paused, click to resume" chip) and the toggle resumes it. A click on a bot's name in the party table centres the map
on it. **Dim map** darkens the game picture under the drawing (0-80 %). The drawing's colours are fixed and bright with dark outlines
(route cyan, current leg thicker, tank blue, healer green, damage orange, commander purple, hostile red, neutral grey, packs dashed yellow,
bosses magenta, doors and objects white), and its labels keep one size at every zoom. The page is read-only and loads no outside script or
font. The wording is plain: the state line reads "Sentinel · pack 4 of 5 · fighting Stone Steward", counters have a short name and a hover
explanation, orders read "Attack Stone Steward -> OK" with the raw line on hover. The dashboard has no creature names of its own: it takes
them from the "Name (entry)" mentions in the plan's notes and says "creature <id>" otherwise.

**What is drawn.** The **Layers** button opens a panel of checkboxes. The choice is **one setting for all maps and all cards**, saved once in the
browser (`localStorage` key `maplayers.v2`; a change on one card shows on every card at once, and **Reset layers** returns to the defaults).
The list is in `internal/dashboard/static/layers.js`; adding a layer there (plus its drawing in `app.js`) puts it in every card's panel and on
every map. A layer whose data the card's map does not have is greyed out with the reason beside it (its saved choice is kept).
Shown by default, as before layers could be chosen: the game map picture (with the dim slider), planned route, pull-group circles, cleared
groups, bosses, doors and objects, section labels, walked path, next-stop arrow, bot dots and names, commander, hostile mobs. Off by default:
neutral and friendly mobs, step numbers, and the newer layers: **grid**, **distance scale bar**, **critters** (their own kind; they used to
count as neutral), **threat lines** (each mob to the unit it targets, from the feed's `target` guid), **bot target lines**, **the lead's heading
arrow** (the feed has no facing, so it is the direction of the lead's last movement; it needs a walk of 4 yd within 20 s), **healer range**
(a 40 yd ring around each living healer), **aggro range** (a 20 yd ring around the pull targets: the hostile mobs of the pack being pulled,
else the ones fighting), **the line from the commander to the lead**, **times on the walked path** (the clock time the lead was last seen at
a point, kept with the points) and **zone and sub-zone names** (greyed out: dungeon maps have no zone outlines in this dashboard). The planned route runs through every
leg's waypoints, pull groups and boss in plan order: done legs dimmed, the current one bright and thick, the ones to come dashed, with the
leg's name where it starts and the step number on hover. The walked path is the lead bot's position at each poll, kept in memory per agent
(thinned, at most 1500 points, reset when a new run starts) and drawn fading with age. The arrow points from the party to the next stop of the
current leg ("-> pack 2: Relic Hunter, 34 yd"). When several groups run on the same map each has its own colour and a chip to show or hide its
path and dots. A door or object is a small door symbol; its name shows on hover or when zoomed in.

**Marks, hover box, events and run line.** New layers (the **Marks** and **Run** groups of the panel; bosses and health bars start on, the rest off):
**boss mark** (a live boss gets a bigger gold ring, a crown and its name always shown; "boss" comes from the plan), **health bars** over bots and
creatures, **status icons** (skull dead, spark casting, sheep crowd controlled, cup resting/drinking/eating, arrow fleeing), **threat colours**
(a creature's ring is blue when it attacks the tank, red when it attacks anyone else; a white outline marks the tank's current target and a
dashed yellow ring the pull target), **names of hostile mobs** (off), **event pins** (deaths, wipes, boss kills, doors and chests opened or
looted), **event feed** (the last 8 events under the map) and **run line** (timer, section x of y, pack, boss checklist with the next boss).
The Layers panel has a **Legend** that explains every mark. Hovering a unit with the mouse (or tapping it on a phone; tap elsewhere to close)
opens a small box: name or "creature <entry>", kind (boss, hostile, neutral, friendly, critter, bot role), level, health %, what it is
doing ("casting Fireball on Forthur", "drinking", "fighting X", "crowd controlled"), its target, spell, distance to the lead and pack. A row the
feed does not carry is left out. The party table has a **Doing** column. The words and the box are built in `static/unitinfo.js` (tested under node,
`testdata/unitinfo_check.js`); the page puts every string in with `textContent`.
What the feed carries: bots have state, last action and its result, target and now `cast` (the spell name from `Spell` of the P record);
creatures have health, target, flags (crowd controlled, evading, stealthed), `cast` (the spell of the H record) and reaction. The feed has **no level**
for a bot or creature, no creature name (only the plan's notes name them) and no crowd-control flag for bots: those rows and icons are absent.
Card fields added: `bots[].cast`, `mobs[].cast` and `events`. `events` (`{at, kind, text, pos, x, y}`, kinds death, wipe, boss, section, open, loot,
use) is kept per agent by the dashboard process, from the run report (its death list with times, `wipes`, finished legs: a finished leg with a
boss is a boss kill, named from the plan's notes) and from the accepted open, loot and use orders of the trace; at most 80, reset by a new run. An
event seen already in the first report after the dashboard started has no time of its own (the kill of a boss is stamped when the leg is seen
finished, to the poll); a pin sits where the bot died, at the boss's place, or at the party for an order.

**Path layers (Plan, Walk, Run and Mobs groups).** Layers that draw lines have **opacity** and **thickness** sliders (the trails also **minutes shown**,
the top of the slider being the whole run); they show under the layer's row while it is on and are saved once for all maps in `localStorage`
`maplayers.style.v1`. On by default: the lead's trail (in its role colour, brighter than the others, whole run), the **waypoints** (the current leg's planned
walk line dashed, its via points, the target point, and in orange-red the pieces of the lead's path more than 15 yd from the planned lines), the
**pull groups by state** (the numbered circles turn green cleared, yellow next or being pulled, grey skipped, red failed with the reason on hover; the
boss gets a check mark when killed) and the **rest and gather spots** of the run. Off by default: **other members' trails** (role colour, last 5 minutes),
**objects with state** (doors, chests, altars, keystones: pending, open, looted, used, failed), **patrol routes** (the plan's patrol loop and the patrol's
live position, when the feed sees it), the **ghost** (the best earlier run of the dungeon drawn faint, a dot where it was at this run's time, and "ghost is
1.3 min ahead" or "behind" in the run line), **stall spots** (the party stood within 5 yd for over 30 s: how long and why, from the lead's combat flag, the
rest gates and its action), **refused or unreachable walks** (red X with the module's reason: refused `move` orders, pulls and walks the navmesh did not
reach, bots reported stuck), **aggro rings around hostile packs** (20 yd; plus or minus 1 yd per level of difference, 5 to 45, once a creature level is
known) and **combat lines** (red mob to its victim, green healer to the friend it targets, cyan caster to the creature it crowd controls) and the **death
heat map** (where parties died in the latest 20 runs of this dungeon).
Data: the member trails, stalls, refusals, rests, gathers, pulls and object steps come from the run's own trace, which the server follows by reading the bytes
it gained since the last poll (`digest.go`, `live.go`; the trace is only read). `GET /api/live/<agent id>?min=N` answers with that digest, the walks cut to the
last N minutes (at most 120); `GET /api/ghost/<plan>?exclude=<run id>&legs=<plan sections>` the fastest earlier clear of the plan, else the run that got through
most sections; `GET /api/heat/<plan>` the death places of the latest 20 runs. Both read the run traces (`r-*.jsonl`) under `--tests-dir` (and `--traces-dir`),
at most 600 files, 6 folders deep, listed at most every 90 s, parsed once per change and cached (28 digests). What the data lacks is left out and the legend
says so: the feed has **no creature level** (rings are 20 yd), **no name of who put a crowd control on** (the cyan line needs a `cmd cc` order of the commander),
**no heal spell target** (a healer that targets a friend is taken to heal it), and patrols are only drawn when the plan has them. Runs that started at a later
section (`from_leg`) are not counted as a clear of the whole dungeon. Botwatch files are not read: they carry no plan name.

**Limits.** The map shows what `bot_view` reports: the module's perception of the party, so a creature none of the bots sees
is not there. A run on a plan the dashboard cannot find or load shows the reason in place of the map. A card of an agent that
answered once and then stopped stays as "offline"; agents that never answered are listed in the header only.

## Minimaps

The dashboard can draw the run over the real in-game map. The pictures come out of **your own game client** and stay on your
disk: `turtle-agent minimap-extract` reads the client's MPQ archives (read only; a pure-Go reader, no StormLib) and writes
`<map id>.png` and `<map id>.json` into a folder; nothing extracted is ever committed (the default folder
`~/turtle-agent-minimaps` is outside the repository, and `/minimaps/` is in `.gitignore` for a folder inside it). Run it yourself:

```
turtle-agent minimap-extract --client "/path/to/WoW/Data" --out ~/turtle-agent-minimaps --maps all-instances
turtle-agent minimap-extract --client .../Data --out ~/turtle-agent-minimaps --maps 70,209 --mmaps ~/turtle-agent-mmaps
turtle-agent minimap-extract --client .../Data --list          # what each dungeon and raid would get, nothing written
turtle-agent minimap-extract --client .../Data --out ... --verbose   # log every map and every skipped tile
turtle-agent dashboard --minimap-dir ~/turtle-agent-minimaps
```

`--client` is the client's `Data` folder. The archives are read in the client's load order (`dbc`, `fonts`, `interface`, `misc`,
`model`, `sound`, `speech`, `terrain`, `texture`, `wmo`, `base`, `backup`, `patch`, then `patch-2` ... `patch-9`, a later one
overriding an earlier one), so a map a patch adds or changes, such as Turtle WoW's own dungeons, comes out as the client shows it.
`--maps` is a comma list of map ids or `all-instances`: every `Map.dbc` row whose instance type is dungeon (party) or raid. A map
can have up to three images, written as `<id>.<source>.png` + `.json`; the first source that exists in `--prefer` (default
`worldmap,minimap,navmesh`) is also copied to `<id>.png` + `<id>.json`, which is the one the dashboard draws:

| Source | What it is | Where it comes from |
| --- | --- | --- |
| `worldmap` | the map picture the client shows for the dungeon (the 4 x 3 tiles of `Interface\WorldMap\<area>`, cut to 1002 x 668); a dungeon with several floors gives `<id>.worldmap-2f.png`, ... too | `WorldMapArea.dbc` rows of the map: the picture's edges in world coordinates |
| `minimap` | the map's minimap picture: the terrain tiles, the client's own indoor WMO minimap tiles, or the WMO tiles drawn over the terrain | `Textures\Minimap\md5translate.trs`, `Map.dbc` and the map's WDT/ADTs: a terrain tile `map<A>_<B>.blp` is column A (from world y), row B (from world x); a WMO tile `<base>_<group>_<A>_<B>.blp` is placed by the WMO's MODF position and yaw and the group boxes of its MOGI (2 px per yard, bottom-left anchored tiles) |
| `navmesh` | a height-shaded top-down render of the server's navmesh (transparent background, 2 px per yard) | `--mmaps`: the `.mmtile` files of the map, parsed in Go (no cgo) |

The JSON beside each image: `map`, `name`, `source`, `width`, `height` (pixels), `px_per_yard`, and the world coordinates of
its edges `north` and `south` (world x of the top and bottom edge), `west` and `east` (world y of the left and right edge).
The image is north up and west left, as the in-game map is: pixel column `c` is world `y = west - c / px_per_yard`, pixel row
`r` is world `x = north - r / px_per_yard`. The dashboard draws it at `(-west, -north)`, `west - east` yards wide and
`north - south` yards tall in the plane its plan drawing uses (`x -> -y`, `y -> -x`). The test pins this against the Uldaman
plan: the entrance, Ironaya and Archaedas land on their rooms of the client's Uldaman map.

A composed minimap's JSON also says `from`: `wmo` (only the client's indoor WMO tiles: a WMO-only dungeon, or one whose
terrain tiles are blank) or `terrain+wmo` (the WMO tiles drawn over the terrain). A map with a usable terrain minimap
keeps the old terrain-only picture byte for byte (no `from`), except the five maps whose indoor part the plan puts over
the terrain: Deadmines 36, Shadowfang Keep 33, Scholomance 289, Stratholme 329 and Karazhan Crypt 800. The WMO tiles are
the art the client itself draws indoors. Every placed WMO group that has tiles is drawn, groups whose bounding box
bottom is higher (`PZ + z0`) over lower ones; outdoor props such as farms and ziggurats are included (they are the
client's own art), and a group with no tiles is skipped. A placement whose box lies entirely outside the map navmesh
box grown by 200 yd is not drawn (when `--mmaps` gives a navmesh), and two known placements of another zone are listed
below. The placement transform is settled and pinned by tests: 2 px per yard, a tile index step of 128 yd, bottom-left
anchored tiles, `U = 2(x-x0)`, `Vup = 2(y-y0)` with B counting up from the group box minimum, a 180-degree turn,
`th = +rot.y`, `P = (17066.666 - pos[2], 17066.666 - pos[0], pos[1])` for an ADT placement and `P = 0` for a WDT one.
A composed picture is capped at 8192 px a side (the scale is lowered, the tiles are still sampled at 2 px per yard),
at 16384 group drawables, at 4096 tile images (256 MiB of decoded pixels) and at 33.5 M sampled pixels per map; a map
over a cap gets a reason instead of a picture. A tile the archives lack or that fails to decode is skipped, and a WMO
or a group with no usable tile never ends the run.

A picture whose art has a known client-data gap carries a one-line `note` in its JSON (readers ignore the key):

| map | note |
| --- | --- |
| 189 Scarlet Monastery | Herod's room has navmesh but no client minimap art; the plan point there is off the picture |
| 819 Timbermaw Hold | part of the WMO has no client minimap art (navmesh areas are drawn red); the entrance is off the picture |
| 822 Frostmane Hollow | the two Silithus mini-hives (uids 303286 and 303232) belong to another zone and are not drawn |

When a source gives no picture, the extractor writes `<id>.status.json` beside the images with one line per missing
source: `{"minimap":{"reason":"..."},"navmesh":{"reason":"..."},"worldmap":{"reason":"..."}}` (only the missing ones,
each reason capped at 160 characters). The dashboard shows it on the disabled slot and otherwise uses its own default
text. A missing indoor minimap names the case: (A) a WMO-only map, (B) blank terrain tiles, (C) no terrain tiles, then
why the WMO picture is missing (no WMO placement, no WMO tile lines for it, none of its tiles decoded, or a blank
composed picture). A map whose pictures all exist gets no status file, and a stale one is removed.

The output table lists, for every map, its type, the main image, its size and the other images; a minimap shows its
makeup as `minimap(terrain)`, `minimap(wmo)` or `minimap(terrain+wmo)`. Archives it cannot read (a compression method
used for audio only) are an error; a map no source has an image for is listed as `none`.

**The three picture slots.** `GET /api/minimap/<map id>` always answers the same three slots in Zul'Farrak's order:
`worldmap` (the main picture, then one entry per floor: `World map 2f`, ...), `minimap` and `navmesh`. Each entry has
`kind`, `floor` (`""` or `2f`), `label`, `available`, and for a missing one a one-line `reason` instead of the picture's
`url`, `x`, `y`, `w`, `h`. Every entry also keeps the older `name` and `source` fields: `name` is now the same as `label`
(`World map`), and `source` is the slot's kind. The folder's own `<id>.<source>[-<n>f].json` is the slot of that source;
the main `<id>.json` stands in only for a source the folder has no file of (an old folder), so a picture is never listed
twice. A map the folder has nothing for still answers 200 with three missing slots; 404 means no `--minimap-dir` at all.
The page's picker shows the three, disables a missing one as `<label> (none)` with its reason on hover, and draws the first
available picture, so a saved choice of a kind the folder lost falls back instead of leaving a blank map.

A slot's own reason comes from an optional `<id>.status.json` beside the pictures, which `minimap-extract` writes and an
operator may edit: `{"minimap": {"reason": "..."}, "navmesh": {"reason": "..."}, "worldmap": {"reason": "..."}}`. It is read
only if it is at most 64 KiB and a file of the folder itself (no symbolic link out, no other name); a malformed or oversized
file is ignored, unknown kinds are ignored, control characters (a line break, a tab) become spaces, and each reason is cut to
160 characters. Without it the dashboard uses a
built-in line per kind (for the minimap: an indoor dungeon whose in-game minimap the client draws from WMO tiles;
`minimap-extract` composes it from those tiles, and a map it could not compose gets its reason here).

## Navmesh walking

`walk_to` walks in a straight line and knows nothing of walls. `walk_path` plans on the navmesh the server's own
PathFinder uses (the core's `data/mmaps`), then runs the whole path as one walk, as a client does: one
`MSG_MOVE_START_FORWARD`, a `MSG_MOVE_SET_FACING` at each corner that turns 0.01 rad or more from the facing last sent
(smaller turns add up to one), with `MOVEFLAG_FORWARD` still set and the position the walk has reached on the new leg,
heartbeats every 500 ms (read on the session's 100 ms tick) and one `MSG_MOVE_STOP` at the goal; it stops early only
when the server moves or roots the player, the call is cancelled, or a partial path ends short of the goal (it then
plans on from there). The core holds a facing packet to the same distance-for-time bound as a heartbeat (`Movement.cpp`,
no check of its own), which a point on the path reached at the run speed keeps. It uses the same checks as `walk_to`
(see [Movement](#movement)): a client time that only runs forward, the run speed the server has set, each step held to
the anticheat's bound, no walking while rooted or in the air. Between corners the heartbeats carry the height of the
ground (the mesh is sampled every 4 yd), so a straight leg over a hill does not cut through it. The path starts and ends
on the floor under the points asked for, not at their height. Each leg moves along its 3D length at the run speed, so no
beat changes height by more than the speed allows (the anticheat's z check), even up the steepest walkable slope; a
packet's height is also held within that of the one before, should a ground sample land on another floor. Arriving at
the path's end on the mesh floor clears `status.z_unknown`.

When the server moves the player mid-walk (fear, a scripted move, a teleport on the same map), knocks it back or
roots it, `walk_path` waits until the server lets go (the spline's end, the knockback's landing, up to 15 s for a
root), plans again from where the player now is and walks on. It plans again up to 8 times; the 9th interruption ends
the walk. A teleport to another map ends it too. A partial path that gained at least 10 yd is planned on from its
end, as the tiles round it may reach further, up to 32 times. The report says how it ended:

| `result` | Meaning |
| --- | --- |
| `arrived` | the goal was reached |
| `partial` | stopped short: the mesh has no route all the way (it walks to the reachable point nearest the goal), the server kept moving the player, or took it to another map; `reason` says which |
| `no_path` | nothing walked: the start or the goal is not on the mesh (no walkable polygon within 5 yd), or the map has no route |

A goal more than 7 yd from the mesh (in the air, say) gives `partial`, as in the server's PathFinder, with the path
ending on the floor nearest it.

`trace` lists each `plan` (status, points, length), each `walk` (the walk along that plan's path, `from`, `to` its
end, and the error that ended it early) and each `interrupted`, with `t_ms` since the call began.

**Tiles.** Copy the server's `data/mmaps/NNN.mmap` and the map's `NNNyyxx.mmtile` files into `mmaps_dir`; the
wow-play-harness tile cache folder (one subfolder per server) works as it is. Tiles load on demand: a map with up to 16
tiles (a dungeon) loads whole, a bigger one the tiles round the start and the goal, one tile wider, and three wider
when that gives no complete path. At most 64 tiles stay loaded per map: a box that would hold more keeps the tiles
nearest the straight line from start to goal, and the least recently used tiles are unloaded first. Tiles copied in
later are picked up on the next call. **`mmaps_dir` must hold every map you play on** (Kalimdor is map 1, the eastern kingdoms 0): a
map without its `.mmap` or tiles fails with a `DataMissingError` (`errors.As`). The bot brain's walks tell it from other failures: the
action that walked (`melee_reposition`, `heal_reposition`...) stays off for 5 min and the error is reported once per 5 min, not at every
tick. A map image's background (see [Map images](#map-images)) reads its tiles into a
planner of its own, so drawing an image unloads none of these.

**Engine.** The planner is the core's own Detour (recastnavigation, as vendored by
[tortoise-wow](https://github.com/tortoise-wow/tortoise-wow) with its 64-bit poly refs), compiled in through cgo, and
it makes the PathFinder's calls: `findNearestPoly` with its extents (5 yd round, 10 yd of height, never a floor more
than 3 yd above the point), `findPath` with a player's filter (ground and water; lava, slime and too-steep slopes left
out) and `findStraightPath`. So the corners are the ones the server computes; only the two ends differ, put on the
floor where the server keeps the points it was given. Two alternatives were turned down:
a pure-Go Detour such as [arl/go-detour](https://github.com/arl/go-detour) (zlib) has 32-bit poly refs and 12-byte
links, so it cannot read these tiles without a fork, and its port of Detour is older than the core's; and a port of
the harness's Python fallback (polygon graph, A*, funnel) would give similar, not the same, paths. The cost is cgo:
a C++ compiler at build time, and cross-compiling needs a cross C++ toolchain.

Limits: the mesh has no game objects, so a path can run through a closed door (what the server does with a
headless walk through one is not tested yet); every leg uses the run speed, in water too; the corners are the
server's straight path, not its smoothed one (which follows the same polygon corridor in 2-8 yd steps).

## Fleet: several players at once

`turtle-agent fleet` starts one `login` process per entry of a fleet file, for example a 5-player dungeon party or both
battleground sides, each with its own RPC port.

```json
{
  "members": [
    { "account": "TANKACC",   "character": "Tankone", "rpc": "127.0.0.1:8771" },
    { "account": "HEALACC",   "character": "Healtwo", "rpc": "127.0.0.1:8772", "reconnect": 5 }
  ]
}
```

```sh
bin/turtle-agent fleet --config fleet.json            # settings from ~/.config/turtle-agent/settings.json
bin/turtle-agent fleet --config fleet.json --settings other.json --stagger 3s --wait 3m
bin/turtle-agent fleet --config fleet.json --reconnect 3   # each child logs in again after a dropped connection
```

- `account` is a key into the settings file's `accounts`; passwords stay there, are read by each child, and never appear in the
  fleet file, on a command line or in output. Every account in the fleet file must exist in the settings file.
- `rpc` must be a loopback address with a fixed port (no port 0), and ports and characters must be distinct. The
  supervisor polls each port's `status` method.
- RPC tokens: each child generates its own and writes `rpc-<port>.token` next to the settings file (one file per member
  port); the supervisor reads the right one for each poll. With `rpc_token` in the settings file every child shares that
  token. Clients of a fleet read the file of the member they talk to.
- Children start `--stagger` apart (default 2 s). When all are `in_world` the supervisor prints a table of character, RPC
  address and state; each child's log goes to stderr with a `[Character]` prefix.
- realmd refuses the 15th account that logs in from one address within 300 s with a busy answer (`AuthSocket.cpp:439-456`: it
  counts the accounts already logged in in the window, 14, plus the new one), so the fleet shares one login budget of 14
  per 300 s plus a 10 s margin (a slot is booked before the child starts, realmd stamps the login a few seconds later). The
  supervisor books a slot for each child it starts; a start that would be the 15th waits until the oldest login has left the
  window and says so (`fleet: waiting 4m12s to log in Name: realmd allows 14 logins per 5m0s ...`). `--wait` counts from the
  last start. With `--stop-on-failure` a child that fails during that wait still stops the fleet at once. Logins from other
  programs on the same address (a game client, another fleet) are not seen. The query compares `last_login` with
  `NOW() - 300` as a number in `YYYYMMDDHHMMSS` form, which looks like a window nearer 3 minutes than 5 (inferred from the
  source, not measured), so 300 s is on the safe side.
- `--reconnect N` (default 0) is passed to every child as `login --reconnect N`: after an unplanned disconnect the child
  logs in again, up to N times. A member's `"reconnect": N` overrides the flag for that member (`0` turns it off).
- The children share the budget through a loopback port the supervisor opens (the internal `login --login-budget ADDR`).
  Every call needs a random bearer token that the supervisor puts in each child's environment
  (`TURTLE_AGENT_LOGIN_BUDGET_TOKEN`, never argv); calls with an `Origin` header or another `Host` are refused, and bookings
  are capped (at most one hour ahead, 512 outstanding). Each reconnect books its login there, and a busy answer from realmd,
  at the first login or at a reconnect, closes the window for the whole fleet. A reconnecting child logs `reconnect: waiting
  ... for the fleet's share of realmd's login limit` when it has to wait, and falls back to its own window if the
  supervisor cannot be reached.
- A child that exits non-zero is reported (`fleet: Healtwo exited with code 6 (the server closed the connection)`) and
  the others keep running; so does a child whose process could not be launched, or that exits before the fleet is in world.
  Every exit is reported once, and the fleet ends when all children have exited or on Ctrl-C. `--stop-on-failure` brings back
  the all-or-nothing fleet: the first failed child stops the rest. An exit 10 (a logout that did not complete) never stops
  the others.
- Ctrl-C, SIGTERM or SIGHUP (a closed terminal) sends one interrupt to every child so they all log out cleanly; a child that
  has not exited after 60 s is killed. On Linux a child also gets an interrupt from the kernel if the supervisor dies without
  forwarding one (a SIGKILL), so it logs out instead of staying in world.
- Exit code: 0 only when every child exited 0; 9 when any child exited non-zero, was killed or could not be launched, or when
  the fleet is not fully in world after `--wait` (default 2 m; it then stops every child). The last line says how many failed.

## Addon host

The addon host runs the real TortoiseBotsManager addon (Lua 5.1) next to the agent, wired to the live session, so a
test clicks its buttons and reads what it shows instead of a person doing it in the game client. Layout and pixels
still need the real client; everything the addon decides (transport, toggles, lamps, roster rows, CC marks, the bot
panel, the ACK/ERR it keeps) runs from the addon's own code.

```sh
tools/tbmcontract/fetch.sh            # once: the addon at the pinned commit, into .cache/ (or set tbm_addon_dir)
r addon_start '{}'                    # loads the addon; it sends its handshake (".bot help") as on login
r addon_click '{"button":"Fight.AoE"}' # /tbm, the Fight tab, then the switch's own OnClick; waits for the ACK
r addon_state '{}' | jq '.switches'   # [{"key":"aoe","text":"AoE Off","lamp":"grey","state":"off",...}, ...]
```

- How it runs: `addon_start` extracts `internal/addonhost/lua/` (the host, a small JSON codec and the 1.12 widget
  mock shared with the TBM contract test) to a temp directory, starts `lua5.1 host.lua <addon dir>` and removes the
  directory once the host has read it. The host loads the `.toc` files in order and fires `ADDON_LOADED`,
  `PLAYER_LOGIN` and `PLAYER_ENTERING_WORLD`. Requests go in on stdin; replies come back on file descriptor 3, one
  JSON object per line, so nothing printed to stdout can pass for a reply (a line that is not JSON kills the
  sidecar). The RPC caller cannot pick the interpreter or the checkout: `lua5.1` from PATH, the addon from the
  settings or the pinned cache. A sidecar rather than a Go Lua VM: the addon runs on the C Lua 5.1 the contract test
  uses, the module keeps no dependencies, and an addon error or endless loop cannot take the agent down (a call that
  does not answer in 10 s of the addon's own time, sending excluded, kills the sidecar; `addon_stop`, then
  `addon_start` again). Saved variables start empty, like a fresh install.
- Sandbox: each addon file runs with `setfenv` in an environment holding only the mocked WoW API, the 1.12 short
  names (`getn`, `strfind`, `format`, …), `string` (without `dump`), `table`, `math`, a formatting-only `date`, and
  `print`, which writes to the chat frame. `os`, `io`, `loadfile`, `dofile`, `loadstring`, `require`, `package`,
  `debug`, `getfenv`/`setfenv` are not there, and the `.toc` may not name files outside the addon. It keeps an addon
  from reaching the agent's machine by accident or by a hostile checkout; it is not a limit on CPU or memory.
- Lifetime: the sidecar ends with `addon_stop`, when the agent logs out or exits (the agent stops it after its last
  RPC call), when its stdin closes, and on Linux also when the agent is killed (`PR_SET_PDEATHSIG`).
- Into the addon: every `system` event as `CHAT_MSG_SYSTEM` (the raw text), `addon` events as `CHAT_MSG_ADDON`
  (prefix, text, the channel upper-cased, sender), whispers to the player as `CHAT_MSG_WHISPER`, and `in_world` or
  `new_world` as `PLAYER_ENTERING_WORLD`. The player, target, party1-4 and raid1-40 units come from `status`,
  `unit` and `party`: a change fires `PARTY_MEMBERS_CHANGED` (and `RAID_ROSTER_UPDATE` in a raid) at the place of
  the group event in the log, so the addon drops its transport verdict before it reads the next roster, as the
  client does; a target change fires `PLAYER_TARGET_CHANGED`, a party member's death `UNIT_HEALTH`. Not known to the
  agent, so not shown to the addon: classes of other units (`UnitClass` returns nothing), ghosts (reported dead),
  item names (`GetItemInfo` returns nothing, the bot panel shows question marks), trades (`TRADE_SHOW`).
- Out of the addon: `SendAddonMessage` goes out as an addon message on its channel, `SendChatMessage` as chat of
  its type (a `.bot` line as say, which the server runs), `TargetUnit`/`TargetByName` as a `target`. These are the
  same send paths as `addon_send`, `chat` and `target`; the host reads the session only through the event log and
  its read-only views, so the session loop stays the single owner of the connection. Each send is in `sent`, with
  the error when the agent could not send it. `GuildUninvite` and `StaticPopup_Show` are not provided (the addon
  checks for them), so releasing a claimed bot from the Guild tab does nothing.
- Clicks are a player's: `addon_click` types `/tbm full` when the window is closed (which polls the roster, as in
  the game), clicks the tab, then the widget whose text is the label (exactly, else the one that starts with it:
  `AoE` finds `AoE Off`) among the visible ones in that view. A greyed-out button is refused (`force` calls its
  `OnClick` anyway). Roster and Guild buttons tick exactly the rows of `name`/`names` first (only rows on screen,
  seven on the Roster tab); `Fight.CCAssign`/`Fight.CCClear` open the marks panel (`CC Mark`) and press Next or X
  in the row of `mark` (Next gives the mark to the next online bot, as in the game, not to a bot you name); BotPanel
  buttons open the bot's panel (`/tbm bags <bot>`, which asks for its inventory), its `view`, and for items click
  the item at `bag`/`slot` and then Equip, Unequip or Give to me. Any other widget: `tab` and `label` (and `row`,
  e.g. the Party tab's role buttons: `{"tab":"Party","bot":"Gorm","label":"Tank"}`).
- The addon throttles its own sends (one per 0.35 s) and queues the rest, so a click right after another send leaves
  a little later, from the queue. With the real clock (default) the host ticks the addon's `OnUpdate` every 100 ms
  and `addon_click` waits until the addon sent something and the server's reply arrived (ACK/ERR, the end of an
  inventory or roster, or any line for text replies) or `wait_ms` ran out; nothing sent within 1 s ends the wait
  (the click only changed the window). `clock: manual` stops the clock: only `addon_tick` advances `GetTime()` and
  runs `OnUpdate` (in 100 ms steps), events are fed on every call, and `addon_click` does not wait. The tests use it.
- The addon's periodic work runs as in the game: a roster poll 1 s after loading (its last poll is "never") and 3.5 s
  after entering the world, every 8 s while the window is open (20 s closed), and 1.4 s after a non-action command.
- `make addon-host` fetches the addon and runs the end-to-end tests (hire a bot, the addon shows it, click AoE, the
  ACK turns the lamp grey and the next roster agrees); `make test` runs them too when the addon is cached and
  `lua5.1` is installed, and skips them otherwise.

## TBM contract test

`make tbm-contract` checks that the TortoiseBots server module and its TortoiseBotsManager addon (TBM) still
understand each other. It loads the real addon (Lua 5.1, [Sagiroth/TortoiseBotsManager](https://github.com/Sagiroth/TortoiseBotsManager),
MIT) under a mock of the 1.12 widget API, replays `TBM:` lines as `CHAT_MSG_SYSTEM` and `CHAT_MSG_ADDON` events, and
compares the addon's state with what the lines say: `TBM:VERSION`, `CAPS` and `TRANSPORT`, the roster (`ROSTER_*`), CC
assignments, the claimed list, per-bot `BOTSTATE` (move, loot, aoe, autocc, ... spread), `INV_*` snapshots, and `ACTION_ACK` /
`ACTION_ERR`. It also fails when the addon refuses a line the protocol allows ("Malformed action response", a roster
snapshot error), when the addon's chat filter would let a protocol line reach the chat frame, and for a `TBM:` verb the contract has
no expectation for. Each mismatch is tagged `[server]` (the line breaks the protocol format), `[addon]` (the addon refuses a
valid line or stores something else) or `[contract]` (no expectation yet).

```sh
make tbm-contract                                # the fixtures: ok-*.txt must match, bad-*.txt must fail
make tbm-contract EVENTS="run1/events.json"      # replay the TBM: lines of a saved events reply
```

`EVENTS` takes what the `events` RPC returns, saved to a file (a bare array, `{"events": [...]}` or the whole reply).
`tools/tbmlines` extracts the lines; it reads `data.raw` when present, because the colour-stripped `text` has lost the
`|r` of fields such as `|ready`. The addon is downloaded at the commit pinned in `tools/tbmcontract/fetch.sh` into
`.cache/` (ignored, never vendored); bump the commit there on purpose. The mock in `internal/addonhost/lua/mock.lua` (shared with the addon host) is copied
from the addon's own `tests/regression.lua` (MIT). Without `lua5.1` or network the target skips with a message
(`TBM_CONTRACT_STRICT=1` makes that an error); it is not part of `make test`. `TBM_ADDON_DIR=/path` uses your own
checkout of the addon instead (read only, the pin is bypassed, and a directory without the `.toc` is refused rather
than replaced). `run.sh` warns when the addon's `tests/regression.lua` is not the file `mock.lua` was copied from. The expectations are in
`tools/tbmcontract/contract.lua`, written from `commands/BotCommands.cpp` and the protocol comments in the addon, not
copied from its parser.

Fixtures (`tools/tbmcontract/fixtures/`) are scrubbed lines from live build 16/17 captures (bot names replaced). No
capture has a non-empty roster yet, so `ok-roster-synthetic.txt` is built by hand from the server's format, and says so.
`bad-known-build16.txt` holds real lines the addon rejects today (an `ACTION_ERR` with an empty intent from the
`.bot action` usage error, and `ACTION_ACK|behavior mimic|party:mimic|...`, whose scope is neither `party` nor
`bot:<name>`); `bad-synthetic.txt` is deliberately broken. A bad fixture lists one `# expect: <text>` per mismatch it must
produce and must fail with exactly those. Offline only: not run against a live server.

## Python

The companion test harness (wow-play-harness, not public yet) has a `HeadlessClient` that wraps this API
with the same calls as its real-client backend (`chat`, `gm`, `state`, `tail_chat`, `tail_combat_log`,
`fight_report`, `live_lines`), plus MCP tools and a scripted fight (`tools/headless_mvp.py`). There is no Python
client in this repository; since the RPC needs a token, that client must send it (see
[RPC authentication](#rpc-authentication)): read `rpc-<port>.token` from the settings folder (or take an `rpc_token`
you set).

## Layout

| Package | What it does |
| --- | --- |
| `internal/auth` | SRP6 and realmd: logon challenge/proof, realm list |
| `internal/worldconn` | TCP framing, the 1.12 header cipher, auth session with the addon block |
| `internal/session` | single-owner session loop: login, ping, chat, addon messages, channels, gossip, names, party, world, actions, combat, raid icons, loot rolls, inventory, equip and use, vendors, trainers and quests, corpse loot, mounts, logout, and `Replay`, which runs a capture's packets through the handlers offline |
| `internal/game` | packet codecs for chat, addon messages, channels, gossip, queries, groups, actions, the combat log, raid icons, loot rolls, the loot window, items, mounts, vendors, trainers and quests |
| `internal/objects` | update-object parser and the object store: units (positions, splines, auras, casts), the items in the backpack and bags, and the quest log |
| `internal/spells` | spell names, mounts and metadata (cost, cast time, range, cooldowns, GCD, effects) by id, from the core's `spell_template` and the server's DBCs (`tools/spellnamegen`) |
| `internal/spellstate` | the agent's own spellbook, cooldowns, global cooldown and cast state, fed by the session (see [Agent state](#agent-state)) |
| `internal/navmesh` | tile loading and path planning over the core's Detour (cgo); `navfixture` builds synthetic tiles |
| `internal/events` | ordered event log with cursors |
| `internal/dungeon`, `dungeons/`, `tools/dungeongen` | dungeon plans and runs: the JSON format, loader, stepper and the driver (`dungeon_run`), the plan files, and the skeleton generator over world database exports (see [Dungeon plans](#dungeon-plans), [Dungeon runs](#dungeon-runs)) |
| `internal/trace`, `internal/tracecheck`, `internal/tracerun` | run traces: the `bt-trace` format, writer, reader and summary files; the pure checks that score a trace; the recorder a run writes with (see [Run traces and checks](#run-traces-and-checks)) |
| `internal/tbm` | the TortoiseBots addon (TBM) protocol: handshake commands, the `TBM:` reply lines, the generated button table, the transport rule and reply matching (`tools/tbmbuttongen` writes the table) |
| `tools/tbmlines`, `tools/tbmcontract` | extract `TBM:` lines from an events file; replay them through the real TBM addon (Lua) |
| `internal/addonhost` | the addon host: the lua5.1 sidecar that runs the real TBM addon on the 1.12 widget mock, and the events it gets |
| `internal/mapview` | map images: the run from frames, events and reports, floors by height, the SVG and HTML renderers (see [Map images](#map-images)) |
| `internal/capture` | packet capture files and the secret scan |
| `internal/dashboard` | the live dashboard (see [Live dashboard](#live-dashboard)): agent discovery, the read-only poller, the card model, the plan drawing, the secrets filter, the embedded page |
| `internal/rpc`, `internal/netutil` | loopback JSON-RPC server |
| `internal/tactics` | the `tactics` block of a plan's boss: the file format and its checks (see [Boss tactics](#boss-tactics)) |
| `cmd/turtle-agent` | the CLI (`login`, `fleet`, capture tools including `capture events`, `map`, `dashboard`, `trace-score`, `pulltest`, `rotation`) |

Parser tests: every SMSG parser in `internal/game` and `internal/objects` has a `Fuzz*` target (no panic, no hang
within 5 s per input) seeded from the committed captures, a few literal packets copied from live m910q captures
(`internal/game/realpackets_test.go`: gossip, vendor list, raid icons, root and knock back orders, new world; the runs
never opened a trainer, a quest panel or a loot window, so those parsers still run only on `gametest` packets) and the
`gametest` builders. `make fuzz` runs them all; an input a fuzzer finds is saved under `<package>/testdata/fuzz/` and
stays as a regression seed that `make test` runs.

Design and implementation plan: `docs/superpowers/`.

## Licence

GNU Affero General Public License v3.0 or later, see [LICENSE](LICENSE). Versions up to the commit that changed the licence were released under the MIT License (kept in [LICENSES/MIT-turtle-agent-before-agpl.txt](LICENSES/MIT-turtle-agent-before-agpl.txt)); copies you already received under MIT stay under MIT. Parts are ported from [agent-wow](https://github.com/agent-wow/agent-wow) (MIT; the files keep their own MIT headers), and
`internal/navmesh/detour` is Detour from recastnavigation (zlib, `internal/navmesh/detour/License.txt`); see
[NOTICE](NOTICE). Dungeon event data derived from [jrad7/mod-dungeon-clear](https://github.com/jrad7/mod-dungeon-clear) (AGPL-3.0-or-later) is marked with its source where it appears. World of Warcraft is a trademark of Blizzard Entertainment; this project is not affiliated with it.
