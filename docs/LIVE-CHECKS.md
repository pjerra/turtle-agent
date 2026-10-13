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

Second live run 2026-10-13 (build 19g, level 49 party with the `world` buffs, binary c0892ca4): 0 deaths and 0 wipes in four attempts, but no pass
(keystone pull group 3 failed twice, baelog group 1 once, and a buff batch once). Findings and what was changed (turtle-agent PR "Dungeon driver:
fixes from the second Uldaman live run", refs #73):

| Finding | Cause and change |
| --- | --- |
| Run B / retry: the baelog group `[4852 4851]` pulled a Rockchewer 42 yd away; the tank answered `pullback unreachable` | The group's own mobs had been killed by run A's walk aggros and `candidates` fell back to every living unit of those entries within 80 yd. Now only units within 25 yd of the group's positions (or already fighting the party) are members; a group with none left is "taken as already dead". Boss encounters keep the wide look |
| The driver never read the bots' `TBM:ACTION_ERR` replies, so it saw "no fight" | The driver reads them (and the `<mob> is evading` / `is dead` whisper): `unreachable`/evading skips the mob and pulls the next member, a group with nothing left counts as cleared after the bounded tries, `dead-target`/`no-target` picks again; all in the report's `refusals[]` |
| Keystone group 3, Stonevault Ambushers (7175): "pull 1 evaded, pull 2 evaded, pull 3 drew no fight" (86 s, twice) | **They do not evade.** The event log shows the tank fighting them and 5 of the 6 dying (kills 18:36:04, :06, :32, :33, :37). The world export has them as six stationary spawns (movement_type 0, wander 0), no stealth or other aura (creature_template, creature_addon), no creature group: two rows of three at y 215 +-5, so a pull brings one or two and the others stand idle. The driver counted the idle ones (inside the 25 yd the tracked set takes) as part of the fight and ended each pull as "evaded" after 12 s, using one of 3 tries per partial kill. Now a pull in which a member died is progress and does not use up a try, a mob that drew no fight is passed over for the next try, and a group that made progress counts as cleared when the rest will not come (noted). The plan has the east row as its own pull group (`radius` 10 yd on both rows: they are 14 yd apart, inside the default 25). The 6th Ambusher ((-212, 220); never took damage in 22 s of `attack_start` in A, still alive in the retry) is not explained: the export shows nothing special about it (inferred: a spot the tank cannot melee; only a unit-row dump of it, with `flags`, would tell). Baelog group 3's 7175 is another spawn: a wanderer (movement_type 1, wander 3), also unstealthed |
| Pre-run: the first `.aura` of the 4th companion's batch (Rallying Cry) landed on the leader (1 of 3 starts) | Cause not proven (the server may drop a selection of a unit that is not yet in the leader's view; the batch for the 3 others in the same second was fine). The batch now waits for the companion to be in the leader's object store within 80 yd, checks the first `.aura` (companion shows it within 3 s, else: the leader's own aura list is read), selects again and resends once, stops the batch and fails the run on a second miss; an aura on the leader is never a success. Every resend is flagged as a `buff` |
| `from_leg keystone` from the entrance: 167 s of walking with 7 walk-aggro fights | `from_leg` assumes the earlier legs are cleared; use `"teleport": true` with it to start at the leg start (GM, flagged `teleport_to_leg_start`). Without teleport the report now says so in a note. Documented in README "Dungeon runs" |
| `setup[7]: needs 1-200 characters` | The cap is 400 characters |

Still open from run 2: the instance did not reset for a GM leader leaving to Badlands for 2.5 min with `.instance unbind all`, so a clean retry needs a
longer wait or another route (not tried); the Seal and Ironaya were not reached.

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
     after a GM revive. Look at the report's `refusals[]` (an `unreachable` pull, a `dead-target`) and at the `notes` ("landed on the leader; selecting ... again"
     means the race of run 2 happened and was caught). Setup recommendation: hire the companions 3-5 levels above the dungeon and list that in `setup`; a buffed run is a GM-assisted run.
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

## Bot brain: the leader casts on the tank's mob (issue #80 step 2)

`internal/brain` is tested against a scripted fake game and the RPC rig only. Not seen live: that the server accepts the
brain's casts as a player's, and how the opening delay and the back-off hold up with real latency. Run in Uldaman with a
frost mage leader (any level that knows Frostbolt; the leader may still be a GM, the run makes no change to that):

1. Log the mage in, hire or invite a tank (a warrior bot) and a healer. `dungeon_run {"name":"uldaman","brain":"frost_mage"}`
   (or start the brain by hand with `brain_start {"strategies":["frost_mage"],"tank":"<tank name>"}` while the party fights).
2. At the first pull, read `events` kinds `brain_decision`, `brain_idle`, `spell_fail`, `spell_start`. Expect: `brain_idle` with
   `vetoed:opening_delay` for about 2 s after the tank's mob is first in combat, then `brain_decision` `frostbolt` (`result ok`,
   `trigger fight_target`) about every GCD-plus-cast (about 2.6 s at rank 4), each followed by a `spell_start` of the same spell,
   with the mob as target (the tank's `target`, not the leader's selection).
3. Check no `spell_fail` repeats: a refusal (`out_of_range`, `line_of_sight`, `no_power`) must show up in `brain_status.holds`
   and the same spell must not be sent again before the hold ended. Note every `spell_fail.reason_name` seen.
4. If a mob gets onto the mage: `frost_nova` then `fire_blast` decisions on the mob that stands within 8 yd. If two mobs stand
   in front on the tank, `cone_of_cold`; none while a mob nobody fights stands within 12 yd.
5. At the rest gate (`dungeon_run`): `ice_armor` or `frost_armor`, `arcane_intellect` when the auras are missing, `drink` below
   65% mana (the item must be a drink in the bags; `stand_state` goes to `sit`), `evocation` below 20%. The leader must not
   sit down during a walk. `brain_status.rest_ok` is true only inside a gate.
6. After the leg: `brain_state stopped`, `brain_status.running` false, no cast sent between legs.
7. Grep the server's anticheat log for the run; the brain sends casts and facings only, so expect nothing new.

Report the first `brain_decision` that did not match this list, with the `spell_fail` or unit rows around it.

## Map images (issue #79)

Not settled without a live run: the images come out of the first Uldaman captures and the committed fixtures only.

1. After any dungeon run, `turtle-agent map --capture run.cap --events events.json --report report.json --plan dungeons/<name>.json --mmaps <dir> --out run.svg --html`: open the SVG and check against what the run did (the legs' order, each pull where the report says, the wipe and the GM teleports flagged `GM`).
2. In a dungeon with stacked floors (Uldaman past the main hall, Scarlet Monastery): the cut `map` prints in its notes lands between the floors, and a run that climbs shows its route on the upper image only. If it splits a level in two or leaves two apart, say with `--floors` which heights are right.
3. `map_snapshot` during a run (`{"plan":"uldaman","html":true}`): the party's route so far and the plan agree with the live `units`, and the call costs no visible lag on a 40-member fleet child.

## Bot brain: positioning, sight, aggro, interrupts, consumables (issue #80 step 3b)

Tested against a scripted fake game and a fake navmesh only; the raycast against the navmesh fixtures. Not seen live: that the
mesh ray agrees with the server's sight in a real dungeon, that Detour's `walk_path` steps behave in a fight, that `SMSG_SPELL_START`
of enemies carries the target the brain reads, that dynamic objects arrive with a position and a radius, and the item and First
Aid behaviour. Run a frost mage with `brain_start {"strategies":["frost_mage"],"tank":"<name>"}` (movement is on by default, also in a dungeon run, where the driver holds the move lock around its walks) in a dungeon with a warrior tank, with
`mmaps_dir` set:

1. **Band and sight.** At a pull, `brain_decision` `reposition` (`trigger bad_position`) when the mage is closer than 19 yd or
   farther than 29 yd from the tank's mob, in its front or out of its sight. After the step, `units` must show the mob 20-28 yd away,
   and the next `frostbolt` must not be refused with `line_of_sight`. Note every `spell_fail` `line_of_sight` while the brain
   believed it had sight: each is a place where the mesh ray and the server disagree (report map, x, y, z of both ends). A `line_of_sight` refusal must be followed by a `reposition` (the fight's mob counts as blocked for 5 s), not by the same
   cast again after 3 s.
2. **No walk against the driver.** In a dungeon run the brain moves the leader in fights: `reposition`, `step_away`, `run_to_tank` and
   `leave_ground_aoe` decisions appear (see "The brain positions the leader in a run" below). While `brain_status.move_held` is true (the
   driver is in a walk) there is none, in a run or by hand.
   While a walk step runs (2.5 s at most), a `counterspell`, `ice_block`, potion or `leave_ground_aoe` must still come in the next
   tick: the step is cancelled (the mage stops) and the action is a `brain_decision` as usual; no `frostbolt` starts mid-step.
3. **Aggro.** Let a mob run to the mage: `run_to_tank` while it is over 9 yd away and the tank over 8 yd, then `frost_nova` at melee,
   then `step_away` (the mob carries the Frost Nova aura), then back to `frostbolt`. Below 50% health in melee with Frost Nova on
   cooldown and the mob behind: `blink`; below 20% with a mob on the mage: `ice_block` if the talent is known. Note whether the
   server accepts Blink and where the mage lands.
4. **Ground effects.** Stand in a boss's fire patch or Blizzard of an enemy: `leave_ground_aoe` within a second, the mage ends
   outside. Check `units`/dynamic objects: the object must have a position and radius; a bot's own Blizzard must not trigger it.
5. **Interrupts.** An enemy healer or caster: `interrupt` (`enemy_casting`) with Counterspell on a heal or crowd-control cast, or a
   damage cast on a party member, not on a cast with under 350 ms left. Read `spell_start` `target_guid` of the enemy cast and note
   whether it is the party member the spell is aimed at; note how often the Counterspell is `interrupted` or `not_ready`.
6. **Items.** With a healthstone and potions in the bags: below 25% health `healthstone`, then `health_potion`, and not again for 2
   minutes. A bandage below 45% with no mob on the mage: the First Aid channel must start (otherwise the bandage is skipped for 5
   minutes: note whether the mage has the skill). A mana potion in a boss fight below 10% mana.
7. **Wand.** Out of mana with a wand equipped: `wand` (Shoot) once, shots continue; without a wand: `spell_fail`
   `equipped_item_class` once, then nothing for 30 s.

## The brain positions the leader in a run (issue #80, move lock wired)

Tested against the fake game (the driver's holds around every walk, teleport, release and portal; the brain's lock, steps and
cancels) and the RPC rig; the lock ordering is exercised by `go test -race`. Not seen live: that `walk_path` honours the cancel of a
brain step in a fight, how often the hold and the retry cost a pull a second, and the timing of an emergency escape during a slow
cancel. Run `dungeon_run {"name":"uldaman","gm":false,"brain":"frost_mage"}` with a tank, a healer and two damage dealers, `mmaps_dir` set:

1. **Moves in a run.** `brain_status` during the run has no `no_move`. At a pull, `brain_decision` shows `reposition`
   (`bad_position`) or `run_to_tank`, and `units` shows the mage move to its band; nothing moves it between the decision and the
   arrival except that step.
2. **Never both.** While the driver walks (`brain_status.move_held` true) there is no brain decision with a walk and no `walk` error
   in `brain_decision` (`the dungeon driver is walking`). When a pull starts in the middle of a driver walk, the walk stops (the
   driver cancels it), the lock is let go (`move_held` absent) and the next step of the brain is a `reposition`, not an error.
3. **A driver walk cancels a brain step.** Start a pull so that the mage is mid-step when the party finishes (the fight ends): the next
   driver walk begins within a second or so; there is no `walk_path` refusal "already walking" in the run's events.
4. **Emergency during a step.** Stand the mage in a boss's fire while it walks: `leave_ground_aoe` runs within a tick or two, and if
   a decision says `the brain's earlier step is still stopping` the next tick shows `leave_ground_aoe` ok (it is not backed off for 1 s).
5. **Corpse run.** Let the leader die and run back (see below) with the brain started by hand before the run: the ghost's `walk_path`
   steps are not interrupted by brain steps. Note any hold that times out (`did not stop within 5s` in the report notes).
6. **Deaths count.** Let the leader die during a wipe recovery (`recover_wipe`) five times in one run: the sixth stops the run with "the
   leader has died 6 times". A leader dying while dead members are brought back, so that nobody lives, is recovered as a wipe (a
   `wipe_recovery` event), not ended with "wipe".

## A non-GM leader: death, resurrection and the corpse run (issue #80 step 3a)

Tested against the fake game and the RPC rig only. Not seen live: the layouts of the death packets against a real server, the ghost's
walk, the portal and the probe. Run in Uldaman with a **player** account (no GM level), a frost mage leader of level 35-45 and a
hired party of a tank, a healer and two damage dealers; set up by hand (stand at the entrance, -228.9 46.1 -46, or outside at the portal).

1. `dungeon_run {"name":"uldaman","gm":false,"brain":"frost_mage","setup":["by hand: entrance"]}`. Expect `leader_gm` `false` in the
   answer and in the report, and **no `say` line starting with `.`** in the chat log; `teleport` and `buffs` are refused with `gm: false`.
2. The probe: the same without `gm`, once on the player account and once on a GM account. Player: `report.leader_gm` false,
   `account_level` 0, a note "account level is 0 (a player)", the `.account` line answered by "Your account level is: 0" and no `say`.
   GM: `leader_gm` true, `account_level` N and `gm_caps` matching it (1: teleport only; 2: also visible; 3: all), `.gm visible on`
   only from level 2. On a level-2 account a dead member is not `.revive`d (recover_wipe runs it back). Note the exact reply text and event kind of `.account` (the
   probe matches `Your account level is: N` in a system event) and report anything else.
3. Resurrection: let the leader die in a fight with the healer alive (pull two packs at once, or `levelup -10` before). Expect
   `dungeon_leader` `died`, then `resurrect_request` (the healer casts Resurrection) and `resurrect_accepted`, `back`; `status` `dead`
   false; `resurrections` 1 in the report. Note how long the healer takes (the driver waits 60 s) and whether it resurrects in combat.
4. Corpse run: kill or stop the healer first (`.bot action` or a second death), so that nobody resurrects. Expect `released`, a
   `ghost` true status at the graveyard, `corpse_run`, the walk (`walk_path` results), `waiting_reclaim_delay` while
   `corpse_reclaim_delay` runs, `reclaimed`, `back`, and `resurrection sickness` in the notes. Look at: whether the ghost reaches the
   corpse along the navmesh (ghosts walk through water and over the same terrain), the reclaim within 34 yd, the delay the server sent.
5. Corpse inside Uldaman: die in the dungeon. Expect the graveyard outside (map 0), a `corpse_query` with `map` 0 and `corpse_map` 70,
   a walk to the entrance (about -6053.7 -2954.6 213.7), `portal` with trigger 286, and the leader alive at the dungeon entrance. Report
   what the server does if the trigger is refused (4 tries, then `area trigger 286 did not take the ghost into map 70`).
6. Wipe: let all die. Without `recover_wipe`: the run ends `wipe`. With `"recover_wipe":true`: `.bot action release` and
   `.bot action corpse run` go to the party, the leader runs back, the bots stand up one by one (note which do and which do not: that
   is TortoiseBots behaviour), the run gathers, rests and goes on from the step; `wipes` counts it.
7. The rest gate after the return: `rest_eat` eats when health is under 85% (`brain_decision` `eat`, the Food aura, `stand_state` sit),
   after `drink`; the gate passes once the leader has 90% health.
8. Grep the server's anticheat log for the run: the ghost's movement, `CMSG_AREATRIGGER` and `CMSG_RECLAIM_CORPSE` come from the walk and
   the session only, so expect nothing new.

Report the first thing that did not match this list, with the `dungeon_leader` events and the `status`/`death_status` rows around it.

## Bot brain: roles and the warrior (issue #80 step 3c)

Tested against a scripted fake game only. Not seen live: that the warrior's stance shows as an aura under the stance's name
(`Battle Stance`, `Defensive Stance`, `Berserker Stance`), that rage arrives in the unit rows in tenths, that `SMSG_ATTACKERSTATEUPDATE` reaches the log with the
outcome `dodge`, that the server accepts Heroic Strike queued by a `cast`, and how the melee spot behind a mob holds up with real
mob movement. Run a warrior (a level 30+ character with Berserker Stance and Mortal Strike or Bloodthirst, ideally level 40+) in a dungeon with a tank bot
and a healer, `mmaps_dir` set, and start it by hand with `brain_start {"strategies":["arms_warrior"],"tank":"<tank name>"}` (or
`fury_warrior`); a dungeon run starts the brain with `no_move`, so check the positioning by hand:

1. **Stance.** Out of combat, in the wrong stance: one `stance_arms` (or `stance_fury`) `brain_decision` casting 2457 (2458), and `units` for the
   leader shows the stance aura within a second. If it casts again every 2 s, the aura name does not match: report the aura's `name`.
   In combat with over 25 rage no stance change happens.
2. **Opening.** At a pull, `brain_idle` `vetoed:opening_delay` for about 2 s, then `auto_attack` (the log shows `attack_start` with the leader as source)
   and the rotation: Arms `mortal_strike`, `rend`, `overpower` after a `melee` event with `outcome dodge` on the mob (and not otherwise), `execute` below 20% health,
   `heroic_strike` with spare rage (not twice within 1.5 s); Fury `bloodthirst`, `whirlwind`, `heroic_strike`. Rage use: no `spell_fail` `no_power`
   repeating (each is a back-off in `brain_status.holds`).
3. **Positioning.** A mob that faces the leader (the tank holds it): `melee_reposition` once, then the leader stands within 6 yd and behind
   the mob (compare the mob's `o` with the way to the leader: at least 100 degrees round). No walk when the mob is on the leader. Note a
   `spell_fail`/`attack_error` `bad_facing`/`not_in_range` after the walk.
4. **Gap closers.** The leader 10 to 20 yd from the tank's mob when the fight starts: Arms `charge` after the opening delay (out of combat,
   Battle Stance), no walk before it; Fury `intercept` when in combat. Never a Charge into a mob the tank does not hold.
5. **Interrupts.** `interrupt` with Pummel on a heal or a crowd-control cast within 5 yd, in Berserker (Fury) or Battle Stance (Arms; the data says it works there, check it, and whether the spellbook holds rank 13491 as well); a tank with a shield: Shield Bash.
6. **Berserker Rage** (Fury) when feared: one `berserker_rage`, the fear ends. Note whether the server allows the cast while feared.
7. **Tank.** `brain_start {"strategies":["tank_warrior"]}` in Defensive Stance with a shield: `sunder` to 5 stacks, `revenge` after a dodge or parry,
   `thunder_clap` and `demoralizing_shout` with 2+ mobs, `taunt` when a mob attacks the healer (note the range: Taunt reaches 5 yd and the tank walks
   to the mob), `melee_reposition` once when the mob faces the group, `shield_block` when hurt. Report `brain_idle` reasons that
   repeat.
8. **Healer.** Done in step 4a; see the next section.

## Bot brain: the healer and the priest (issue #80 step 4a)

Tested against a scripted fake game only. Not seen live: that a party member's unit row carries its real health (not a percentage) and the
cast of another healer (`casting` with its target), that the auras `Weakened Soul`, `Power Word: Shield`, `Shadowform`, `Inner Fire`,
`Power Word: Fortitude` and `Abolish Disease` show under those names, that Priest spells reach 40 yd with the 38.5 yd the brain assumes, and
that Silence and Shadowform are in the spellbook of a talented priest. Run a level 30+ priest with `mmaps_dir` set in a dungeon with a tank
bot and two damage bots (no healer bot, or the TortoiseBots healer set to follow), start it by hand with
`brain_start {"strategies":["holy_priest"],"tank":"<tank name>"}`; a dungeon run starts the brain with `no_move`, so check the position by hand:

1. **Triage.** In a fight, `brain_decision` `heal` targets the member with the lowest health (the tank a little earlier), the player itself when it is
   the lowest. Pull a member's health down with `.damage` (GM) and watch the order; `target` in the decision is the guid. With a TortoiseBots healer in the
   party, no two `heal` decisions on the same member within one cast.
2. **Rank.** A small deficit takes a small rank (`spell_id` 2052, 2053), a big one a Heal or Greater Heal rank, a member under 45% Flash Heal.
   Report any `heal` decision on a member who was above 90% (overheal) and any `spell_fail` `no_power` repeating.
3. **Range and sight.** A member 45 yd away or behind a wall is left alone (`brain_idle` `not_possible` only when nobody else needs a heal);
   a `spell_fail` `line_of_sight` for a heal must be followed by a heal on someone else, not by the same member within 3 s.
4. **Shield.** `shield` on the tank when it targets the mob, none while the tank carries `Weakened Soul` (look at `units` for the aura's
   name), and `shield` on a member who just lost a quarter of its health. Report if the server refuses a shield for the Weakened Soul
   the brain did not see.
5. **Dispel.** Let a mob polymorph or slow a member: `dispel` with Dispel Magic (527, 988) on it, once per aura per 4 s. A diseased member:
   Cure Disease (528) or Abolish Disease (552) and then nothing while the Abolish aura stands. A poison or a curse: no cast.
6. **Position.** By hand (no `no_move`): `heal_reposition` once at the start of a fight to a spot 20 to 30 yd from the tank, out of the
   mob's front and with sight of the party; no walk while a heal is cast. A mob that attacks the priest: `fade` and `run_to_tank`.
7. **Fade and Scream.** Two mobs on the priest at 70% health or less and nothing sleeping within 30 yd: `psychic_scream`; with a sleeping mob within 30 yd: only
   `fade`. Note what the feared mobs did.
8. **Rest.** At a rest gate: `inner_fire`, `fortitude` on the priest and then on each member, `heal` of members under 90%, `drink` below
   80% mana (before the top-up heal). In a boss fight below 10% mana: `mana_potion`.
9. **Shadow.** `brain_start {"strategies":["shadow_priest"]}` with Shadowform learned: `shadowform` (15473; look at `units` for the aura named
   `Shadowform`), `vampiric_embrace`, `shadow_word_pain`, `mind_blast` on cooldown, `mind_flay` as the filler, `interrupt` with Silence (15487) on a
   heal or crowd-control cast within 20 yd. The priest stands 14 to 19 yd from the mob. Without Shadowform: `smite` when Mind Flay is out of reach.
10. **Sitting.** After a drink or a meal the leader sits: a `brain_decision` `stand_up` (one `CMSG_STANDSTATECHANGE`, then `stand_state` `stand`) must come before
    the first buff or cast, and no `spell_fail` `not_standing` repeats. While the Drink aura is up and nothing happens, no cast is sent; a mob on the
    leader or a member under 45% breaks the meal with one `stand_up`. Note whether the core takes state 0 from a sitting player and whether the Drink aura goes with it.
11. **Dispel range and walking.** A polymorphed member 32 yd away: `heal_approach` once, then `dispel`; a slowed member 32 yd away: no walk. A hurt member just out of reach: `heal_approach`,
    then `heal`, no `heal_reposition` between them.

## Movement client time over a long session (ClockDesync)

Live run 3 (Uldaman, agent on a WSL2 laptop) logged 49 `ClockDesync` Inform lines for the leader, the desync growing from 6.9 s to 73 s in
40 minutes: the agent counted the movement client time on the monotonic clock, which runs about 3-4% fast under WSL2 (only the wall
clock is disciplined, stepped back about a second every half minute). The client time now follows the wall clock's pace (clock.go).

1. Run the agent for at least an hour with walking (a dungeon run, or walks of a minute or two with idle between), on the WSL2 laptop.
2. Grep the server's anticheat log for the character: the `ClockDesync` count must stay 0 over the hour.
3. Same log: no `TimeBack`, `HeartbeatSkip` or `OverspeedZ` lines the run did not have before.
4. Also one continuous walk of six minutes or more (`walk_path` across a big zone, or a dungeon leg with no pack on the way): the
   same log shows no `ClockDesync` for it. The walk is timed on the client time itself (`walkTime`), since a monotonic-timed walk
   gained 1.8% x 360 s = 6.5 s on a 3.8% fast host.
5. `turtle-agent map --capture` on that run's capture (also one made with `login --create`) reports frames, not 0.

## Bot brain: rogue, hunter and retribution paladin (issue #80 step 4b)

Tested against a scripted fake game only. Not seen live. Run each class in a dungeon with a tank bot and a healer, `mmaps_dir` set, start the brain
by hand (`brain_start {"strategies":["combat_rogue"],"tank":"<tank name>"}`, `marksman_hunter`, `ret_paladin`) and read the `brain_decision` and
`brain_idle` events and `brain_status`.

**Rogue** (level 30+: Slice and Dice, Kick, Evasion known)
1. **Combo points.** `combo_points` events step 1..5 under `sinister_strike`; `slice_and_dice` once at 2+ points (the aura shows in `units` for the
   leader), `eviscerate` at 5 (or 4 on a mob under 35%), the points back to 0 after it. Check that the combo points are on the mob the brain attacks
   (`combo_target`): on a target switch the finisher waits for new points. No `sinister_strike` at 5 points. No `spell_fail` `no_power` repeating.
2. **Energy.** Energy arrives in whole points in the unit rows (not tenths) and the costs (40, 30, 20) match what the server charges.
3. **Position.** Behind the mob (`melee_reposition` once when the tank turns it to face the rogue); no walk when the mob is on the rogue.
4. **Interrupt.** `interrupt` with Kick (spell 1767 or its rank) on a heal or crowd-control cast within 5 yd; the server's `interrupted` after it.
5. **Threat.** A mob on the rogue while the tank is near: one `feint`, not again for 10 s. Health under 50% with a mob on the rogue in melee: `evasion`.
   Under 25%: `vanish`. Under 30% with the tank more than 8 yd away: `sprint`, then `flee_to_tank` (the leader ends about 4 yd from the tank).
   Note whether Feint, Evasion and Vanish are accepted in combat as cast, and whether Sprint starts the run.
6. **Not done:** Stealth openers, Backstab, poisons.

**Hunter** (level 30+ with a bow or gun and ammo)
1. **Auto Shot.** One `auto_shot` after the opening delay and the `attack_start`/shot events keep coming; the brain sends it again every 3.5 s: check
   that there is no second send while the shots keep coming (a resend would restart the ranged timer at 500 ms), and one after a walk. After a `hunter_reposition` walk the shots start again.
2. **Rotation.** `hunters_mark`, `serpent_sting`, `arcane_shot` on cooldown, `multi_shot` with 2+ mobs on the tank. No sting or arcane below 20% mana.
   No `multi_shot` when a mob near the target is not engaged.
3. **Dead zone.** A mob within 9 yd that is not on the hunter: `hunter_reposition`, the hunter ends 12 to 29 yd from the mob with sight of it. Note any
   `spell_fail` `out_of_range`/`too_close`/`line_of_sight` after it.
4. **Aspects.** Out of combat: `aspect_hawk` once. Cheetah/Pack on in combat: Hawk. Below 20% mana in combat: `aspect_viper` (level 56+ on Turtle), back to
   the Hawk from 60%. Note the aura names (`Aspect of the Hawk`, `Aspect of the Viper`).
5. **Aggro.** A mob running at the hunter: `concussive_shot` once. A mob on the hunter within 15 yd with the tank near: `feign_death`; then no shots for 6 s.
   The brain sends `CMSG_CANCEL_AUTO_REPEAT_SPELL` just before the Feign Death cast: check that no shot lands during the feint (Auto Shot is really off), that the mob drops the hunter, and that Auto Shot starts again after the 6 s. Without a tank: `hunter_swing`
   and `raptor_strike` (the swing starts; Raptor Strike is not sent twice within 1.5 s).
6. **Ammo.** With the quiver empty: `spell_fail` `no_ammo` (or `need_ammo`) once, then the shots held 30 s (`brain_status.holds`).
7. **Pets** are driven since step 4d: see "Bot brain: pets and the warlock" below.

**Retribution paladin** (level 30+ with Seal of Command; level 20+ for Crusader Strike on Turtle)
1. **Seals.** A `seal` (Command) as soon as the tank's mob is within 15 yd, also before the first attack; `judgement` on cooldown (10 s), the seal gone after it
   and cast again; `crusader_strike` between. Check the aura names `Seal of Command`, `Seal of Righteousness`.
2. **Companion paladins.** With a companion paladin judging: the mob carries `Judgement of the Crusader` or `Wisdom` from it; the leader does not choose
   Seal of the Crusader then, and at low mana does not choose Wisdom if the mob has Judgement of Wisdom. Check that its own Judgement does not remove theirs.
3. **Auras and blessings.** Out of combat: `paladin_aura` (Retribution) once, `blessing` (Might) once; a companion's Devotion Aura does not stop the
   Retribution Aura; a blessing from a companion counts as the blessing and is not cast over. Does a second paladin's blessing of the same name show twice?
4. **Interrupts and emergencies.** `interrupt` with Hammer of Justice on a cast within 10 yd; a mob on the leader in melee below 45%: `hammer_of_justice`;
   below 35%: `divine_protection` and no attacks while the aura lasts (does the server really pacify the leader?); below 20%: `lay_on_hands`.
5. **Consecration** with 2+ mobs in melee, not with a loose mob near; **drink** out of combat below 65% mana at a rest gate.
6. **Mana floor.** Run the paladin dry in a long fight: below 30% mana the seal is Wisdom (unless the mob has Judgement of Wisdom) and only that is judged, below 15% no Crusader Strike and no other seal; the swing goes on.
## Bot brain: the druid and the shaman (issue #80 step 4c)

Tested against a scripted fake game only. Not seen live, and the first four are the ones that decide whether the presets work at all:
that the form aura shows under its spell's name in the player's own unit row (`Cat Form`, `Bear Form`, `Dire Bear Form`) and the unit's `power_type` follows it
(3 energy, 1 rage) with the mana in `mana`/`max_mana`; that `CMSG_CANCEL_AURA` for the form's aura takes the form off (the brain sends it with the aura's own spell id)
and a heal sent after it is accepted; that the core really refuses food, drink and potions in a form (`no_items_while_shapeshifted`; the brain does not try); that a weapon imbue
cast with `TARGET_FLAG_ITEM` and the main-hand weapon's guid is accepted (`Session.CastOnMainHand`; spell 8232 / 8024 / 8017 and the like); that the totem creatures show in `units`
under a name that holds the totem's spell name and `Totem`; that Water Shield (spells 45527 and 51533 to 51536, level 26 and up) is in the spellbook of a Turtle shaman and the aura
shows as `Water Shield`. Run each preset in a dungeon with a tank bot (or by hand with `brain_start {"strategies":[...],"tank":"<name>"}`; a dungeon run starts the brain with
`no_move`, so check the positioning by hand), `mmaps_dir` set, and read `brain_decision`, `brain_idle` and `brain_status.holds`.

1. **Resto druid** (`resto_druid`, level 30+): `heal` with Healing Touch (5185 to 25297) on a deficit that fits, Regrowth (8936 to 9858) under 25% health (the tank 35%) and for a member under
   45% when it covers sooner; `hot_tank` Rejuvenation on the tank in a fight; `dispel` with Abolish Poison (2893; none again while its aura stands), Cure Poison (8946), Remove Curse (2782).
   Out of combat at a rest gate: `mark_of_the_wild` on the druid and each member, `thorns` on the tank, `drink` below 80% mana. In a fight below 20% mana: `innervate` (level 40).
   Shift into a form by hand (Cat Form) with a member hurt: one `leave_form` (relevance 42) before the first `heal`, no `spell_fail` `not_shapeshift`, and `brain_idle` `vetoed:caster_form_only`
   in between. Report whether the unit row still shows the aura for a moment after the cancel (the brain holds `leave_form` 2 s and cancels again after that).
2. **Balance** (`balance_druid`): `moonfire` (and `insect_swarm` from level 20), then `starfire` or `wrath`; no cast with a cast time while moving. A mob on the druid under 80% health:
   `entangling_roots` **outdoors** and then `step_away`; indoors the server refuses it (`only_outdoors`), which must be a 30 s back-off in `holds`, not a repeat. The band is 20 to 28 yd.
3. **Cat** (`feral_cat_druid`, level 20+ with Cat Form): at a pull `enter_form` casts 768 (or at a rest gate with nothing to do) and the unit row shows `Cat Form`, `power_type` 3; then
   `faerie_fire_feral`, `tigers_fury`, `rake`, `shred` from behind the mob (compare the mob's `o` with the way to the druid) and `claw` otherwise, `rip` at 4 combo points and
   `ferocious_bite` at 5 (or 3 on a mob under 35%). Report any `spell_fail` `not_behind`, `no_combo_points`, `only_shapeshift` or `no_power` that repeats. Never `leave_form` in a fight, whatever the mana or health.
4. **Cat, out of combat**: below 50% mana with a drink in the bags a `leave_form`, then `drink` (the Drink aura, the druid sits), then `enter_form`; below 85% health with food, `leave_form` then `eat`;
   with Mark of the Wild missing and 60% mana, `leave_form`, `mark_of_the_wild`, `enter_form`. Report if an item is used in a form (`inventory_error`/`spell_fail` `no_items_while_shapeshifted`).
5. **Bear** (`bear_tank_druid`, level 10+, Dire Bear Form from 40): `enter_form` 5487 or 9634 at a pull, `enrage` at the start with no rage, `swipe` and `demoralizing_roar` with 2+ mobs, `maul`
   from 40 rage (once per swing: no `spell_fail` repeating), `taunt` with Growl (6795) on a loose add within 5 yd, `frenzied_regeneration` under 35% health with rage. Growl is not tried in another form.
6. **Resto shaman** (`resto_shaman`): `heal` Healing Wave (331 to 25357) for most deficits, Lesser Healing Wave (8004 to 10468) in an emergency, `chain_heal` (1064) only with two or more members
   under 85%; `dispel` Cure Poison (526) and Cure Disease (2870), nothing for a curse or magic; `shield` Water Shield when known, else Lightning Shield.
7. **Totems** (any shaman preset, with the four totem tools in the bags): one `drop_totem` per tick at the start of a fight, in the order of the preset (fire, air, water, earth), none again for 10 s and
   none while the creature stands within 8 yd of the drop; look at `units` for the totem's name. Walk 30+ yd away with the group and fight again: the slot is dropped again. Without the tool: no `drop_totem`
   at all (report if the tool is not needed in Turtle, then `spell_fail` `totems`/`item_gone` is the only answer). Kill a totem or let it run out: it is dropped again. Report if the name never matches (the manager then drops it again after 10 s each time; an unresolved name counts as ours).
8. **Elemental** (`elemental_shaman`): `flame_shock`, then `lightning_bolt`; `chain_lightning` (level 32) with 2+ mobs near the target and none loose near the shaman; none of the bolts while moving; `interrupt`
   with Earth Shock (8042 to 10414) on a heal or crowd-control cast within 20 yd. The shaman stands 14 to 19 yd from the mob (the shocks reach 20, Lightning Bolt 30).
9. **Enhancement** (`enhancement_shaman`): out of combat at a rest gate `imbue` once (Windfury Weapon from level 30, Flametongue Weapon from 10, Rockbiter Weapon before)
   enchantment in the client or with `inspect`; it is not cast again for 55 minutes. In a fight `auto_attack`, `stormstrike` (level 30) every 8 s, `flame_shock`, `earth_shock` above 60% mana; the shaman stands behind the mob.
   Take the weapon off and start the brain: one `imbue` that fails (`item_gone` or the session's error), then a minute's silence.
10. **Never**: Ghost Wolf, Prowl, Pounce, Hibernate, a heal in a form, a potion or a meal in a form. Grep `brain_decision` for those spells.

## Dungeon runs: required items and conjured water (run 3 follow-ups)

1. **Keystone item.** Uldaman `from_leg keystone`, `gm:false`, no Staff of Prehistoria (7733) in the bags: `dungeon_run` fails at once with
   "the leader does not carry Staff of Prehistoria (item 7733) ..." (leg 2 "keystone" step 0), before any walk or order. With the staff
   (`additem 7733`) the Keystone opens and the Seal of Khaz'Mul is active in about 27 s. With `gm: true` the staff is not asked for; with `gm` omitted on a GM account that plays `.gm off` (run 3), the check still fails the run (the core lets a GM through the lock only in GM mode).
2. **Conjure rank.** A level 45 mage that has learned every spell (`learn all_myspells`), no drink in the bags, at a rest gate below 65% mana:
   `brain_decision` `conjure_water` carries spell 10139 (rank 6, Conjured Sparkling Water, level 45), not 10140; the water it makes
   is drunk. Same for `conjure_food` with 10145 (Conjured Sweet Roll). At level 55 the casts are 10140 and 28612.

## Bot brain: pets and the warlock (issue #80 step 4d)

Tested against a scripted fake game and a scripted world server only (packet layouts checked against the core, tortoise-wow 5d79486). Not seen live. Run a
hunter with a tamed pet and a warlock in a dungeon with a tank bot and a healer, `mmaps_dir` set; start the brain by hand (`marksman_hunter`,
`affliction_warlock`, with `"tank":"<tank name>"`), poll `pet` and read `pet_*` events, `brain_decision` and `brain_idle`.

**The pet state** (any pet, no brain needed)
1. **Summon field.** After Call Pet / a summon, `pet` shows `present`, the right `guid`, `entry`, `name` and `level`, and one `pet_summoned` event. Does the
   player's `UNIT_FIELD_SUMMON` arrive in the same update block as the pet's create block, or later? (`SMSG_PET_SPELLS` is sent when the pet joins the map, the field with the next
   update; the agent keeps the bar by pet guid so either order works.) Note which came first.
2. **Bar.** `pet.bar` true with `react` and `command` as the client shows them, and `spells[]` with Growl/Bite/Claw (hunter) or Torment/Firebolt (demon) on slots 3 to 6 and the right
   `autocast`. Mount: `enabled` false after `SMSG_PET_MODE`, true again on dismount. Does `react`/`command` stay right after you change them with the client's own buttons (the core sends no
   state packet for an order, so the agent only knows what it sent itself)? Note any drift.
3. **Happiness and focus** of a hunter's pet: `happiness` falls with time and in combat, `happiness_state` 3/2/1 at 666000 and 333000, `power_type` 2.
4. **Death.** A hunter's pet killed: `pet_died` once, `pet.dead` true, the pet stays in `pet` until Revive Pet; `pet_revived` after it. A demon killed: `pet_died`, then `pet_dismissed`
   (`was_dead` true) about 15 s later.

**Pet orders** (`pet_action`)
5. `attack` at a mob the tank has: the pet runs and fights (`pet.target` shows the mob); at a friendly unit or none: `pet_error` `feedback` `cant_attack_target` / `nothing_to_attack`.
   `follow` ends the attack, `stay` keeps it still, `passive` / `defensive` / `aggressive` change `react`. `cast` of Growl by `slot` / `name` at a mob: the pet growls; right after, `pet_error`
   `reason` `not_ready` from the cooldown. `autocast_off` for Growl: the pet stops growling by itself; the client's bar shows the same.

**The strategy** (hunter and warlock)
6. **Attack timing.** With a tank bot pulling: no `pet_attack` decision during the 2 s opening delay or while the mob is on nobody; one `pet_attack` once the mob targets the tank; the pet never goes at a
   mob nobody has pulled (check on a patrol that the pet stands still while the tank walks up). The pet does not change target on every tick (no repeated `attack` order for the same mob).
7. **Recall.** When the mob dies: one `pet_recall` (`follow`) within about 3 s if the pet still has a target; none when the pet is already following. Run away from the fight with the pet a long way
   off: `pet_recall` with trigger `leader_fleeing`. A pet told to `stay` by hand is left alone.
8. **Defensive and taunt.** In a group, an aggressive pet becomes `defensive` once, and a hand-set `aggressive` afterwards is not fought. With the tank bot in view the pet's Growl/Torment autocast goes
   off once per spell (check on the client's pet bar that Growl/Torment no longer glows); the tank keeps the mob. Solo: untouched. Does the pet take mobs from the tank anyway?
9. **Hunter upkeep.** No pet out, out of combat: `pet_summon` (Call Pet) once; a hunter without a pet gets `spell_fail` `no_pet` once and then nothing for a minute. Dead pet: `pet_revive` out of
   combat only (does the 10 s cast get interrupted by a mob? note it). Hurt pet within 20 yd: `pet_mend` (below 70% out of combat, 50% in); no second Mend while the channel runs. **Feed Pet is not driven**: feed by hand when `happiness_state` is 1.

**Warlock** (level 30+: Corruption, Curse of Agony, Drain Life, Life Tap, a demon known; a shard or two in the bags)
10. **Rotation.** `corruption` then `curse` (Agony), then `shadow_bolt` as the filler; no cast while the warlock moves. A mob that already carries another warlock's curse gets none from the brain. With a mage and a priest
    in the group and a mob above 60%, the curse is the Elements.
11. **Position.** The warlock ends 18 to 26 yd from the mob with sight of it. Drain Life (20 yd) needs the near half: note how often `out_of_range` follows a `drain_life`.
12. **Drains and tap.** Health under 45%: `drain_life` (the channel heals); a mob at 25% or less with fewer than 5 shards in the bags: `drain_soul`, and a shard appears if the mob dies during it. A grey mob must not get one.
    Mana under 25% with 60%+ health in combat: `life_tap`; out of combat under 40% mana with 85%+ health: `life_tap` before the drink.
13. **Emergencies.** A mob on the warlock below 40% health: `death_coil`; below 35% with no tank in view and no sleeping pack within 30 yd: `fear` (does the mob run into something?). **With the tank bot present: no `fear`, ever.**
    A shadow cast aimed at the warlock: `shadow_ward` (note whether the ward goes up before the cast lands).
14. **Upkeep and demon.** Out of combat: `demon_armor` once, `create_healthstone` when none is in the bags and two shards are (the stone shows in the bags; its aura name is `Demon Armor` or `Demon Skin`),
    `pet_summon` with the Voidwalker when a shard is in the bags, the Imp otherwise (`pet_imp` always the Imp). Does the 10 s summon survive the mob that wanders up?
15. **Aura names to read off the live units:** `Demon Armor` / `Demon Skin`, `Corruption`, `Curse of Agony`, `Curse of the Elements`, `Shadow Ward`, `Mend Pet`, `Drain Soul`. A different spelling breaks the matching rules.
