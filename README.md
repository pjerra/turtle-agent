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
the [addon host](#addon-host) runs (default: the pinned copy `tools/tbmcontract/fetch.sh` keeps in `.cache/`). `dungeons_dir` is the folder
`dungeon_run` reads plans from (default `dungeons` under the working directory; see [Dungeon runs](#dungeon-runs)). `traces_dir` is the folder run traces go to (default `traces`, or `$TURTLE_AGENT_TRACES_DIR`; see [Run traces and checks](#run-traces-and-checks)).

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
turtle-agent login [--config FILE] [--capture FILE] [--stay DURATION] [--rpc ADDR] [--reconnect N] [--create race,class[,gender]] [<account>] <character>
turtle-agent fleet --config FILE [--settings FILE] [--stagger DURATION] [--wait DURATION] [--reconnect N] [--stop-on-failure]
turtle-agent scrub-check [--config FILE] <capture>...
turtle-agent capture-stats <capture>
turtle-agent capture events [--from RFC3339] [--to RFC3339] [--kinds a,b] [--out FILE [--force]] <capture>
turtle-agent map [--capture FILE] [--events FILE] [--report FILE] [--botwatch FILE [--members a,b]] [--plan dungeons/x.json] [--out run.svg] [--html] [--force] [--mmaps DIR | --config FILE] [--map N] [--box x0,y0,x1,y1] [--from RFC3339] [--to RFC3339] [--pad YD] [--floors auto|off|z,z] [--background=false] [--width PX]
turtle-agent trace-score [--json] [--summary] [--set check=threshold]... <trace.jsonl>...
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
| `dungeon_run` | `name` (a plan in `dungeons_dir`, `a-z0-9_-`), `from_leg` (a leg number from 0 or its name), `teleport` (GM-teleport to the start and summon the party), `no_shortcuts` (no GM revive), `setup` (the GM commands you ran to set the run up, as a list of text such as `["go entrance", "levelup -15"]`; they go into the report's `setup_shortcuts`, the run sends none of them), `buffs` (optional GM boost for the companions: a spell id, a preset name or a list of both, `"world"` being the classic world buffs; see [Dungeon runs](#dungeon-runs)), `brain` (a [bot brain](#bot-brain) preset such as `"frost_mage"`: it plays the leader's character during the run's fights; off by default), `gm` (`false`: the leader's account is a player's and the run sends no GM command at all; `true`: a GM, no probe; omitted: the run asks the server and a probe with no answer counts as a player, see [Dungeon runs](#dungeon-runs)), `gm_level` (with `gm: true`: the account level, 1 to 6, default 3; it gates teleport, `.gm visible on`, revive and buffs), `recover_wipe` (the party, all dead, runs back to its corpses and the run goes on; off by default), `wait_sickness` (wait resurrection sickness out after the leader's corpse run), `resume` (`true`: skip what the party has already passed when it starts inside the leg, off by default, see **Resume** under [Dungeon runs](#dungeon-runs)), `mode` (`"group"`, the default, or `"remote"`: lead a bot group from outside it, see [Remote leader](#remote-leader)) with `lead_bot`, `bots`, `tank`, `healer`, `cc`, `carrier` (the bot that does the object steps), `loot_restore` (`group`, `rr`, `master` or `nbg`: the loot method put back after its free-for-all loot), `object_fallback` (`agent`, the default, or `off`: whether the agent's own character takes an object step over when the carrier's order fails twice, see [Remote leader](#remote-leader)), `team` (`alliance` or `horde`: the party's side for the team gate; omitted: the agent's own), `preflight` (`warn`, the default, `strict` or `off`: the kit check before the run), `on_end` (`stay`, the default, `logout` or `none`), `enter` (`auto`, the default, `gm`, `walk` or `off`: how the run gets a party that is outside the plan's map in, see [Remote leader](#remote-leader)) and `ignore_entry_limit` (try a 6th new instance within the hour) for it, and in either mode `watchdog` (`{"warn_s":120,"recover_s":240,"fail_s":420}`: the stall levels of the [run watchdog](#run-watchdog), seconds without progress; a field left out takes the plan's or the default, a negative one turns its level off) | `started`, `plan`, `map`, `from_leg`, `legs[]`, `buffs[]` (the spells the run will apply, `Name (id)`), `brain`, `leader_gm` (`true`, `false` or `probe`), `mode`, `lead_bot`; the run goes on in the background, see [Dungeon runs](#dungeon-runs) |
| `death_status` | | `dead` (health 0, spirit not released), `ghost` (released), `resurrect` (an open offer: `caster`, `name`, `sickness`, `timer`; null for none), `reclaim_in_ms` (the corpse may be reclaimed then); see [Death and the corpse run](#death-and-the-corpse-run) |
| `accept_resurrect` | | Answers the open offer with yes (CMSG_RESURRECT_RESPONSE) and returns it. Errors when the player is alive or nothing is on offer |
| `release_spirit` | | CMSG_REPOP_REQUEST: the dead player becomes a ghost at the graveyard. Errors when alive or already a ghost |
| `corpse_query` | | MSG_CORPSE_QUERY: `found`, `map`, `x`/`y`/`z`, `corpse_map`. A corpse in a dungeon the ghost is not in shows the dungeon's entrance (`map` is the entrance's map, `corpse_map` the dungeon's) |
| `reclaim_corpse` | | CMSG_RECLAIM_CORPSE for a ghost within 39 yd of its corpse and past the reclaim delay (an error before it) |
| `area_trigger` | `id` | CMSG_AREATRIGGER, as the client sends it on stepping into a trigger's volume (the server checks the position); a ghost uses it at a dungeon's portal |
| `pet` | | The player's pet, from `UNIT_FIELD_SUMMON` of the player and the pet's own unit row: `present` (false: no pet), `guid`, `entry`, `name`, `level`, `dead`, `health`, `max_health`, `power_type` (2 focus for a hunter's pet, 0 mana for a demon), `power`, `max_power`, `happiness` (the pet's POWER5, 0 to 1,000,000) and `happiness_state` (1 unhappy, 2 content, 3 happy; absent for a demon), `loyalty`, `in_combat`, `target` (what it attacks), `distance`, `moving`; and its action bar, from `SMSG_PET_SPELLS`: `bar` (the session has it), `react` (`passive`, `defensive`, `aggressive`), `command` (`stay`, `follow`), `enabled` (false while the bar is off, e.g. mounted), `spells[]` (`spell_id`, `name`, `slot` 3 to 6 or -1 off the bar, `autocast`, `passive`), `refused` (the reason of its last refused cast). See [Pets](#pets) |
| `pet_action` | `action` (`attack`, `follow`, `stay`, `passive`, `defensive`, `aggressive`, `cast`, `autocast_on`, `autocast_off`), `target` (guid; `attack` and `cast` default to the player's target), `slot` / `spell_id` / `name` (which spell, for `cast` and the autocast actions; it must be on the pet's bar) | CMSG_PET_ACTION (or CMSG_PET_SPELL_AUTOCAST for the autocast actions), as a client sends them from the pet bar; returns the `pet` result with the order applied. Errors without a pet, with a dead pet, while the bar is off, and for a spell not on the bar. The server drops what it refuses (a target the player may not attack: `pet_error` event with `data.feedback`) |
| `dungeon_status` | | `running`, `state` (`idle`, `running`, `done`, `failed`, `stopped`), `report`, and the [run watchdog](#run-watchdog)'s view for a poller: `last_progress_at`, `stalled_for_s`, `stall_level` (`none`, `warn`, `recover`, `fail`) |
| `dungeon_stop` | | the same as `dungeon_status`, after the run has ended |
| `brain_start` | `strategies` (a list of preset or strategy names, e.g. `["frost_mage"]`), `role` (`dps`, `tank`, `healer`; empty takes the role of the strategies, `dps` when none names one; it must fit them), `tank` (the party member to follow; default: found by class and target), `no_move` (never walk the player: no positioning, no stepping out of ground effects, no run to the tank; default off; a dungeon run's brain moves) | the `brain_status` result. Turns the [bot brain](#bot-brain) on; refused while one runs |
| `dungeon_run` (run traces) | `run_id` (name the run: letters, digits, `.`, `_`, `-`, at most 64; default `r-<utc yyyymmddThhmmss>-<4 hex>`), `trace` (`false` writes no trace file; on by default), `trace_meta` (free text for the trace header such as `{"tb_sha": "..."}`, at most 16 entries) | `run_id`, `trace_file` (where the run's trace goes). The report has `run_id`, `trace_file` and, once the run ended, `trace` (path, counts, `verdict`, `failed[]`, `checks[]`); see [Run traces and checks](#run-traces-and-checks) |
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
| `commander` | `op` (`bind`, `unbind`, `status`, `view` or `cmd`), `lead_bot` and `bots` (bind), `on` (view), `verb` and `args` (cmd: `attack`, `pull`, `pullback`, `cc`, `focus`, `move`, `stay`, `follow`, `rest`, `revive`, `summon`, `loot`, `open`, `use`, `lootmethod`), `wait_ms` (default 4000) | `sent` (the `.bot` line), `reply` (`kind` `ack`, `err` or `timeout`, `ack`/`err` parsed, `lines`). `loot`, `open` and `use` wait through `ACTION_PENDING` for the final line and add `order` (`pending`, `loot`, `opened`, `spell`, `waited_ns`). The [remote commander](#remote-leader) by hand |
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
that carries a plan out through the RPC is described under [Dungeon runs](#dungeon-runs). `dungeons/uldaman.json` is the first one.

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

- `version` is 1 (a file of another version is refused), `map` the instance map id, `entrance` where the party stands after the
  portal. `notes` (plan and leg) are free text for people.
- `entrance_trigger` (the id of the entrance's area trigger, Uldaman 286) is what a ghost's corpse run sends at the portal. A remote run's
  enter phase reads two more optional keys: `entrance_tele` (the `game_tele` name that puts a player at the entrance inside, Uldaman
  `ulda`, game_tele 169; letters, digits and `_`) and `portal` (`{"map": 0, "x": .., "y": .., "z": ..}`, the centre of the entrance's area
  trigger on the outer map; Uldaman: areatrigger 286, radius 10). A plan without them can only be entered by hand.
- `watchdog` (optional, `{"warn_s": .., "recover_s": .., "fail_s": ..}`) sets the plan's stall levels for the [run watchdog](#run-watchdog);
  the loader refuses levels that are not in order (`warn_s` < `recover_s` < `fail_s`, defaults filled in). `dungeon_run`'s `watchdog` wins field by field.
- A **leg** is done in this order: walk to `to`; pull each of `pull_groups` in order (`entries` are the creature entries, one
  position per member in `positions`, the first is the one to pull; `ambush: true` marks a pack of stealthed mobs, see
  [Dungeon runs](#dungeon-runs); `radius` (yards, 1-60, default 25) is how far from a position a unit of the entries may stand and still be
  a member of the group, for packs of one entry that stand closer together than that); run `steps` in order; walk to `boss.position` and kill the
  boss; run `after_boss` (steps of the same kinds, done once the boss is dead or skipped). Each part is optional, but a leg needs at least one of pull groups, steps, a boss and after_boss steps. `skip` lists creature entries the leg
  never waits for (rares, escorts, friendly NPCs, adds that die with the boss). `boss.first` lists entries to kill before the boss,
  in order. `rest` overrides the leg's rest gates (all keys optional, `0` or absent keeps the default): `{"health": 90, "healer_mana": 50, "mana": 35, "max_s": 60}`
  (percentages 1-100; `max_s` 1-600 seconds; `walk_yd`, `walk_health`, `walk_healer_mana` and `walk_mana` set the walking rest, see **rest gate** under
  [Dungeon runs](#dungeon-runs)).
- A **step** is exactly one of `use_object`, `wait_for`, `loot_object` and `use_item`. `use_object` names a game object by `entry`, by `near` (a position;
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
  close), with a `timeout_s` (default 60, at most 600) after which the plan fails. A door that a lever or key opens may do so well after
  the use (the Seal of Khaz'Mul took 27 s live): keep the 60 s.
- `loot_object` is `use_object` for a chest or node: the same reference (`entry`, `near`, `range`), `stand`, the same walk to it, then the Opening
  cast and the loot (`open_object` with `take`: see [Game objects](#game-objects)), and `expect_item` (`{"entry": 7740, "name": "Gni'kiv Medallion"}`):
  the item that must be in the bags afterwards. The driver waits up to 8 s for it to show and fails the step, naming the item, when it does not;
  a leader that carries it already (a chest looted before, a GM's `.additem`) passes the step without opening anything (noted). One retry on a failed open.
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
`use_object`, `loot_object`, `use_item`, `kill_boss`, `wait`, `done` or `failed`) and repeats it until the driver calls `Complete` (or `Fail` with a reason);
it asks a `World` (position, game objects in view, hostiles within a radius) and decides by itself that a walk is not needed when the
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

`go run ./tools/dungeongen -check dungeons/uldaman.json -dir tools/dungeongen/testdata/uldaman -team alliance -friendly-factions 122` lints a plan against
the export (`creatures.tsv`) and prints a warning for each boss, `boss.first` entry or pack member that has `npc_flags` (a quest giver, vendor or trainer is no
mob) or a faction named friendly to the leader's team; what is gated to the other team is not looked at, and the flags of a part gated to a team are not repeated.
dungeongen cannot tell friend from foe by itself: that is in `FactionTemplate.dbc`, which the export does not carry, so only 35 and the factions of
`-friendly-factions` count as friendly, and a faction that creatures with `npc_flags` also use is noted as possibly friendly. Faction 122, for one, is
the Ironforge dwarves': friendly to the Alliance, and nothing says it is hostile to the Horde but the data of the users of 122 (Bael'dun soldiers are Horde kill targets).
`-strict` makes any warning exit status 1, for CI. Generating a skeleton puts `WARNING:` lines in the leg notes for a boss with `npc_flags` or a faction of `-friendly-factions`.

`internal/dungeon`'s `TestUldamanOnNavmesh` plans a `path_to` route through every waypoint of `dungeons/uldaman.json` in the order
a run goes and asks for status `complete`; `TestUldamanPlanMatchesTheWorldExport` checks that each pack member, boss and door
in the plan is a spawn in the export. The navmesh test needs the map 70 files (`070.mmap` and the three `070*.mmtile`) in
`$TURTLE_AGENT_MMAPS`, `~/tb-ci/mmaps-turtle070` or `~/tortoise-wow-server/data/mmaps`, and a cgo build; it skips otherwise. The navmesh
has no doors, so `path_to` walks through a closed one: the door steps are checked in the game, not by that test.

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
  were on the tank: their targets, not threat), `tank_threat_samples`, `loose_add_orders`, `cc_respected`, `target_switches`. Absent without a tank in view. The fake game
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
  `skipped` and `refills[]`. **Lighter gates (both modes):** a gate that finds everyone at the targets on its first frame costs no time (`skipped: true`, result `rested`);
  the gate that opens a leg whose first step is a walk of 60 yd or more lets the party go when everyone is over the walking floors (health 70%, healer mana 40%, other
  mana 25%) instead of holding: result `walking`, `skipped: true`, a note, and the party rests on the way; the gate before the first pull still waits for the targets.
  A leg's `rest` overrides it: `walk_yd` (`-1`: always hold, 1-5000), `walk_health`, `walk_healer_mana`, `walk_mana`. The report totals them as `rest_gates_skipped` (of which
  `rest_walk_passes` walking ones) and gives `rest_share` (percent of the run spent in rest gates, also in the `dungeon_done`/`dungeon_failed` summary, the trace's end counters
  and the `trace-score` `rest_share` check, which still sums the `rest` events' seconds). **Refills (remote mode):** the preflight sends `.bot maintenance <bot>` once and the module
  buys only 6-7 drinks a bot. At a gate where a mana user is short of mana, the driver sends `.bot maintenance <bot>` again, at most once per bot per leg, when its drink count
  (`.bot inv`, read afresh once per bot per leg) is under 3 (`low-drinks`), its drink nudge fired (`nudge`), or the feed says its last action was a drink that failed for want
  of one (`drink` `F` `no-item`: at once) or a drink that failed in two different frames within 10 s (`drink-failed`; TortoiseBots reports item-action failures as `drink,F,-`, with no reason), then reads `.bot inv` again and orders the party to rest anew. `remote.refills[]` has `bot`, `leg`, `why`, `before`, `after`
  (drinks, `-1`: not read) and `maintenance`; each is a `refill` trace event and a note, and `refills` is in the summary. With maintenance off on the server (the preflight's
  `disabled`) or a game that cannot send `.bot` lines, none is sent. A dead member is
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
`pull_retries`, `rest_seconds`, `walk_aggros`, `ambushes`, `leader_deaths`, `resurrections`, `corpse_runs`, `seconds_dead`), `notes[]`, `refusals[]` (see **refusals**), `gm_shortcuts[]`: every GM action the run took instead of
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

**Progress** is any of: the step or leg index advanced; a pull started or ended; a kill, or the fought mobs lost health (so a long boss fight
that wears the boss down never stalls); the lead bot (remote) or the leader (group) moved more than 5 yd; an order was ACKed, *if it is not one of the last 16
orders ACKed* (the same `cmd move` or `cmd rest`, or two orders alternating, sent again and again does not keep a stuck run alive; remote
orders only, a chat order has no ACK); a rest gate finished; an object step ended. A death's recovery (corpse runs, resurrection waits), a
plan's `wait_for` (its own `timeout_s`) and a rest gate while the party is out of combat (its own cap) pause the clock: the time held is not
counted, the stall before it is. **After the recovery has fired**, what a stuck run can fake by repeating itself (a pull started or ended,
an order ACKed, a rest gate passed) is not progress any more; only a kill, lost health, a step, movement or an object step is. So a recovery
that re-pulls a boss that stays stuck at full health cannot keep the run alive: `fail_s` is a ceiling, and the next interval without real
progress goes to the failure.

**Levels**, seconds without progress, `watchdog {warn_s, recover_s, fail_s}` in the plan or in `dungeon_run` (defaults 120/240/420; a
negative field turns that level off; `dungeon_run`'s field wins over the plan's; levels out of order are refused). Each fires once per stall;
progress ends the stall and the levels can fire again.

| Level | What happens |
| --- | --- |
| warn | a `stall` trace event and a report note: the time, the last progress, the phase and plan step the run waits on, the last order and its answer |
| recover | one bounded recovery attempt, by phase (below); noted, and a `stall` event with `recovery` |
| fail | the run stops cleanly: `error` is `stalled: <phase> <step> for N s, last progress <what>`, `state` is `failed`; the end-of-run fight handling and the unbind still run (the watchdog is off by then) |

Recoveries. Remote: **walk** re-plans from the lead bot's position with a shorter leg (`cmd move` at most 40 yd on), **object step** cancels the carrier order's retries so that the step's retry or fallback takes
over, **pull/fight** sends `cmd follow` and ends the fight so that the target is picked again, **feed silent** (no frame for `FeedStale`)
sends `commander view on` again. The feed is also switched on again once, at half of `FeedLost` silence, before the run is failed for it.
Group: **pull/fight** orders `follow` and picks again, an **object step** gives up its wait for the item; a
group **walk** has nothing to cancel (each try plans again from where the leader stands). A phase with no recovery of its own uses the
enclosing phase's; with none the stall is noted and the failure comes at `fail_s`.

The watchdog is looked at from the drivers' own waits, never from a goroutine: a single game call that blocks is bounded by its own
timeout and is judged at the next wait. A player order that reported `ACTION_PENDING` waits for its final answer at most 90 s, whatever budget the
server named (`tbm.MaxPendingWait`).

**RPC.** `dungeon_status` has `last_progress_at`, `stalled_for_s` (0 while the clock is paused) and `stall_level`, and the same with the limits
and totals in `report.watchdog`: `warn_s`/`recover_s`/`fail_s` (0: off), `last_progress_at`, `last_progress` (what it was), `stalled_for_s`,
`stall_level`, `waiting`, `warnings`, `recoveries`, `longest_stall_s`. A poller needs only `stall_level`: `none` is a run that moves,
`warn` and `recover` a run that is being looked after, `fail` a run that has been stopped.

Tests: `internal/dungeon/watchdog_test.go` (the watchdog on a fake clock: each level fires once per stall, progress resets it, the recovery is
attempted once, the failure text, the pause) and `watchdog_driver_test.go` (both drivers: a rest gate holds the clock, a stalled pull recovered once and then failed at `fail_s`, a long fight that wears its mob down never stalls, a walk re-planned, a silent feed, the carrier's retries cancelled); the RPC
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
in the trace's header and file name. A caller that sets `run_id` can stamp the same id on the server side later; nothing is sent to the
server yet. `trace_meta` (up to 16 `key: text` pairs, e.g. `{"tb_sha": "...", "build": "19h", "scenario": "uldaman-1"}`) is copied
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
spell it casts (feed), `act` its last action (feed), plus the agent's `np` (position unknown: x, y, z are 0) and `lead`. Frame mobs (`mobs[]`):
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
| `rest` | a rest gate ended: `seconds` waited, `leg` |
| `stall` | the [run watchdog](#run-watchdog) fired: `level` (`warn`, `recover`, `fail`), `stalled_s`, `last_progress`, `waiting` (phase, step, last order), `phase`; a recovery has `recovery` (what it did) and `recovery_error`, a failure `error` |
| `walk_start`, `walk_end` | a walk of the plan: target `x y z`; the end has `ok`, `error`, `seconds` |
| `death`, `wipe` | a party member died (`a` its guid, `name`); the whole party was dead (a wipe, recovered or not) |
| `combat_on`, `combat_off` | a member's combat state changed between two frames (`a`, `name`), as the server's |
| `gm_shortcut` | a GM command taken instead of playing: `kind` (`teleport_to_entrance`, `teleport_to_leg_start`, `revive`, `buff`), `detail` |
| `stuck`, `unstuck` | a member that should be moving stood still for 5 s, with its position (`a`, `name`, `x y z`, `seconds`, `lead`, `dist_lead`); it moved again or stopped being expected to move (`seconds` is the total) |
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
| `threat_off_tank` | a party member other than the tank held the top threat of a living mob during a pull for longer than this; the tank is `header.tank`, else the `tank` role, else whoever holds the top threat most often | 4 s | no threat data (group runs have none) |
| `deaths` | any death or wipe | 0 | never |
| `order_refusals` | more than this share of the orders was refused (`err` replies and `refusal` events) | 20 % | fewer than 3 orders |
| `feed_stalls` | the longest gap between two feed frames is over this | 10 s | no feed frames (group runs) |
| `gm_shortcuts` | the honest-run flag: the run used any GM shortcut or was set up with `setup` commands, so a player could not have done it | 0 | never |
| `stuck` | a member that should be moving (the lead in a walk of the plan, or a member more than 10 yd from the lead in one; alive and out of combat) did not move 1 yd for this long; the detail lists where, for a per-dungeon stuck-spot map | 5 s, any stall | no walk of the plan, or no frames |
| `facing` | a bot in combat stands (no movement) within 6 yd of its hostile target for longer than this while its `fa` is over 90 degrees (the server rule, without its casting case: the agent does not see casts) | 2 s | the header lacks the `facing` capability, or no frame has a bot in melee range of its target |

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

## Remote leader

Issue #83. `dungeon_run {"name":"uldaman","mode":"remote","lead_bot":"Tanky"}` leads a bot group **without being in it**, through the
TortoiseBots remote commander (TortoiseBots #884: `.bot commander`, `.bot cmd`, the `TBM:VIEW` perception feed;
[player-controls.md section 1a](https://github.com/Sagiroth/TortoiseBots/blob/dev/docs/guides/player-controls.md)). The agent can stand in
another zone or outside the instance; it needs no fighting brain, and the bots do the fighting. The module needs the build with the
remote commander and `AiPlayerbot.RemoteCommander = 1` (off by default: the first command answers `disabled`).

**Perception: `TBM:VIEW`.** While bound with the view on, the module sends a frame about once a second as system lines
`TBM:VIEW|v1|<seq>|B|<lead>,<party>,<hostiles>,<threats>[,<truncated>]`, then `P` (party records: guid, name, health, power, position, map,
target, spell, dead, state, last action and, with TB #924, that action's result letter and failure reason: `PartyRec.Result`/`Reason`, e.g. `drink` `F` `no-item`), `H` (hostiles the bots fight or see: guid, entry, position, health %, target, flags `s` stealthed,
`e` evading, `c` crowd-controlled), `T` (threat: per hostile the top three `guid:pct`) and `E|<lines>`. `internal/tbm` assembles them by
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
| pull group / boss | the nearest hostile of the group's entries **as the bots see it** (not the agent's view), `.bot cmd pullback <guid>`; out of range, the lead bot is moved up to 38 yd first; `too-far` moves it closer and retries; `evading` skips the mob; **`unreachable`** (the tank found no firing position) is not a way to clear the group: the party is walked to 20 yd of the mob, then 8 yd and the pull is sent again from each; still unreachable, the step fails with the mob's position (`... is unreachable for the tank ...; the group is not cleared`), so a pack with a living mob is never counted cleared. Every `unreachable` answer, to a pull or to a `cmd move`, is a note (`unreachable pull|move to x y z (...)`), a trace event `unreachable` and counts in `remote.unreachable`, so the plan's positions can be corrected; **`tank-busy`** (the only tank is in combat, it cannot start a pull while it fights) waits up to 10 s for the tank to leave its fight (or for the mob to come to the party by itself) and sends the pull again, up to 3 times, never an attack order in its place (`tank_busy_waits`; still busy after that, the pull counts as one that drew no fight); `no-tank` (no tank-capable bot in the party at all) falls back to `.bot cmd attack`; **a group the bots do not see is not "already dead"**: it counts as cleared only with evidence for each of its positions (its mobs died there in a won pull of this run, listed dead, a living bot within 25 yd of the position and a whole frame that lists none, a pull of a mob that stood there failed and is named in the notes, on a First add's step the boss seen standing there with no add in view, or the lead bot walked to the position, once per position, and two whole frames in a row showed none; `pack_looks`, `packs_seen_empty`), else the lead bot walks up to 15 yd short of the position and the group is pulled when it shows. "Of the group" is always the entries the step picks its target from: the boss standing at its position is not its First add. A won pull does not end the group (the fight tracks only the pulled mob and what joins it): the driver picks again, so a group's other positions are pulled or looked at as well. Only a won pull's dead mobs mark their spots; an evaded pull marks none. Giving up (mobs that drew no fight or were refused, or the pulls used up) still needs every position seen, else the step fails (`... were never seen: not counting it as cleared`). A boss counts as killed only when it died in this run (in its own pull or a First add's it joined, or in any other fight of the run: **the kill ledger** follows every hostile the feed shows fighting the party, whichever pull or walk fight it came to, and counts one as dead when it is listed at 0 health, or is absent from two whole frames after being seen fighting at 20% health or less, not walking home, with a living bot within 25 yd of where it was last seen; a wipe clears the counts; a boss seen alive again is alive again, one that left view healthier, unfought, evading or out of every bot's sight is not dead) or is listed dead, or its corpse (health 0 or the dead flag, the boss's entry, within 80 yd of the step's boss position; a step without positions takes none) is in the **agent's own object store** (a boss killed before this driver ran, as on a resume; the agent is brought near the party first, see **Keeping the agent near**), never because its adds died. A pull's fight tracks every hostile that joins it, not only the step's own entries, so a boss that comes into a pack's fight and dies there is that fight's kill (run 1: Revelosh died inside a 4852 pull that ended `evaded`). The ledger proves the boss dead only; the group positions are still cleared only by a won pull's dead mobs (`killedAt`). A position the lead bot cannot get within 25 yd of fails the step instead of passing it. The resume rule believes an empty view on the same 25 yd terms and wants every position of a group seen |
| team gate | `boss.team` and a group's `team` work as in a group run: the party's team is `team`, else the agent's own (the feed carries no race, and the owned bots share the account's faction); the skipped steps are in the notes (`skip_team` events) and `remote.team` says which team ran. The gate is **data-driven**: a part gated to the other team is skipped only if none of its mobs is hostile (listed alive now, or a unit seen alive earlier in the run, by GUID, and not killed since) **and** the party has looked: the feed lists only what the bots see, so the part is skipped on the evidence a pack needs to count as cleared (a living bot within 25 yd of each of its positions and a whole frame that lists none of its mobs, else the lead bot walks up to look, once per position; `... : skipped (looked: ...)`), and a position that cannot be looked at fails the step rather than skipping blind; if the mobs are hostile it is fought like a pull group (a note, `team_override` event: `... its mobs are hostile to the party: fought`). Uldaman's Baelog, Eric and Olaf are `team: horde` in the plan (the world database has them friendly to the Alliance), but were hostile to an Alliance party in remote run 2 (the pull of the last pack aggroed them), so an Alliance run skips them only while they are not in the feed. Group mode keeps the plan's gate as written. With no team known nothing is gated (a note says so) |
| kit check (`preflight`) | before the first step: `.bot maintenance <bot>` for each bot in the first frame (plain `.bot` lines; the module refreshes consumables, reagents, ammo and repairs when `AiPlayerbot.OwnedBotMaintenanceEnabled = 1`; `disabled` or a refusal is recorded and the run goes on), then `.bot inv <bot>`: drinks for every mana user (10; 20 for the `healer`), arrows or bullets for the shot of a ranged weapon in the ranged slot (100; a gun with arrows is named), and a shield in the off hand of the `tank` (only when named). Entries come from the core's item table (`tools/itemkinds`, `internal/dungeon/itemkinds_gen.go`); a drink or ammo the table lacks reads as missing. Before each bot's bags it reads its **spent talent points** with `.bot command <bot> talents` (the bot answers in its own words, "My current talent spec is: ... (a/b/c)", as a whisper; the one `.bot command` line the agent sends, and the plain-line check refuses any other word after `talents`): a bot with none spent (a respec that was reset and never applied again, a 0/0/0 tank) or no answer is a warning. The role is the `tank`/`healer` the run names (the module has no read for it that the agent may send). Result in `remote.preflight` (`bots[]` with `maintenance`, `inventory`, `talents` (`"0/5/31"`), `drinks`, `ammo`, `shield`; `warnings[]`; `ok`). `warn` goes on; `strict` fails the run before any order; `off` sends nothing |
| end of run | **while the party is in combat the binding is kept** and the fight logic (tank first, attack orders) keeps running, on any end (done, failed, stopped) and with its own context, for at most 90 s until the party is out of combat (`remote.end_fight`: `out of combat after N s` or `still in combat after 90 s; gave up`); unbound bots would die in the pack that aggroed them (run 2). Then `cmd stay` before the unbind (`remote.end_stay`), because the restored followers would run after the agent, however far away it is; `on_end: "logout"` then sends `.bot logout <bot>` for each bot after the unbind (`remote.logged_out`); `"none"` is the unbind only |
| fight | the feed's hostiles, targets, health and threat; `.bot cmd attack <guid>` at the engaged mob the tank holds best every 10 s (**Tank first**, below). A mob is dead when its health reads 0, or when it was seen fighting (engaged or hurt) and is then absent from **two frames in a row**; an absence in a `truncated` frame counts for nothing. A pull whose mobs all leave the feed without ever being seen fighting is `drew no fight`, not a win. `evaded` when they walk home |
| cc | `cc` option: `[{"entry":4863,"mark":"moon","bot":"Magey"}]` sends `.bot cmd cc <mark> <guid> [bot]` for mobs of that entry around the pull target, before the pull; such a mob is not picked as the pull target while another is there |
| rest gate | party health and mana, and (before a pull or an ambush walk, not at a leg's start) everybody near the lead bot, from the feed, against the same targets and cap as a group gate (90% health, 50% healer mana, 35% other mana, 60 s; a leg's `rest` overrides them); `.bot cmd rest` (again every 45 s); the feed has no class, so the bot named by `healer` gets the healer mana target and, without one, every caster does. The commander has no order for a single bot: a member whose mana has not risen at all in 15 s gets one more `.bot cmd rest` (recorded in `rest_gates[]` as a drink order). A dead bot is first left to a living caster: while one is alive out of combat (the `healer`, else any bot with mana) the gate waits up to 30 s of calm for the resurrection (fights meanwhile are helped and do not count; `rez_waits`, `rezzed`), because `cmd revive` releases the spirit and a released body cannot be raised in place; then, or at once when no caster is left, `.bot cmd revive <name>` (owner or GM; refused: the run fails) |
| wipe / death | from the feed: every bot dead is a wipe; with `recover_wipe` a revive order for each and the step is tried again |
| `loot_object`, boss `loot_item`, `use_item`, `use_object`, `wait_for` a game object | done by the **carrier** bot (below), on the per-bot player orders of TortoiseBots (#928): `cmd open`, `cmd loot`, `cmd use`, `cmd lootmethod`, each waited for through its `ACTION_PENDING` to the final ACK or ERR. A game that cannot give those orders (a fake) still fails the step with `remote mode: object step needs the agent nearby` |

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
| `wait_for` a game object (the Seal) | none | the state in the agent's object store when the agent sees the object; else the wait is taken as met after 10 s and a note says so |

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
  until another mob leads it by 25 points, so two mobs the tank holds alike do not flip it back and forth; it is sent at the next mob at once when the one before died (`target_switches` counts the times it left a
  mob that still lived).
- *Opener.* A mob the party was not sent at yet waits until the tank has held it (top of its `T` record, the mob hitting it) for `OpenerDelay` (2 s, the existing threat gate
  extended; `threat_waits` counts it too), or until `ThreatWait` (6 s) has passed. The feed gives shares, not absolute threat, so the delay stands in for "the tank has N threat".
- *Loose adds.* A mob that has been hitting a bot that is not the tank for 3 s (and whose list the tank does not top) gets `.bot cmd attack <guid> <tank>` (TortoiseBots #928: with a
  bot name only that bot of the party goes, so the tank alone is sent at the add). It was `.bot cmd pull <guid> 0`, which the module refuses `tank-busy` while the tank is in
  combat, which is exactly when an add is loose (the first remote Uldaman run: 19 refusals, 11 threat overrides); `cmd pull` stays for opening pulls. The 3 s count starts at the
  look that first showed the mob on that bot. At most one order per look, `AddCooldown` (8 s) apart for the same mob, 3 for the fight (`loose_add_orders`). A refusal is noted and
  counted in `refused` (`attack:<code>`); it does not end the fight.
- *Crowd control.* The feed's `c` flag is stunned, confused, fleeing or rooted (`UNIT_STAT_*`): it cannot tell a sheep from a stunned or rooted mob. So only a mob a `cc` order
  was accepted for is spared, while it carries the flag or no bot is fighting it: it is not attacked, not picked as the pull target while another candidate is, and not sent the tank
  at, unless every engaged mob is like that (the last one is fought). The mob the party fights and the tank's own target are never spared. Each spared mob is counted once in
  `cc_respected`. Crowd control the bots start by themselves, and the moon mark, cannot be seen: the feed has neither.
- *Counters* (`report.remote`): `tank_threat_share` (the mean over the frames of a fight of the tank's share on the fighting mobs that have a `T` record, in percent; the
  deploy-19h run had it at 15-25 with the tank leading), `tank_threat_samples`, `loose_add_orders`, `cc_respected`, `target_switches`.

**Always unbound.** The unbind (`commander unbind`) is deferred inside the run: done, failed, stopped (`dungeon_stop`) or a panic, it goes out with
its own context before the report says the run ended. A panic in a remote run is recovered and reported as a failed run (`internal error: ...`,
with the stack in the notes); group mode has no such guard. A bind the module refused is no binding (nothing to unbind); one that got no answer is
unbound anyway. The module can end the binding itself (the lead bot logged out or left, the party disbanded, a player joined, the switch turned
off): its `commander unbind` ACK or a `not-bound`/`disabled` answer fails the run with `the commander binding ended: <reason>`.

**Entering the dungeon.** A dungeon's instance bind belongs to the *group*, and a bot that leaves its group inside the dungeon is sent to its
homebind about a minute later (deploy-19i: the bots were ejected twice that way). So the party that fights has to be the bots' own group from
the first step in, and the agent stays **outside and ungrouped**. When a bot of the first frame is not on the plan's map (and `enter` is not
`off`), the run does, after the bind and the first frame:

1. The agent's character leaves its group if it is still in one (a bind that keeps the bots-plus-commander party, TortoiseBots #929, removes
   the agent itself; the bound bots do not leave with their master). It is refused when the agent stands on the plan's map.
2. It picks the way in. `auto` asks the server for the account level once (`.account`): a developer-level account (3; `.tele name` needs it)
   with an `entrance_tele` in the plan teleports the lead bot, anyone else with a `portal` walks it. `gm` and `walk` force one.
   - **gm:** `.tele name <lead bot> <entrance_tele>`, a wait of up to 30 s for the lead bot to show on the plan's map (it goes into *its group's*
     instance, and the first entry binds the instance to that group; the agent never enters), then `cmd summon <bot>` for each other bot,
     6 s apart.
   - **walk:** `cmd move` legs along the navmesh to the portal on the outer map (the lead bot's area trigger takes it in; the followers get
     8 s to come in by themselves), then `cmd summon` for those still outside. *Not verified live:* whether a bot that walks into the trigger
     goes in (a lead bot that stands in the portal and does not enter fails the run saying so; a `cmd enter` order in the module, built
     on its area-trigger action, would be the answer)..
3. Every bot's feed map must be the plan's within 30 s of the entry, else the run fails naming the bots that are not (the group is **not**
   changed to fix it).
4. From the first entry order on, the group is left alone: no leave, invite or bind until the run ends (the unbind is the only thing left).

**Instance limit.** The core refuses a 6th *new* instance per account per hour (`MAX_INSTANCE_PER_ACCOUNT_PER_HOUR`, the player sees
`TRANSFER_ABORT_TOO_MANY_INSTANCES`; re-entering one on the list is free, and a mangosd restart clears the list). The owned bots share the
agent's account, and a GM teleport counts too. Every run that enters a dungeon is one new instance, said in the report (`remote.enter`:
`entries`, `entries_last_hour`, `entry_limit`, `warning`) and in the notes. The agent keeps the entries of the last hour per account in a small
file next to the settings file (`instance-entries-<hash>.json`, unix times only) and **refuses the 6th** within the hour, saying when the oldest
leaves the list; `ignore_entry_limit` tries anyway (after a server restart, say). Entries made outside this agent (by hand, a game client)
are not in the file.

`remote.enter`: `method` (`gm` or `walk`), `account_level`, `agent_left_group`, `tele`, `summoned[]`, `arrived` (seconds from the phase's start,
per bot), `seconds`, and the entry numbers above. A party already inside the plan's map has no `enter`; `from_leg` works as in a group run.

**GM.** The only GM command the remote run sends is that one `.tele name` (`remote.gm_commands` is 1 then, else 0): no `.revive`, `.aura` or
`.gm visible`. `teleport`, `buffs`, `brain`, `gm`, `gm_level` and `wait_sickness` are refused with `mode: "remote"`, `leader_gm` is `false` and
`gm_shortcuts` is empty. The kit check's `.bot command <bot> talents` is a bot command (the owner's), not a GM command.
**Keeping the agent near.** The bots walk and the agent's own character does not, but the object steps (and the carrier's `cmd open|loot` orders) need it in view of the chest, the corpse or the door. So, in remote mode only, after each leg's walk and before an object step the driver looks at how far the agent stands from the lead bot (the agent's position against the feed's) and, over 40 yd (or on another map): a **GM** account (the `.account` level reaches the teleport level, 1) sends `.appear <lead bot>`, but never during a fight, and only when the caller's `setup` lists `gm visible off` (run it first and pass it, so the mobs cannot take the arriving agent for a target; otherwise the run notes it and does not `.appear`); any other account walks its own character (`walk_path`) to a point 30 yd short of the lead bot, only out of combat, never when a hostile the bots see stands within 25 yd of the way or the stand point, and it stops at once when a fight begins or after 60 s. **Each `.appear` is a GM assist during the run**, not part of the party's play: it is counted in `run_gm_assists`, noted, and listed in `setup_shortcuts` as `.appear <lead bot> (GM assist during the run)`. `gm_commands` does not count it (it stays the enter phase's), so a "0 GM commands" target has to read `run_gm_assists` too. `agent_walks` counts the walks. A failed `.appear` or walk is noted and does not fail the run. An object step is tried once per step, and so is a boss step (before its kill is judged). Not verified live (run 1 needed `.appear` by hand once; Glacienne stayed ~110 yd behind).

**Report.** `dungeon_status.report.remote`: `mode`, `lead_bot`, `bots`, `tank`, `bound`, `unbound`, `unbind_via` (`ack`, `already unbound`, `no
answer`, ...), `ended_by_module`, `gm_commands`, `run_gm_assists`, `agent_walks`, `frames_received`/`frames_missed`/`frames_invalid`/`frames_stale`/`frames_truncated`/`last_seq`
(since the bind), `feed_stalls`, `orders_sent`/`orders_refused`/`orders_unanswered`, `orders` (by verb), `refused` (by `verb:code`; the details are
in the report's `refusals[]` with intent `cmd <verb>`), `move_stalls`, `walk_aggros`, the threat counters above, `pull_fallbacks`, `tank_busy_waits`, `cc_orders`,
`revives`, `rez_waits`, `rezzed`, `pack_looks`, `packs_seen_empty`, the tank-first counters (`tank_threat_share`, `tank_threat_samples`, `loose_add_orders`, `cc_respected`, `target_switches`), `walk_stops`, `walk_stopped_s`, `look_ahead_moves`, `fight_holds`, `object_steps_ahead`, `team`, `enter`, `preflight`, `end_stay`, `end_fight`, `unreachable`, `logged_out`. Once a fight is over for good (the loot wait past and nothing hitting the party) the lead bot is sent a `move` to the place it stands (`fight_holds`): `cmd move` sets its stay spot (BotCommands.cpp CommanderMove), and the module's "return to stay position" would otherwise walk it back to the last point it was sent to before the rest gate and the next walk. It is not sent earlier, because it resets the bot's movement and the followers' formation; the bot may therefore walk back a little during the loot wait (to be seen live). The `dungeon_step`/`dungeon_leg` events carry `data.mode: "remote"`; `dungeon_done` and `dungeon_failed` add
`remote`, `lead_bot`, `frames_received`, `orders_sent`, `orders_refused`.

**Limits.** The feed has no names for mobs (entries only), no class for bots (so a caster below 30 % mana always holds a rest gate, healer or not; `healer` names the bot that gets the healer mana target),
no loot or dead flag for hostiles (a hostile seen fighting that leaves the feed for two frames is counted dead), and the bots see only what they see: a pack
nobody has in sight is not counted dead: the lead bot walks up to its positions and looks (see the pull group row). A feed that goes quiet for 6 s makes the fight loops wait
(`feed_stalls`); one quiet for 45 s fails the run.

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
only while the rage pays for it.

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
| `internal/rpc`, `internal/netutil` | loopback JSON-RPC server |
| `cmd/turtle-agent` | the CLI (`login`, `fleet`, capture tools including `capture events`, `map`, `trace-score`) |

Parser tests: every SMSG parser in `internal/game` and `internal/objects` has a `Fuzz*` target (no panic, no hang
within 5 s per input) seeded from the committed captures, a few literal packets copied from live m910q captures
(`internal/game/realpackets_test.go`: gossip, vendor list, raid icons, root and knock back orders, new world; the runs
never opened a trainer, a quest panel or a loot window, so those parsers still run only on `gametest` packets) and the
`gametest` builders. `make fuzz` runs them all; an input a fuzzer finds is saved under `<package>/testdata/fuzz/` and
stays as a regression seed that `make test` runs.

Design and implementation plan: `docs/superpowers/`.

## Licence

MIT, see [LICENSE](LICENSE). Parts are ported from [agent-wow](https://github.com/agent-wow/agent-wow) (MIT), and
`internal/navmesh/detour` is Detour from recastnavigation (zlib, `internal/navmesh/detour/License.txt`); see
[NOTICE](NOTICE). World of Warcraft is a trademark of Blizzard Entertainment; this project is not affiliated with it.
