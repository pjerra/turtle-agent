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

## Format additions for rotations from the knowledge base

These fields are part of version 1, are all optional, and are checked by `rotation check`. They exist so that a rotation written from a
spec guide can say what the guide says (the KB rotations themselves come later, from the recipes of `tools/kbrotgen`).

- **Level bands:** `when.level_min` and `when.level_max` (a character level, 1-60; `level_min` <= `level_max`) limit an entry to a range of levels.
  Advice that only holds while leveling ("Sunder before Heroic Strike at leveling rage") is an entry with `level_max`.
- **Mobs near the fight's mob:** `when.mobs_near_mob_min: N` needs at least N hostile mobs in combat within 8 yd of the fight's mob (the fight's
  mob included). It is the ranged classes' "3+ targets" for an AoE row; a melee row uses `mobs_in_reach_min`.
- **Healing targets:** `target: "heal"` casts on the member the healer's triage picked this tick (`HealPlan`: the party member the healer role would
  heal, counting heals in flight, the tank's bonus included). The entry cannot run when no member needs a heal or healing is not allowed now. A
  heal entry is never harmful and has no threat; it is cast through the healer's cast path, so the heal memory (the in-flight count) sees it.
  Conditions that look at that member are valid only with this target: `heal_hp_below` (percent), `heal_is_tank` (bool) and `heal_no_aura` (none
  of these auras on the member, e.g. `Renew`); `members_hurt_min` (members in reach below the heal threshold) works with any target.
  Without `when.state` a heal entry runs in and out of combat (the triage itself decides), not only while a mob is fought.
- **One rank:** `rank: N` (1 or above) casts exactly rank N of the spell's name, if the spellbook knows that rank; it never falls back to the
  highest rank (a guide that says "Renew (rank 1)" means rank 1). The rank must exist for that name in the spell data.
- **Add mode:** `"mode": "add"` with an empty `replaces`: the file replaces nothing and its strategy is inserted right after `role_healer`. It is
  for a healing class's extra casts (a guide's tank upkeep, an extra heal), whose entries in band `high` pre-empt the generic heal. The class
  must have a heal kit, and the preset must have `role_healer`; anything else is refused.
- **Where the file came from:** the optional top-level `kb` block names the guide file, its sha256, the preset, the recipe and the rows the
  rotation does not express (`{"row", "text", "why"}` each). `rotation show` prints `kb: <file>, N rows not expressed (see the file's
  kb.unexpressed)`; `preset` must be a known preset.

```json
{"version": 1, "name": "kb_holy_priest_extra", "class": "priest", "mode": "add", "replaces": [],
 "priorities": [
  {"id": "renew_tank", "spell": "Renew", "level": 8, "rank": 1, "target": "heal", "band": "high",
   "when": {"heal_is_tank": true, "heal_no_aura": ["Renew"]}}],
 "kb": {"file": "classes/yaml/priest/holy-healer.yaml", "sha256": "…", "preset": "holy_priest",
        "recipe": "tools/kbrotgen/recipes/kb_holy_priest.json",
        "unexpressed": [{"row": "rotation_single[1]", "text": "…", "why": "pre-pull/opener outside the brain's fight"}]}}
```

## Rotations from the Turtle knowledge base

The knowledge-base rotations are one file per PvE spec, generated from the [Turtle WoW knowledge base
(stevemcqueenz)](https://stevemcqueenz.github.io/turtle-knowledge-base/#/) and named `kb_<spec>_<class>`: 31 files, one for every
`<class>/<spec>-<role>.yaml` of the KB (`*-tank`, `*-healer`, `*-melee-dps`, `*-ranged-dps`), in `internal/brain/rotations/`
and listed by `turtle-agent rotation list`. What is not converted: `*-pvp.yaml` (the agent plays PvE) and `gear.yaml`, and the
KB's rogue `combat-tank` (rogues have no taunt in the core, so `role_tank` cannot hold a mob: not supported).

Each file's `kb` block names the guide file and its sha256, the preset it plays in and the recipe it came from; its `sources`
credit the guide and the forum and Discord posts that guide file lists. The rows the rotation cannot express are in
`kb.unexpressed` with the reason, never dropped silently.

### The recipe and the generator

- A **recipe** `tools/kbrotgen/recipes/<name>.json` is the human judgment: one row per element of the KB file's
  `rotation_single`, `rotation_aoe` and `cooldowns`, each mapping to zero or more rotation entries or to an `unexpressed`
  reason; then the final `order`, and the `oracle` lists (`core`, `situational`, `never`, `signature`, `taunt`) for the spell
  oracle. The `text_sha` of a recipe row pins the KB text it was written for.
- The **generator** `tools/kbrotgen/kbrotgen.py` (Python 3 with PyYAML, run as `python3 -I`; it is not part of
  `make test`) reads the KB YAML and the recipes and fails, naming every problem, when the KB text changed, a KB row has no
  recipe row or a recipe row no KB row, the `order` names an id the recipe does not define (or a doubled one), an entry id is
  in no `order`, or there are more than 40 entries. It writes the rotation JSON (in the Go struct's key order) and
  `internal/kbspells/oracle/<class>.json`, byte-identical on every run.
- Commands: `make kb-rotations` (`KB` defaults to `~/TortoiseBots-audit/turtle-kb`), `python3 -I
  tools/kbrotgen/kbrotgen.py --kb <dir> --only kb_protection_warrior` for one recipe, and `--check`, which regenerates in memory
  and exits 1 with the files that differ.
- The Go tests hold the committed files to the recipes: `TestKBRotationsMatchRecipes` (entries and order equal the recipe,
  `kb.unexpressed` equal its rows), `TestKBRotationsResolveInTheirPreset` (every `kb_*` rotation is playable in the preset its
  recipe names, with no entry id clashing with another active strategy), `TestEveryKBSpecHasARotation` (the 31 specs, each
  embedded with its `kb` block), and a play test per class. `turtle-agent rotation check internal/brain/rotations/kb_*.json`
  checks the spell names and first ranks against the Turtle spell data.

### The format additions they use

The KB files use the format additions above, all still version 1 and all optional: level bands (`level_min`/`level_max`),
`mobs_near_mob_min` for a ranged "3+ targets" row, `target: "heal"` with `heal_hp_below`/`heal_is_tank`/`heal_no_aura`
(healer rotations), `members_hurt_min`, `rank` for a guide's deliberate low rank ("Renew (rank 1)"), and `mode: "add"` for the
four healer files (holy priest, holy paladin, restoration druid, restoration shaman), whose entries play right after
`role_healer` instead of replacing a strategy.

### Presets and the A/B arms

A rotation plays in the preset its `kb.preset` names. Specs that had no preset of their own got one that is the sibling's list
with the KB rotation in place of the class strategy: `protection_paladin` (retribution paladin with the tank role),
`discipline_priest`, `tank_shaman` (enhancement with the tank role and `Earthshaker Slam` as its taunt), `beast_mastery_hunter`,
`survival_hunter` (which also stands in melee), `assassination_rogue`, `subtlety_rogue`, `demonology_warlock`,
`destruction_fire_warlock`, `destruction_shadow_warlock`. The warrior `fury_tank` plays on `tank_warrior`; the other specs use
their class's existing preset (`arms_warrior`, `fury_warrior`, `tank_warrior`, `combat_rogue`, `frost_mage`, `fire_mage`,
`arcane_mage`, `affliction_warlock`, `marksman_hunter`, `shadow_priest`, `holy_priest`, `ret_paladin`, `holy_paladin`,
`balance_druid`, `feral_cat_druid`, `bear_tank_druid`, `resto_druid`, `elemental_shaman`, `enhancement_shaman`, `resto_shaman`).

The A/B test is the pull test below, in the same preset with only the rotation changed: arm A the preset with no `--rotation`
(the class strategy in Go), arm B the same preset with `--rotation kb_<spec>_<class>`; three or more tries each, then
`turtle-agent pulltest --compare`. When the spec's list belongs to another role than the Go strategy (`tank_shaman`,
`protection_paladin`, `survival_hunter`), the spec's preset already names the KB rotation: arm B is that preset as it stands and
arm A is the sibling it was cloned from. Which `kb_*` file becomes a preset's default is decided by these tests, not by this
work; the seven older files stay as they are.

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
