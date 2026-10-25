package dungeon

// DefaultLookPoint is where the lead bot stands to look at a pack at pack when it comes from `from` on a straight line, with the
// driver's default look (LookStandoff, the arrival slack): the point the plan check and the dashboard draw as "computed". The real
// walk follows the navmesh, so the real point can differ; ok false means the line is shorter than the standoff and the lead bot
// stays where it is.
func DefaultLookPoint(from, pack Point) (Point, bool) {
	return LookPoint([]Point{from, pack}, pack, LookStandoff, lookView(LookStandoff, lookArrive), nil, nil)
}

// DefaultGatherSpot is GatherSpot of the pull group `group` of leg `leg` over straight lines (no navmesh).
func (p *Plan) DefaultGatherSpot(leg, group int) Point {
	spot, _ := p.GatherSpot(leg, group, nil)
	return spot
}
