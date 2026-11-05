---
type: Guide
title: Repeat a live test with scenarios
description: Write a scenario file, check it, run it against live agents, and re-apply its normaliser.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/scenario_cmd.go
tags: [scenarios, guide]
sources:
  - id: scenario-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/scenario_cmd.go
  - id: scenario-file
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/scenario/file.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:52:00+01:00
status: draft
---

# Repeat a live test with scenarios

A scenario is a repeatable live test: steps drive 1–8 running agents over JSON-RPC, built-in checks print PASS/FAIL, and `raw.txt` plus `norm.txt` diff cleanly before and after a deploy. The examples are `scenarios/lft-addon-path.json` and `scenarios/itemleg3-g1-items.json`.
Scenarios drive the same RPC the agent pages document ([the agent and its RPC](/agent/index.md)) and the combat they exercise is scored as in [trace checks](/observability/trace-checks-concept.md).

## 1. Write the file

Strict JSON, no comments. The top level holds `scenario` (lower-case name), `sessions` (each `{rpc, token_file}`; RPC must be loopback), and `steps`. Each step has `do` plus its fields:

| `do` | Does |
| --- | --- |
| `section` | prints `=== STEP n: <title>` and moves the match window |
| `mark` | moves the match window |
| `gm` | a `.`-command (`text`) |
| `bot` | a `.bot` command without `.bot` (`command`) |
| `addon_send` | an addon message (`text`, `channel`, `prefix`, `to`) |
| `rpc` | any agent method (`method`, `params`, `save`, `record_result`, `ignore_error`) |
| `sleep` | waits `seconds` (0–3600) |
| `wait` / `expect` / `expect_not` | match event lines (`session`, `kind`, `prefix`, `channel`, `source`, `match`, `capture`, `timeout_s`) |
| `check_db` | row counts or item counts over ssh (`table`, `where`, `rows`, `op`; or `item`, `count`) |
| `check_rpc` | `equals`, `match` or `len` over a result path |

Matching is ordered per session: each `expect` sees only later lines, while `expect_not` scans everything since the window. A failed check is FAIL (`on_fail` `continue` or `stop`); anything unevaluatable — an RPC or DB error, an unset var, a denied GM command, the deadline — is ERROR and aborts. World-changing GM commands (AGENTS.md) are refused at load and at run time. Variables are `${name}`: file `vars` (overridden by `--var`), `${S.character}` built-ins, `capture` groups and `save` paths; `$$` is a literal `$`.

## 2. Check the file

```sh
turtle-agent scenario check scenarios/lft-addon-path.json
turtle-agent scenario check --var plan=zulfarrak scenarios/itemleg3-g1-items.json
```

`check` loads the file and prints each step (`N <do> <short form>`). `--var NAME=VALUE` overrides a file var; the name must exist. Exit 2 on any load error.

## 3. Run it against live agents

```sh
turtle-agent scenario run --out <walk-dir>/lft-1 scenarios/lft-addon-path.json
turtle-agent scenario run --var plan=zulfarrak --session C=127.0.0.1:8771@<walk-dir>/rpc-8771.token scenarios/itemleg3-g1-items.json
```

Every `run` flag and its default:

| Flag | Default | What it does |
| --- | --- | --- |
| `--out` | `scenario-out/<scenario>-<UTC yyyymmddThhmmss>` | output directory; a non-empty one needs `--force` |
| `--force` | off | write into a non-empty `--out` |
| `--quiet` | off | print only PASS/FAIL/ERROR/SKIP lines and the summary |
| `--var` | none, repeatable | override a file var (the name must exist) |
| `--session` | none, repeatable | override a session: `NAME=ADDR[@TOKENFILE]` (the name must exist; the address is re-checked for loopback) |

The run writes `raw.txt` (seq, times, expanded text), `norm.txt` (templates, no timings, DB counts kept), `transcript.jsonl` (one object per line) and `result.json`. Exit 0 when every check passed, 14 when a check failed, 15 on abort or error, 2 on usage.

## 4. Re-apply changed normaliser rules

```sh
turtle-agent scenario norm scenarios/lft-addon-path.json <walk-dir>/lft-1
```

`norm` re-applies the file's (possibly edited) `normalise` rules to the stored `transcript.jsonl` and rewrites `norm.txt`. The run wrote `norm.txt` with the old rules; after you fix a rule, `norm` updates the artifact without re-running the live test. DB checks run `SELECT COUNT(*)` over ssh in a read-only transaction, from an allowlist of tables, and only the count comes back.
