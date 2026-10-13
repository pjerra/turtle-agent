# Live checks still open

Things the code review raised that only a running server can settle. Each is a short, one-off run.

## Anticheat as a player account (final review M4)

`Movement::VerifyMovementFlags` returns early for any account above SEC_PLAYER (Movement.cpp:121-123),
so every live run so far (GM level 3) never exercised the flag-based checks.

1. Grep every `CHEAT_TYPE_*` name in libanticheat.hpp:36-59 against the server log after the run, not a subset
   (the earlier grep left out WALL_CLIMB, TELEPORT, TELEPORT_FAR, BAD_ORDER_ACK and MULTIJUMP).
2. Log a SEC_PLAYER account in. Place the character with `.go xyz` from a GM session (a player cannot teleport itself).
3. Run `walk_to`, `face`, then a walk that stops, and read the server log for anticheat lines (FLY_HACK, WATER_WALK,
   ROOT_MOVE, FIXED_Z, ...). Expect none.

## Warden timeout and addon fingerprint (issue #47)

What to expect is in the README, "What the server sees". Grep the server's anticheat log after a run:

1. `grep -c 'WARDEN: Account .* timeout'`: at most one line per agent session, and only when `Warden.ModuleDir` holds a
   Windows module. Note it as expected; it is not a failure of the run. If the agent is disconnected (exit 6) about
   `Warden.Timeout` seconds after login, the server's `Warden.cpp:498` `KickPlayer()` is live: say so in the report.
2. `grep 'Generated new fingerprint'`: log in twice with the same settings file. Expect the line at the first login
   only (or none, if the account's `addon-fingerprint-*` file already existed), and never at the second. The agent's
   file holds the same value as the line's `0x...`. With `LOG_ANTICHEAT_DEBUG` on, `ADDON: Found fingerprint` marks the
   second login.
3. `ADDON: Found invalid fingerprint` or a new `Generated` line at the second login means the stored value was not
   presented: check the file is next to the settings file in use, and that the same account logged in both times (the file is per account).

Not yet run live: this was added with unit tests against the core's layout only.

## Chat language (final review M5)

`BuildMessageChat` sends every say, party and whisper in LANG_UNIVERSAL. The core accepts it (skill 0,
ChatHandler.cpp:193-203), but a real client speaks its racial language. With one Horde and one Alliance
character in range, check what the other side sees from a non-GM account saying something. Only then decide
whether to send the racial language (and keep Universal for `.` commands).

## Cast result 7 (task 16)

Cast result 7 is mapped to a "wrong faction" text that was never seen live. Trigger it (for example a friendly
spell on an enemy-faction player) and compare the text.

## Falling after `.go xyz` and after a knockback (final review M13, #23)

Since #23 the agent falls and lands on the navmesh floor (README "Falling and knockbacks"). Run on m910q build 17,
2026-10-12 (PR #31):

- `.go xyz` 20 yd above flat ground (GM account): fell 1439 ms, landed at the mesh floor 26.82; the server's `.gps`
  has the same z, GroundZ 26.72. A player account cannot be put in the air: `.summon` snaps z to the ground.
- Knockback (spell 10689 via `.cast self` on the player account): flat, downhill (1473 ms, 6.2 yd lower) and uphill
  (a 45° slope met at once) all acked, flown and landed; the anticheat's movement history shows the ack,
  `0x6000` heartbeats and the landing, every step inside the bound; no "does not match pending" line.

Still open:

1. The m910q anticheat has no `anticheat.conf` (`[Anticheat] Could not find configuration file`), so every
   detector's thresholds are 0 (`AnticheatConfig::IsEnabled` false) and `Movement.SpeedHack.Enable` is off: no
   CHEAT_TYPE can fire there. The steps were replayed offline against `GetMaxAllowedDist` instead. Repeat with the
   template (`src/game/Anticheat/anticheat.conf.readonly`) installed.
2. Fall damage after a knockback over 14.57 yd (`Player::HandleFall`), and feather fall applied mid-flight (expect no
   OVERSPEED_Z under the 7 yd/s bound, Movement.cpp:874-881).

## Game objects over RPC (#73 step 1)

Run on m910q build 17, 2026-10-13 (GM account, `.gm visible on`, own character only; teleports by `teleport`), Uldaman, map 70:

- `gameobjects` at the entrance listed 55 objects within 200 yd, named from the query responses (Dwarven Campfire, Baelog's Chest, Keystone,
  Large Mithril Bound Chest ...). Doors carry flags 32, the Ancient Vault 33 (in use, `usable` false), the Large Mithril Bound Chest 2 (`locked`).
  A mining node 66 yd away came back as `outcome` `none` with the distance hint.
- `use_object` on the Keystone door 2.6 yd away answered at once: `gameobject` events `flags` 32 to 5 and `state` ready to active, and the linked
  Seal of Khaz'Mul door opened with the same two changes. It stayed open for the 45 s watched.
- `use_object` on chests ends in `none`: the Food Crate (1.3 yd away, `dyn_flags` 1), Baelog's Chest and the Iron Deposit (3.1 yd) got no answer.
  The core's `GameObject::Use` for a chest only runs scripts, so the client opens a chest by casting an Opening spell at it. Not covered yet.
- The capture of the run is `testdata/captures/m7-gameobjects.twcap` (trimmed: no creature moves, spell hits, chat), used by the objects, game
  and session tests.

Still open: a lever or button (type 1), a goober or altar (type 10) and a `text` object (Baelog's Journal; the agent does not read `SMSG_GAMEOBJECT_PAGETEXT` yet) were not used; run one of each and record what the server sends.

## Dungeon plan: Uldaman (#73 step 2)

`dungeons/uldaman.json` was written from the world database (m910q, 2026-10-13, SELECTs only) and checked offline: every waypoint
(39 of them, entrance to Ironaya) is a `complete` navmesh route from the one before it (`TestUldamanOnNavmesh`, map 70 tiles from m910q).
Nothing in it has been walked by an agent yet. Still open, for the driver (step 3):

- The navmesh has no doors: `path_to` goes through the Keystone and Seal of Khaz'Mul while they are closed. Walk the Keystone leg live
  and confirm the door steps (use the Keystone from the `stand` point, the Seal reaches `active`) before the Ironaya leg.
- Pack positions are spawn points. Pack members that patrol (`creature_movement`) or wander, and the Stonevault Ambushers (stealth),
  may stand elsewhere when the party arrives.
- The Seal at 55 yd must be in view (listed by `gameobjects`) from the Keystone's stand point for the `wait_for` to see its state change.

## Dungeon driver on Uldaman (issue #73, step 3)

First live run 2026-10-13 on m910q (build 19f, GM leader at level 45 with a tank, a healer, a mage and a hunter, `mmaps_dir` with map 070).
It reached Ironaya once (554 s, 0 deaths, only with a trimmed plan) and failed five other times; the findings and what was changed:

| Finding | Change |
| --- | --- |
| Stonevault Cave Lurkers (4850) spawn stealthed (aura 5916 *Shadowstalker Stealth*, `creature_addon.auras`; the template also has *Faded* 6408); the companions never attack them, so `pullback` drew no fight (keystone groups 2 and 4, 3 pulls each) | Stealthed packs are walked through, not pulled (`ambush`, see README "Dungeon runs"); `tools/dungeongen` exports the auras and flags all-stealthed packs |
| The Seal of Khaz'Mul became active 27 s after the Keystone use; the wait was 20 s | `uldaman.json` waits 60 s (also the loader's default) |
| The rest gate burned 150 s every time: the priest sat at 49% mana and never drank | A caster whose mana does not rise for 30 s no longer holds the gate; noted in the report (report to TortoiseBots). A healer below 30% mana still waits the whole gate out; the whole gate is capped (150 s + 2 x 300 s) |
| The gate gave up with "not rested" while the party was still winning a fight | The gate waits for the fight to end, its timeout counts from then |
| A GM `.revive` was judged "still dead" 3 s later although the party list followed a moment after | The dead flag is polled for 5 s after a revive; one revive per look |
| A pack aggroed during a walk (the walk is blocking) and the fight was only picked up at the next action | The walk is stopped, the pack fought, the walk resumed (counts as an extra pull) |
| Setup by hand (`.go` to the entrance, `.levelup -15`, `tbm summon all`) was not in the report | `dungeon_run` takes `setup`, shown as `setup_shortcuts` |

Worth knowing when reading the first run: in the world database 4852 is the *Stonevault Oracle* and 7175 the *Stonevault Ambusher* (not the
other way round); the Ambushers spawn without a stealth aura (their AI casts Backstab at range), the Oracles are not stealthed, and the
baelog group 1 failure of run B (`[4852 4851]`) was the Rockchewer: the tank said "pull start -> reach pull" and lost its target, which is
a TortoiseBots matter, not stealth. The ironaya leg's Cave Lurker group was pulled in run A5 although the lurkers spawn stealthed, so the
driver decides by the aura it sees on the unit, and the plan's `ambush` flag only when nothing is in view.

Still to check live with the new driver (a free slot, a GM leader and a hired party of a tank, a healer and two damage dealers at level
40-45, `mmaps_dir` with map 070, the leader as the party leader):

1. Hire the party at the leader, log in, `invite`/`accept_invite`, walk or `.go` into Uldaman (map 70) next to the entrance
   (-228.9, 46.1, -46) and list the setup you did by hand in `dungeon_run`'s `setup`. Start `dungeon_run {"name":"uldaman","from_leg":"keystone",
   "setup":["go entrance","levelup -15"]}`, then `{"name":"uldaman","setup":[...]}` from the start. A teleport start is `"teleport": true` (flagged).
2. Poll `dungeon_status` and `events` (`kinds`: `dungeon_leg`, `dungeon_step`, `dungeon_failed`, `dungeon_done`); keep the capture.
3. Pass: the run reaches and kills Ironaya from the keystone leg in one run (Keystone, Seal, Ironaya) and Baelog (with Eric and Olaf) from the start,
   and ends with a report (`state` done or a clear `error`), per-leg seconds, deaths, wipes, `gm_shortcuts` and `setup_shortcuts`.
4. Things to look at that the fakes assume:
   - a stealthed group (keystone group 2): the party stands at each position for 6 s, the lurkers reveal themselves or the note says
     "passed with no fight"; whether a lurker left behind then aggros on the next walk and the walk stops, the pack is fought (`walk_aggros`),
     and the walk resumes from where the party stands (`walk_path` after a cancelled walk);
   - that a unit row of a stealthed mob really lists the aura (`auras[]` 5916) while the mob is out of the party's sight, and drops it once it fights;
   - the rest gate: no 150 s waits for the priest (the note "not drinking"), no "not rested" failure during a fight, a gate that starts after a fight
     ends in a reasonable time (the walk-aggro and ambush fights are followed by a gate before the next pull);
   - `.revive <name>` on a companion: how long the party list takes to show it (the driver waits up to 5 s), with no second revive;
   - the Seal of Khaz'Mul active within the 60 s wait; Eric and Olaf needing a kill (plan decision (b): no script links them to Baelog).
   - the optional GM buffs (`"buffs": "world"`, issue #73): `.aura <id>` on each selected companion lands on the companion (not the leader; developer
     level needed) and shows in its unit row `auras[]` within the 1.5 s the driver waits; each application is a `buff` in `gm_shortcuts`; the buffs are back
     after a GM revive. Setup recommendation: hire the companions 3-5 levels above the dungeon and list that in `setup`; a buffed run is a GM-assisted run.
5. Findings that are TortoiseBots behaviour (the priest not drinking at 49% mana, the tank losing its target after "reach pull", companions skipping a pack,
   not following, not looting) are reported to TortoiseBots as a bug fix or a behaviour change per its vision document, not worked around here.


## Agent state: cooldowns, channels, combo points (issue #80 step 1)

The spell state (README "Agent state") was written from the core's source and tested against the committed captures
(`SMSG_INITIAL_SPELLS`, `SMSG_SPELL_START`/`GO`, `SMSG_CAST_RESULT`, `SMSG_SPELL_FAILED_OTHER`) and `gametest` packets.
These have not been seen on a live server, because the captures hold none: `SMSG_LEARNED_SPELL` for a trainer purchase,
`SMSG_SUPERCEDED_SPELL`, `SMSG_REMOVED_SPELL`, `SMSG_SPELL_COOLDOWN`, `SMSG_COOLDOWN_EVENT`, `SMSG_CLEAR_COOLDOWN`,
`MSG_CHANNEL_START`/`UPDATE`, and the combo point fields. One agent on a player character of a class with all of them
(a rogue, or a mage for the channel) settles it:

1. `login`, then `spells` with `passive: false`: the list matches the spellbook the 1.12 client shows, one row per
   spell, `highest` true on the top rank only. `.levelup` and train a spell: `spell_learned`, then `spell_superseded` for
   the old rank (check the order and that `Highest(name)` follows).
2. Cast a spell with a cooldown (Fire Blast, Sinister Strike then Eviscerate). Read `cast_state` right after: `gcd` is
   active for about 1.5 s (1 s for energy users: check what `gcd.total_ms` says and whether the server agrees by
   casting again at `remaining_ms`: no `spell_fail` with `reason_name` `not_ready`). `spells` shows the cooldown with
   `ready` false; a `cooldown_start` (`source` `cast`) and, `ms` later, a `cooldown_end`. Compare the end with when a cast
   of the same spell stops being refused: the server rounds to whole seconds, so expect the agent to be up to 1 s late
   but never early. A talent that shortens the cooldown is a case where the agent stays late.
3. Interrupt a cast of the agent's (a mob that interrupts, or a GM's `.cast`): `cast_state.last_failure`
   `reason_name` `interrupted`, `gcd` back to 0; and if the interrupt locks a school, a `cooldown_start` with `source` `server`
   (`SMSG_SPELL_COOLDOWN` for the player's own guid, which is the one packet of this kind a player gets).
4. `.cooldown` (GM): a `cooldown_clear` with `all`, and `spells` all `ready`. `.cooldown <spell>`: one `cooldown_clear`.
5. Channel Arcane Missiles: `channel_start` with its `ms`, `cast_state.cast.state` `channelling`; let it end, and move
   to break another: `channel_end` both times.
6. Rogue: `combo_points` events and `status.combo_points` after each Sinister Strike, `combo_target` the mob, and `0`
   after Eviscerate. Check that `status.combo_points` is 0 for a character that has never used any (the field reads
   byte 1 of `PLAYER_FIELD_BYTES`).
7. The item cooldown is not tracked: drink a health potion twice in a row and note that `spells` knows nothing of it
   (expected), and that the second `use_item` is refused with a `spell_fail` `reason_name` `not_ready`.

Also: `SMSG_INITIAL_SPELLS` carries spell ids as u16, so a Turtle spell with an id above 65535 would be cut by the core;
the m910q GM warrior's list held ids up to 56047. Look for a spell in the spellbook that `spells` lacks.

Also from the review of the agent state (items not settled without a live run):

- Resend a cast while it casts (`cast` twice): expect `spell_fail` `reason_name` `spell_in_progress` with `cast_state.cast` still `casting` and the GCD active. Resend an instant spell inside its GCD: `not_ready`, and the cooldown still starts at its `spell_go`.
- A `cooldown_on_fade` spell (Stealth, Cold Blood): `spells` shows it on cooldown while the aura is up, and `cooldown_start` (`source` `event`) when the aura ends. If the end comes without `SMSG_COOLDOWN_EVENT`, the agent keeps it on cooldown (not seen on a live server).
- Shoot with a wand reads `ready` always (no template cooldown; the core uses the ranged attack time). A slow on cast speed (Curse of Tongues) can stretch a 1.0 s GCD to 1.5 s; the agent says 1.0 s.
