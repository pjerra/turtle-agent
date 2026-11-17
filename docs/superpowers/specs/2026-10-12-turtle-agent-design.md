# turtle-agent — design

Date: 2026-10-12. Status: draft for owner review.
Owner decisions (2026-10-12): purpose = bot testing; route = our own small Go client (not a fork); MVP = login + chat/GM,
party + combat log, move + attack; interface = local JSON-RPC + thin Python wrapper in wowharness; repo =
github.com/TortoiseBots/turtle-agent (private). Built even though another Turtle port of agent-wow exists (owner's call).
Background: spike report `~/TortoiseBots-audit/spike-agent-wow.md` (agent-wow `5ef4916` vs core `~/tortoise-wow` `5d79486`).

## Goal

A headless WoW client that logs a test character into our Tortoise WoW 1.18.1 server (vanilla 1.12 protocol plus Turtle
changes), so test agents can play next to the TortoiseBots bots and see what they do — live, with no game window, no
Windows laptop, no ReloadUI flushes and no screenshots. It replaces the real-client harness for most bot tests; the real
client stays for visual checks.

**Done means:** the harness MVP runs end to end headless on m910q: log in Harnessb, `.gm off`/`.gm visible on`, hire or
invite 2 bots, target and attack a mob, and return the parsed combat log plus what each bot did — the same report the
real-client harness produces today.

## Non-goals (v1)

- Rendering, maps, pathfinding, quests, vendors, inventory management, Warden emulation.
- Running many characters per process (one process per character is fine; several processes may run side by side).
- Character creation beyond what a test needs (characters are made once with the real client or GM commands).
- The agent-wow Docker/gRPC module system.

## Architecture

One Go program, one process per logged-in character. Packages:

| Package | Responsibility | Origin |
|---|---|---|
| `auth` | SRP6 logon against realmd: logon challenge with build **7272**, 1.12 logon proof (75 bytes), 26-byte proof reply, realm list in the vanilla layout (`uint32 0`, `uint8` count). Retry once on the known ~1/256 unpadded-BigNumber SRP failure. | SRP math copied from agent-wow `pkg/auth/srp.go` (MIT), layouts rewritten |
| `worldconn` | TCP framing: server header `u16 BE size + u16 opcode`, client header `u16 BE size + u32 opcode`; vanilla rolling-XOR header cipher over the 40-byte session key (`AuthCrypt`), 4-byte `SMSG_AUTH_CHALLENGE` seed, `CMSG_AUTH_SESSION` with build **5875** and a zlib addon block the anticheat accepts, `SMSG_AUTH_RESPONSE` with 1.12 lengths. | Framing skeleton from agent-wow `pkg/worldconn`, cipher and session new |
| `opcode` | The 1.12.1 opcode table generated from the core's `Opcodes_1_12_1.h`. | Generator pattern from agent-wow `tools/opcodecatalog` |
| `session` | Single-owner loop per character: char enum (1.12 layout), `CMSG_PLAYER_LOGIN`, "in world" on `SMSG_LOGIN_VERIFY_WORLD`, keep-alive pings, Warden packets ignored, clean logout (`CMSG_LOGOUT_REQUEST`, wait for `SMSG_LOGOUT_COMPLETE`). | Loop pattern from agent-wow `pkg/world` |
| `objects` | Object store from `SMSG_UPDATE_OBJECT` and `SMSG_COMPRESSED_UPDATE_OBJECT` (1.12 block format: create, values, movement, out of range): GUID, type, entry, name (via name/creature queries), position, health/max, power, level, target GUID, combat/dead flags, party membership. | New |
| `game` | Actions: chat (say/party/whisper; GM and `.bot` commands are `.`-prefixed chat), group invite/accept/leave, select target, start/stop auto-attack, cast spell by id on a unit, set facing, short walks via `MSG_MOVE_*` heartbeats, GM teleport as the long-distance move. Incoming chat decoded including Turtle's `CHAT_MSG_HARDCORE`. | New |
| `events` | Ordered event log with sequence numbers: chat/whisper/system lines, melee (`SMSG_ATTACKERSTATEUPDATE`), spell damage (`SMSG_SPELLNONMELEEDAMAGELOG`), periodic (`SMSG_PERIODICAURALOG`), heals (`SMSG_SPELLHEALLOG`), spell start/go/fail, aura applied/removed, deaths, party stats. Names resolved from the object store. | New |
| `rpc` | Local JSON-RPC 2.0 server (loopback only), one method per action plus `events(since)` and `units(filter)`. | Pattern from agent-wow `pkg/worldrpc` |
| `cmd/turtle-agent` | CLI: `login <account> <character>` starts the session and the RPC server; `--config` for host/ports/credentials file. | New |

Credentials live in a local file outside the repo (like the harness's settings file); never in code, logs or events.

### JSON-RPC methods (v1)

`status`, `chat(type, text, to?)`, `gm(text)` (sends `.`-prefixed chat), `invite(name)`, `accept_invite()`,
`leave_group()`, `party()`, `target(guid | name)`, `attack()`, `stop_attack()`, `cast(spell_id, target?)`,
`face(guid)`, `walk_to(x, y, z)` (short distances), `teleport(...)` via GM, `units(near?, entry?, hostile?)`,
`events(since, kinds?)`, `chat_log(since)`, `logout()`.

### Python wrapper

`wowharness/headless.py` in the existing harness: a `HeadlessClient` with the same surface the testers use today
(`chat`, `gm`, `bot`, `state`, `tail_combat_log`, `tail_chat`, `fight_report`) backed by the JSON-RPC, so test briefs and
MCP tools work with either backend. The dashboard and SOAP paths stay as they are.

## Data flow

Server packets → `worldconn` (decrypt, frame) → `session` loop → handlers update `objects` and append to `events` →
`rpc` reads snapshots / event ranges. Actions: `rpc` → `game` → `session` loop (single writer) → `worldconn`.

## Error handling

- Auth/world failures return a clear error code and message (wrong password, build rejected, realm offline, kicked).
- Disconnect: the session reports it as an event and the RPC `status` shows it; reconnect is a new `login` (no automatic
  reconnect in v1).
- Unknown opcodes are counted and skipped; a parse error in one packet is logged with the opcode and a hex dump and does
  not end the session.
- Throttles respected: realmd's 15-accounts-per-IP-per-300 s limit and `MinRealmListDelay` (tests reuse one account).

## Testing

- **Unit tests** on recorded packets: SRP vectors, XOR cipher round trip, framing, char enum, update-object blocks, every
  combat event kind. Packet recordings are taken from m910q during step 1 (a capture mode writes raw decrypted packets
  to a file) and checked in as fixtures (no credentials in them).
- **Live tests per milestone** against m910q (the owner's test server): each milestone has a scripted check (below).
- **End-to-end:** the harness MVP scenario, compared with a real-client run of the same scenario.

## Milestones (estimates are guesses from the spike)

1. Login → in world → clean logout on m910q, with a packet capture of a session (2–3 days).
2. Chat, whispers, GM and `.bot` commands in and out (½–1 day).
3. Party: invite bots, accept, member stats (½ day).
4. Object store: units around the player with HP, target, combat state (2–3 days).
5. Target, attack, cast, face, short walks (1 day).
6. Combat events filtered to the party, `fight_report` through the Python wrapper (1–1½ days).

## Risks

- **Turtle packet changes** beyond stock 1.12 (update fields, movement, spell packets) are not audited; step 1's capture
  and the update-object parser will show them. Known Turtle differences: login build 7272 vs world build 5875,
  `challengeMask` in `CMSG_CHAR_CREATE`, races Goblin=9 / High Elf=10, `CHAT_MSG_HARDCORE`, Turtle-signed addons in
  `SMSG_ADDON_INFO`.
- **Anticheat/Warden:** `Warden.WinEnabled = 0` in the template; ignoring Warden is safe only while the timeout kick
  stays disabled. Movement anticheat could flag synthetic movement; v1 uses GM teleport for long moves.
- **PIN:** not a problem on m910q (`ForcePinAccountRank = 10`, checked 2026-10-12).
- **SRP edge case** (~1/256) handled by one retry.
- **Toolchain:** Go is not installed on the WSL machine yet; agent-wow targets Go 1.27.1.

## Licence and provenance

agent-wow is MIT. Copied or ported files keep its copyright notice; `NOTICE`/`README` list the source repo, SHA
`5ef4916`, the files taken and whether each was copied or ported.
