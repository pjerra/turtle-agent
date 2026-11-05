# Observability and testing

The dashboard, captures, traces, events and scenarios: watch a run live, record what happened, and check it afterwards.

## Watch it live

- [Observability overview](/observability/overview.md) — what this area covers and how the pieces fit.
- [Watch your first run on the dashboard](/observability/first-dashboard.md) — start the page, open it, read a run card.
- [Start and run the dashboard](/observability/dashboard-guide.md) — flags, agents, folders, remote access.
- [Turn on route editing](/observability/route-editing-guide.md) — checked plan-route versions from the plan view.
- [Control a run from the dashboard](/observability/run-controls-guide.md) — pause, resume, skip, stop with tokens.
- [Replay a recording into a test daemon](/observability/dashreplay-guide.md) — drive the TortoiseBots dashboard without a server.

## Record what happened

- [Captures](/observability/captures-concept.md) — what a capture file holds and what it never holds.
- [Record and decode a capture](/observability/captures-guide.md) — `login --capture`, `scrub-check`, `capture-stats`, `capture events`.
- [Run traces](/observability/traces-concept.md) — the `bt-trace` file, its records, its summary.
- [Read the live event log](/observability/events-concept.md) — the `events` RPC, kinds, paging.
- [Draw the run map](/observability/map-guide.md) — `turtle-agent map` from a capture to SVG and HTML.
- [Decision captures](/observability/decisions-concept.md) — what the TortoiseBots daemon records and how to pull it.
- [Test fixtures](/observability/fixtures-concept.md) — the recorded captures the unit tests play back.

## Check it afterwards

- [Check a run with trace-score](/observability/trace-score-tutorial.md) — score one trace, tune one threshold, record the verdict.
- [Re-score a trace](/observability/trace-score-guide.md) — `trace-score` flags and output.
- [The trace checks](/observability/trace-checks-concept.md) — what each check measures, its threshold, when it answers n/a.
- [Score a finished fight](/observability/fight-score-guide.md) — `fight-score` and the debugger's tank readings.
- [Offline plan checks](/observability/offline-checks-guide.md) — `stage-check` and `trash-check` without a server.
- [Repeat a live test with scenarios](/observability/scenarios-guide.md) — the scenario file, `check`, `run`, `norm`.

## Reference
- [Command and flag reference](/observability/commands-reference.md) — every command and flag in this area, with defaults.

## When it breaks

- [Observability troubleshooting](/observability/troubleshooting.md) — real error messages, what they mean, the fix.
