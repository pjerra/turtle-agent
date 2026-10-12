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

> Use it only on servers you run or are allowed to test on. It has no Warden emulation and does no rendering or
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
land falls and knockbacks on the ground (see [Falling](#falling-and-knockbacks)).

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
turtle-agent login [--config FILE] [--capture FILE] [--stay DURATION] [--rpc ADDR] [--reconnect N] [<account>] <character>
turtle-agent fleet --config FILE [--settings FILE] [--stagger DURATION] [--wait DURATION]
turtle-agent scrub-check [--config FILE] <capture>...
turtle-agent capture-stats <capture>
turtle-agent capture events [--from RFC3339] [--to RFC3339] [--kinds a,b] [--out FILE [--force]] <capture>
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
| 9 | `fleet`: a child failed, or not every character reached the world in time |
| 10 | a logout did not complete (`lost` or `timeout`); in a fleet, the other characters keep running |

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
| `status` | | state, character, map, x/y/z, `falling`, `z_unknown`, health, power, target, last event seq, `mono_ms`, `base_time` and `wall_offset_ms` (the monotonic axis of events: `base_time` is the wall time of mono_ms 0; `wall_offset_ms` is the wall clock now minus `base_time` + `mono_ms`, so host clock steps and drift show) |
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
| `party` | | `in_group`, `raid`, `leader`, `assistant` and `subgroup` (the player's own), `members[]` with health, online state, `assistant` and `subgroup`, `marks` (raid icon name to guid) |
| `units` | `within` (yards, 3D; `near` is its older name), `entry`, `hostile`, `name`, `limit` | nearby units, nearest first: guid, name, level, health, dead, hostile, in_party, `x`/`y`/`z`/`o`, `pos_age_ms`, `moving`, `distance` (3D) and `distance_2d` from the player (see [Unit positions](#unit-positions)), `in_combat`, `target`, `stand_state`, `auras[]`, `casting` (see [Unit state](#unit-state)); `hostile` means an attackable-looking creature (no NPC flags, not player-controlled), not a faction check: it counts neutral critters and never an enemy-faction player |
| `unit` | `guid` or `name` (prefers living units; the player's own name or guid gives the player, at the `status` position) | one unit, as a `units` row; an error when none is in view |
| `target` | `guid` or `name` (prefers living units) | |
| `attack` / `stop_attack` | | |
| `cast` | `spell_id`, `target` | |
| `cancel_cast` | `spell_id` (optional; none cancels whatever is being cast) | |
| `face` | `guid` | |
| `walk_to` | `x`, `y`, `z` (short, straight-line walks that ignore terrain; see Movement; `walk_path` follows the terrain) | |
| `path_to` | `x`, `y`, `z` | a navmesh path from where you stand, without moving: `map`, `from`, `to`, `status` (complete, partial, no_path), `reason`, `points`, `length`, `engine` |
| `walk_path` | `x`, `y`, `z` | walks the navmesh path: `result` (arrived, partial, no_path), `reason`, `goal`, `position`, `dist_left`, `walked` (yards the agent walked itself; a server move such as fear, a charge or a teleport is not counted), `replans`, `seconds`, `trace` |
| `teleport` | `x`, `y`, `z`, `map` (a GM `.go xyz`; needs a GM account) | |
| `mark` | `guid` or `name`, `icon` (skull, cross, square, moon, triangle, diamond, circle, star, clear) | `icon`, `guid` once the server echoes the mark. Errors at once outside a group or when the player is neither leader nor assistant; errors after 2 s without an echo |
| `roll` | `roll_id` or `item_slot` (plus `looted` when two corpses share a slot), `choice` (need, greed, pass) | `roll_id`, `looted`, `item_slot`, `item_entry`, `choice` |
| `inventory` | `bags` (true: the items in the four bags too) | backpack items (bag 255, slots 23-38), then with `bags` the bag items (bag 19-22, the slot in that bag): `bag`, `slot`, `guid`, `item_entry`, `count`; `item_entry` and `count` are 0 until the item's create block arrives |
| `equip` | `item_entry` (first match in the backpack, then the bags), or `slot` (and `bag`: 255 for the backpack, the default, or 19-22 for a bag) | the item sent; an empty slot is an error; a refusal arrives as an `inventory_error` event |
| `use_item` | `item_entry`, or `slot` (and `bag`, as `equip`), `target` (unit guid; none uses it on yourself) | the item sent; the outcome arrives as events (see [Items, loot and mounts](#items-loot-and-mounts)) |
| `loot` | `guid` or `name` (nearest dead unit), `wait_ms` (default 3000); no params: the open window | the window: `guid`, `type`, `money`, `items[]` (`slot`, `item_entry`, `count`, `display_id`, `random_property`, `slot_type`) |
| `loot_take` | `slot`, or `all`; `money`; `wait_ms` (default 3000) | `taken`, `left`, `skipped` (slot numbers), `money` (copper asked for), `window` (what remains, null once closed) |
| `loot_close` | `wait_ms` (default 3000) | |
| `mount` | `item_entry` or `slot` (and `bag`), or `spell_id`; `wait_ms` (default 8000) | `mounted`, `display_id`, `via` (item, spell), `result` |
| `dismount` | `spell_id` (only when the mount aura is unknown), `wait_ms` (default 3000) | `mounted`, `display_id` |
| `addon_send` | `prefix`, `text`, `channel` (party, raid, guild, officer, raid_leader, raid_warning, battleground, battleground_leader, channel), `to` (channel name) | |
| `tbm` | `command` (a `.bot` command without `.bot`), `via` (chat, the default, or addon) | `since` |
| `tbm_handshake` | `via`, `wait_ms` (default 5000) | `since`, `next`, `version`, `caps`, `transport`, `roster[]`, `bot_state`, `errors`, `lines`, `complete` |
| `tbm_buttons` | `tab` (Fight, Camp, Party, Roster, Guild, Raid, BotPanel; default all) | `addon_commit`, `send_delay_ms`, `poll_after_cmd_ms`, `action_timeout_ms`, `buttons[]` (`name`, `label`, `tab`, `command`, `bot_command`, `args`, `optional`, `scope`, `toggle`, `needs_enemy`, `pull_seconds`, `reply`, `intent`, `ok`, `fail`, `follow_up`, `mini_bar`, `requires_cap`, `multi`, `source`) |
| `tbm_click` | `button` (`Fight.AoE`), `args` (`state`, `bot`, `name`, `mark`, `seconds`, `bag`, `slot`, …), `transport` (auto, chat, addon), `wait_ms` (default 8000), `force`, `follow_up` | `since`, `next`, `button`, `scope`, `sent` (`transport`, `text`, `channel`, `prefix`), `transport_reason`, `refreshed`, `reply` (`kind` ack, err, text, inventory, snapshot or timeout; `ok`, `line`, `ack`, `acks`, `err`, `lines`), `follow_up` (the roster response) |
| `tbm_send` | `command` (with or without `.bot`), `transport`, `wait_ms` | like `tbm_click` without `button` and `scope`: the reply is the first ACK/ERR or roster response |
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
auras, deaths, kills, party changes, raid icons (`raid_mark`), group loot rolls (`loot_roll_start`, `loot_roll`, `loot_roll_won`,
`loot_all_passed`, tied together by `data.roll_id`), refused equips and item uses (`inventory_error`), NPC dealings (vendor, trainer and quest events, see
[Vendors, trainers and quests](#vendors-trainers-and-quests)), corpse loot (`loot_open`,
`loot_taken`, `loot_money`, `loot_money_share`, `loot_error`, `loot_closed`), the player's own mount and stand state (`mount`,
`mount_result`, `stand_state`) and session state. Cursors are exclusive; `dropped` says older events were evicted.
A chat or system line whose colour codes were stripped keeps the original in `data.raw` (protocol lines such as
`TBM:ROSTER|…|removing` lose `|r` otherwise).

Group control (`promote`, `assistant`, `convert_raid`, `raid_move`, `disband`): the server drops a group command it
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

Not yet exercised on a live server (see the pull request for the live steps): the unit tests use packets built from the core's
source, not a capture.

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
`internal/spells/names.tsv.gz` by `make spellnames CORE=<tortoise-wow checkout>`. A spell the server's database
added later has no `name`.

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
has the player. The client time never runs backwards and is never 0.

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
  swim: it stands on the water's surface, where the mesh has it); `guess` is the old behaviour.

Without a navmesh (no `mmaps_dir`, a build without cgo, no tile under the point) a knockback lands back at the height
it started at and a teleport stays where the server put it, both with `z_unknown` true. Limits: the mesh has no game
objects and no walls of its own, so the wall rule is a stand-in for collision; a floor is where the mesh has it, within
its detail error (a few tenths of a yard); a `walk_to` that ends above the ground does not fall (it sets `z_unknown`);
a far teleport (`SMSG_NEW_WORLD`) does not fall either; landing in water sends `MSG_MOVE_FALL_LAND`, not the client's
`MSG_MOVE_START_SWIM`. The floor search runs in the session loop after the knockback (or feather-fall) ack is sent, as
the server drops an order not acked within 2 s; a flight's first query on an unloaded tile reads that tile (0.5-3 MB),
and a missing tile is looked up once (until the next `path_to`/`walk_path` plan).

## Navmesh walking

`walk_to` walks in a straight line and knows nothing of walls. `walk_path` plans on the navmesh the server's own
PathFinder uses (the core's `data/mmaps`), then walks the corners one straight walk at a time, with the same
movement packets and the same checks as `walk_to` (see [Movement](#movement)): a client time that only runs forward,
the run speed the server has set, each step held to the anticheat's bound, no walking while rooted or in the air. Between corners the heartbeats carry the height of the ground (the mesh is sampled every 4 yd), so a
straight leg over a hill does not cut through it. The path starts and ends on the floor under the points asked for,
not at their height. Each leg moves along its 3D length at the run speed, so no beat changes height by more than the
speed allows (the anticheat's z check), even up the steepest walkable slope; a packet's height is also held within
that of the one before, should a ground sample land on another floor. Arriving at a corner on the mesh floor clears
`status.z_unknown`.

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

`trace` lists each `plan` (status, points, length), each `walk` (one segment, `from`, `to`, and the error that ended
it early) and each `interrupted`, with `t_ms` since the call began.

**Tiles.** Copy the server's `data/mmaps/NNN.mmap` and the map's `NNNyyxx.mmtile` files into `mmaps_dir`; the
wow-play-harness tile cache folder (one subfolder per server) works as it is. Tiles load on demand: a map with up to 16
tiles (a dungeon) loads whole, a bigger one the tiles round the start and the goal, one tile wider, and three wider
when that gives no complete path. At most 64 tiles stay loaded per map: a box that would hold more keeps the tiles
nearest the straight line from start to goal, and the least recently used tiles are unloaded first. Tiles copied in
later are picked up on the next call.

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
    { "account": "HEALACC",   "character": "Healtwo", "rpc": "127.0.0.1:8772" }
  ]
}
```

```sh
bin/turtle-agent fleet --config fleet.json            # settings from ~/.config/turtle-agent/settings.json
bin/turtle-agent fleet --config fleet.json --settings other.json --stagger 3s --wait 3m
```

- `account` is a key into the settings file's `accounts`; passwords stay there, are read by each child, and never appear in the
  fleet file, on a command line or in output. Every account in the fleet file must exist in the settings file.
- `rpc` must be a loopback address with a fixed port (no port 0), and ports and characters must be distinct. The
  supervisor polls each port's `status` method.
- RPC tokens: each child generates its own and writes `rpc-<port>.token` next to the settings file (one file per member
  port); the supervisor reads the right one for each poll. With `rpc_token` in the settings file every child shares that
  token. Clients of a fleet read the file of the member they talk to.
- Children start `--stagger` apart (default 2 s, realmd throttles logins). When all are `in_world` the supervisor prints a
  table of character, RPC address and state; each child's log goes to stderr with a `[Character]` prefix.
- Ctrl-C (or SIGTERM) sends one interrupt to every child so they all log out cleanly; a child that has not exited after 60 s
  is killed.
- A child that exits non-zero, or a fleet that is not fully in world after `--wait` (default 2 m), stops the others and
  makes `fleet` exit 9. It exits 0 only when every child exited 0.

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
`.cache/` (ignored, never vendored); bump the commit there on purpose. The mock in `tools/tbmcontract/mock.lua` is copied
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
| `internal/spells` | spell names by id, from the core's `spell_template` (`tools/spellnamegen`) |
| `internal/navmesh` | tile loading and path planning over the core's Detour (cgo); `navfixture` builds synthetic tiles |
| `internal/events` | ordered event log with cursors |
| `internal/tbm` | the TortoiseBots addon (TBM) protocol: handshake commands, the `TBM:` reply lines, the generated button table, the transport rule and reply matching (`tools/tbmbuttongen` writes the table) |
| `tools/tbmlines`, `tools/tbmcontract` | extract `TBM:` lines from an events file; replay them through the real TBM addon (Lua) |
| `internal/capture` | packet capture files and the secret scan |
| `internal/rpc`, `internal/netutil` | loopback JSON-RPC server |
| `cmd/turtle-agent` | the CLI (`login`, `fleet`, capture tools including `capture events`) |

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
