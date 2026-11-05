# Observability and testing

The dashboard, captures, traces, events and scenarios: watch a run live, record what happened, and check it afterwards.

## Watch it live

- [Observability overview](overview.md) — what this area covers and how the pieces fit.
- [Watch your first run on the dashboard](first-dashboard.md) — start the page, open it, read a run card.
- [Start and run the dashboard](dashboard-guide.md) — flags, agents, folders, remote access.
- [Turn on route editing](route-editing-guide.md) — checked plan-route versions from the plan view.
- [Control a run from the dashboard](run-controls-guide.md) — pause, resume, skip, stop with tokens.
- [Replay a recording into a test daemon](dashreplay-guide.md) — drive the TortoiseBots dashboard without a server.

## Record what happened

- [Captures](captures-concept.md) — what a capture file holds, what it never holds, and the test fixtures.
- [Record and decode a capture](captures-guide.md) — `login --capture`, `scrub-check`, `capture-stats`, `capture events`.
- [Run traces](traces-concept.md) — the `bt-trace` file, its records, its summary.
- [Read the live event log](events-concept.md) — the `events` RPC, kinds, paging.
- [Draw the run map](map-guide.md) — `turtle-agent map` from a capture to SVG and HTML.
- [Decision captures](decisions-concept.md) — what the TortoiseBots daemon records and how to pull it.

## Check it afterwards

- [Check a run with trace-score](trace-score-tutorial.md) — score one trace, tune one threshold, record the verdict.
- [Re-score a trace](trace-score-guide.md) — `trace-score` flags and output.
- [The trace checks](trace-checks-concept.md) — what each check measures, its threshold, when it answers n/a.
- [Score a finished fight](fight-score-guide.md) — `fight-score` and the debugger's tank readings.
- [Offline plan checks](offline-checks-guide.md) — `stage-check` and `trash-check` without a server.
- [Repeat a live test with scenarios](scenarios-guide.md) — the scenario file, `check`, `run`, `norm`.

## Reference
- [Command and flag reference](commands-reference.md) — every command and flag in this area, with defaults.

## When it breaks

- [Observability troubleshooting](troubleshooting.md) — real error messages, what they mean, the fix.
