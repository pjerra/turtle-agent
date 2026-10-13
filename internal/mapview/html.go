package mapview

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// replayData is what the replay's script draws from: positions already projected to the map area's pixels.
type replayData struct {
	Seconds   float64           `json:"seconds"`
	Start     string            `json:"start,omitempty"`
	Floors    int               `json:"floors"`
	Gap       float64           `json:"gap"`
	Tracks    []replayTrack     `json:"tracks"`
	Creatures []replayCreat     `json:"creatures"`
	Markers   []replayMarker    `json:"markers"`
	Colors    map[string]string `json:"colors"`
}

type replayTrack struct {
	Name   string       `json:"name"`
	Color  string       `json:"color"`
	Leader bool         `json:"leader,omitempty"`
	Pts    [][5]float64 `json:"pts"` // t, x, y, floor, break (0 none, 1 hole, 2 leap) before the point
}

type replayCreat struct {
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	F     int     `json:"f"`
	Name  string  `json:"name"`
	Boss  bool    `json:"boss,omitempty"`
	Seen  float64 `json:"seen"`
	FateT float64 `json:"fate_t"`
	Fate  Fate    `json:"fate"`
}

type replayMarker struct {
	Kind  string      `json:"kind"`
	T     float64     `json:"t"`
	X     float64     `json:"x"`
	Y     float64     `json:"y"`
	F     int         `json:"f"`
	Label string      `json:"label"`
	Flag  bool        `json:"flag,omitempty"`
	From  *[2]float64 `json:"from,omitempty"`
}

func r1(v float64) float64 { return math.Round(v*10) / 10 }

// data projects the scene for the replay script.
func (s *Scene) data() replayData {
	d := replayData{Seconds: r1(s.Run.Seconds), Floors: len(s.Floors), Gap: s.Cfg.GapAfter.Seconds(),
		Colors: map[string]string{string(FateKilled): fateColor(FateKilled), string(FateSkipped): fateColor(FateSkipped),
			string(FateEvaded): fateColor(FateEvaded), string(FateAlive): fateColor(FateAlive), "boss": colBoss,
			"teleport": colTeleport, "door": colDoor}}
	if !s.Run.Start.IsZero() {
		d.Start = s.Run.Start.UTC().Format("2006-01-02 15:04:05")
	}
	for i, tr := range s.Run.Tracks {
		rt := replayTrack{Name: tr.Name, Color: MemberColor(i), Leader: tr.Leader, Pts: [][5]float64{}}
		var prev *Sample
		for k := range tr.Samples {
			sm := tr.Samples[k]
			if sm.Map != s.Run.Map {
				prev = nil
				continue
			}
			brk := 0.0
			if prev != nil {
				switch {
				case dist2D(prev.Pos, sm.Pos) >= s.Cfg.TeleportMin:
					brk = 2
				case sm.T-prev.T > s.Cfg.GapAfter.Seconds():
					brk = 1
				}
			} else if len(rt.Pts) > 0 {
				brk = 1
			}
			x, y := s.px(sm.X, sm.Y)
			rt.Pts = append(rt.Pts, [5]float64{r1(sm.T), r1(x), r1(y), float64(s.FloorOf(sm.Z)), brk})
			prev = &tr.Samples[k]
		}
		d.Tracks = append(d.Tracks, rt)
	}
	for _, c := range s.Run.Creatures {
		x, y := s.px(c.X, c.Y)
		d.Creatures = append(d.Creatures, replayCreat{X: r1(x), Y: r1(y), F: s.FloorOf(c.Z), Name: creatureTitle(c),
			Boss: c.Boss, Seen: r1(c.Seen), FateT: r1(c.FateT), Fate: c.Fate})
	}
	for _, m := range s.Run.Markers {
		if m.Map != s.Run.Map {
			continue
		}
		x, y := s.px(m.X, m.Y)
		rm := replayMarker{Kind: m.Kind, T: r1(m.T), X: r1(x), Y: r1(y), F: s.FloorOf(m.Z), Label: m.Label, Flag: m.Flag}
		if m.From != nil {
			fx, fy := s.px(m.From.X, m.From.Y)
			rm.From = &[2]float64{r1(fx), r1(fy)}
		}
		d.Markers = append(d.Markers, rm)
	}
	return d
}

// HTML is the single-file replay: the floors' backgrounds and legends as inline SVG and a script that draws the
// routes, creatures and events up to the time on the slider. It loads nothing from outside.
func (s *Scene) HTML() ([]byte, error) {
	var data bytes.Buffer
	enc := json.NewEncoder(&data)
	enc.SetEscapeHTML(true) // "<" and "&" in names cannot end the script block
	if err := enc.Encode(s.data()); err != nil {
		return nil, err
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "<!doctype html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	fmt.Fprintf(&b, "<title>%s</title>\n<style>%s</style>\n</head>\n<body>\n", esc(s.Run.Title+" replay"), replayCSS)
	b.WriteString("<main>\n<div class=\"bar\">\n<button id=\"play\" type=\"button\" aria-label=\"play or pause\">Play</button>\n")
	b.WriteString("<label>Speed <select id=\"speed\"><option value=\"1\">1x</option><option value=\"10\">10x</option><option value=\"30\" selected>30x</option><option value=\"120\">120x</option><option value=\"600\">600x</option></select></label>\n")
	b.WriteString("<input id=\"slider\" type=\"range\" min=\"0\" max=\"1000\" value=\"0\" step=\"1\" aria-label=\"time\">\n<output id=\"clock\">0:00</output>\n")
	if len(s.Floors) > 1 {
		b.WriteString("<span class=\"tabs\" role=\"tablist\">")
		for i, f := range s.Floors {
			fmt.Fprintf(&b, "<button type=\"button\" role=\"tab\" class=\"tab\" data-floor=\"%d\">%s</button>", i, esc(f.Name))
		}
		b.WriteString("</span>\n")
	}
	b.WriteString("</div>\n<div class=\"stage\">\n")
	for i := range s.Floors {
		fmt.Fprintf(&b, "<div class=\"floor\" data-floor=\"%d\"%s>\n", i, map[bool]string{true: "", false: " hidden"}[i == 0])
		w := &svgw{}
		s.writeSVG(w, i, true)
		b.WriteString(strings.Replace(w.String(), `<g id="dyn">`, `<g class="dyn">`, 1))
		b.WriteString("</div>\n")
	}
	b.WriteString("<ol id=\"events\" aria-live=\"polite\"></ol>\n</div>\n</main>\n")
	b.WriteString("<script type=\"application/json\" id=\"run-data\">")
	b.Write(bytes.TrimSpace(data.Bytes()))
	b.WriteString("</script>\n<script>")
	b.WriteString(replayJS)
	b.WriteString("</script>\n</body>\n</html>\n")
	return b.Bytes(), nil
}

const replayCSS = `
:root{color-scheme:light dark;--bg:#f2f1ec;--fg:#222;--muted:#666;--card:#fff;--line:#c9c6bb}
@media (prefers-color-scheme:dark){:root{--bg:#1d1d1b;--fg:#e8e6df;--muted:#a09d92;--card:#2a2a27;--line:#4a4944}}
body{margin:0;background:var(--bg);color:var(--fg);font:14px/1.4 system-ui,sans-serif}
main{max-width:1500px;margin:0 auto;padding:12px 16px}
.bar{display:flex;flex-wrap:wrap;gap:10px 14px;align-items:center;margin-bottom:10px}
.bar button,.bar select{font:inherit;padding:4px 10px;border:1px solid var(--line);border-radius:6px;background:var(--card);color:var(--fg)}
.bar input[type=range]{flex:1 1 320px;min-width:200px}
#clock{min-width:11ch;font-variant-numeric:tabular-nums}
.tab[aria-selected=true]{outline:2px solid #0072B2}
.stage{display:flex;flex-wrap:wrap;gap:12px;align-items:flex-start}
.floor{max-width:100%;overflow:auto;border:1px solid var(--line);border-radius:6px}
.floor svg{display:block;max-width:none}
#events{flex:1 1 220px;min-width:200px;max-height:60vh;overflow:auto;margin:0;padding:8px 8px 8px 28px;background:var(--card);border:1px solid var(--line);border-radius:6px;font-size:12px}
#events li{margin:0 0 2px}
`

const replayJS = `
(function(){
var D=JSON.parse(document.getElementById('run-data').textContent);
var NS='http://www.w3.org/2000/svg';
var floor=0,T=0,playing=false,last=0,speed=30;
var slider=document.getElementById('slider'),clock=document.getElementById('clock'),play=document.getElementById('play');
var evList=document.getElementById('events');
function mk(n,a,p){var e=document.createElementNS(NS,n);for(var k in a)e.setAttribute(k,a[k]);if(p)p.appendChild(e);return e;}
function fmt(t){t=Math.round(t);var h=Math.floor(t/3600),m=Math.floor(t/60)%60,s=t%60;return (h?h+':'+(m<10?'0':''):'')+m+':'+(s<10?'0':'')+s;}
function star(x,y,o,i){var d='';for(var k=0;k<10;k++){var r=k%2?i:o,a=-Math.PI/2+k*Math.PI/5;d+=(k?'L':'M')+(x+r*Math.cos(a)).toFixed(1)+' '+(y+r*Math.sin(a)).toFixed(1);}return d+'Z';}
function dyn(){var f=document.querySelector('.floor[data-floor="'+floor+'"] .dyn');return f;}
function posAt(tr,t){var p=tr.pts;if(!p.length)return null;if(t<=p[0][0])return t<p[0][0]-D.gap?null:p[0];
 var lo=0,hi=p.length;while(lo<hi){var mid=(lo+hi)>>1;if(p[mid][0]>t)hi=mid;else lo=mid+1;}
 if(lo>=p.length){return t-p[p.length-1][0]>D.gap?null:p[p.length-1];}
 var a=p[lo-1],b=p[lo];if(b[4]||b[0]-a[0]<=0)return t-a[0]>D.gap?null:a;
 var f=(t-a[0])/(b[0]-a[0]);return [t,a[1]+(b[1]-a[1])*f,a[2]+(b[2]-a[2])*f,f<.5?a[3]:b[3],0];}
function draw(){try{drawAll();}catch(e){evList.textContent='replay error: '+e.message;}}
function drawAll(){
 var g=dyn();if(!g)return;while(g.firstChild)g.removeChild(g.firstChild);
 var multi=D.floors>1;
 D.creatures.forEach(function(c){
  if(c.seen>T||(multi&&c.f!==floor))return;
  var fate=(c.fate==='skipped'||T>=c.fate_t)?c.fate:'seen';
  var col=fate==='seen'?'#ffffff':D.colors[fate];
  if(c.boss){mk('path',{d:star(c.x,c.y,10,4.5),fill:col==='#ffffff'?'#f0e0a0':col,stroke:D.colors.boss,'stroke-width':2},g).appendChild(mk('title',{})).textContent=c.name;return;}
  var e=mk('circle',{cx:c.x,cy:c.y,r:3.2,fill:fate==='skipped'||fate==='seen'?'#ffffff':col,stroke:fate==='skipped'||fate==='seen'?'#7a7a7a':'#ffffff','stroke-width':fate==='skipped'||fate==='seen'?1.4:1},g);
  mk('title',{},e).textContent=c.name;});
 D.tracks.forEach(function(tr){
  var full='',part='',seg=false,pseg=false;
  for(var i=0;i<tr.pts.length;i++){var p=tr.pts[i];
   if(multi&&p[3]!==floor){seg=false;pseg=false;continue;}
   var brk=p[4]||!seg;full+=(brk?'M':'L')+p[1]+' '+p[2];seg=true;
   if(p[0]<=T){part+=((p[4]||!pseg)?'M':'L')+p[1]+' '+p[2];pseg=true;}else pseg=false;}
  var cur=posAt(tr,T);
  if(cur&&(!multi||cur[3]===floor)&&cur[0]<=T+.01&&part){part+=(pseg?'L':'M')+cur[1].toFixed(1)+' '+cur[2].toFixed(1);}
  mk('path',{d:full,fill:'none',stroke:tr.color,'stroke-width':1,opacity:.22,'stroke-linejoin':'round'},g);
  if(part)mk('path',{d:part,fill:'none',stroke:tr.color,'stroke-width':tr.leader?2.4:1.7,opacity:.92,'stroke-linejoin':'round','stroke-linecap':'round'},g);
  if(cur&&(!multi||cur[3]===floor)){mk('circle',{cx:cur[1],cy:cur[2],r:5.5,fill:tr.color,stroke:'#fff','stroke-width':1.6},g);
   var t=mk('text',{x:cur[1]+8,y:cur[2]+4,'font-size':11,'font-weight':'bold',fill:'#222',stroke:'#fff','stroke-width':3,'paint-order':'stroke'},g);t.textContent=tr.name;}});
 var shown=[];
 D.markers.forEach(function(m){
  if(m.t>T)return;shown.push(m);if(multi&&m.f!==floor)return;
  var k=m.kind,x=m.x,y=m.y;
  if(m.from)mk('path',{d:'M'+m.from[0]+' '+m.from[1]+'L'+x+' '+y,fill:'none',stroke:D.colors.teleport,'stroke-width':1.4,'stroke-dasharray':'6 4'},g);
  var e;
  if(k==='pull')e=mk('path',{d:'M'+x+' '+(y-7)+'l7 7l-7 7l-7 -7z',fill:'#fff',stroke:D.colors.evaded,'stroke-width':1.8},g);
  else if(k==='death')e=mk('path',{d:'M'+(x-5)+' '+(y-5)+'L'+(x+5)+' '+(y+5)+'M'+(x-5)+' '+(y+5)+'L'+(x+5)+' '+(y-5),stroke:D.colors.killed,'stroke-width':3,'stroke-linecap':'round'},g);
  else if(k==='wipe')e=mk('circle',{cx:x,cy:y,r:11,fill:'#fff','fill-opacity':.8,stroke:'#111','stroke-width':3},g);
  else if(k==='door')e=mk('rect',{x:x-6,y:y-6,width:12,height:12,fill:D.colors.door,stroke:'#fff'},g);
  else if(k==='teleport'||k==='gm')e=mk('path',{d:'M'+x+' '+(y-7)+'l7 12h-14z',fill:D.colors.teleport,stroke:'#fff'},g);
  else if(k==='boss_kill')e=mk('circle',{cx:x,cy:y,r:14,fill:'none',stroke:D.colors.boss,'stroke-width':2},g);
  else e=mk('path',{d:'M'+x+' '+y+'v-16',stroke:'#222','stroke-width':1.5},g);
  mk('title',{},e).textContent=m.label+' at '+fmt(m.t);});
 var html='';for(var i=Math.max(0,shown.length-60);i<shown.length;i++){var m=shown[i];html='<li><b>'+fmt(m.t)+'</b> '+esc(m.label||m.kind)+'</li>'+html;}
 evList.innerHTML=html||'<li>no events yet</li>';
}
function esc(s){return String(s).replace(/[&<>"]/g,function(c){return {'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c];});}
function setT(t){T=Math.max(0,Math.min(D.seconds,t));slider.value=D.seconds?Math.round(T/D.seconds*1000):0;
 clock.textContent=fmt(T)+' / '+fmt(D.seconds);draw();}
slider.addEventListener('input',function(){setT(slider.value/1000*D.seconds);});
document.getElementById('speed').addEventListener('change',function(e){speed=+e.target.value;});
function tick(ts){if(!playing)return;var dt=(ts-last)/1000;last=ts;setT(T+dt*speed);if(T>=D.seconds){playing=false;play.textContent='Play';return;}requestAnimationFrame(tick);}
play.addEventListener('click',function(){if(playing){playing=false;play.textContent='Play';return;}
 if(T>=D.seconds)T=0;playing=true;play.textContent='Pause';last=performance.now();requestAnimationFrame(tick);});
function pick(i){floor=i;document.querySelectorAll('.floor').forEach(function(f){f.hidden=(+f.getAttribute('data-floor')!==i);});
 document.querySelectorAll('.tab').forEach(function(b){b.setAttribute('aria-selected',+b.getAttribute('data-floor')===i);});draw();}
document.querySelectorAll('.tab').forEach(function(b){b.addEventListener('click',function(){pick(+b.getAttribute('data-floor'));});});
var hm=/f=(\d+)/.exec(location.hash);if(D.floors>1)pick(hm&&+hm[1]<D.floors?+hm[1]:0);
var tm=/t=([0-9.]+)/.exec(location.hash);setT(tm?+tm[1]:0);
})();
`
