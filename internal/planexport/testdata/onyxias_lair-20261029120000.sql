-- Onyxia's Lair (map 249, the 40-man raid): the plan for the run leader (.bot run), exported from turtle-agent
-- prep/raid-ony 9097884 (dungeons/onyxias_lair.json, status 'draft (not mapped, not live-checked)') with
--   turtle-agent plan-export --dir dungeons --mmaps <map 249 mmaps> --allow-draft --gm-cross-direct \
--     --rev 9097884 --stamp 20261029120000 --out <this file> onyxias_lair
-- The plan, leg and step rows below are that export; this header, the ai_dungeon_boss block and the boss stand's
-- comment ("boss stand outside her ~62 yd 3D wake radius and the raid's follow ring": plan v1 has no comment on an
-- approach point) are the prep's own (~/TortoiseBots-audit/RAID-PREP-ony.md).
-- No --approach-hops: the 81 yd walk from the stand to her would be cut into move rows, the last of them 30.7 yd
-- from her and inside her ~62 yd 3D wake (plan-check exempts the boss's own walk from the wake rule), so the run
-- leader walks that hop in navmesh hops of its own on the boss step, stopping 30 yd short, and the party stands at
-- the stand until then.
--
-- What the prep changed from the dungeongen draft (1 leg, 5 pull groups, no approach points):
--   * the census of map 249 has 6 creature spawns and no other hostile unit: Onyxia 10184 (rank 3, level 63,
--     1,983,026 hp, script boss_onyxia) and five Onyxian Warders 12129 (rank 1, level 62, 216,478 hp each,
--     faction 103). No two warders stand within 15 yd (the closest pair is 35.4 yd apart), so the five pull groups
--     stay five single-unit packs (radius 1) and nothing is merged; there is no creature_linking, linking_template or
--     creature_groups row on the map.
--   * four warders are on waypoint patrols (creature_movement 52050 80.5 yd, 52049 110.5 yd, 52052 158.9 yd,
--     52048 98.3 yd; periods estimated at 2.5 yd/s: 32, 44, 64 and 39 s). The plan carries the four patrol blocks and
--     the rows do not: the run leader pulls the pack where it stands when it comes.
--   * one approach stand per pack (pack 2 on), 15-20 yd from it, and one for the boss at -80.0 -211.4 -83.0, 80.7 yd
--     out in 3D. Her wake is 58 yd (CheckForTargetsInAggroRadius) measured with both bounding radii added, so about
--     62 yd; the raid's follow ring puts its front bots 5-7 yd nearer than the lead, so a stand needs 75.1 yd
--     (58 + her bounding 3.6 + a bot 0.5 + the raid ring 8 + margin 5). The walk's first stand, 62.7 yd, was inside
--     it and walk 2 woke her with the raid still walking in. Every stand is on its own floor with a complete server
--     path from the row before it; the leg's `to` is the first pack's stand. The draft had 5 run-leader hops of
--     105-270 yd; this plan has one, the 81 yd walk from the stand to her, which no row can shorten.
--   * the boss step is Onyxia at her spawn (0.70 -211.97 -86.08) after all five warders, pulled from that stand,
--     15 yd east of x -95, where LeashIfOutOfCombatArea starts her evade.
--   * no use_on, wait_object, gossip or hold step: the map's 149 game objects are 95 Onyxia Eggs 176511, 52 lava
--     fissures and the two lava traps 177984/177985, all at state 1, the instance script has no door, and no object
--     starts the fight.
--
-- plan-check (turtle-agent plan-check --mmaps <map 249 mmaps>): ok, 12 points on the navmesh, 0 failures, 1 run-leader
-- hop notice (the 81 yd walk from the boss stand to her: a row may not stand nearer and outside the ~62 yd wake) and 4
-- warnings, all "the patrol of ... has only an estimated period_s": the mapping walk (B2) measures the rounds. The ROWS:
-- 1 leg, 29 steps (23 move, 5 pull, 1 boss), 31 rows with the plan and leg rows.
--
-- WHAT THE RUN LEADER CANNOT DO (nothing is dropped silently). Not in these rows:
--   * the patrol paths and periods (4 groups): no column; the run leader pulls the pack where it stands when it comes.
--   * the 81 yd hop from the boss stand to her: one long row hop the run leader walks in navmesh hops of its own (it sees
--     60 yd): a row may stand 80.7 yd out, outside her ~62 yd 3D wake, and no nearer.
--   * Onyxia's Aggro respawns every dead Onyxian Warder within 200 yd (boss_onyxia.cpp Aggro; the grid check in
--     GridNotifiers.h AllCreaturesOfEntryInRange is IsWithinDist(..., false), i.e. 2D), and the five stand 109.1,
--     127.0, 151.5, 168.1 and 199.8 yd from her in 2D: all five come back alive when she is pulled. No step kind
--     prevents it. BLOCKER: B2 watches what a boss pull after the clear spawns.
--   * the fight's phases (65 % health: airborne with Hover 17131; 40 %: lands) are health thresholds in
--     boss_onyxia.cpp, not steps, and no tactic row: the bots' own Onyxia fight strategy (OnyxiasLairDungeonStrategies.cpp)
--     handles the air phase (shoot, raid spread), the whelps (onyxia kill whelps, AiPlayerbot.OnyxiaWhelpsAndEggs=1 by
--     default), the egg field (onyxia avoid eggs) and Deep Breath (onyxia breath safe zone); the universal dragon
--     flank (DungeonTriggers.cpp IsRaidDragonEntry lists 10184) keeps non-tanks out of the front and tail cones.
--   * the whelps: Onyxia summons Onyxian Whelp 11262 (16, then 5-7 every 30 s in the air; one every 1-10 s on the
--     ground). The map has no GO_WHELP_SPAWNER 176510 spawn (the instance script's OnObjectCreate has nothing to fire
--     on), so every whelp is the boss's own summon.
--   * the entrance (trigger 2848: raid group, level 50, Drakefire Amulet condition 16309; landing 30.89 -54.08 -5.03),
--     level_band 60-60 and the raid composition 4/10/26: the run leader starts inside the instance and leads the bound
--     party as it is.
--   * Turtle 1.18.1's second section (Broodcommander Axelus 49018, Cindarion 50143, Onyxian Hatcher 50144, Inciter
--     49016, Flamespawn 49017): their creature_template rows exist, but no map 249 spawn, summon or script references
--     them in the effective world DB, and the core's boss_onyxia.cpp is the vanilla phase script. No leg is written for
--     them; B2 looks for the passage.
--

-- onyxias_lair: map 249, 1 legs, 29 steps
DELETE FROM `ai_dungeon_step` WHERE `plan` = 'onyxias_lair';
DELETE FROM `ai_dungeon_leg` WHERE `plan` = 'onyxias_lair';
DELETE FROM `ai_dungeon_plan` WHERE `plan` = 'onyxias_lair';
INSERT INTO `ai_dungeon_plan` (`plan`, `map`, `cc_priority`, `cc_always`, `kill_first`, `source`, `comment`) VALUES
('onyxias_lair', 249, '', '', '', 'turtle-agent 9097884 dungeons/onyxias_lair.json', 'plan v1, draft (not mapped, not live-checked)');
INSERT INTO `ai_dungeon_leg` (`plan`, `leg`, `name`, `comment`) VALUES
('onyxias_lair', 0, 'onyxia', '');
INSERT INTO `ai_dungeon_step` (`plan`, `leg`, `seq`, `kind`, `entry`, `x`, `y`, `z`, `radius`, `item`, `param`, `flags`, `timeout_s`, `entries`, `comment`) VALUES
('onyxias_lair', 0, 0, 'move', 0, 59.46, -103.12, -22.98, 0, 0, 0, 0, 0, '', 'to'),
('onyxias_lair', 0, 1, 'pull', 0, 49.82, -114.61, -24.31, 1, 0, 0, 0, 0, '12129', 'pull group 0'),
('onyxias_lair', 0, 2, 'move', 0, 48.799988, -101.06677, -23.947386, 0, 0, 0, 0, 0, '', 'path'),
('onyxias_lair', 0, 3, 'move', 0, 47.999985, -94.66677, -24.347387, 0, 0, 0, 0, 0, '', 'path'),
('onyxias_lair', 0, 4, 'move', 0, 47.199986, -92.26677, -25.047386, 0, 0, 0, 0, 0, '', 'path'),
('onyxias_lair', 0, 5, 'move', 0, 46.399986, -90.66677, -25.447386, 0, 0, 0, 0, 0, '', 'path'),
('onyxias_lair', 0, 6, 'move', 0, 42.666656, -85.33344, -25.947386, 0, 0, 0, 0, 0, '', 'path'),
('onyxias_lair', 0, 7, 'move', 0, 41.33332, -82.66677, -25.847387, 0, 0, 0, 0, 0, '', 'path'),
('onyxias_lair', 0, 8, 'move', 0, 39.99999, -82.13344, -25.547386, 0, 0, 0, 0, 0, '', 'path'),
('onyxias_lair', 0, 9, 'move', 0, 38.39999, -81.600105, -25.747387, 0, 0, 0, 0, 0, '', 'path'),
('onyxias_lair', 0, 10, 'move', 0, 28.533325, -78.40011, -28.547386, 0, 0, 0, 0, 0, '', 'path'),
('onyxias_lair', 0, 11, 'move', 0, 18.933329, -77.866776, -25.947386, 0, 0, 0, 0, 0, '', 'path'),
('onyxias_lair', 0, 12, 'move', 0, 14.666662, -78.13344, -25.247387, 0, 0, 0, 0, 0, '', 'path'),
('onyxias_lair', 0, 13, 'move', 0, 3.9999988, -81.600105, -24.947386, 0, 0, 0, 0, 0, '', 'path'),
('onyxias_lair', 0, 14, 'move', 0, -18.13346, -92.26677, -30.247387, 0, 0, 0, 0, 0, '', 'path'),
('onyxias_lair', 0, 15, 'move', 0, -33.05, -95.14, -36.06, 0, 0, 0, 0, 0, '', 'pull group 1 approach'),
('onyxias_lair', 0, 16, 'pull', 0, -52.97, -96.88, -38.64, 1, 0, 0, 0, 0, '12129', 'pull group 1'),
('onyxias_lair', 0, 17, 'move', 0, -82.40011, -106.13343, -35.247387, 0, 0, 0, 0, 0, '', 'path'),
('onyxias_lair', 0, 18, 'move', 0, -92.00011, -112.0001, -37.947388, 0, 0, 0, 0, 0, '', 'path'),
('onyxias_lair', 0, 19, 'move', 0, -113.88, -127.47, -48.98, 0, 0, 0, 0, 0, '', 'pull group 2 approach'),
('onyxias_lair', 0, 20, 'pull', 0, -131.2, -137.47, -52.52, 1, 0, 0, 0, 0, '12129', 'pull group 2'),
('onyxias_lair', 0, 21, 'move', 0, -163.15, -176.31, -63.21, 0, 0, 0, 0, 0, '', 'pull group 3 approach'),
('onyxias_lair', 0, 22, 'pull', 0, -166.62, -196, -66.26, 1, 0, 0, 0, 0, '12129', 'pull group 3'),
('onyxias_lair', 0, 23, 'move', 0, -184.95, -195.68, -67.15, 0, 0, 0, 0, 0, '', 'pull group 4 approach'),
('onyxias_lair', 0, 24, 'pull', 0, -199.1, -209.83, -68.91, 1, 0, 0, 0, 0, '12129', 'pull group 4'),
('onyxias_lair', 0, 25, 'move', 0, -149.46112, -210.48434, -74.62397, 0, 0, 0, 0, 0, '', 'path'),
('onyxias_lair', 0, 26, 'move', 0, -99.82225, -211.13869, -80.58664, 0, 0, 0, 0, 0, '', 'path'),
('onyxias_lair', 0, 27, 'move', 0, -80, -211.4, -83, 0, 0, 0, 0, 0, '', 'boss stand outside her ~62 yd 3D wake radius and the raid''s follow ring'),
('onyxias_lair', 0, 28, 'boss', 10184, 0.69628, -211.972, -86.075, 0, 0, 0, 0, 0, '', 'boss');

-- The one boss of the plan's boss step (map 249), for the boss rank and the dashboard: a DELETE of this map and
-- entry, then REPLACE.
DELETE FROM `ai_dungeon_boss` WHERE `map` IN (249) AND `entry` IN (10184);
REPLACE INTO `ai_dungeon_boss` (`map`, `entry`, `name`) VALUES
(249, 10184, 'Onyxia');
