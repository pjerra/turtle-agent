-- Dungeon plans for the run leader (.bot run), exported by turtle-agent (plan-export). Plan format v1; the mapping is
-- docs/guides/dungeon-run-leader.md ("From plan v1 to rows"). The plan file in turtle-agent is the single source: edit it
-- there and export again, never these rows by hand. The plan row carries the unit lists cc_priority, cc_always and kill_first
-- (columns from 20261015200000_world.sql). Never edit this file once applied; export a new one.
--
-- Idempotent: each plan's rows are deleted, then inserted. The tables come from 20261014150000_world.sql.

-- zul_gurub: map 309, 2 legs, 4 steps
DELETE FROM `ai_dungeon_step` WHERE `plan` = 'zul_gurub';
DELETE FROM `ai_dungeon_leg` WHERE `plan` = 'zul_gurub';
DELETE FROM `ai_dungeon_plan` WHERE `plan` = 'zul_gurub';
INSERT INTO `ai_dungeon_plan` (`plan`, `map`, `cc_priority`, `cc_always`, `kill_first`, `source`, `comment`) VALUES
('zul_gurub', 309, '', '', '14750,15146', 'turtle-agent placefb-test zul_gurub.json', 'fallback-note fixture');
INSERT INTO `ai_dungeon_leg` (`plan`, `leg`, `name`, `comment`) VALUES
('zul_gurub', 0, 'high_priestess_jeklik', ''),
('zul_gurub', 1, 'high_priest_venoxis', '');
INSERT INTO `ai_dungeon_step` (`plan`, `leg`, `seq`, `kind`, `entry`, `x`, `y`, `z`, `radius`, `item`, `param`, `flags`, `timeout_s`, `entries`, `comment`) VALUES
('zul_gurub', 0, 1, 'move', 0, -11916.6, -1243.5, 60, 0, 0, 0, 0, 0, '', ''),
('zul_gurub', 0, 2, 'pull', 0, -12000, -1500, 60, 10, 0, 0, 0, 0, '15111', ''),
('zul_gurub', 1, 3, 'pull', 0, -12029.8, -1707.9, 60, 10, 0, 0, 0, 0, '15111', ''),
('zul_gurub', 1, 4, 'boss', 11347, -12029.8, -1707.9, 60, 10, 0, 0, 0, 0, '11347', '');
