package scenario

// allowedTables is the read-only allowlist (spec S2): a scenario cannot widen
// it; a change is a code change with review. Nothing from tw_logon (account
// hashes), tw_logs, mysql, information_schema, performance_schema or sys.
var allowedTables = map[string]bool{
	"tw_world.lft_user_groups":      true,
	"tw_world.ai_dungeon_plan":      true,
	"tw_world.ai_dungeon_leg":       true,
	"tw_world.ai_dungeon_step":      true,
	"tw_world.ai_boss_tactic":       true,
	"tw_world.migrations":           true,
	"tw_char.characters":            true,
	"tw_char.character_inventory":   true,
	"tw_char.item_instance":         true,
	"tw_char.ai_run_given_item":     true,
	"tw_char.groups":                true,
	"tw_char.group_member":          true,
	"tw_char.character_queststatus": true,
	"tw_char.mail":                  true,
	"tw_char.mail_items":            true,
}

func allowedTable(table string) bool { return allowedTables[table] }

// DBQuery is one read-only count: either a table row count or an item count.
// Built by the runner from a check_db step, rendered by BuildSQL below.
type DBQuery struct {
	Table string
	Where map[string]any
	Op    string
	Want  int64
	Item  *ItemQuery
}
