"use strict";
// The map picture slots of a card, kept free of the page so a test can run them under node. The server lists three slots per
// map in Zul'Farrak's order (World map, plus a slot per world-map floor, Minimap, Navmesh) and marks a slot without a picture
// unavailable with one line of reason. The picker shows all of them: a missing one is disabled and reads "<label> (none)";
// the drawing falls back to the first available slot, so a choice saved before a picture went missing never leaves a blank map.
(function (root) {
  const KINDS = ["worldmap", "minimap", "navmesh"]; // the order Zul'Farrak has them in
  const ASK_AGAIN_MS = 30000; // a map whose answer has no picture at all is asked again after this long (a re-extract fills it)
  const TITLE = { worldmap: "World map", minimap: "Minimap", navmesh: "Navmesh" };
  const num = (v) => typeof v === "number" && isFinite(v);
  // the extractor's image names, and nothing else: "<map id>[.<source>[-<n>f]].png"
  const PNG = /^\/minimap\/\d{1,9}(?:\.(?:minimap|worldmap|navmesh)(?:-\d+f)?)?\.png$/;

  // labelFloor reads the floor off a label ("World map 2f" -> "2f") for an entry that has no floor field of its own.
  function labelFloor(label) {
    const m = /(?:world map|worldmap) (\d+f)$/i.exec(label || "");
    return m ? m[1] : "";
  }

  // slots keeps what the page can draw: an available entry needs a picture URL and a finite, positive rectangle, as before;
  // an unavailable entry passes through with its label and reason, so the picker can say what is missing and why.
  function slots(list) {
    const out = [];
    for (const raw of Array.isArray(list) ? list : []) {
      if (!raw || typeof raw !== "object") continue;
      const kind = KINDS.indexOf(raw.kind) >= 0 ? raw.kind : "";
      const label = typeof raw.label === "string" && raw.label ? raw.label : (TITLE[kind] || "");
      if (!kind && !label) continue;
      const floor = typeof raw.floor === "string" ? raw.floor : labelFloor(label);
      if (raw.available === false) {
        out.push({ kind, label, floor, available: false, reason: typeof raw.reason === "string" ? raw.reason : "" });
        continue;
      }
      if (!label || !PNG.test(raw.url) || !num(raw.x) || !num(raw.y) || !num(raw.w) || !num(raw.h) || raw.w <= 0 || raw.h <= 0) continue;
      out.push({ kind, label, floor, available: true, url: raw.url, x: raw.x, y: raw.y, w: raw.w, h: raw.h });
    }
    return out;
  }

  // order puts the slots in the three kinds' order, a world map's floors after its main picture. A kind the server left out
  // is added as a missing slot, so the picker always offers the three.
  function order(list) {
    const out = [];
    for (const kind of KINDS) {
      const mine = list.filter((s) => s.kind === kind);
      if (mine.length) out.push(...mine); else out.push({ kind, label: TITLE[kind], floor: "", available: false, reason: "" });
    }
    for (const s of list) if (KINDS.indexOf(s.kind) < 0) out.push(s);
    return out;
  }

  // options are the picker's entries: all slots, a missing one disabled, reading "<label> (none)" with its reason on hover.
  function options(list) {
    return list.map((s) => ({ value: s.label, text: s.available ? s.label : s.label + " (none)", disabled: !s.available, title: s.available ? "" : s.reason }));
  }

  // hasPicture says whether an answer gives at least one drawable slot.
  function hasPicture(list) {
    return slots(list).some((s) => s.available);
  }

  // keep says whether an answer already in hand can stand: one with a picture always can; one with none (or no answer at all)
  // is asked again after ASK_AGAIN_MS, while its reasons stay on screen in the meantime.
  function keep(list, askedAt, now) {
    return hasPicture(list) || now - (askedAt || 0) <= ASK_AGAIN_MS;
  }

  // noPicture is the reason of the map picture layer when nothing can be drawn: every missing slot's own line, so it is clear
  // that no kind has an image and not only which one.
  function noPicture(list) {
    const says = list.filter((s) => !s.available && s.reason);
    if (!says.length) return "";
    return "No picture for this map: " + says.map((s) => s.label + " (" + s.reason + ")").join("; ");
  }

  // plan is what the page draws and shows: the picker's entries, whether the picker is shown at all (at least one available
  // slot), the slot to draw and the reason to print. A stored choice that is available wins; one that is now missing falls
  // back to the first available slot and keeps its reason printed. With nothing available the picker stays hidden and the
  // reason covers every missing slot, which greys the map picture layer as before.
  function plan(list, stored) {
    const all = order(slots(list));
    const have = all.filter((s) => s.available);
    const want = typeof stored === "string" ? all.find((s) => s.label === stored) : null;
    const pick = (want && want.available ? want : null) || have[0] || null;
    const reason = want && !want.available ? want.reason : pick ? "" : noPicture(all);
    return { slots: all, options: options(all), shown: have.length > 0, pick, reason };
  }

  const api = { KINDS, TITLE, ASK_AGAIN_MS, slots, order, options, hasPicture, keep, noPicture, plan };
  if (typeof module !== "undefined" && module.exports) module.exports = api; else root.PictureSlots = api;
})(typeof self !== "undefined" ? self : this);
