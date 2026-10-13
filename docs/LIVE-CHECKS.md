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
Aid behaviour. Run a frost mage with `brain_start {"strategies":["frost_mage"],"tank":"<name>"}` (movement is on by default; a
dungeon run starts the brain with `no_move` until the driver holds the move lock) in a dungeon with a warrior tank, with
`mmaps_dir` set:

1. **Band and sight.** At a pull, `brain_decision` `reposition` (`trigger bad_position`) when the mage is closer than 19 yd or
   farther than 29 yd from the tank's mob, in its front or out of its sight. After the step, `units` must show the mob 20-28 yd away,
   and the next `frostbolt` must not be refused with `line_of_sight`. Note every `spell_fail` `line_of_sight` while the brain
   believed it had sight: each is a place where the mesh ray and the server disagree (report map, x, y, z of both ends). A `line_of_sight` refusal must be followed by a `reposition` (the fight's mob counts as blocked for 5 s), not by the same
   cast again after 3 s.
2. **No walk against the driver.** In a dungeon run (`no_move`) there is no `reposition`, `step_away`, `run_to_tank` or
   `leave_ground_aoe` decision. By hand, with `brain_status.move_held` true (once the driver holds the lock), none either.
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
8. **Healer.** `brain_start {"strategies":["role_healer"]}` and `{"role":"healer",...}` are refused with `not implemented yet`.

