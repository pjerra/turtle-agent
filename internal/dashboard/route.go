package dashboard

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

// The real route (README "Route editor"): the path the bots walk, as the navmesh planner draws it, instead of the straight 2D stubs of the plan
// file's own points. GET /api/plan/<name>/route runs the plan check's walk (dungeon.TracePlan: the same planner, the same look point, the walk
// between legs, each pack's walk to its look point) and answers the stops in run order and the segments with their paths. A segment the planner
// cannot path is flagged with the reason. Nothing is written and nothing is sent to an agent: it is GET only, like the rest of the page.

// maxRouteEntries bounds the routes kept (a route is a few hundred kilobytes at the most). The oldest is dropped first.
const maxRouteEntries = 48

// routeErrorTTL is how long a failed trace (no navmesh files for the map, say) is remembered before it is tried again.
const routeErrorTTL = 20 * time.Second

// RouteStopView is a stop of the run in the drawing's plane.
type RouteStopView struct {
	Seq      int      `json:"seq"`
	Leg      int      `json:"leg"`
	Kind     string   `json:"kind"` // via, stage, pack, step, boss
	Group    int      `json:"group,omitempty"`
	X        float64  `json:"x"`
	Y        float64  `json:"y"`
	Label    string   `json:"label"` // what happens there, in words
	Failed   bool     `json:"failed,omitempty"`
	Issues   []string `json:"issues,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	Notes    []string `json:"notes,omitempty"`
}

// RouteSegView is the walk to a stop. Pts is the planner's path ([x, y] pairs in the drawing's plane); a segment with no path (OK false, Pts empty)
// is drawn as the chord from A to B.
type RouteSegView struct {
	Stop     int          `json:"stop"`
	Leg      int          `json:"leg"`
	Kind     string       `json:"kind"` // the stop's kind, or direct, gmcross, skip
	Part     string       `json:"part"`
	Link     bool         `json:"link,omitempty"` // the first walk of a leg: from the end of the leg before
	A        GeoXY        `json:"a"`
	B        GeoXY        `json:"b"`
	Pts      [][2]float64 `json:"pts,omitempty"`
	Len      float64      `json:"len"`
	OK       bool         `json:"ok"`
	Reason   string       `json:"reason,omitempty"`
	Detail   string       `json:"detail,omitempty"`
	Exempt   bool         `json:"exempt,omitempty"`
	Warnings []string     `json:"warnings,omitempty"`
	Text     string       `json:"text"` // the hover text
}

// RouteSummary counts what the route holds.
type RouteSummary struct {
	Stops    int     `json:"stops"`
	Segments int     `json:"segments"`
	Length   float64 `json:"length"` // yards over the segments the planner could path (and the straight hops)
	Failed   int     `json:"failed"` // segments the planner could not path
	Links    int     `json:"links"`  // segments that join a leg to the one before
	Warnings int     `json:"warnings"`
}

// RouteDoc is the answer of the route endpoint.
type RouteDoc struct {
	Plan    string          `json:"plan"`
	Map     uint32          `json:"map"`
	SHA256  string          `json:"sha256"` // of the plan the route is for
	Mmaps   string          `json:"mmaps"`
	Edited  bool            `json:"edited,omitempty"` // the plan is the file with the editor's ops applied
	Stops   []RouteStopView `json:"stops"`
	Segs    []RouteSegView  `json:"segments"`
	Summary RouteSummary    `json:"summary"`
	Millis  int64           `json:"computed_ms"`
}

// r1 is round1 without a negative zero (which JSON would write as -0).
func r1(v float64) float64 { return round1(v) + 0 }

// BuildRouteDoc turns a trace into the answer: the points in the drawing's plane, the creature names in the stops' words.
func BuildRouteDoc(plan *dungeon.Plan, tr *dungeon.RouteTrace, sha, mmaps string, edited bool) *RouteDoc {
	names := EntryNames(plan)
	doc := &RouteDoc{Plan: plan.Name, Map: plan.Map, SHA256: sha, Mmaps: mmaps, Edited: edited, Stops: []RouteStopView{}, Segs: []RouteSegView{}}
	labels := map[int]string{}
	for _, st := range tr.Stops {
		x, y := Project(st.At.X, st.At.Y)
		v := RouteStopView{Seq: st.Seq, Leg: st.Leg, Kind: st.Kind, Group: st.Group, X: r1(x), Y: r1(y),
			Failed: len(st.Issues) > 0, Issues: st.Issues, Warnings: st.Warnings, Notes: st.Notes}
		v.Label = runStopLabel(plan, st, names)
		labels[st.Seq] = v.Label
		doc.Stops = append(doc.Stops, v)
	}
	for _, sg := range tr.Segs {
		ax, ay := Project(sg.From.X, sg.From.Y)
		bx, by := Project(sg.To.X, sg.To.Y)
		v := RouteSegView{Stop: sg.Stop, Leg: sg.Leg, Kind: sg.Kind, Part: sg.Part, Link: sg.Link, A: GeoXY{r1(ax), r1(ay)}, B: GeoXY{r1(bx), r1(by)},
			Len: r1(float64(sg.Length)), OK: sg.OK, Reason: sg.Reason, Detail: sg.Detail, Exempt: sg.Exempt, Warnings: sg.Warnings}
		if sg.OK || len(sg.Path) >= 2 {
			for _, p := range sg.Path {
				px, py := Project(p.X, p.Y)
				v.Pts = append(v.Pts, [2]float64{r1(px), r1(py)})
			}
		}
		v.Text = segText(v, labels[sg.Stop])
		doc.Segs = append(doc.Segs, v)
		if sg.OK {
			doc.Summary.Length += float64(sg.Length)
		} else {
			doc.Summary.Failed++
		}
		if sg.Link {
			doc.Summary.Links++
		}
		doc.Summary.Warnings += len(sg.Warnings)
	}
	for _, st := range doc.Stops {
		doc.Summary.Warnings += len(st.Warnings)
		if st.Failed {
			doc.Summary.Failed++
		}
	}
	doc.Summary.Stops, doc.Summary.Segments = len(doc.Stops), len(doc.Segs)
	doc.Summary.Length = r1(doc.Summary.Length)
	return doc
}

// runStopLabel says what happens at a stop: "walk to waypoint 2 of Sentinel room", "pull pack 3: Relic Hunter, Geologist", "boss Revelosh".
func runStopLabel(plan *dungeon.Plan, st dungeon.RouteStop, names map[string]string) string {
	leg := "leg " + strconv.Itoa(st.Leg+1)
	if st.Leg >= 0 && st.Leg < len(plan.Legs) {
		leg = legLabel(plan.Legs[st.Leg].Name, st.Leg)
	}
	switch st.Kind {
	case "via":
		n := strings.TrimPrefix(st.Part, "via ")
		if i, err := strconv.Atoi(n); err == nil {
			n = strconv.Itoa(i + 1)
		}
		return fmt.Sprintf("walk to waypoint %s of %s", n, leg)
	case "stage":
		return "walk to the stage point of " + leg + " (the party gathers here before the packs)"
	case "pack":
		if st.Leg >= 0 && st.Leg < len(plan.Legs) && st.Group >= 1 && st.Group <= len(plan.Legs[st.Leg].PullGroups) {
			l := "pull " + packLabel(st.Group-1, plan.Legs[st.Leg].PullGroups[st.Group-1], names)
			if st.Team != "" {
				l += " (" + st.Team + " only: the other team's run looks, then skips)"
			}
			return l
		}
		return "pull pack " + strconv.Itoa(st.Group)
	case "boss":
		l := "boss"
		if st.Leg >= 0 && st.Leg < len(plan.Legs) && plan.Legs[st.Leg].Boss != nil {
			b := plan.Legs[st.Leg].Boss
			l = "boss " + strconv.FormatUint(uint64(b.Entry), 10)
			if n := names[strconv.FormatUint(uint64(b.Entry), 10)]; n != "" {
				l = "boss " + n
			}
		}
		if st.Team != "" {
			l += " (" + st.Team + " only)"
		}
		return l + " of " + leg
	case "step":
		if st.Text != "" {
			return "step: " + st.Text + " (" + leg + ")"
		}
		return "step (" + leg + ")"
	}
	return st.Part
}

// segText is the hover text of a segment: what it is, how long, and why it failed.
func segText(sg RouteSegView, label string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "stop %d: %s", sg.Stop, label)
	switch {
	case !sg.OK:
		fmt.Fprintf(&b, "\nCANNOT BE PATHED (%s): %s", sg.Reason, sg.Detail)
	case sg.Exempt:
		fmt.Fprintf(&b, "\nnot walked on the navmesh, the plan check skips it: %s", sg.Detail)
	case sg.Kind == "direct":
		fmt.Fprintf(&b, "\na direct step: a straight line, %.0f yd", sg.Len)
	default:
		fmt.Fprintf(&b, "\nnavmesh path, %.0f yd", sg.Len)
	}
	if sg.Link {
		b.WriteString("\nthe walk from the end of the leg before")
	}
	for _, w := range sg.Warnings {
		b.WriteString("\nwarning: " + w)
	}
	return b.String()
}

type routeEntry struct {
	done chan struct{}
	body []byte
	err  string
	at   time.Time
}

// routeService traces plans on one navmesh and keeps the answers by what they are for: the sha256 of the plan and the mmaps folder. A plan is
// traced once per version of its file (or of its edit); the same request again is a lookup.
type routeService struct {
	nav   dungeon.Navmesh
	mmaps string
	run   sync.Mutex // one trace at a time: a trace is a burst of planner calls, and a drag in the editor can ask many

	mu       sync.Mutex
	m        map[string]*routeEntry
	order    []string
	computes atomic.Int64 // traces made, for the tests
	now      func() time.Time
}

func newRouteService(nav dungeon.Navmesh, mmaps string) *routeService {
	return &routeService{nav: nav, mmaps: mmaps, m: map[string]*routeEntry{}, now: time.Now}
}

// get returns the answer for plan, whose bytes (or canonical form, for an edit) have sha256 sha. A request that arrives while the same trace is
// running waits for it.
func (rs *routeService) get(plan *dungeon.Plan, sha string, edited bool) ([]byte, string) {
	key := sha + "|" + rs.mmaps
	if edited {
		key = "edit:" + key
	}
	rs.mu.Lock()
	e := rs.m[key]
	if e == nil || (e.err != "" && rs.now().Sub(e.at) > routeErrorTTL && closed(e.done)) {
		e = &routeEntry{done: make(chan struct{})}
		rs.m[key] = e
		rs.order = append(dropKey(rs.order, key), key)
		for len(rs.order) > maxRouteEntries {
			delete(rs.m, rs.order[0])
			rs.order = rs.order[1:]
		}
		rs.mu.Unlock()
		rs.compute(e, plan, sha, edited)
		return e.body, e.err
	}
	rs.mu.Unlock()
	<-e.done
	return e.body, e.err
}

func closed(c chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}

func dropKey(s []string, k string) []string {
	out := s[:0]
	for _, x := range s {
		if x != k {
			out = append(out, x)
		}
	}
	return out
}

func (rs *routeService) compute(e *routeEntry, plan *dungeon.Plan, sha string, edited bool) {
	defer close(e.done)
	rs.run.Lock()
	defer rs.run.Unlock()
	rs.computes.Add(1)
	t0 := rs.now()
	tr, err := dungeon.TracePlan(plan, rs.nav)
	e.at = rs.now()
	if err != nil {
		e.err = firstLine(err.Error())
		return
	}
	doc := BuildRouteDoc(plan, tr, sha, rs.mmaps, edited)
	doc.Millis = rs.now().Sub(t0).Milliseconds()
	b, err := json.Marshal(doc)
	if err != nil {
		e.err = err.Error()
		return
	}
	e.body = b
}

// SetNavmesh gives the dashboard the navmesh of the server the plans are run on (--mmaps-dir): with it, /api/plan/<name>/route answers. Call it
// before serving.
func (p *Poller) SetNavmesh(nav dungeon.Navmesh, mmaps string) {
	p.route = newRouteService(nav, mmaps)
}

// planAndSource finds the plan the way the map draws it: the copy agent agentID's runs read when it is given and found, else the dashboard's.
func (p *Poller) planAndSource(name, agentID string) (*dungeon.Plan, PlanSource, string) {
	if agentID != "" {
		if a, ok := p.agentByID(agentID); ok {
			cp, plan, _, errText := p.plans.copyFor(a, name, p.runPlanOf(agentID))
			if plan == nil {
				return nil, PlanSource{}, errText
			}
			return plan, cp.Drawn, ""
		}
	}
	plan, _, src, errText := p.plans.lookup(p.plans.dirs, name, "dashboard")
	if plan == nil {
		return nil, PlanSource{}, errText
	}
	return plan, src, ""
}

// serveRoute answers GET /api/plan/<name>/route[?agent=ID].
func serveRoute(w http.ResponseWriter, r *http.Request, p *Poller, name string) {
	if !planName.MatchString(name) {
		jsonError(w, http.StatusNotFound, "not a plan name")
		return
	}
	rs := p.route
	if rs == nil {
		jsonError(w, http.StatusServiceUnavailable, "no navmesh: start the dashboard with --mmaps-dir (the navmesh folder of the server the plans are run on) to draw the real route")
		return
	}
	agent := ""
	if id := r.URL.Query().Get("agent"); agentID.MatchString(id) {
		agent = id
	}
	plan, src, errText := p.planAndSource(name, agent)
	if plan == nil {
		jsonError(w, http.StatusNotFound, "%s", errText)
		return
	}
	body, errText := rs.get(plan, src.SHA256, false)
	if body == nil {
		jsonError(w, http.StatusBadGateway, "the route could not be traced: %s", errText)
		return
	}
	writeJSON(w, body)
}

// canonicalSHA is the sha256 of a plan in its canonical form (an edited plan has no file).
func canonicalSHA(plan *dungeon.Plan) (string, error) {
	b, err := dungeon.Marshal(plan)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
