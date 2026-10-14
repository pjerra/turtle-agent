"use strict";
// Plain player language for what the dashboard shows. Pure functions of the card (and the plan's creature names), kept free of
// the page so a test can run them under node.
(function (root) {
  const cap = (s) => (s ? s[0].toUpperCase() + s.slice(1) : "");
  const lowerFirst = (s) => (s ? s[0].toLowerCase() + s.slice(1) : "");
  const pretty = (s) => cap(String(s || "").replace(/[_\-]+/g, " ").trim());
  const plural = (n, one, many) => n + " " + (n === 1 ? one : many || one + "s");

  // "grp-a" -> "Group A"; anything else is just tidied.
  function groupName(folder) {
    const m = /^(?:grp|group)[-_ ]?([A-Za-z0-9]+)$/i.exec(folder || "");
    return m ? "Group " + m[1].toUpperCase() : pretty(folder);
  }
  function fmtMin(m) { return m < 1 ? Math.round(m * 60) + " s" : (m < 10 ? m.toFixed(1) : String(Math.round(m))) + " min"; }
  function clock(sec) { const s = Math.max(0, Math.round(sec)); return Math.floor(s / 60) + ":" + String(s % 60).padStart(2, "0"); }

  // The feed knows a creature by its entry id; the plan's notes name some of them.
  function mobName(names, entry) { return (names && names[String(entry)]) || "creature " + entry; }
  function entryText(names, s) { return String(s || "").replace(/\bentry (\d+)/g, (_, e) => mobName(names, e)); }

  function section(run) { return run.leg_name ? pretty(run.leg_name) : run.leg >= 0 ? "Section " + (run.leg + 1) : ""; }

  // Names of the creatures the party fights now: "Stone Steward", "Stone Steward x3, Guardian".
  function fightingNames(c, names) {
    const count = new Map();
    for (const m of c.mobs || []) {
      if (m.kind !== "hostile" || !m.engaged || m.cleared) continue;
      if (m.leg >= 0 && c.run.leg >= 0 && m.leg !== c.run.leg) continue;
      const n = mobName(names, m.entry);
      count.set(n, (count.get(n) || 0) + 1);
    }
    return [...count].slice(0, 4).map(([n, k]) => (k > 1 ? n + " ×" + k : n));
  }
  // The pack in hand, before it is engaged.
  function packNames(c, names) {
    const count = new Map();
    for (const m of c.mobs || []) {
      if (m.kind !== "hostile" || m.cleared || m.leg !== c.run.leg || m.group !== c.run.group - 1) continue;
      const n = mobName(names, m.entry);
      count.set(n, (count.get(n) || 0) + 1);
    }
    return [...count].slice(0, 4).map(([n, k]) => (k > 1 ? n + " ×" + k : n));
  }

  const ENDED = { idle: "Idle", done: "Finished", failed: "Failed", stopped: "Stopped" };

  // What the run is doing, in a word or two.
  function phaseWord(c) {
    const r = c.run, text = ((r.text || "") + " " + (r.waiting || "")).toLowerCase();
    if (r.state !== "running") return ENDED[r.state] || pretty(r.state);
    switch (r.phase) {
      case "prepare": return /hire/.test(text) ? "Hiring the party" : "Getting the party ready";
      case "enter": return "Entering the dungeon";
      case "walk": return "Walking";
      case "rest": return "Resting";
      case "object": return /ritual/.test(text) ? "Ritual at the altar" : "Using an object";
      case "wait": return /door|gate/.test(text) ? "Waiting for the door" : "Waiting";
      case "boss": return fightingNames(c).length || (c.mobs || []).some((m) => m.boss && m.engaged) ? "Fighting the boss" : "Pulling the boss";
      case "pull": return fightingNames(c).length ? "Fighting" : "Pulling";
      case "step":
        if (/ritual/.test(text)) return "Ritual at the altar";
        if (/door|gate/.test(text)) return "Waiting for the door";
        if (/loot/.test(text)) return "Looting";
        if (/wait/.test(text)) return "Waiting";
        return "Doing a plan step";
      case "leader": return r.leader && r.leader.state === "paused" ? "Run leader paused" : "Run leader";
      case "starting": return "Starting";
      default: return "Running";
    }
  }

  // "Sentinel room · pack 4 of 5 · fighting Stone Steward"; an observed run: "Run leader: Sentinel · pull 7/41 · pulling"
  function stateLine(c, names) {
    const r = c.run, parts = [];
    // An observed run: the run leader on the server does the clearing and says what it does (TBM:RUN); the card shows that.
    if (r.leader && r.state === "running") return r.leader.text;
    if (r.state === "running" || r.leg >= 0) { const s = section(r); if (s) parts.push(s); }
    if (r.state === "running" && r.group > 0) parts.push("pack " + r.group + (r.groups ? " of " + r.groups : ""));
    let doing = phaseWord(c);
    if (r.state === "running" && (r.phase === "pull" || r.phase === "boss")) {
      let list = fightingNames(c, names);
      if (!list.length && r.phase === "pull") list = packNames(c, names);
      if (list.length) doing += " " + list.join(", ");
    }
    parts.push(parts.length ? lowerFirst(doing) : doing);
    return parts.join(" · ");
  }

  // One line for the top of a card: "Uldaman · Group A (Glacienne) · 18 min · 0 deaths · Sentinel room"
  function summaryLine(c) {
    const parts = [];
    if (c.label.plan) parts.push(pretty(c.label.plan));
    const grp = groupName(c.label.folder);
    if (grp || c.character) parts.push((grp || "") + (c.character ? (grp ? " (" + c.character + ")" : c.character) : ""));
    if (c.run.state === "running" || c.run.minutes) parts.push(fmtMin(c.run.minutes));
    if (c.run.state !== "idle") parts.push(plural(c.counters.deaths, "death"));
    const s = c.run.state === "running" ? section(c.run) : "";
    if (s) parts.push(s);
    else if (c.run.state !== "running" && c.run.state !== "idle") parts.push(phaseWord(c).toLowerCase());
    return parts.join(" · ");
  }

  // The stall timer: calm while it moves, then "Stuck" at the yellow and red levels.
  function stallText(stall) {
    if (stall.level === "yellow" || stall.level === "red") return "Stuck: no progress for " + clock(stall.seconds);
    return "Last progress " + clock(stall.seconds) + " ago";
  }

  // Short labels with a sentence that explains each.
  function counterList(c) {
    const n = c.counters;
    const k = (label, value, tip, hot) => ({ label, value: String(value), tip, hot: !!hot });
    return [
      k("Deaths", n.deaths, "Bots that died during the run.", n.deaths > 0),
      k("Wipes", n.wipes, "Times the whole party died.", n.wipes > 0),
      k("Whole party down", n.party_downs, "Times every bot was down at once (party_downs).", n.party_downs > 0),
      k("Tank lost a mob", n.threat_overrides, "Times the agent had to step in because a monster went for someone other than the tank (threat overrides).", n.threat_overrides > 3),
      k("Rest time", n.rest_pct + " %", "Share of the run spent resting for health and mana."),
      k("Rest stops", n.rest_gates, "Times the party stopped to recover before going on (rest gates)."),
      k("Packs pulled", n.pulls, "Groups of monsters pulled so far."),
      k("Regroups", n.gathers, "Times the party was called back together."),
      k("Walk stops", n.walk_stops, "Walks that were stopped half way."),
      k("GM help (commands)", n.gm_commands, "GM commands used in the whole session, for example to move a bot (gm_commands).", n.gm_commands > 1),
      k("GM help (this run)", n.gm_assists, "GM commands used to get this run past a problem (run_gm_assists).", n.gm_assists > 0),
      k("Commander walks", n.agent_walks, "Times the commander character walked itself (agent_walks)."),
      k("Orders", n.orders_sent + (n.orders_refused ? " (" + n.orders_refused + " refused)" : ""), "Orders sent to the bots and how many of them the bots refused.", n.orders_refused > 0),
    ];
  }

  const VERBS = { attack: "Attack", move: "Move", follow: "Follow", stay: "Stay", flee: "Retreat", pullback: "Pull back", pull: "Pull", assist: "Assist", loot: "Loot", use: "Use", rest: "Rest" };
  const guidKey = (g) => String(g || "").toLowerCase().replace(/^0x/, "").replace(/^0+/, "");

  // "Attack Stone Steward -> OK", "Move -> refused: no walkable path"; the raw line goes in raw.
  function orderText(o, c, names) {
    const raw = o.text || "";
    let verb = o.verb || (/^cmd (\w+)/.exec(raw) || [])[1] || (/\.bot action (\w+)/i.exec(raw) || [])[1] || "";
    let base = VERBS[verb.toLowerCase()] || pretty(verb) || "Order";
    const g = /0x[0-9a-f]+/i.exec(raw);
    if (g) {
      const mob = (c.mobs || []).find((m) => guidKey(m.guid) === guidKey(g[0]));
      base += " " + (mob ? mobName(names, mob.entry) : "a target");
    }
    const reply = (o.reply || "").toUpperCase();
    let result;
    if (reply === "ACK") result = "OK";
    else if (reply === "ERR") result = "refused" + (o.message || o.code ? ": " + (o.message || String(o.code).replace(/[_\-]+/g, " ")) : "");
    else if (!reply) result = "sent";
    else result = reply.toLowerCase();
    return { text: base + " → " + result, ok: reply !== "ERR", raw };
  }

  // One row of the party table.
  function botRow(b, names) {
    return {
      name: (b.lead ? "★ " : "") + b.name,
      kind: [pretty(b.class), b.role === "dps" ? "DPS" : b.role].filter(Boolean).join(" · "),
      hp: b.hp_pct,
      power: b.power_pct,
      powerLabel: b.power_type ? pretty(b.power_type) : "Power",
      target: entryText(names, b.target),
      dist: b.dist_tank == null || (b.role === "tank" && b.dist_tank === 0) ? "" : Math.round(b.dist_tank) + " yd from tank",
    };
  }

  const api = { pretty, groupName, fmtMin, clock, mobName, entryText, section, fightingNames, packNames, phaseWord, stateLine, summaryLine, stallText, counterList, orderText, botRow, plural };
  if (typeof module !== "undefined" && module.exports) module.exports = api; else root.PlainText = api;
})(typeof self !== "undefined" ? self : this);
