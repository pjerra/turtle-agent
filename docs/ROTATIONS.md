# Rotations: the file format and how to A/B one

A rotation is the priority list of one class and spec for the agent's own character, as a JSON file the bot brain plays instead of the Go
strategy it replaces. The full reference is README "Rotations as data"; this is the short version, and the recipe to test a change.

## The format (version 1)

```json
{"version": 1, "name": "my_frost", "class": "mage", "spec": "frost", "replaces": ["mage_frost"],
 "notes": "what it plays and why", "sources": ["where the idea comes from"],
 "priorities": [
  {"id": "frost_nova", "spell": "Frost Nova", "level": 10, "target": "self", "band": "high", "threat": "aoe",
   "when": {"melee_mob": true, "no_mob_aura": ["Frost Nova"]}},
  {"id": "fire_blast", "spell": "Fire Blast", "level": 6, "target": "melee_mob", "when": {"melee_mob": true}},
  {"id": "frostbolt", "spell": "Frostbolt", "level": 4, "target": "fight"}]}
```

- `replaces`: the strategies of the preset this takes the place of (`mage_frost`; a warrior's are `warrior_melee` + `warrior_arms`). The preset keeps
  everything else: buffs, drinking, positioning, interrupts, consumables.
- `priorities` are read **top to bottom**; an earlier entry beats a later one of the same `band` (`low`, `normal`, `high`, `move`, `emergency`). The engine
  casts the most relevant entry that can run now (spell known, off cooldown, enough power, in range, the `when` true), so the list is "the first
  that works".
- An entry: `id` (its name in `brain_decision` events), `spell` (a name; the highest known rank is cast) or `spell_id`, `level` (the level of the first
  rank, checked against the Turtle spell data; `talent: true` for a talent), `target` (`fight`, `melee_mob`, `self`), `kind` (`cast`, `attack`, `next_swing`,
  `stance`), `threat`, `alt`, and `when`.
- `when` is a closed set of conditions, all of which must hold: state (`combat`, `rest`, `any`), mobs and distances, health and power percents, auras on the
  character or the mob (`self_aura`, `no_self_aura`, `self_aura_missing`, `mob_aura`, `no_mob_aura`, `mob_stacks_below`, `mobs_lacking_aura`), `moving`,
  `stance`, `after` a dodge or an avoided hit, `combo_points_min`. The full list is in the README. An unknown field is an error.

Built in: `frost_mage`, `fire_mage`, `arcane_mage`, `shadow_priest`, `arms_warrior`, `fury_warrior`, `tank_warrior` (`turtle-agent rotation list`). Put your own
files in a folder and name it `rotations_dir` in the settings; names must be new (a built-in name is refused).

## Check a file

```sh
turtle-agent rotation check my_frost.json         # spell and aura names, first-rank levels, ids, bands, conditions
turtle-agent rotation show --level 30 my_frost    # the list, with the rank cast at level 30
```

## A/B one rotation against another

The test is the leader-mode pull test: the party kills the same pack three or more times with the agent's character playing arm A, then arm B.

1. **Pick a pull** that is cheap and repeatable: one pull group of one leg (Uldaman leg 0, `0:0`), with a party of hired or named bots in the
   instance, the character standing at the pack. For a reset between tries use packs of the same kind marked with the plan's `equiv` label, or a GM account
   of developer level (`.respawn`; refused when a boss is within 250 yd and not alive).
2. **Run arm A**, 3 or more tries (10 at most):
   `tools/run-uldaman.sh --mode leader --pulltest 0:0:5 --rotation frost_mage` (A, the built-in file) and keep its `pulltest.json`. For arm B change only
   the rotation: `--rotation my_frost`. Leave the brain preset, the role, the party, the pull and the character as they are. To test a file against the
   Go strategy it replaces, run one arm with no `--rotation`.
3. **Compare:** `turtle-agent pulltest --compare a.json b.json`. It prints which brain and rotation digest each side played, then the party's numbers (time to
   kill, deaths, rest) and the character's: damage and healing per second and per power, actions per try, casts the server refused by reason, idle
   time in combat, mobs taken off the tank, deaths, the lowest power. Each row says better, worse or same.
4. **Read it with care.** It warns for fewer than 3 scored tries and when the two are not the same pull. Run one arm twice to see the spread; a difference
   inside it is noise. A try with a death or a wipe is in the sums, not in the means. Keep a rotation only if it is better on the rows you care about and
   nothing is worse; write down the result either way.

What the numbers are (and are not): damage and healing come from the combat log with the character as the source (a pet's or a totem's damage is not
the character's); power spent is the sum of the drops between brain ticks, so regeneration hides a little; idle is "in combat, nothing running, not
casting or walking", which also counts the wait for the global cooldown. They compare two arms on the same pull; they are not absolute.

Not checked live yet: see `docs/LIVE-CHECKS.md`, "Run modes, rotations as data and boss tactics", item 7.
