package dungeon

// DiffPlans is the JSON patch that turns plan a into plan b: the op list a live reload records (plan_reload), here for the route editor's
// record of an edit.
func DiffPlans(a, b *Plan) []PatchOp { return planDiff(a, b) }
