# Dungeon progress

Every instance goes through three steps. Tracker: [issue #198](https://github.com/pjerra/turtle-agent/issues/198).

1. **Mapped:** the plan is written from the server's world database, its instance script and the guides. The plan check and the navmesh test pass.
2. **Walked:** a GM walks every plan point in game, invisible, so positions, nearby packs and patrol times are settled. The plan status becomes `mapped`.
3. **Cleared by bots:** a bot party led by the run leader kills every boss in one run (3 groups per test). The plan status becomes `live-checked`.

All 48 instances have a generated draft plan; a draft counts as none of the three steps. Hold (owner, 2026-10-15): only Ragefire Chasm, Wailing Caverns and the Deadmines are being worked on besides Uldaman and Zul'Farrak. Raids are blocked until raid mode exists (TortoiseBots #938/#900: groups are capped at 5).

Levels are the plan file's level band; for classic instances the Wowhead range is in brackets next to the link. Wowhead lists 19 dungeons and 7 raids on its [Classic instances page](https://www.wowhead.com/classic/zones/instances); several of them split into more than one plan (Scarlet Monastery 4, Maraudon 2, Dire Maul 3, Stratholme 2, Blackrock Spire 2). Every Wowhead instance has a plan. The plan statuses below are as of the PRs listed; some plans are still on open PR branches, so their file on `dev` still reads draft.

The Uldaman and Zul'Farrak plan files carry the status `live-checked` (a live-checked plan); neither has had a full clear of every boss in one run yet, so Cleared stays open.

Legend: ✓ done, — not done. PR numbers are in the `pjerra/turtle-agent` repository.

## Done or in progress

| Instance | Levels | Wowhead | Plan file | Mapped | Walked | Cleared | PR | What's left |
|---|---|---|---|---|---|---|---|---|
| Uldaman | 41-45 | [zone 1337](https://www.wowhead.com/classic/zone=1337) (35-52) | `dungeons/uldaman.json` (14 legs) | ✓ | ✓ | — | merged | Grimlok's section; a full clear (best run: 6 of 9 bosses) |
| Zul'Farrak | 44-48 | [zone 1176](https://www.wowhead.com/classic/zone=1176) (43-54) | `dungeons/zulfarrak.json` (12 legs) | ✓ | ✓ | — | #192, #195 merged; #197 open (end legs, absorbs #194 gossip) | A clear of every boss in one run (round 3: Zum'rah killed by all 3 groups, best 86 min, 0 wipes, all 28 graves; round 4 running) |
| Ragefire Chasm | 13-18 | [zone 2437](https://www.wowhead.com/classic/zone=2437) (13-22) | `dungeons/ragefire_chasm.json` (4 legs) | ✓ | ✓ | — | #196 | Taragaman, Jergosh, Bazzalan; a full clear (first live run: Oggleflint killed, stopped by a walk-step-under-the-floor bug on the tunnel slope, fix in progress) |
| Wailing Caverns | 18-23 | [zone 718](https://www.wowhead.com/classic/zone=718) (15-28) | `dungeons/wailing_caverns.json` (7 legs) | ✓ | ✓ | — | #199 | A full clear; the Naralex escort event (record only). First live run in progress; the hop to the Serpentis ramp is crossed by a GM move (gm_cross) |
| The Deadmines | 17-22 | [zone 1581](https://www.wowhead.com/classic/zone=1581) (15-28) | `dungeons/deadmines.json` (7 legs) | ✓ | ✓ | — | #200 (draft) | The live run (checks the three catwalk Overseers); the scripted doors |

## Classic 5-man, by level

| Instance | Levels | Wowhead | Plan file | Mapped | Walked | Cleared | PR | What's left |
|---|---|---|---|---|---|---|---|---|
| Shadowfang Keep | 19-25 | [zone 209](https://www.wowhead.com/classic/zone=209) (18-32) | `dungeons/shadowfang_keep.json` (8 legs) | — | — | — | — | Not started (draft plan only) |
| Blackfathom Deeps | 22-27 | [zone 719](https://www.wowhead.com/classic/zone=719) (20-35) | `dungeons/blackfathom_deeps.json` (7 legs) | — | — | — | — | Not started (draft plan only) |
| The Stormwind Stockade | 23-28 | [zone 717](https://www.wowhead.com/classic/zone=717) (22-30) | `dungeons/stormwind_stockade.json` (5 legs) | — | — | — | — | Not started (draft plan only) |
| Razorfen Kraul | 26-31 | [zone 491](https://www.wowhead.com/classic/zone=491) (24-40) | `dungeons/razorfen_kraul.json` (7 legs) | — | — | — | — | Not started (draft plan only) |
| Gnomeregan | 27-33 | [zone 721](https://www.wowhead.com/classic/zone=721) (24-40) | `dungeons/gnomeregan.json` (5 legs) | — | — | — | — | Not started (draft plan only) |
| Scarlet Monastery: Graveyard | 30-35 | [zone 796](https://www.wowhead.com/classic/zone=796) (29-45) | `dungeons/scarlet_graveyard.json` (3 legs) | — | — | — | — | Not started (draft plan only) |
| Scarlet Monastery: Library | 33-38 | [zone 796](https://www.wowhead.com/classic/zone=796) (29-45) | `dungeons/scarlet_library.json` (2 legs) | — | — | — | — | Not started (draft plan only) |
| Scarlet Monastery: Armory | 35-40 | [zone 796](https://www.wowhead.com/classic/zone=796) (29-45) | `dungeons/scarlet_armory.json` (2 legs) | — | — | — | — | Not started (draft plan only) |
| Razorfen Downs | 35-40 | [zone 722](https://www.wowhead.com/classic/zone=722) (33-47) | `dungeons/razorfen_downs.json` (4 legs) | — | — | — | — | Not started (draft plan only) |
| Scarlet Monastery: Cathedral | 37-42 | [zone 796](https://www.wowhead.com/classic/zone=796) (29-45) | `dungeons/scarlet_cathedral.json` (3 legs) | — | — | — | — | Not started (draft plan only) |
| Maraudon (purple) | 44-49 | [zone 2100](https://www.wowhead.com/classic/zone=2100) (40-58) | `dungeons/maraudon_purple.json` (6 legs) | — | — | — | — | Not started (draft plan only) |
| Maraudon (orange) | 45-50 | [zone 2100](https://www.wowhead.com/classic/zone=2100) (40-58) | `dungeons/maraudon_orange.json` (6 legs) | — | — | — | — | Not started (draft plan only) |
| The Temple of Atal'Hakkar (Sunken Temple) | 47-52 | [zone 1477](https://www.wowhead.com/classic/zone=1477) (44-60) | `dungeons/sunken_temple.json` (6 legs) | — | — | — | — | Not started (draft plan only) |
| Blackrock Depths | 52-57 | [zone 1584](https://www.wowhead.com/classic/zone=1584) (48-60) | `dungeons/blackrock_depths.json` (10 legs) | — | — | — | — | Not started (draft plan only) |
| Dire Maul East | 53-58 | [zone 2557](https://www.wowhead.com/classic/zone=2557) (54-61) | `dungeons/dire_maul_east.json` (4 legs) | — | — | — | — | Not started (draft plan only) |
| Lower Blackrock Spire | 55-60 | [zone 1583](https://www.wowhead.com/classic/zone=1583) (52-61) | `dungeons/lower_blackrock_spire.json` (7 legs) | — | — | — | — | Not started (draft plan only) |
| Stratholme (live side) | 55-60 | [zone 2017](https://www.wowhead.com/classic/zone=2017) (56-61) | `dungeons/stratholme_live.json` (3 legs) | — | — | — | — | Not started (draft plan only) |
| Stratholme (undead side) | 56-60 | [zone 2017](https://www.wowhead.com/classic/zone=2017) (56-61) | `dungeons/stratholme_undead.json` (5 legs) | — | — | — | — | Not started (draft plan only) |
| Dire Maul West | 57-60 | [zone 2557](https://www.wowhead.com/classic/zone=2557) (54-61) | `dungeons/dire_maul_west.json` (5 legs) | — | — | — | — | Not started (draft plan only) |
| Dire Maul North | 57-60 | [zone 2557](https://www.wowhead.com/classic/zone=2557) (54-61) | `dungeons/dire_maul_north.json` (5 legs) | — | — | — | — | Not started (draft plan only) |
| Scholomance | 57-60 | [zone 2057](https://www.wowhead.com/classic/zone=2057) (56-61) | `dungeons/scholomance.json` (10 legs) | — | — | — | — | Not started (draft plan only) |

## Turtle WoW 5-man, by level

These instances are custom to Turtle WoW and are not on Wowhead.

| Instance | Levels | Wowhead | Plan file | Mapped | Walked | Cleared | PR | What's left |
|---|---|---|---|---|---|---|---|---|
| Frostmane Hollow | 13-18 | — | `dungeons/frostmane_hollow.json` (4 legs) | — | — | — | — | Not started (draft plan only) |
| Windhorn Canyon | 24-29 | — | `dungeons/windhorn_canyon.json` (7 legs) | — | — | — | — | Not started (draft plan only) |
| Dragonmaw Retreat | 26-31 | — | `dungeons/dragonmaw_retreat.json` (5 legs) | — | — | — | — | Not started (draft plan only) |
| Crescent Grove | 31-36 | — | `dungeons/crescent_grove.json` (5 legs) | — | — | — | — | Not started (draft plan only) |
| Stormwrought Ruins | 33-38 | — | `dungeons/stormwrought_ruins.json` (11 legs) | — | — | — | — | Not started (draft plan only) |
| Gilneas City | 42-47 | — | `dungeons/gilneas_city.json` (7 legs) | — | — | — | — | Not started (draft plan only) |
| Hateforge Quarry | 50-55 | — | `dungeons/hateforge_quarry.json` (5 legs) | — | — | — | — | Not started (draft plan only) |
| Stormwind Vault | 59-60 | — | `dungeons/stormwind_vault.json` (6 legs) | — | — | — | — | Not started (draft plan only) |
| Karazhan Crypt | 59-60 | — | `dungeons/karazhan_crypt.json` (6 legs) | — | — | — | — | Not started (draft plan only) |
| Black Morass | 59-60 | — | `dungeons/black_morass.json` (4 legs) | — | — | — | — | Not started (draft plan only) |

## Raids

Blocked until raid mode exists. Upper Blackrock Spire is a classic 10-man and is kept here with the raids; the last four are Turtle WoW raids.

| Instance | Levels | Wowhead | Plan file | Mapped | Walked | Cleared | PR | What's left |
|---|---|---|---|---|---|---|---|---|
| Upper Blackrock Spire | 58-60 | [zone 1583](https://www.wowhead.com/classic/zone=1583) (52-61) | `dungeons/upper_blackrock_spire.json` (5 legs) | — | — | — | — | Not started (draft plan only) |
| Molten Core | 60-60 | [zone 2717](https://www.wowhead.com/classic/zone=2717) (60) | `dungeons/molten_core.json` (11 legs) | — | — | — | — | Not started (draft plan only) |
| Onyxia's Lair | 60-60 | [zone 2159](https://www.wowhead.com/classic/zone=2159) (60) | `dungeons/onyxias_lair.json` (1 legs) | — | — | — | — | Not started (draft plan only) |
| Blackwing Lair | 58-60 | [zone 2677](https://www.wowhead.com/classic/zone=2677) (60) | `dungeons/blackwing_lair.json` (8 legs) | — | — | — | — | Not started (draft plan only) |
| Zul'Gurub | 58-60 | [zone 1977](https://www.wowhead.com/classic/zone=1977) (60) | `dungeons/zul_gurub.json` (8 legs) | — | — | — | — | Not started (draft plan only) |
| Ruins of Ahn'Qiraj | 59-60 | [zone 3429](https://www.wowhead.com/classic/zone=3429) (60) | `dungeons/ruins_of_ahnqiraj.json` (6 legs) | — | — | — | — | Not started (draft plan only) |
| Temple of Ahn'Qiraj | 58-60 | [zone 3428](https://www.wowhead.com/classic/zone=3428) (60) | `dungeons/temple_of_ahnqiraj.json` (8 legs) | — | — | — | — | Not started (draft plan only) |
| Naxxramas | 59-60 | [zone 3456](https://www.wowhead.com/classic/zone=3456) (60) | `dungeons/naxxramas.json` (15 legs) | — | — | — | — | Not started (draft plan only) |
| Emerald Sanctum | 60-60 | — | `dungeons/emerald_sanctum.json` (2 legs) | — | — | — | — | Not started (draft plan only) |
| Lower Karazhan Halls | 59-60 | — | `dungeons/lower_karazhan_halls.json` (4 legs) | — | — | — | — | Not started (draft plan only) |
| Upper Karazhan Halls | 60-60 | — | `dungeons/upper_karazhan_halls.json` (8 legs) | — | — | — | — | Not started (draft plan only) |
| Timbermaw Hold | 60-60 | — | `dungeons/timbermaw_hold.json` (6 legs) | — | — | — | — | Not started (draft plan only) |
