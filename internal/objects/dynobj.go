package objects

import (
	"math"
	"sort"

	"github.com/pjerra/turtle-agent/internal/packet"
)

// HighDynamicObject is the high word of a dynamic object's guid (HIGHGUID_DYNAMICOBJECT).
const HighDynamicObject uint16 = 0xF100

// DynamicObject is the area a ground spell leaves: Blizzard, Rain of Fire, a consecration, a fire patch under a boss. The
// server sends it as an object of its own with the spell, the caster and the radius.
type DynamicObject struct {
	GUID   packet.GUID `json:"guid"`
	Spell  uint32      `json:"spell_id"`
	Caster packet.GUID `json:"caster,omitempty"`
	Radius float32     `json:"radius"`
	X      float32     `json:"x"`
	Y      float32     `json:"y"`
	Z      float32     `json:"z"`
	// Distance is the flat distance from the player to the object's centre, -1 without a position.
	Distance float32 `json:"distance"`
}

func isDynamicObject(o *object) bool {
	if o.typ == TypeDynamic {
		return true
	}
	return o.typ == 0 && o.guid.High() == HighDynamicObject // values seen before a create block
}

func floatAt(v map[uint16]uint32, i uint16) float32 { return math.Float32frombits(v[i]) }

// DynamicObjects lists the dynamic objects the store holds, nearest first (ox, oy are the player's position). The position is the
// create block's, else the object's own position fields.
func (s *Store) DynamicObjects(ox, oy float32) []DynamicObject {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []DynamicObject
	for _, o := range s.objs {
		if !isDynamicObject(o) {
			continue
		}
		d := DynamicObject{GUID: o.guid, Spell: o.values[DynSpell], Caster: guidAt(o.values, DynCaster),
			Radius: floatAt(o.values, DynRadius), Distance: -1}
		switch {
		case !o.posAt.IsZero():
			d.X, d.Y, d.Z = o.x, o.y, o.z
		case o.values[DynPosX] != 0 || o.values[DynPosX+1] != 0:
			d.X, d.Y, d.Z = floatAt(o.values, DynPosX), floatAt(o.values, DynPosX+1), floatAt(o.values, DynPosX+2)
		default:
			out = append(out, d)
			continue
		}
		d.Distance = float32(math.Hypot(float64(d.X-ox), float64(d.Y-oy)))
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].Distance < 0) != (out[j].Distance < 0) {
			return out[j].Distance < 0
		}
		if out[i].Distance != out[j].Distance {
			return out[i].Distance < out[j].Distance
		}
		return out[i].GUID < out[j].GUID
	})
	return out
}
