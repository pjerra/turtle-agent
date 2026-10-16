"use strict";
// The route editor's page part (plan view, #/plan/<name>): the editor is always on in the plan view (there is no button to find), the handles on the
// map show on hover or when selected, undo and redo, the help box, the unsaved-changes bar with Save and Discard, the check result, the diff and the
// list of versions. The unsaved edits are kept in the tab's session storage as a draft. The model and the words are in routeedit.js (tested under node); this file is the glue, and all
// text from the server goes into the page with textContent.
(function (root) {
  const RE = root.RouteEdit;
  const SESSION_TOKEN = "routeedit.token";

  function create(env) {
    const { h, s, MV } = env;
    const states = new Map();
    let enabled = null, enabledAsked = false; // null: not known yet
    let nonce = ""; // the page's capability the server gives a trusted caller; a trusted save echoes it (it is not a token)
    let trusted = false; // the server saves this browser's edits with its own token (--edit-trust-local): no token is asked for

    const sess = {
      get() { try { return window.sessionStorage.getItem(SESSION_TOKEN) || ""; } catch (e) { return ""; } },
      set(v) { try { window.sessionStorage.setItem(SESSION_TOKEN, v); } catch (e) { /* ignore */ } },
    };

    const draftStorage = () => { try { return window.sessionStorage || null; } catch (e) { return null; } };
    function saveDraft(st) {
      const ss = draftStorage();
      if (!ss || !st.draftReady || !st.modelSha) return;
      const key = JSON.stringify([st.hist.ops, st.note.value, dirty(st)]);
      if (st.draftKey === key) return;
      st.draftKey = key;
      if (dirty(st)) RE.draftSave(ss, st.name, st.modelSha, st.hist.ops, st.note.value); else RE.draftClear(ss, st.name);
    }

    async function jget(url) {
      try {
        const r = await fetch(url, { cache: "no-store" });
        const text = await r.text();
        let json = null;
        try { json = JSON.parse(text); } catch (e) { /* not JSON */ }
        return { ok: r.ok, status: r.status, json, text };
      } catch (e) { return { ok: false, status: 0, json: null, text: String(e) }; }
    }

    function state(name) {
      let st = states.get(name);
      if (!st) {
        st = { name, active: true, draftReady: false, draftKey: "", hist: RE.history([]), savedJSON: "[]", selected: null, picked: new Set(), msg: "", reply: null, diff: null, info: null, infoAt: 0, busy: false,
          model: null, modelSha: "", drag: null, el: h("div", { class: "editbox", hidden: true }), note: h("input", { type: "text", maxlength: "500", placeholder: "note for this version", "aria-label": "note" }),
          token: h("input", { type: "password", autocomplete: "off", placeholder: "editing token", "aria-label": "editing token" }) };
        st.token.value = sess.get();
        st.note.addEventListener("input", () => saveDraft(st));
        states.set(name, st);
      }
      return st;
    }

    function ensureEnabled() {
      if (enabledAsked) return;
      enabledAsked = true;
      jget("/api/edit/status").then((r) => { enabled = r.ok && r.json && r.json.enabled === true; trusted = !!(enabled && r.json.local_trusted === true && typeof r.json.trust_nonce === "string" && r.json.trust_nonce); nonce = trusted ? r.json.trust_nonce : ""; env.rerender(); });
    }

    function loadInfo(st) {
      if (st.busy || Date.now() - st.infoAt < 4000) return;
      st.infoAt = Date.now();
      jget("/api/edit/" + encodeURIComponent(st.name) + "/versions").then((r) => {
        if (r.ok && r.json) { st.info = r.json; refresh(st); }
      });
    }

    const dirty = (st) => JSON.stringify(st.hist.ops) !== st.savedJSON;
    function why(st, geo) {
      if (!st.info || !st.info.base) return st.info && st.info.base_error ? "the editable copy of this plan does not load: " + st.info.base_error : "loading...";
      const src = geo && geo.source;
      if (!src) return "loading...";
      if (src.sha256 !== st.info.base.sha256) return "This map shows " + src.path + " (" + src.sha256.slice(0, 8) + "), but edits start from " + st.info.base.source + " (" + st.info.base.sha256.slice(0, 8) + "). Start the dashboard with --plans-dir on the folder to edit, or --edit-source.";
      return "";
    }

    function setOps(st, ops, savedJSON) { st.hist = RE.history(ops); st.savedJSON = savedJSON != null ? savedJSON : st.savedJSON; st.selected = null; }

    // ---- the map: handles on the working route ----
    function worldOf(rec, e) {
      const r = rec.svg.getBoundingClientRect();
      return MV.toWorld(rec.v, rec.svg.clientWidth, rec.svg.clientHeight, e.clientX - r.left, e.clientY - r.top);
    }
    function place(rec, el, x, y) {
      el.dataset.x = String(x); el.dataset.y = String(y);
      el.setAttribute("transform", "translate(" + x + " " + y + ") scale(" + (1 / rec.v.scale).toFixed(5) + ")");
    }
    async function snapOk(st, op) {
      const r = await jget("/api/edit/" + encodeURIComponent(st.name) + "/snap?" + RE.snapQuery(st.hist.ops, op));
      if (!r.ok || !r.json) return { ok: false, message: "the server did not answer the floor check (" + (r.status || "no connection") + ")" };
      return r.json;
    }
    async function addOp(st, op) {
      const sn = await snapOk(st, op);
      if (!sn.ok) { st.msg = "Not placed: " + (sn.message || "refused"); return false; }
      st.hist = RE.push(st.hist, op);
      if (sn.change && sn.change.floor_picked) st.picked.add(JSON.stringify(op));
      st.reply = null; st.diff = null;
      st.msg = sn.change && sn.change.floor_picked ? "Floor picked: " + sn.change.floors + " floors are in range of that height; the nearest to the old height was taken. Check it is the level you meant." : "";
      return true;
    }

    function drawOverlay(rec, st, geo, visible) {
      const g = rec.svg && rec.svg.querySelector("g.editg");
      if (!g || st.drag) return;
      rec.svg.classList.toggle("editing", st.active);
      if (!st.active || !st.model) { g.replaceChildren(); return; }
      const w = RE.apply(st.model, st.hist.ops), kids = [], doc = env.routeDoc ? env.routeDoc(st.name) : null;
      for (const i of visible) {
        const leg = w.legs[i];
        if (!leg) continue;
        const gl = s("g", { class: "edleg" }), pts = RE.routePoints(leg);
        const line = s("polyline", { class: "ed-route", points: pts.map((p) => p.x + "," + p.y).join(" ") });
        gl.append(line);
        // the packs and the boss: hover only, they are not moved here
        for (const pk of leg.packs || []) {
          const el = s("g", { class: "mk edpack", "data-x": pk.x, "data-y": pk.y });
          el.append(s("circle", { class: "hit", r: 9 }), s("title", {}, RE.packTip(leg, pk)));
          gl.append(el);
        }
        // a line is a target: click it to add a point, hover it for what the bots do along it
        for (let k = 0; k + 1 < pts.length; k++) {
          if (Math.hypot(pts[k + 1].x - pts[k].x, pts[k + 1].y - pts[k].y) < 0.05) continue;
          const hit = s("polyline", { class: "ed-hit", points: pts[k].x + "," + pts[k].y + " " + pts[k + 1].x + "," + pts[k + 1].y });
          hit.append(s("title", {}, RE.segmentTip(leg, pts, k, doc)));
          hit.addEventListener("pointerdown", (e) => {
            if (e.button > 0) return;
            const p = worldOf(rec, e), m = RE.segmentAt(leg, i, p.x, p.y, rec.v.scale, 24);
            if (!m) return;
            const el = s("g", { class: "mk edm tmp", "data-x": m.x, "data-y": m.y });
            el.append(s("circle", { class: "hit", r: 11 }), s("circle", { class: "vis", r: 5 }));
            gl.append(el); place(rec, el, m.x, m.y);
            startDrag(rec, st, geo, e, el, { kind: "mid", m, leg, pts, line });
          });
          gl.append(hit);
        }
        for (const m of RE.midHandles(leg, i)) {
          const el = s("g", { class: "mk edm", "data-x": m.x, "data-y": m.y });
          el.append(s("circle", { class: "hit", r: 11 }), s("circle", { class: "vis", r: 5 }), s("title", {}, "Click to put a point here, or drag to put it where you drop it"));
          el.addEventListener("pointerdown", (e) => startDrag(rec, st, geo, e, el, { kind: "mid", m, leg, pts, line }));
          gl.append(el);
        }
        for (const hd of RE.handles(leg, i)) {
          const sel = st.selected && st.selected.leg === i && st.selected.index === hd.index && st.selected.kind === hd.kind;
          const flagged = st.picked.size && st.hist.ops.some((op) => op.leg === i && st.picked.has(JSON.stringify(op)) && ((op.kind === "move" && op.index === hd.index) || (op.kind === "insert" && op.index === hd.index)));
          const info = RE.docAt(doc, i, hd.x, hd.y), red = !!(info.seg && !info.seg.ok) || !!(info.stop && info.stop.failed);
          const el = s("g", { class: "mk edh" + (hd.locked ? " lock" : "") + (sel ? " sel" : "") + (hd.kind === "to" ? " to" : "") + (flagged ? " picked" : "") + (red ? " red" : ""), "data-x": hd.x, "data-y": hd.y });
          el.append(s("circle", { class: "hit", r: 12 }), s("circle", { class: "vis", r: 7 }),
            s("title", {}, RE.pointTip(leg, hd, doc) + (flagged ? "\nFloor picked: check it is the level you meant." : "")));
          el.addEventListener("pointerdown", (e) => startDrag(rec, st, geo, e, el, { kind: "move", hd, leg, pts, line }));
          gl.append(el);
        }
        kids.push(gl);
      }
      if (st.reply && st.reply.check) {
        for (const m of RE.issueMarks(st.reply.check)) {
          const el = s("g", { class: "mk edissue" + (m.isNew ? " new" : ""), "data-x": m.x, "data-y": m.y });
          el.append(s("path", { d: "M-7,-7 L7,7 M7,-7 L-7,7" }), s("title", {}, (m.isNew ? "new: " : "already in the plan: ") + m.text));
          kids.push(el);
        }
      }
      g.replaceChildren(...kids);
      env.applyViewBox(rec);
    }

    function startDrag(rec, st, geo, e, el, d) {
      if (e.button > 0) return;
      e.stopPropagation(); e.preventDefault();
      if (d.kind === "move" && d.hd.locked) { st.msg = "Locked: " + d.hd.why + ". Edit it in the plan file."; refresh(st); return; }
      st.drag = { ...d, pid: e.pointerId, x0: e.clientX, y0: e.clientY, moved: false, el };
      st.drag.cancel = () => { // Esc: nothing is placed, the point goes back where it was
        el.removeEventListener("pointermove", move); el.removeEventListener("pointerup", up); el.removeEventListener("pointercancel", up);
        try { el.releasePointerCapture(st.drag.pid); } catch (x) { /* ignore */ }
        st.drag = null; st.msg = "Move cancelled."; refresh(st);
      };
      try { el.setPointerCapture(e.pointerId); } catch (x) { /* ignore */ }
      const move = (ev) => {
        const dr = st.drag;
        if (!dr || ev.pointerId !== dr.pid) return;
        if (Math.hypot(ev.clientX - dr.x0, ev.clientY - dr.y0) > 4) dr.moved = true;
        if (!dr.moved) return;
        const p = worldOf(rec, ev);
        dr.x = p.x; dr.y = p.y;
        place(rec, el, p.x, p.y);
        if (dr.kind === "move") {
          const pts = dr.pts.map((q) => ({ ...q }));
          const k = pts.findIndex((q) => q.kind === dr.hd.kind && q.index === dr.hd.index);
          if (k >= 0) { pts[k].x = p.x; pts[k].y = p.y; dr.line.setAttribute("points", pts.map((q) => q.x + "," + q.y).join(" ")); }
        }
      };
      const up = async (ev) => {
        const dr = st.drag;
        if (!dr || ev.pointerId !== dr.pid) return;
        el.removeEventListener("pointermove", move); el.removeEventListener("pointerup", up); el.removeEventListener("pointercancel", up);
        try { el.releasePointerCapture(ev.pointerId); } catch (x) { /* ignore */ }
        st.drag = null;
        if (dr.kind === "move") {
          if (!dr.moved) st.selected = { leg: dr.hd.leg, kind: dr.hd.kind, index: dr.hd.index };
          else { st.busy = true; await addOp(st, RE.moveOp(dr.hd, dr.x, dr.y)); st.busy = false; st.selected = { leg: dr.hd.leg, kind: dr.hd.kind, index: dr.hd.index }; }
        } else {
          st.busy = true;
          const at = dr.moved ? { x: dr.x, y: dr.y } : { x: dr.m.x, y: dr.m.y };
          await addOp(st, RE.insertOp(dr.m.leg, dr.m.index, at.x, at.y));
          st.busy = false;
          st.selected = null;
        }
        refresh(st);
      };
      el.addEventListener("pointermove", move); el.addEventListener("pointerup", up); el.addEventListener("pointercancel", up);
    }

    // ---- the panel ----
    function deleteSelected(st) {
      const sl = st.selected;
      if (!sl || sl.kind !== "via") { st.msg = sl ? "A leg needs its `to`: only a via point can be deleted." : "Click a via point first."; refresh(st); return; }
      const w = RE.apply(st.model, st.hist.ops), op = RE.deleteOp(sl.leg, sl.index), bad = RE.checkOp(w.legs, op);
      if (bad) { st.msg = "Not deleted: " + bad; refresh(st); return; }
      st.hist = RE.push(st.hist, op); st.selected = null; st.reply = null; st.diff = null; st.msg = "";
      refresh(st);
    }
    function discard(st) { setOps(st, [], "[]"); st.reply = null; st.diff = null; st.msg = "Edits discarded."; st.picked = new Set(); st.note.value = ""; refresh(st); }
    function undo(st) { st.hist = RE.undo(st.hist); st.selected = null; st.reply = null; st.msg = ""; refresh(st); }
    function redo(st) { st.hist = RE.redo(st.hist); st.selected = null; st.reply = null; st.msg = ""; refresh(st); }

    async function save(st, note) {
      const token = trusted ? "" : st.token.value.trim(); // a trusted browser sends none: the server holds the token and never shows it
      if (!trusted && !token) { st.msg = "Type the editing token first (it is kept for this browser tab only)."; refresh(st); return; }
      if (!st.hist.ops.length) { st.msg = "Nothing to save."; refresh(st); return; }
      st.busy = true; st.msg = "Saving and checking on the navmesh..."; refresh(st);
      let r;
      try {
        r = await fetch("/api/edit/" + encodeURIComponent(st.name), { method: "POST", cache: "no-store",
          headers: token ? { "Content-Type": "application/json", Authorization: "Bearer " + token } : { "Content-Type": "application/json", "X-Route-Edit-Nonce": nonce },
          body: JSON.stringify({ base_sha256: st.info.base.sha256, note: note != null ? note : st.note.value, ops: st.hist.ops }) });
      } catch (e) { st.busy = false; st.msg = "Could not reach the dashboard: " + e; refresh(st); return; }
      let json = null;
      try { json = await r.json(); } catch (e) { /* not JSON */ }
      st.busy = false;
      if (r.status === 401 && trusted) { trusted = false; st.msg = "The server did not accept this browser without a token. Type the editing token to save."; refresh(st); return; }
      if (r.status === 401 || r.status === 429) { st.msg = r.status === 401 ? "Wrong or missing editing token." : "Too many wrong tokens: wait a minute."; refresh(st); return; }
      if (token) sess.set(token);
      if (json && (json.status === "checked" || json.status === "unchecked" || json.status === "refused")) {
        st.reply = json; st.diff = json.diff || null;
        if (json.status === "checked") { st.savedJSON = JSON.stringify(st.hist.ops); st.msg = "Saved as " + json.name + ": checked."; }
        else if (json.status === "unchecked") { st.savedJSON = JSON.stringify(st.hist.ops); st.msg = "Saved as " + json.name + ", but UNCHECKED: " + (json.message || "the plan has plan check issues of its own") + "."; }
        else st.msg = "Refused (kept as " + json.name + "): " + (json.message || "the plan check failed") + ".";
        st.infoAt = 0; loadInfo(st);
      } else {
        st.reply = null;
        st.msg = (json && json.message) || "The save failed (" + r.status + ").";
      }
      refresh(st);
    }

    async function showDiff(st, v) {
      const r = await jget("/api/edit/" + encodeURIComponent(st.name) + "/" + v + "/diff");
      st.diff = r.ok ? r.text : "(no diff: " + (r.status || "no connection") + ")";
      st.msg = "Diff of " + v + " against the plan file it started from.";
      refresh(st);
    }
    async function loadVersion(st, v, andSave) {
      const r = await jget("/api/edit/" + encodeURIComponent(st.name) + "/" + v);
      if (!r.ok || !r.json) { st.msg = "Could not read " + v + "."; refresh(st); return; }
      if (r.json.base && st.info && st.info.base && r.json.base.sha256 !== st.info.base.sha256) { st.msg = v + " started from another version of the plan file (" + r.json.base.sha256.slice(0, 8) + "); it cannot be loaded on this one."; refresh(st); return; }
      setOps(st, r.json.ops || [], andSave ? st.savedJSON : JSON.stringify(r.json.ops || []));
      st.reply = null; st.diff = null; st.picked = new Set();
      st.msg = "Loaded " + v + " (" + (r.json.ops || []).length + " edit(s)).";
      if (andSave) await save(st, "revert to " + v);
      else refresh(st);
    }

    function panelBody(st, geo) {
      const kids = [];
      const bad = why(st, geo);
      const btn = (text, title, fn, dis) => { const b = h("button", { type: "button", class: "mb", title, text }); b.disabled = !!dis || st.busy; b.addEventListener("click", fn); return b; };
      kids.push(h("h3", { text: "Route editor" }));
      if (bad) { kids.push(h("div", { class: "muted", text: bad })); return kids; }
      const n = st.hist.ops.length;
      if (dirty(st)) {
        const bar = h("div", { class: "unsavedbar", role: "status" }, h("b", { text: "Unsaved changes" }), h("span", { text: " " + n + " edit" + (n === 1 ? "" : "s") + " not saved yet" }));
        bar.append(st.note);
        if (!trusted) bar.append(st.token);
        bar.append(btn("Save", "Check the route on the navmesh and keep it as a new version" + (trusted ? " (saved with the dashboard's own token)" : ""), () => save(st), !n),
          btn("Discard", "Throw the unsaved edits away and go back to the plan file", () => { if (window.confirm("Throw away " + n + " unsaved edit" + (n === 1 ? "" : "s") + "?")) discard(st); }, !n));
        kids.push(bar);
      }
      const tools = h("div", { class: "legtools" });
      tools.append(btn("Undo", "Undo the last edit (Ctrl+Z)", () => undo(st), !st.hist.ops.length),
        btn("Redo", "Redo (Ctrl+Y)", () => redo(st), !st.hist.redo.length),
        btn("Delete point", "Delete the selected walk point (Delete)", () => deleteSelected(st), !st.selected));
      kids.push(tools);
      const help = h("details", { class: "edhelp", open: "open" }, h("summary", { text: "How to edit" }));
      help.append(h("ul", {}, ...RE.HELP.map((t) => h("li", { text: t }))));
      help.append(h("ul", { class: "edkey" }, ...RE.KEY.map((k) => h("li", {}, h("i", { class: "ks " + k[0].split(" ")[0] }), h("span", { text: k[1] })))));
      kids.push(help);
      if (!n && !st.msg) kids.push(h("div", { class: "muted", text: "No changes yet." }));
      if (st.msg) kids.push(h("div", { class: "editmsg", text: st.msg }));
      if (st.reply && st.reply.check) {
        const c = st.reply.check, lines = [];
        lines.push(h("div", { class: "pill " + (st.reply.status === "checked" ? "running" : st.reply.status === "unchecked" ? "yellow" : "failed"), text: st.reply.status + (c.checked ? " · " + c.points + " points checked" : " · not checked") }));
        if (c.skipped) lines.push(h("div", { class: "err-line", text: "Could not check: " + c.skipped }));
        for (const is of c.issues || []) lines.push(h("div", { class: is.new ? "err-line" : "muted", text: (is.new ? "NEW " : "already there: ") + (is.text || is.reason) }));
        for (const t of RE.describeChanges(st.reply.changes)) lines.push(h("div", { text: t }));
        for (const w of c.warnings || []) lines.push(h("div", { class: "muted", text: "warning: " + (w.leg + 1) + " " + (w.part || "") + " " + (w.reason || "") }));
        kids.push(h("div", { class: "editresult" }, ...lines));
      }
      if (st.diff) kids.push(h("details", { class: "editdiff", open: "open" }, h("summary", { text: "Diff" }), h("pre", { text: st.diff })));
      const vs = (st.info && st.info.versions) || [];
      if (vs.length) {
        const rows = vs.slice().reverse().map((m) => {
          const v = "v" + String(m.version).padStart(4, "0");
          const row = h("div", { class: "verrow" + (m.status === "refused" ? " refused" : "") }, h("b", { text: v }),
            h("span", { text: " " + m.who + " · " + (m.at ? new Date(m.at).toLocaleString() : "") + " · " + m.status + " · " + ((m.ops || []).length) + " edit(s)" + (m.note ? " · " + m.note : "") }));
          row.append(btn("Diff", "Show what this version changed", () => showDiff(st, v)), btn("Load", "Put this version's edits on the map", () => loadVersion(st, v, false)),
            btn("Revert", "Make a new version equal to this one", () => { if (window.confirm("Make a new version equal to " + v + "?")) loadVersion(st, v, true); }, m.status === "refused"));
          return row;
        });
        kids.push(h("div", {}, h("h3", { text: "Versions" }), ...rows));
      }
      return kids;
    }

    function refresh(st) {
      const geo = env.geoOf(st.name);
      if (geo && geo.legs && (!st.model || st.modelSha !== (geo.source ? geo.source.sha256 : ""))) refreshModel(st, geo);
      const keep = [document.activeElement === st.note, document.activeElement === st.token];
      st.el.hidden = enabled !== true;
      saveDraft(st);
      st.el.replaceChildren(...(enabled === true ? panelBody(st, geo) : []));
      if (keep[0]) st.note.focus(); else if (keep[1]) st.token.focus();
      env.rerender();
    }

    document.addEventListener("keydown", (e) => {
      const st = states.get(env.currentPlan());
      if (st && st.drag && e.key === "Escape") { e.preventDefault(); st.drag.cancel(); return; }
      if (!st || !st.active || st.drag) return;
      const t = e.target && e.target.tagName;
      if (t === "INPUT" || t === "TEXTAREA" || t === "SELECT") return;
      if ((e.key === "Delete" || e.key === "Backspace") && st.selected) { e.preventDefault(); deleteSelected(st); }
      else if ((e.ctrlKey || e.metaKey) && !e.shiftKey && e.key.toLowerCase() === "z") { e.preventDefault(); undo(st); }
      else if ((e.ctrlKey || e.metaKey) && (e.key.toLowerCase() === "y" || (e.shiftKey && e.key.toLowerCase() === "z"))) { e.preventDefault(); redo(st); }
      else if (e.key === "Escape" && st.selected) { st.selected = null; refresh(st); }
    });
    window.addEventListener("beforeunload", (e) => { for (const st of states.values()) if (dirty(st)) { e.preventDefault(); e.returnValue = ""; return; } });

    return {
      // The panel of a plan (one element that stays, so what is typed in it stays), kept up to date; hidden when the editor is off.
      panel(name, geo) {
        ensureEnabled();
        const st = state(name);
        if (enabled === true) { loadInfo(st); if (!st.model && geo && geo.legs) refreshModel(st, geo); }
        st.el.hidden = enabled !== true;
        if (!st.el.firstChild && enabled === true) st.el.replaceChildren(...panelBody(st, geo));
        return st.el;
      },
      // The handles on the map; called each time the map is drawn.
      overlay(rec, name, geo, visible) {
        const st = states.get(name);
        if (!st || enabled !== true) { const g = rec.svg && rec.svg.querySelector("g.editg"); if (g && g.firstChild) g.replaceChildren(); return; }
        if (geo && geo.legs && (!st.model || st.modelSha !== (geo.source ? geo.source.sha256 : ""))) refreshModel(st, geo);
        drawOverlay(rec, st, geo, visible);
      },
      active(name) { const st = states.get(name); return !!(st && st.active); },
      // The edits made so far on a plan being edited ([] when it is not being edited): the real route is traced with them applied.
      ops(name) { const st = states.get(name); return st && st.active ? st.hist.ops.slice() : []; },
    };

    function refreshModel(st, geo) {
      if (st.model && dirty(st)) st.msg = "The plan file changed on disk: your unsaved edits were discarded.";
      st.model = RE.buildModel(geo); st.modelSha = geo.source ? geo.source.sha256 : "";
      if (st.hist.ops.length && st.info && st.info.base && st.info.base.sha256 !== st.modelSha) setOps(st, [], "[]");
      if (!st.hist.ops.length && st.modelSha) { // a draft left by a reload of this tab, if it was made on this very plan file
        const d = draftStorage() ? RE.draftLoad(draftStorage(), st.name, st.modelSha) : null;
        if (d) { setOps(st, d.ops, "[]"); st.note.value = d.note; st.msg = "Your unsaved edits were restored from this tab (" + d.ops.length + ")."; }
      }
      st.draftReady = !!st.modelSha;
    }
  }

  const api = { create };
  if (typeof module !== "undefined" && module.exports) module.exports = api; else root.RouteEditUI = api;
})(typeof self !== "undefined" ? self : this);
