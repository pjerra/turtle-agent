#!/usr/bin/env bash
# Export the creature and game object spawns and the entrances of one map from the Tortoise world database to the
# three TSV files that dungeongen reads:   export.sh <map> <outdir>   (writes <outdir>/creatures.tsv, gameobjects.tsv, entrances.tsv, portals.tsv and movement.tsv)
#
# The database is the one in the docker stack on the server host. SELECTs only: the session is opened with
# SET SESSION TRANSACTION READ ONLY and every statement below is a SELECT. No credential is read, printed or stored here:
# the password is expanded inside the container from the container's own environment (MARIADB_ROOT_PASSWORD).
#
# Environment:  DUNGEONGEN_HOST      ssh host that runs docker (default m910q; "local" runs docker here)
#               DUNGEONGEN_CONTAINER database container (default tortoise-db)
#               DUNGEONGEN_DB        world database (default tw_world)
set -euo pipefail
[ $# -eq 2 ] || { sed -n '2,3p' "$0"; exit 2; }
map=$1 out=$2
[[ $map =~ ^[0-9]+$ ]] || { echo "map must be a number" >&2; exit 2; }
host=${DUNGEONGEN_HOST:-m910q} ctr=${DUNGEONGEN_CONTAINER:-tortoise-db} db=${DUNGEONGEN_DB:-tw_world}
mkdir -p "$out"

query() { # reads one SELECT on stdin, prints tab-separated rows without a header
  local remote="docker exec -i $ctr sh -c 'MYSQL_PWD=\"\$MARIADB_ROOT_PASSWORD\" exec mariadb -uroot -N -B --init-command=\"SET SESSION TRANSACTION READ ONLY\" $db'"
  if [ "$host" = local ]; then bash -c "$remote"; else ssh -o LogLevel=ERROR "$host" "$remote"; fi
}

{
  printf 'guid\tentry\tname\tlevel_min\tlevel_max\trank\tfaction\tnpc_flags\tx\ty\tz\twander\tmovement_type\tgroup_leader\tpatrol\tauras\n'
  query <<SQL
SELECT c.guid, c.id, t.name, t.level_min, t.level_max, t.rank, t.faction, t.npc_flags,
       c.position_x, c.position_y, c.position_z, c.wander_distance, c.movement_type,
       COALESCE((SELECT g.leader_guid FROM creature_groups g WHERE g.member_guid = c.guid LIMIT 1),
                (SELECT g.leader_guid FROM creature_groups g WHERE g.leader_guid = c.guid LIMIT 1), 0),
       EXISTS(SELECT 1 FROM creature_movement m WHERE m.id = c.guid),
       COALESCE(CONCAT_WS(' ', NULLIF(t.auras, ''), NULLIF(a.auras, '')), '')
FROM creature c JOIN creature_template t ON t.entry = c.id
LEFT JOIN creature_addon a ON a.guid = c.guid
WHERE c.map = $map ORDER BY c.guid;
SQL
} > "$out/creatures.tsv"

# A locked object's key item is in Lock.dbc, not in this database, so gameobjects.tsv has no key_item/key_name columns here.
# dungeongen reads them when someone adds them (the item a non-GM needs, e.g. 7733 "Staff of Prehistoria" for the Uldaman Keystone)
# and notes them on the leg; the plan's use_object step then carries requires_item.
{
  printf 'guid\tentry\tname\ttype\tflags\tx\ty\tz\tstate\n'
  query <<SQL
SELECT g.guid, g.id, t.name, t.type, t.flags, g.position_x, g.position_y, g.position_z, g.state
FROM gameobject g JOIN gameobject_template t ON t.entry = g.id
WHERE g.map = $map ORDER BY g.guid;
SQL
} > "$out/gameobjects.tsv"

# Areatriggers that enter or leave the map (the entrance position).
{
  printf 'id\tname\ttarget_map\tx\ty\tz\to\n'
  query <<SQL
SELECT id, name, target_map, target_position_x, target_position_y, target_position_z, target_orientation
FROM areatrigger_teleport WHERE target_map = $map ORDER BY id;
SQL
} > "$out/entrances.tsv"
# The areatriggers of the map's entrances with their own position on the outer map (areatrigger_template): the plan's portal.
{
  printf 'target_map\tid\tname\trequired_level\touter_map\tx\ty\tz\tradius\tbox_x\tbox_y\tbox_z\ttarget_x\ttarget_y\ttarget_z\n'
  query <<SQL
SELECT t.target_map, t.id, t.name, t.required_level, a.map_id, a.x, a.y, a.z, a.radius, a.box_x, a.box_y, a.box_z,
       t.target_position_x, t.target_position_y, t.target_position_z
FROM areatrigger_teleport t LEFT JOIN areatrigger_template a ON a.id = t.id
WHERE t.target_map = $map ORDER BY t.id;
SQL
} > "$out/portals.tsv"
# The waypoints of the map's patrolling spawns (creature_movement, movement type 2): a plan's patrol paths are copied from here.
{
  printf 'guid\tpoint\tx\ty\tz\twaittime\n'
  query <<SQL
SELECT m.id, m.point, m.position_x, m.position_y, m.position_z, m.waittime
FROM creature_movement m JOIN creature c ON c.guid = m.id
WHERE c.map = $map ORDER BY m.id, m.point;
SQL
} > "$out/movement.tsv"
wc -l "$out"/*.tsv
