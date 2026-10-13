package mapview

import "github.com/pjerra/turtle-agent/internal/dungeon"

// Overlay is a dungeon plan drawn over the run: the walk from the entrance through the legs' waypoints, the
// positions of the pull groups, the bosses and the objects the plan uses.
type Overlay struct {
	Name   string
	Map    uint32
	Route  []Pos // the entrance, then each leg's To
	Legs   []OverlayLeg
	Groups []OverlayGroup
	Bosses []OverlayBoss
	Uses   []OverlayUse
}

// OverlayLeg is a leg's waypoint.
type OverlayLeg struct {
	Name string
	To   Pos
}

// OverlayGroup is a pull group: where its members stand.
type OverlayGroup struct {
	Leg       string
	Entries   []uint32
	Positions []Pos
	Ambush    bool
}

// OverlayBoss is a leg's boss.
type OverlayBoss struct {
	Leg   string
	Entry uint32
	Pos   Pos
}

// OverlayUse is a door, lever or altar the plan uses, at the spot the party stands (or the object's).
type OverlayUse struct {
	Leg   string
	Entry uint32
	Pos   Pos
}

func pos(p dungeon.Point) Pos { return Pos{p.X, p.Y, p.Z} }

// OverlayFromPlan reads a loaded plan.
func OverlayFromPlan(p *dungeon.Plan) *Overlay {
	o := &Overlay{Name: p.Name, Map: p.Map, Route: []Pos{pos(p.Entrance)}}
	for _, l := range p.Legs {
		o.Route = append(o.Route, pos(l.To))
		o.Legs = append(o.Legs, OverlayLeg{Name: l.Name, To: pos(l.To)})
		for _, g := range l.PullGroups {
			og := OverlayGroup{Leg: l.Name, Entries: g.Entries, Ambush: g.Ambush}
			for _, q := range g.Positions {
				og.Positions = append(og.Positions, pos(q))
			}
			o.Groups = append(o.Groups, og)
		}
		if l.Boss != nil {
			o.Bosses = append(o.Bosses, OverlayBoss{Leg: l.Name, Entry: l.Boss.Entry, Pos: pos(l.Boss.Position)})
		}
		for _, s := range l.Steps {
			if u := s.UseObject; u != nil {
				at := u.Stand
				if at == nil {
					at = u.Near
				}
				if at != nil {
					o.Uses = append(o.Uses, OverlayUse{Leg: l.Name, Entry: u.Entry, Pos: pos(*at)})
				}
			}
		}
	}
	return o
}

// Points lists every position of the overlay, for the image's bounds.
func (o *Overlay) Points() []Pos {
	if o == nil {
		return nil
	}
	pts := append([]Pos(nil), o.Route...)
	for _, g := range o.Groups {
		pts = append(pts, g.Positions...)
	}
	for _, b := range o.Bosses {
		pts = append(pts, b.Pos)
	}
	for _, u := range o.Uses {
		pts = append(pts, u.Pos)
	}
	return pts
}

// BossEntries lists the creature entries of the plan's bosses and the creatures it names to kill before them.
func BossEntries(p *dungeon.Plan) []uint32 {
	var out []uint32
	for _, l := range p.Legs {
		if l.Boss != nil {
			out = append(out, l.Boss.Entry)
			out = append(out, l.Boss.First...)
		}
	}
	return out
}
