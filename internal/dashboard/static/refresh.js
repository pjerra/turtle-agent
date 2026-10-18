"use strict";
// Refresh rate of the live dashboard: the choice "Refresh every 1 / 2 / 5 / 10 / 30 s / paused", its saved value, and the
// timer that polls no more often than that. Free of the page so a test can run it under node (internal/dashboard/testdata).
// The server emit stays at 2 s; only what the browser shows is slowed down or frozen. Same options, key shape and default
// as the TortoiseBots Commanders tab picker (Sagiroth/TortoiseBots#982), so the two dashboards behave the same.
(function (root) {
  const OPTIONS = [1, 2, 5, 10, 30, 0]; // seconds; 0 = paused
  const DEFAULT_SEC = 2;                // the poll interval: nothing is held back until the viewer picks something slower
  const STORAGE_KEY = "tadash.refresh.v1";

  function normalize(v) {
    const n = typeof v === "string" && /^[0-9]{1,3}$/.test(v) ? Number(v) : v;
    return typeof n === "number" && OPTIONS.indexOf(n) >= 0 ? n : DEFAULT_SEC;
  }

  function label(sec) { return sec === 0 ? "paused" : sec + " s"; }

  // The saved choice of this browser, or the default when there is none, it is junk, or the storage throws (a private
  // window, blocked site data). `storage` is whatever the page could get from window.localStorage (an object, or null
  // where even asking threw).
  function load(storage) {
    try {
      if (!storage) return DEFAULT_SEC;
      const raw = storage.getItem(STORAGE_KEY);
      return raw == null ? DEFAULT_SEC : normalize(raw);
    } catch (e) {
      return DEFAULT_SEC;
    }
  }

  // Remember the choice. Returns whether it was stored; a storage that throws only means the choice lasts until the page
  // is closed.
  function save(storage, sec) {
    try {
      if (!storage) return false;
      storage.setItem(STORAGE_KEY, String(normalize(sec)));
      return true;
    } catch (e) {
      return false;
    }
  }

  // A poll timer the page drives: createRefresh(poll, env) polls no more often than sec. `poll(force)` is the page's
  // fetch; env is { setTimer, clearTimer } (default: the page's setInterval/clearInterval). boot() calls poll(true) once
  // even when paused, so a page reloaded while paused still fills, then shows the paused message instead of polling. set()
  // changes the rate: resuming re-polls once at once and re-arms; only one timer ever stands. stop() clears the timer (a
  // test seam).
  function createRefresh(poll, env) {
    const E = env || { setTimer: (fn, ms) => setInterval(fn, ms), clearTimer: (h) => clearInterval(h) };
    let sec = DEFAULT_SEC, timer = 0, started = false;
    function arm() {
      if (timer) { E.clearTimer(timer); timer = 0; }
      if (sec !== 0) timer = E.setTimer(() => poll(false), sec * 1000);
    }
    return {
      sec() { return sec; },
      paused() { return sec === 0; },
      timer() { return timer; },
      boot(saved) {
        sec = normalize(saved);
        poll(true); // unconditional initial fetch: the page fills instead of sitting on "Looking for agents..."
        if (sec === 0) started = true; // the page then shows the paused message; no timer stands
        else arm();
        return sec;
      },
      set(next) {
        sec = normalize(next);
        arm(); // one timer at most: the old one is cleared first
        if (sec === 0) started = true;
        else poll(false); // resume (or a new rate) fetches at once instead of waiting out the interval
        return sec;
      },
      stop() {
        if (timer) { E.clearTimer(timer); timer = 0; }
      },
    };
  }

  const api = { OPTIONS, DEFAULT_SEC, STORAGE_KEY, normalize, label, load, save, createRefresh };
  if (typeof module !== "undefined" && module.exports) module.exports = api; else root.RefreshRate = api;
})(typeof self !== "undefined" ? self : this);
