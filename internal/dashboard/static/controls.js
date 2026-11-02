"use strict";
// The run controls' page part (README "Controls (--control)"): the buttons of one agent's card, the confirm texts and the control log, and
// the DOM that sends one control. The choice of buttons is a pure function of the card (tested under node); the page puts every string in with
// textContent. The box is shown only when GET /api/control/status says the controls are on.
(function (root) {
  // ---- the buttons of a card (D14 / README "Controls"): the matrix, with no DOM ----
  function runButtons(card) {
    if (!card || !card.online || card.synthetic) return [];
    const r = card.run || {};
    const running = r.state === "running";
    const mode = r.mode || "";
    if (!running) {
      return [{ verb: "stop", label: "Unbind kept party", title: "Unbinds the party this agent kept after a run (the hires go back and the instance is lost).", confirm: true, enabled: true }];
    }
    if (mode === "observe") {
      const paused = !!(r.leader && r.leader.state === "paused");
      return [
        { verb: "pause", label: "Pause", title: "Pause the run leader: the bots stop where they are and wait.", confirm: true, enabled: !paused },
        { verb: "resume", label: "Resume", title: "Let the run leader go on.", confirm: false, enabled: paused },
        { verb: "skip", label: "Skip step", title: "Ask the run leader to skip the step it is on.", confirm: false, enabled: true },
        { verb: "stop", label: "Stop run", title: "Ends the run and unbinds: the hires go back to your raid and the instance is lost.", confirm: true, enabled: true },
      ];
    }
    if (mode === "remote" || mode === "commander") {
      return [
        { verb: "pause", label: "Stop, keep party", title: "Ends this agent's commander run; the party and the instance are kept, and the run can be resumed later.", confirm: true, enabled: true },
        { verb: "stop", label: "Stop and unbind", title: "Ends the run and unbinds: the hires go back to your raid and the instance is lost.", confirm: true, enabled: true },
      ];
    }
    return [{ verb: "stop", label: "Stop run", title: "Ends the run and unbinds: the hires go back to your raid and the instance is lost.", confirm: true, enabled: true }];
  }

  // The text of the confirm dialog, naming the character and the plan (a control that ends a run is never silent).
  function confirmText(card, verb) {
    const who = card && card.character ? card.character + " (agent " + card.id + ")" : "agent " + (card && card.id || "?");
    const plan = card && card.label && card.label.plan ? card.label.plan : "no plan";
    if (verb === "pause") {
      if (card && card.run && card.run.mode === "observe") {
        return "Pauses the run of " + who + " (" + plan + "): the bots stop where they are and wait. The party and the instance are kept.";
      }
      return "Ends the run of " + who + " (" + plan + "). The party and the instance are kept; go on later with dungeon_run resume.";
    }
    const end = "the hires go back to the commander's raid and the instance is lost.";
    if (card && card.run && card.run.state === "running") return "Ends the run of " + who + " (" + plan + "): " + end;
    return "Unbinds the kept party of " + who + " (" + plan + "): " + end;
  }

  // The result lines of GET /api/control/<id>/log, said in words.
  function logLines(json) {
    const lines = json && Array.isArray(json.lines) ? json.lines : [];
    return lines.map((l) => {
      const t = l && l.at ? String(l.at).slice(11, 19) : "";
      const what = (l && l.verb ? l.verb : "") + (l && l.ok ? " ok" : l && l.ok === false ? " failed" : "");
      return [t, l && l.who, l && l.via === "trusted" ? "trusted" : "", what, l && l.message ? l.message : ""].filter(Boolean).join(" \u00b7 ");
    });
  }

  const VERBS = ["pause", "resume", "skip", "stop"];

  // ---- the DOM part ----
  function create(env) {
    const h = env.h;
    const st = { asked: false, enabled: null, trusted: false, nonce: "" };
    const states = new Map();
    const sess = {
      get() { try { return window.sessionStorage.getItem("control.token") || ""; } catch (e) { return ""; } },
      set(v) { try { window.sessionStorage.setItem("control.token", v); } catch (e) { /* ignore */ } },
    };

    async function jget(url) {
      try {
        const r = await fetch(url, { cache: "no-store" });
        let json = null;
        try { json = await r.json(); } catch (e) { /* not JSON */ }
        return { ok: r.ok, status: r.status, json };
      } catch (e) { return { ok: false, status: 0, json: null }; }
    }

    function ask() {
      if (st.asked) return;
      st.asked = true;
      jget("/api/control/status").then((r) => {
        st.enabled = !!(r.ok && r.json && r.json.enabled === true);
        if (st.enabled) {
          st.trusted = r.json.local_trusted === true;
          st.nonce = st.trusted && typeof r.json.trust_nonce === "string" ? r.json.trust_nonce : "";
        }
        if (env.footer) env.footer(st.enabled);
        env.rerender();
      });
    }

    function state(card) {
      let s = states.get(card.id);
      if (s) return s;
      const el = h("div", { class: "ctls" });
      s = { el, title: h("h3", { text: "Controls" }), row: h("div", { class: "ctlrow" }), token: h("input", { type: "password", autocomplete: "off", placeholder: "control token", "aria-label": "control token" }),
        msg: h("div", { class: "ctlmsg", hidden: true }), log: h("details", { class: "ctllog" }), btns: new Map(), busy: new Set(), logAt: 0 };
      s.token.value = sess.get();
      s.token.addEventListener("change", () => sess.set(s.token.value.trim()));
      const logPre = h("pre", { class: "ctllogpre" });
      s.log.append(h("summary", { text: "Control log (last 5)" }), logPre);
      s.logPre = logPre;
      s.el.append(s.title, s.row, h("label", { class: "ctltoken" }, h("span", { text: "Token " }), s.token), s.msg, s.log);
      states.set(card.id, s);
      return s;
    }

    function paint(s, card) {
      const specs = runButtons(card);
      s.card = card; // the buttons keep the card and its specs current: a click never sends last poll's state
      s.specs = new Map(specs.map((x) => [x.verb, x]));
      const have = new Set(specs.map((x) => x.verb));
      for (const [verb, b] of s.btns) if (!have.has(verb)) { b.remove(); s.btns.delete(verb); }
      for (const spec of specs) {
        let b = s.btns.get(spec.verb);
        if (!b) {
          b = h("button", { type: "button", class: "mb ctl" });
          b.addEventListener("click", () => { const cur = s.specs.get(b.dataset.verb); if (cur) send(s, s.card, cur); });
          s.btns.set(spec.verb, b);
        }
        b.textContent = spec.label;
        b.title = spec.title;
        b.dataset.verb = spec.verb;
        b.dataset.confirm = spec.confirm ? "1" : "";
        b.disabled = !spec.enabled || s.busy.has(spec.verb);
      }
      // the order of the buttons follows the matrix
      for (const spec of specs) s.row.append(s.btns.get(spec.verb));
    }

    function refreshLog(s, card) {
      jget("/api/control/" + encodeURIComponent(card.id) + "/log?n=5").then((r) => {
        const lines = r.ok ? logLines(r.json) : ["(the control log is not reachable)"];
        s.logPre.textContent = lines.length ? lines.join("\n") : "(no controls yet)";
      });
    }

    async function send(s, card, spec) {
      if (spec.confirm && !window.confirm(confirmText(card, spec.verb))) return;
      const token = st.trusted ? "" : s.token.value.trim();
      if (!st.trusted && !token) { s.msg.textContent = "Type the control token first (kept for this browser tab only)."; s.msg.hidden = false; return; }
      s.busy.add(spec.verb);
      paint(s, card);
      s.msg.textContent = "Sending " + spec.label + "...";
      s.msg.hidden = false;
      let r;
      try {
        r = await fetch("/api/control/" + encodeURIComponent(card.id) + "/run", { method: "POST", cache: "no-store",
          headers: token ? { "Content-Type": "application/json", Authorization: "Bearer " + token } : { "Content-Type": "application/json", "X-Control-Nonce": st.nonce },
          body: JSON.stringify({ verb: spec.verb, confirm: !!spec.confirm }) });
      } catch (e) { s.busy.delete(spec.verb); s.msg.textContent = "Could not reach the dashboard: " + e; paint(s, card); return; }
      let json = null;
      try { json = await r.json(); } catch (e) { /* not JSON */ }
      s.busy.delete(spec.verb);
      if (r.status === 401) { sess.set(""); s.token.value = ""; s.msg.textContent = "Wrong or missing control token" + (st.trusted ? " (the server did not accept this browser without one)." : "."); }
      else if (json && json.status === "ok") s.msg.textContent = spec.verb + " sent to agent " + card.id + " (" + json.rpc + ").";
      else s.msg.textContent = "Refused: " + ((json && json.message) || ("HTTP " + r.status));
      paint(s, card);
      refreshLog(s, card);
    }

    return {
      render(el, card) {
        ask();
        if (st.enabled !== true || !card || card.synthetic) { el.hidden = true; return; }
        el.hidden = false;
        const s = state(card);
        el.replaceChildren(s.el);
        s.token.hidden = st.trusted;
        paint(s, card);
        const now = Date.now();
        if (!s.logAt || now - s.logAt > 10000) { s.logAt = now; refreshLog(s, card); }
      },
      enabled() { return st.enabled; },
    };
  }

  const api = { runButtons, confirmText, logLines, create, VERBS };
  if (typeof module !== "undefined" && module.exports) module.exports = api; else root.CTL = api;
})(typeof self !== "undefined" ? self : this);
