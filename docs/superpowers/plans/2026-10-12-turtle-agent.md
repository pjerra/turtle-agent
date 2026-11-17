# turtle-agent Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A headless Go client that logs one test character into the Tortoise WoW 1.18.1 server (vanilla 1.12 protocol plus Turtle changes), plays next to the TortoiseBots bots (chat, GM and `.bot` commands, party, target, attack, cast, short walks, GM teleport) and exposes what happens as an ordered event log over local JSON-RPC, so the wow-play-harness can run its MVP scenario with no game window.

**Architecture:** One Go process per logged-in character. `auth` does SRP6 against realmd (build 7272); `worldconn` frames and header-encrypts the world socket (build 5875, rolling XOR); `session` is a single-owner loop that dispatches packets to handlers, which update `objects` (units around the player) and append to `events` (ordered, sequence-numbered); `game` holds pure packet codecs; `rpc` serves loopback JSON-RPC 2.0 over HTTP; `cmd/turtle-agent` wires it together and has a capture mode that records decrypted world packets as test fixtures. A thin Python `HeadlessClient` in `~/wow-play-harness` gives testers the same surface as the real-client harness.

**Tech Stack:** Go 1.27.1 (pinned in `~/.local/go`, standard library only: `crypto/sha1`, `math/big`, `compress/zlib`, `net/http`, `encoding/json`), GNU make, Python 3.12 + pytest in the existing `~/wow-play-harness/.venv`.

**Spec:** docs/superpowers/specs/2026-10-12-turtle-agent-design.md

## Global Constraints

- Realmd logon challenge sends version 1.18.1, build **7272**, platform `x86`, OS `Win`, locale `enUS` (RealmList.cpp:39, AuthSocket.cpp:1528-1550).
- Logon proof is the 75-byte `sAuthLogonProof_C_1_11`; the proof reply is 26 bytes (2 + M2[20] + uint32) (AuthSocket.cpp:109-131).
- Realm list is the vanilla layout: `uint32 0`, `uint8` count, per realm `uint32` icon, `uint8` flags, name, address, float population, `uint8` characters, `uint8` timezone, `uint8 0`; trailer `uint16 2` (AuthSocket.cpp:1230-1281). Realm ID is synthesized from list order.
- `CMSG_AUTH_SESSION` sends build **5875** and a non-empty zlib addon block the anticheat accepts (WorldSocket.cpp:144-149, DBCStores.cpp:121-129, Anticheat/AddonHandler.cpp:98-118).
- World header cipher is the vanilla rolling XOR (`AuthCrypt`) keyed with K as `BigNumber::AsByteArray()` renders it (usually 40 bytes); server header 4 bytes (`u16 BE size + u16 opcode`), client header 6 bytes (`u16 BE size + u32 opcode`); no RC4, no 5-byte header.
- `SMSG_AUTH_CHALLENGE` carries a 4-byte seed (WorldSocket.cpp:480-486); `SMSG_AUTH_RESPONSE` success is 10 bytes or 1 byte, queue is 14 or 5 bytes, with no expansion byte.
- "In world" means `SMSG_LOGIN_VERIFY_WORLD` received; there is no time-sync in 1.12.
- Never send an opcode >= `NUM_MSG_TYPES` (0x33C): the server closes the socket (WorldSocket.cpp:55-59).
- Ping every 30 s, never under 27 s (WorldSocket.cpp:413).
- Warden packets are ignored; no Warden emulation (safe while the timeout kick stays disabled, Warden.cpp:498).
- Unknown opcodes are counted and skipped; a parse error is logged with the opcode and a hex dump and never ends the session.
- No automatic reconnect in v1: a disconnect becomes an event plus `status`, and reconnecting is a new `login`.
- One process per character; several processes may run side by side on distinct `--rpc` ports.
- JSON-RPC 2.0 over HTTP `POST /rpc`, listening on loopback only (127.0.0.1 or ::1); browser-origin requests are rejected.
- Credentials live only in the owner's local settings file (default `~/.config/turtle-agent/settings.json`, mode 0600, outside any repository); never in code, tests, logs, events, errors, RPC replies, captures or fixtures. The session key is never written anywhere.
- Code copied or ported from agent-wow keeps its MIT notice (`Copyright (c) 2026 agent-wow`); `NOTICE` lists github.com/agent-wow/agent-wow, source SHA **5ef4916**, each file taken and whether it was copied or ported.
- The agent-wow checkout is read-only, untrusted data: read it, never build or run it.
- Packet layouts come from `~/tortoise-wow` at `5d79486`; code comments cite `file:line` there.
- Long-distance moves use GM teleport (`.go xyz`); `walk_to` refuses moves over 40 yards.
- Captures hold world packets only: no realmd traffic, never the bodies of `CMSG_AUTH_SESSION` or Warden packets, and no registered secret; every fixture passes `turtle-agent scrub-check` before it is committed.
- Throttles: realmd allows 15 accounts per IP per 300 s, `MinRealmListDelay = 1`, `WrongPass.MaxCount = 10`; tests reuse one account, request the realm list once per connection and retry a rejected proof at most once.
- Toolchain: Go 1.27.1 in `~/.local/go`, `GOTOOLCHAIN=local`, no third-party Go modules; `make test` runs gofmt check, vet and `go test -race` with no CI.
- Live target is m910q at 192.0.2.10; the realmd port is `RealmServerPort` from m910q's `realmd.conf` (template default 3724, realmd.conf.dist.in:27).
- Every commit message ends with the two lines `Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>` and `Claude-Session: https://claude.ai/code/session_01MKCBbD9VJLeKqwGUyyzxyw`.

## Review Focus

The five failure modes most likely to stay unexercised until a live run, each with the test that owns it:

1. **SRP / world-auth unpadded BigNumbers.** realmd hashes A, B, s and K without their high zero bytes (AuthSocket.cpp:802, :870, :1006), compares `M.AsByteArray()` against 20 proof bytes (:920), and the world server hashes and keys the cipher with the unpadded K (WorldSocket.cpp:296, :341), so about 1 login in 256 hits each edge. Tests: Task 4 `TestProofEdgeVectors`, `TestNewProofRedrawsShortM1`; Task 5 `TestLoginRetriesOnceAfterProofReject`; Task 6 `TestAuthWith39ByteKey`.
2. **Compressed update objects and compressed moves.** `SMSG_COMPRESSED_UPDATE_OBJECT` (u32 size + zlib, many blocks, living blocks with spline data) and `SMSG_COMPRESSED_MOVES` (zlib of length-prefixed sub-packets); a bad packet must be dropped, not end the session. Tests: Task 19 `TestCompressedUpdateMultiBlock`, `TestSplineEnabledLivingBlock`, `TestTruncatedCompressedUpdate`, `TestCompressedMovesSplit`, and the capture-driven `TestCapturedUpdatesParse` (milestone 1 fixture, extended to milestone 4 in Task 22).
3. **Unknown or Turtle-extended packet fields.** Update masks with indices past the known fields, trailing bytes after the fields we read (`SMSG_GROUP_LIST` ends with a dungeon-difficulty byte, Group.cpp:1431), opcodes we do not handle. Tests: Task 19 `TestValuesBeyondKnownFields`; Task 16 `TestGroupListTrailingBytes`; Task 9 `TestUnknownOpcodeCountedAndParseErrorSurvives`; capture regressions in Tasks 11, 15, 18, 22, 25.
4. **Disconnect mid-session.** The server closes the socket while in world, during logout, or while an RPC call waits. Tests: Task 9 `TestDisconnectInWorld`, `TestDisconnectDuringLogout`; Task 14 `TestRPCAfterDisconnect`.
5. **`events(since)` pagination and ordering.** Exclusive `since`, contiguous sequence numbers, filters that still advance the cursor, limits, ring eviction, concurrent appends, a cursor from an earlier process, and name lookups that must not reorder events. Tests: Task 7 `TestSinceIsExclusiveAndContiguous`, `TestKindsFilterAdvancesNext`, `TestLimitAndMore`, `TestEvictionReportsDropped`, `TestConcurrentAppend`, `TestCursorAheadOfLog`; Task 13 `TestNameWaitKeepsOrder`.

---

## File Structure

All Go paths are relative to `~/turtle-agent`; Python paths are in `~/wow-play-harness`.

| File | Responsibility |
| :--- | :--- |
| `go.mod` | Module `github.com/TortoiseBots/turtle-agent`, `go 1.27.1`, no requirements |
| `Makefile` | `test` (gofmt check, vet, `go test -race`), `fmt`, `build`, `opcodes` |
| `.gitignore` | Ignores `bin/`, raw local captures in `captures/`, any `settings.json` |
| `NOTICE` | agent-wow MIT notice, source SHA 5ef4916, table of files taken |
| `cmd/turtle-agent/main.go` | CLI entry, subcommand switch, exit codes, usage |
| `cmd/turtle-agent/login.go` | `login`: realmd, world, session, capture, RPC server, logout on signal or `--stay` |
| `cmd/turtle-agent/methods.go` | Registers every JSON-RPC method on the session; maps session errors to RPC codes |
| `cmd/turtle-agent/capturecmd.go` | `scrub-check` and `capture-stats` subcommands |
| `cmd/turtle-agent/*_test.go` | CLI and method-registration tests |
| `tools/opcodegen/main.go` | Generates the opcode table from the core's `Opcodes_1_12_1.h` |
| `internal/packet/reader.go`, `writer.go`, `guid.go` | Little-endian payload codec, packed GUIDs, `GUID` type with hex JSON |
| `internal/opcode/opcode.go`, `opcodes_gen.go` | `Name()` and the generated 1.12.1 opcode constants |
| `internal/config/config.go` | Settings file: realmd address, accounts and passwords, RPC address, live-test inputs; redacted printing |
| `internal/auth/srp.go` | SRP6 client math with the server's unpadded BigNumber forms (ported from agent-wow) |
| `internal/auth/realmd.go` | Logon challenge, proof, realm list, `Login` with one retry, `Ticket` |
| `internal/worldconn/crypt.go` | Vanilla rolling-XOR header cipher |
| `internal/worldconn/conn.go` | World framing, auth challenge/session/response, capture tap |
| `internal/worldconn/addon.go` | zlib addon block for `CMSG_AUTH_SESSION` |
| `internal/worldconn/worldtest/server.go` | Scripted world server for tests (server side of the handshake and framing) |
| `internal/events/log.go` | Ordered event log, kinds, `Read`/`Wait` pagination |
| `internal/capture/capture.go` | TWCAP1 capture writer/reader, secret redaction, `Scan`, `Stats`, `Bodies` |
| `internal/session/session.go` | Owner loop, login sequence, ping, logout, disconnect, dispatch, status |
| `internal/session/charenum.go` | `SMSG_CHAR_ENUM` parser |
| `internal/session/names.go` | Name and creature-name cache, queries, order-preserving name wait |
| `internal/session/chat.go` | Chat handlers and `Chat`/`GM` actions |
| `internal/session/party.go` | Group handlers, party state, `Invite`/`AcceptInvite`/`LeaveGroup`/`Party` |
| `internal/session/world.go` | Update-object, destroy, monster-move and movement-relay handlers; `Units` |
| `internal/session/actions.go` | Target, attack, cast, face, walk, teleport and movement acks |
| `internal/session/combat.go` | Combat-log handlers, aura and death events, party flag |
| `internal/game/chat.go` | Chat types, `CMSG_MESSAGECHAT` builder, `SMSG_MESSAGECHAT` parser, colour stripping |
| `internal/game/query.go` | Name and creature query builders and response parsers |
| `internal/game/group.go` | Group invite, command result, group list, member stats codecs |
| `internal/game/movement.go` | `MoveInfo` (MovementInfo) codec and movement flags |
| `internal/game/actions.go` | Selection, attack, cast, facing, teleport/speed/root ack codecs |
| `internal/game/combat.go` | Combat-log parsers (melee, spell damage, periodic, heal, energize, spell start/go/fail/miss, kill) |
| `internal/game/gametest/server.go` | Combat packets written the way the core writes them, for game and session tests |
| `internal/objects/update.go` | `SMSG_(COMPRESSED_)UPDATE_OBJECT` block parser, zlib inflate |
| `internal/objects/moves.go` | `SMSG_COMPRESSED_MOVES` splitter and `SMSG_MONSTER_MOVE` parser |
| `internal/objects/fields.go` | Update-field indices used by the store |
| `internal/objects/store.go` | Object store, change detection, `Units` snapshot with filters |
| `internal/rpc/server.go` | Loopback JSON-RPC 2.0 HTTP server with a method registry (ported from agent-wow) |
| `internal/*/..._test.go` | Unit tests next to each package |
| `testdata/captures/m1-login.twcap` … `m6-fight.twcap` | Scrubbed captures recorded on m910q, one per milestone |
| `wowharness/headless.py` | `HeadlessClient`: chat, gm, bot, state, tail_combat_log, tail_chat, fight_report over JSON-RPC |
| `wowharness/mcp_headless.py` | MCP server with the harness tool names backed by `HeadlessClient` |
| `tools/headless_mvp.py` | The harness MVP scenario run headless |
| `tests/test_headless.py`, `tests/test_mcp_headless.py` | Python tests against a fake JSON-RPC server |

Conventions used by every task:

- Run Go tests with `make -C ~/turtle-agent fmt test` (gofmt rewrites first, then the check, vet and race tests). A single package: `cd ~/turtle-agent && ~/.local/go/bin/go test -race -count=1 ./internal/<pkg>/`.
- Before each commit run `git -C <repo> status --short` and stage only the files the task names; never stage, reset or discard changes you did not make.
- Commit with `git commit -F -` and a heredoc so the two trailer lines end the message, exactly as each task shows.

---

## Task 0: Toolchain and repository skeleton

**Files:**
- Create: `go.mod`, `Makefile`, `.gitignore`, `NOTICE`, `cmd/turtle-agent/main.go`
- Test: `cmd/turtle-agent/main_test.go`

**Interfaces:**
- Produces: `func run(ctx context.Context, args []string, stdout, stderr io.Writer) int`, `func usage(w io.Writer)`, constants `version`, `exitOK`, `exitUsage`, `exitAuth`, `exitRealm`, `exitWorld`, `exitDisconnected`, `exitScrub`.
- Consumes: nothing.

- [ ] **Step 1: Install Go 1.27.1 into `~/.local/go` with checksum verification**

The SHA-256 comes from the official download index, not from this plan:

```bash
GOVER=1.27.1
GOTGZ=go${GOVER}.linux-amd64.tar.gz
DL=$(mktemp -d)
curl -fsSL -o "$DL/$GOTGZ" "https://go.dev/dl/$GOTGZ"
curl -fsSL -o "$DL/index.json" "https://go.dev/dl/?mode=json&include=all"
WANT=$(python3 -I -c 'import json,sys; d=json.load(open(sys.argv[1])); print(next(f["sha256"] for r in d for f in r["files"] if f["filename"]==sys.argv[2]))' "$DL/index.json" "$GOTGZ")
echo "$WANT  $DL/$GOTGZ" | sha256sum -c -
rm -rf ~/.local/go && mkdir -p ~/.local && tar -C ~/.local -xzf "$DL/$GOTGZ"
~/.local/go/bin/go version
```

Expected: `sha256sum` prints `.../go1.27.1.linux-amd64.tar.gz: OK` and the last line is `go version go1.27.1 linux/amd64`. If `sha256sum -c` fails, stop: do not extract.

- [ ] **Step 2: Create `go.mod`, `Makefile`, `.gitignore`**

`~/turtle-agent/go.mod`:

```
module github.com/TortoiseBots/turtle-agent

go 1.27.1
```

`~/turtle-agent/Makefile` (recipe lines start with a TAB):

```make
GOROOT_LOCAL := $(HOME)/.local/go
GO := $(GOROOT_LOCAL)/bin/go
GOFMT := $(GOROOT_LOCAL)/bin/gofmt
CORE ?= $(HOME)/tortoise-wow
export GOTOOLCHAIN := local

.PHONY: test fmt fmt-check vet build opcodes

test: fmt-check vet
	$(GO) test -race -count=1 ./...

fmt:
	$(GOFMT) -w .

fmt-check:
	@out=$$($(GOFMT) -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

vet:
	$(GO) vet ./...

build:
	$(GO) build -o bin/turtle-agent ./cmd/turtle-agent

opcodes:
	$(GO) run ./tools/opcodegen -in $(CORE)/src/game/Protocol/Opcodes_1_12_1.h -out internal/opcode/opcodes_gen.go
```

`~/turtle-agent/.gitignore`:

```
/bin/
/captures/
settings.json
```

- [ ] **Step 3: Write the failing test**

`~/turtle-agent/cmd/turtle-agent/main_test.go`:

```go
package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunWithoutArgsPrintsUsage(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(context.Background(), nil, &out, &errOut); code != exitUsage {
		t.Fatalf("exit code %d, want %d", code, exitUsage)
	}
	if !strings.Contains(errOut.String(), "turtle-agent login") {
		t.Fatalf("usage missing: %q", errOut.String())
	}
}

func TestRunVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"version"}, &out, &errOut); code != exitOK {
		t.Fatalf("exit code %d", code)
	}
	if got := strings.TrimSpace(out.String()); got != "turtle-agent 0.1.0" {
		t.Fatalf("got %q", got)
	}
}
```

- [ ] **Step 4: Run it and see it fail**

Run: `make -C ~/turtle-agent test`
Expected: FAIL, `cmd/turtle-agent/main_test.go: undefined: run` (and `exitUsage`, `exitOK`).

- [ ] **Step 5: Implement `main.go`**

`~/turtle-agent/cmd/turtle-agent/main.go`:

```go
// Command turtle-agent is a headless WoW 1.12 / Tortoise 1.18.1 client for bot tests.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

const version = "0.1.0"

// Exit codes; the Python wrapper and shell scripts branch on them.
const (
	exitOK           = 0
	exitUsage        = 2 // bad flags, arguments or settings file
	exitAuth         = 3 // realmd rejected the login
	exitRealm        = 4 // realm missing or offline
	exitWorld        = 5 // world server rejected the session or the character
	exitDisconnected = 6 // the server closed the connection
	exitScrub        = 7 // scrub-check found a secret in a capture
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return exitUsage
	}
	switch args[0] {
	case "version":
		fmt.Fprintln(stdout, "turtle-agent", version)
		return exitOK
	default:
		usage(stderr)
		return exitUsage
	}
}

func usage(w io.Writer) {
	fmt.Fprintln(w, `usage:
  turtle-agent login [--config FILE] [--capture FILE] [--stay DURATION] [--rpc ADDR] <account> <character>
  turtle-agent scrub-check [--config FILE] <capture>...
  turtle-agent capture-stats <capture>
  turtle-agent version`)
}
```

- [ ] **Step 6: Run the tests**

Run: `make -C ~/turtle-agent fmt test`
Expected: PASS: `ok  github.com/TortoiseBots/turtle-agent/cmd/turtle-agent`.

- [ ] **Step 7: Create `NOTICE`**

`~/turtle-agent/NOTICE` (later tasks append one table row each when they port a file):

```
turtle-agent
Copyright (c) 2026 pjerra

turtle-agent contains code ported from agent-wow
(https://github.com/agent-wow/agent-wow), commit 5ef4916, used under the MIT License:

  MIT License

  Copyright (c) 2026 agent-wow

  Permission is hereby granted, free of charge, to any person obtaining a copy
  of this software and associated documentation files (the "Software"), to deal
  in the Software without restriction, including without limitation the rights
  to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
  copies of the Software, and to permit persons to whom the Software is
  furnished to do so, subject to the following conditions:

  The above copyright notice and this permission notice shall be included in all
  copies or substantial portions of the Software.

  THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
  IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
  FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
  AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
  LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
  OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
  SOFTWARE.

Files taken from agent-wow 5ef4916 (copied = unchanged apart from the header;
ported = structure kept, protocol details rewritten for the 1.12 core):

turtle-agent file | agent-wow source | how
```

- [ ] **Step 8: Commit**

```bash
cd ~/turtle-agent && git status --short
git add go.mod Makefile .gitignore NOTICE cmd/turtle-agent/main.go cmd/turtle-agent/main_test.go
git commit -F - <<'EOF'
Skeleton: Go 1.27.1 module, make test, CLI usage and version

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01MKCBbD9VJLeKqwGUyyzxyw
EOF
```

---

# Milestone 1: login, in world, clean logout, packet capture

## Task 1: Packet codec

**Files:**
- Create: `internal/packet/reader.go`, `internal/packet/writer.go`, `internal/packet/guid.go`
- Test: `internal/packet/packet_test.go`

**Interfaces:**
- Produces:
  - `type GUID uint64` with `High() uint16`, `Entry() uint32`, `IsPlayer() bool`, `IsCreature() bool`, `String() string`, `MarshalJSON() ([]byte, error)`, `UnmarshalJSON([]byte) error`; `func ParseGUID(s string) (GUID, error)`; constants `HighPlayer`, `HighGameObject`, `HighTransport`, `HighUnit`, `HighPet` (uint16).
  - `var ErrShort error`; `type Reader`; `func NewReader(b []byte) *Reader`; methods `U8() uint8`, `U16() uint16`, `U32() uint32`, `I32() int32`, `U64() uint64`, `F32() float32`, `GUID() GUID`, `PackedGUID() GUID`, `CString() string`, `Bytes(n int) []byte`, `Skip(n int)`, `Remaining() int`, `Offset() int`, `Err() error`.
  - `type Writer`; `func NewWriter() *Writer`; chainable `U8`, `U16`, `U32`, `I32`, `U64`, `F32`, `GUID(GUID)`, `PackedGUID(GUID)`, `CString(string)`, `Append([]byte)`; `Bytes() []byte`, `Len() int`.
- Consumes: nothing.

- [ ] **Step 1: Write the failing test**

`~/turtle-agent/internal/packet/packet_test.go`:

```go
package packet

import (
	"encoding/json"
	"errors"
	"math"
	"testing"
)

func TestWriterReaderRoundTrip(t *testing.T) {
	w := NewWriter().U8(0xAB).U16(0x1234).U32(0xDEADBEEF).I32(-5).U64(0x0102030405060708).
		F32(1.5).CString("Harnessb").GUID(0xF130000000001234).PackedGUID(0xF130000000001234).Append([]byte{9, 9})
	r := NewReader(w.Bytes())
	if r.U8() != 0xAB || r.U16() != 0x1234 || r.U32() != 0xDEADBEEF || r.I32() != -5 ||
		r.U64() != 0x0102030405060708 || r.F32() != 1.5 || r.CString() != "Harnessb" ||
		r.GUID() != 0xF130000000001234 || r.PackedGUID() != 0xF130000000001234 {
		t.Fatalf("round trip mismatch, err=%v", r.Err())
	}
	if got := r.Bytes(2); len(got) != 2 || got[0] != 9 || r.Remaining() != 0 || r.Err() != nil {
		t.Fatalf("tail %v remaining=%d err=%v", got, r.Remaining(), r.Err())
	}
}

// ByteBuffer::appendPackGUID (shared/ByteBuffer.h:430-447): a mask byte, then the non-zero bytes.
func TestPackedGUIDLayout(t *testing.T) {
	cases := []struct {
		g    GUID
		want []byte
	}{
		{0, []byte{0x00}},
		{0x17, []byte{0x01, 0x17}},
		{0xF130000000001234, []byte{0xC3, 0x34, 0x12, 0x30, 0xF1}},
	}
	for _, c := range cases {
		got := NewWriter().PackedGUID(c.g).Bytes()
		if string(got) != string(c.want) {
			t.Errorf("%s: got % X want % X", c.g, got, c.want)
		}
		if back := NewReader(got).PackedGUID(); back != c.g {
			t.Errorf("read back %s, want %s", back, c.g)
		}
	}
}

func TestShortReadIsStickyAndReportsOffset(t *testing.T) {
	r := NewReader([]byte{1, 2, 3})
	_ = r.U16()
	if v := r.U32(); v != 0 {
		t.Fatalf("short read returned %d", v)
	}
	if v := r.U8(); v != 0 {
		t.Fatalf("read after the first error returned %d", v)
	}
	if !errors.Is(r.Err(), ErrShort) {
		t.Fatalf("err = %v, want ErrShort", r.Err())
	}
	if r.Offset() != 2 {
		t.Fatalf("offset %d, want 2", r.Offset())
	}
}

func TestUnterminatedCString(t *testing.T) {
	r := NewReader([]byte("abc"))
	if s := r.CString(); s != "" || !errors.Is(r.Err(), ErrShort) {
		t.Fatalf("got %q err %v", s, r.Err())
	}
}

func TestGUIDParts(t *testing.T) {
	g := GUID(0xF13000064C001234) // HIGHGUID_UNIT (ObjectGuid.h:73), entry 0x64C, counter 0x1234
	if g.High() != HighUnit || g.Entry() != 0x64C || !g.IsCreature() || g.IsPlayer() {
		t.Fatalf("parts of %s wrong", g)
	}
	if p := GUID(0x2A); !p.IsPlayer() || p.IsCreature() {
		t.Fatal("player guid misclassified")
	}
}

func TestGUIDJSON(t *testing.T) {
	b, err := json.Marshal(struct{ G GUID }{0xF130000000001234})
	if err != nil || string(b) != `{"G":"0xF130000000001234"}` {
		t.Fatalf("marshal %s %v", b, err)
	}
	var v struct{ G GUID }
	if err := json.Unmarshal([]byte(`{"G":"0xf130000000001234"}`), &v); err != nil || v.G != 0xF130000000001234 {
		t.Fatalf("unmarshal %v %v", v.G, err)
	}
	if _, err := ParseGUID("zz"); err == nil {
		t.Fatal("ParseGUID accepted garbage")
	}
}

func TestFloatBits(t *testing.T) {
	if NewReader(NewWriter().F32(float32(math.Pi)).Bytes()).F32() != float32(math.Pi) {
		t.Fatal("float mismatch")
	}
}
```

- [ ] **Step 2: Run it and see it fail**

Run: `cd ~/turtle-agent && ~/.local/go/bin/go test ./internal/packet/`
Expected: FAIL, `undefined: NewWriter` (and `NewReader`, `GUID`, `ErrShort`).

- [ ] **Step 3: Implement**

`~/turtle-agent/internal/packet/reader.go`:

```go
// Package packet holds the little-endian payload codec shared by realmd and world code.
package packet

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// ErrShort reports a read past the end of a payload.
var ErrShort = errors.New("packet: short read")

// Reader decodes a payload. The first failure sticks: later reads return zero values
// and Err reports the offset of the first short read.
type Reader struct {
	b   []byte
	off int
	err error
}

func NewReader(b []byte) *Reader { return &Reader{b: b} }

func (r *Reader) take(n int) []byte {
	if r.err != nil {
		return nil
	}
	if n < 0 || len(r.b)-r.off < n {
		r.err = fmt.Errorf("%w: need %d bytes at offset %d, have %d", ErrShort, n, r.off, len(r.b)-r.off)
		return nil
	}
	s := r.b[r.off : r.off+n]
	r.off += n
	return s
}

func (r *Reader) U8() uint8 {
	if s := r.take(1); s != nil {
		return s[0]
	}
	return 0
}

func (r *Reader) U16() uint16 {
	if s := r.take(2); s != nil {
		return binary.LittleEndian.Uint16(s)
	}
	return 0
}

func (r *Reader) U32() uint32 {
	if s := r.take(4); s != nil {
		return binary.LittleEndian.Uint32(s)
	}
	return 0
}

func (r *Reader) I32() int32 { return int32(r.U32()) }

func (r *Reader) U64() uint64 {
	if s := r.take(8); s != nil {
		return binary.LittleEndian.Uint64(s)
	}
	return 0
}

func (r *Reader) F32() float32 { return math.Float32frombits(r.U32()) }

// GUID reads a full 8-byte ObjectGuid (`data << ObjectGuid` in the core).
func (r *Reader) GUID() GUID { return GUID(r.U64()) }

// PackedGUID reads ByteBuffer::readPackGUID's form (shared/ByteBuffer.h:354-370).
func (r *Reader) PackedGUID() GUID {
	mask := r.U8()
	var g uint64
	for i := 0; i < 8; i++ {
		if mask&(1<<i) != 0 {
			g |= uint64(r.U8()) << (8 * i)
		}
	}
	return GUID(g)
}

// CString reads a NUL-terminated string (ByteBuffer::operator<<(std::string), ByteBuffer.h:170-175).
func (r *Reader) CString() string {
	if r.err != nil {
		return ""
	}
	for i := r.off; i < len(r.b); i++ {
		if r.b[i] == 0 {
			s := string(r.b[r.off:i])
			r.off = i + 1
			return s
		}
	}
	r.err = fmt.Errorf("%w: unterminated string at offset %d", ErrShort, r.off)
	return ""
}

// Bytes returns a copy of the next n bytes.
func (r *Reader) Bytes(n int) []byte {
	s := r.take(n)
	if s == nil {
		return nil
	}
	return append([]byte(nil), s...)
}

func (r *Reader) Skip(n int)     { r.take(n) }
func (r *Reader) Remaining() int { return len(r.b) - r.off }
func (r *Reader) Offset() int    { return r.off }
func (r *Reader) Err() error     { return r.err }
```

`~/turtle-agent/internal/packet/writer.go`:

```go
package packet

import (
	"encoding/binary"
	"math"
)

// Writer builds a little-endian payload; every method returns the writer for chaining.
type Writer struct{ b []byte }

func NewWriter() *Writer { return &Writer{} }

func (w *Writer) U8(v uint8) *Writer   { w.b = append(w.b, v); return w }
func (w *Writer) U16(v uint16) *Writer { w.b = binary.LittleEndian.AppendUint16(w.b, v); return w }
func (w *Writer) U32(v uint32) *Writer { w.b = binary.LittleEndian.AppendUint32(w.b, v); return w }
func (w *Writer) I32(v int32) *Writer  { return w.U32(uint32(v)) }
func (w *Writer) U64(v uint64) *Writer { w.b = binary.LittleEndian.AppendUint64(w.b, v); return w }
func (w *Writer) F32(v float32) *Writer {
	return w.U32(math.Float32bits(v))
}
func (w *Writer) GUID(g GUID) *Writer        { return w.U64(uint64(g)) }
func (w *Writer) Append(p []byte) *Writer    { w.b = append(w.b, p...); return w }
func (w *Writer) CString(s string) *Writer   { w.b = append(append(w.b, s...), 0); return w }
func (w *Writer) Bytes() []byte              { return w.b }
func (w *Writer) Len() int                   { return len(w.b) }

// PackedGUID writes ByteBuffer::appendPackGUID's form (shared/ByteBuffer.h:430-447).
func (w *Writer) PackedGUID(g GUID) *Writer {
	mask := byte(0)
	var parts []byte
	for i := 0; i < 8; i++ {
		if b := byte(uint64(g) >> (8 * i)); b != 0 {
			mask |= 1 << i
			parts = append(parts, b)
		}
	}
	w.b = append(append(w.b, mask), parts...)
	return w
}
```

`~/turtle-agent/internal/packet/guid.go`:

```go
package packet

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// GUID is a 64-bit ObjectGuid; the high 16 bits are the HighGuid (ObjectGuid.h:68-77)
// and, for creatures and game objects, bits 24-47 hold the entry.
type GUID uint64

const (
	HighPlayer     uint16 = 0x0000
	HighGameObject uint16 = 0xF110
	HighTransport  uint16 = 0xF120
	HighUnit       uint16 = 0xF130
	HighPet        uint16 = 0xF140
)

func (g GUID) High() uint16      { return uint16(g >> 48) }
func (g GUID) Entry() uint32     { return uint32(g>>24) & 0x00FFFFFF }
func (g GUID) IsPlayer() bool    { return g != 0 && g.High() == HighPlayer }
func (g GUID) IsCreature() bool  { h := g.High(); return h == HighUnit || h == HighPet }
func (g GUID) String() string    { return fmt.Sprintf("0x%016X", uint64(g)) }

// MarshalJSON writes a hex string: JSON numbers lose precision above 2^53.
func (g GUID) MarshalJSON() ([]byte, error) { return json.Marshal(g.String()) }

func (g *GUID) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("guid must be a hex string like \"0xF130000000001234\": %w", err)
	}
	v, err := ParseGUID(s)
	if err != nil {
		return err
	}
	*g = v
	return nil
}

// ParseGUID accepts "0x"-prefixed or bare hex.
func ParseGUID(s string) (GUID, error) {
	v, err := strconv.ParseUint(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), "0x"), 16, 64)
	if err != nil {
		return 0, fmt.Errorf("bad guid %q", s)
	}
	return GUID(v), nil
}
```

- [ ] **Step 4: Run the tests**

Run: `make -C ~/turtle-agent fmt test`
Expected: PASS, `ok  github.com/TortoiseBots/turtle-agent/internal/packet`.

- [ ] **Step 5: Commit**

```bash
cd ~/turtle-agent && git status --short
git add internal/packet
git commit -F - <<'EOF'
packet: little-endian reader/writer, packed GUIDs, hex GUID JSON

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01MKCBbD9VJLeKqwGUyyzxyw
EOF
```

---

## Task 2: Opcode table generated from the core

**Files:**
- Create: `tools/opcodegen/main.go`, `internal/opcode/opcode.go`, `internal/opcode/opcodes_gen.go` (generated)
- Modify: `NOTICE` (append one row)
- Test: `tools/opcodegen/main_test.go`, `internal/opcode/opcode_test.go`

**Interfaces:**
- Produces: `func Generate(src []byte) ([]byte, error)` (package main of the tool); package `opcode` with `const NumMsgTypes uint16 = 0x33C`, one `uint16` constant per core opcode named by `goName` (`CMSG_CHAR_ENUM` → `CMSGCharEnum`, `SMSG_UPDATE_OBJECT` → `SMSGUpdateObject`, `MSG_MOVE_HEARTBEAT` → `MSGMoveHeartbeat`), and `func Name(op uint16) string`.
- Consumes: `~/tortoise-wow/src/game/Protocol/Opcodes_1_12_1.h` (826 opcodes, gaps at 0x329 and 0x33A, `NUM_MSG_TYPES = 0x33C` at line 829; sha256 `0e12b66d7a3c643484789b6d9c3b489f4c0b1a657cd6babc1232ec7701768d43`).

Opcode constants later tasks use (all generated by this task): `CMSGCharEnum`, `SMSGCharEnum`, `CMSGPlayerLogin`, `SMSGCharacterLoginFailed`, `SMSGNewWorld`, `SMSGTransferPending`, `CMSGLogoutRequest`, `SMSGLogoutResponse`, `SMSGLogoutComplete`, `CMSGNameQuery`, `SMSGNameQueryResponse`, `CMSGCreatureQuery`, `SMSGCreatureQueryResponse`, `CMSGGroupInvite`, `SMSGGroupInvite`, `CMSGGroupAccept`, `CMSGGroupDecline`, `SMSGGroupUninvite`, `CMSGGroupDisband`, `SMSGGroupDestroyed`, `SMSGGroupList`, `SMSGPartyMemberStats`, `SMSGPartyMemberStatsFull`, `SMSGPartyCommandResult`, `SMSGGroupSetLeader`, `CMSGMessagechat`, `SMSGMessagechat`, `SMSGUpdateObject`, `SMSGDestroyObject`, `MSGMoveStartForward`, `MSGMoveStop`, `MSGMoveSetFacing`, `MSGMoveHeartbeat`, `MSGMoveTeleport`, `MSGMoveTeleportAck`, `MSGMoveWorldportAck`, `SMSGMonsterMove`, `SMSGForceRunSpeedChange`, `CMSGForceRunSpeedChangeAck`, `SMSGForceRunBackSpeedChange`, `CMSGForceRunBackSpeedChangeAck`, `SMSGForceSwimSpeedChange`, `CMSGForceSwimSpeedChangeAck`, `SMSGForceWalkSpeedChange`, `CMSGForceWalkSpeedChangeAck`, `SMSGForceSwimBackSpeedChange`, `CMSGForceSwimBackSpeedChangeAck`, `SMSGForceTurnRateChange`, `CMSGForceTurnRateChangeAck`, `SMSGForceMoveRoot`, `CMSGForceMoveRootAck`, `SMSGForceMoveUnroot`, `CMSGForceMoveUnrootAck`, `CMSGCastSpell`, `SMSGCastResult`, `SMSGSpellStart`, `SMSGSpellGo`, `SMSGSpellFailedOther`, `CMSGSetSelection`, `CMSGAttackswing`, `CMSGAttackstop`, `SMSGAttackstart`, `SMSGAttackstop`, `SMSGAttackswingNotinrange`, `SMSGAttackswingBadfacing`, `SMSGAttackswingNotstanding`, `SMSGAttackswingDeadtarget`, `SMSGAttackswingCantAttack`, `SMSGAttackerstateupdate`, `SMSGSpellheallog`, `SMSGSpellenergizelog`, `SMSGNotification`, `CMSGPing`, `SMSGPong`, `SMSGAuthChallenge`, `CMSGAuthSession`, `SMSGAuthResponse`, `SMSGPartykilllog`, `SMSGCompressedUpdateObject`, `SMSGLoginVerifyWorld`, `SMSGSpelllogmiss`, `SMSGPeriodicauralog`, `SMSGSpellnonmeleedamagelog`, `CMSGSetActiveMover`, `SMSGChatPlayerNotFound`, `SMSGWardenData`, `CMSGWardenData`, `SMSGAddonInfo`, `SMSGCompressedMoves`, `CMSGRequestPartyMemberStats`, `SMSGInitWorldStates` (a test's unhandled opcode), and the movement relays `MSGMoveStartBackward`, `MSGMoveStartStrafeLeft`, `MSGMoveStartStrafeRight`, `MSGMoveStopStrafe`, `MSGMoveJump`, `MSGMoveStartTurnLeft`, `MSGMoveStartTurnRight`, `MSGMoveStopTurn`, `MSGMoveSetRunMode`, `MSGMoveSetWalkMode`, `MSGMoveFallLand`, `MSGMoveStartSwim`, `MSGMoveStopSwim`.

- [ ] **Step 1: Write the failing tests**

`~/turtle-agent/tools/opcodegen/main_test.go`:

```go
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sample = `enum OpcodesList
{
    MSG_NULL_ACTION                                 = 0x000,
    CMSG_BOOTME                                     = 0x001,
    SMSG_AUTH_CHALLENGE                             = 0x003, // gap at 0x002
    NUM_MSG_TYPES                                   = 0x004
};`

func flat(b []byte) string { return strings.Join(strings.Fields(string(b)), " ") }

func TestGenerateKeepsGapsAndLimit(t *testing.T) {
	out, err := Generate([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	s := flat(out)
	for _, want := range []string{
		"const NumMsgTypes uint16 = 0x004",
		"MSGNullAction uint16 = 0x000 // MSG_NULL_ACTION",
		"CMSGBootme uint16 = 0x001 // CMSG_BOOTME",
		"SMSGAuthChallenge uint16 = 0x003 // SMSG_AUTH_CHALLENGE",
		`0x003: "SMSG_AUTH_CHALLENGE",`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestGenerateRejectsBadInput(t *testing.T) {
	for name, src := range map[string]string{
		"decreasing": "enum OpcodesList\n{\n  CMSG_A = 0x002,\n  CMSG_B = 0x001,\n  NUM_MSG_TYPES = 0x003\n};",
		"duplicate":  "enum OpcodesList\n{\n  CMSG_A = 0x001,\n  CMSG_A = 0x002,\n  NUM_MSG_TYPES = 0x003\n};",
		"no limit":   "enum OpcodesList\n{\n  CMSG_A = 0x001\n};",
		"garbage":    "enum OpcodesList\n{\n  CMSG_A = 0x001,\n  int x;\n  NUM_MSG_TYPES = 0x002\n};",
	} {
		if _, err := Generate([]byte(src)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// The checked-in table must match the core header when the core is on this machine.
func TestGeneratedTableMatchesCore(t *testing.T) {
	home, _ := os.UserHomeDir()
	src, err := os.ReadFile(filepath.Join(home, "tortoise-wow/src/game/Protocol/Opcodes_1_12_1.h"))
	if err != nil {
		t.Skipf("core header not present: %v", err)
	}
	want, err := Generate(src)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile("../../internal/opcode/opcodes_gen.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("internal/opcode/opcodes_gen.go is stale: run make opcodes")
	}
}
```

`~/turtle-agent/internal/opcode/opcode_test.go`:

```go
package opcode

import "testing"

// Expected values with their line in ~/tortoise-wow/src/game/Protocol/Opcodes_1_12_1.h.
func TestKnownOpcodes(t *testing.T) {
	cases := []struct {
		got, want uint16
		line      int
	}{
		{CMSGCharEnum, 0x037, 58}, {SMSGCharEnum, 0x03B, 62}, {CMSGPlayerLogin, 0x03D, 64},
		{CMSGLogoutRequest, 0x04B, 78}, {SMSGLogoutComplete, 0x04D, 80}, {CMSGNameQuery, 0x050, 83},
		{CMSGGroupInvite, 0x06E, 113}, {SMSGGroupList, 0x07D, 128}, {CMSGMessagechat, 0x095, 152},
		{SMSGMessagechat, 0x096, 153}, {SMSGUpdateObject, 0x0A9, 172}, {MSGMoveHeartbeat, 0x0EE, 241},
		{CMSGCastSpell, 0x12E, 305}, {CMSGAttackswing, 0x141, 324}, {SMSGAttackerstateupdate, 0x14A, 333},
		{CMSGPing, 0x1DC, 479}, {SMSGAuthChallenge, 0x1EC, 495}, {CMSGAuthSession, 0x1ED, 496},
		{SMSGAuthResponse, 0x1EE, 497}, {SMSGCompressedUpdateObject, 0x1F6, 505},
		{SMSGLoginVerifyWorld, 0x236, 569}, {SMSGPeriodicauralog, 0x24E, 593},
		{SMSGSpellnonmeleedamagelog, 0x250, 595}, {SMSGWardenData, 0x2E6, 745},
		{SMSGAddonInfo, 0x2EF, 754}, {SMSGCompressedMoves, 0x2FB, 766},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("Opcodes_1_12_1.h:%d: got 0x%03X want 0x%03X", c.line, c.got, c.want)
		}
	}
	if NumMsgTypes != 0x33C {
		t.Errorf("NumMsgTypes = 0x%03X, want 0x33C", NumMsgTypes)
	}
	if Name(0x095) != "CMSG_MESSAGECHAT" || Name(0x329) != "UNKNOWN_0x329" {
		t.Errorf("names: %s %s", Name(0x095), Name(0x329))
	}
}
```

- [ ] **Step 2: Run them and see them fail**

Run: `cd ~/turtle-agent && ~/.local/go/bin/go test ./tools/opcodegen/ ./internal/opcode/`
Expected: FAIL, `undefined: Generate` and `undefined: CMSGCharEnum` (no non-test files yet).

- [ ] **Step 3: Implement the generator and `Name`**

`~/turtle-agent/tools/opcodegen/main.go`:

```go
// Copyright (c) 2026 agent-wow
// SPDX-License-Identifier: MIT
// Ported from github.com/agent-wow/agent-wow tools/opcodecatalog/main.go at 5ef4916
// (see NOTICE): the enum-walk-and-assignment-regex shape is kept; it now reads the
// core's `enum OpcodesList` and allows gaps, which the 1.12 table has at 0x329 and 0x33A.

// Command opcodegen writes internal/opcode/opcodes_gen.go from Opcodes_1_12_1.h.
package main

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"go/format"
	"os"
	"regexp"
	"strconv"
	"strings"
)

func main() {
	in := flag.String("in", "", "path to the core's src/game/Protocol/Opcodes_1_12_1.h")
	out := flag.String("out", "internal/opcode/opcodes_gen.go", "output file")
	flag.Parse()
	src, err := os.ReadFile(*in)
	if err == nil {
		var code []byte
		if code, err = Generate(src); err == nil {
			err = os.WriteFile(*out, code, 0o644)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "opcodegen:", err)
		os.Exit(1)
	}
}

var assignment = regexp.MustCompile(`^\s*([A-Z][A-Z0-9_]+)\s*=\s*(0x[0-9A-Fa-f]+)\s*,?\s*(//.*)?$`)

type entry struct {
	goName, cName string
	value         uint16
}

// Generate turns the enum into Go constants plus a name table.
func Generate(src []byte) ([]byte, error) {
	start := bytes.Index(src, []byte("enum OpcodesList"))
	if start < 0 {
		return nil, errors.New("enum OpcodesList not found")
	}
	body := src[start:]
	open, end := bytes.IndexByte(body, '{'), bytes.Index(body, []byte("};"))
	if open < 0 || end < open {
		return nil, errors.New("enum OpcodesList has no body")
	}
	var entries []entry
	seen := map[string]bool{}
	prev, limit := -1, -1
	for _, line := range strings.Split(string(body[open+1:end]), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		m := assignment.FindStringSubmatch(line)
		if m == nil {
			return nil, fmt.Errorf("unexpected line %q", strings.TrimSpace(line))
		}
		v, err := strconv.ParseUint(m[2], 0, 16)
		if err != nil {
			return nil, err
		}
		if int(v) <= prev {
			return nil, fmt.Errorf("%s = %s is not above the previous value", m[1], m[2])
		}
		prev = int(v)
		if m[1] == "NUM_MSG_TYPES" {
			limit = int(v)
			continue
		}
		name := goName(m[1])
		if seen[name] {
			return nil, fmt.Errorf("duplicate Go name %s (%s)", name, m[1])
		}
		seen[name] = true
		entries = append(entries, entry{name, m[1], uint16(v)})
	}
	if limit < 0 {
		return nil, errors.New("NUM_MSG_TYPES missing")
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "// Code generated by tools/opcodegen from Opcodes_1_12_1.h (sha256 %x); DO NOT EDIT.\n\n", sha256.Sum256(src))
	b.WriteString("package opcode\n\n")
	fmt.Fprintf(&b, "// NumMsgTypes is one past the last opcode; the server closes the socket at or above it.\nconst NumMsgTypes uint16 = 0x%03X\n\nconst (\n", limit)
	for _, e := range entries {
		fmt.Fprintf(&b, "\t%s uint16 = 0x%03X // %s\n", e.goName, e.value, e.cName)
	}
	b.WriteString(")\n\nvar names = map[uint16]string{\n")
	for _, e := range entries {
		fmt.Fprintf(&b, "\t0x%03X: %q,\n", e.value, e.cName)
	}
	b.WriteString("}\n")
	return format.Source(b.Bytes())
}

// goName keeps the CMSG/SMSG/MSG prefix and title-cases the rest: CMSG_CHAR_ENUM -> CMSGCharEnum.
func goName(c string) string {
	parts := strings.Split(c, "_")
	var b strings.Builder
	b.WriteString(parts[0])
	for _, p := range parts[1:] {
		if p != "" {
			b.WriteString(strings.ToUpper(p[:1]) + strings.ToLower(p[1:]))
		}
	}
	return b.String()
}
```

`~/turtle-agent/internal/opcode/opcode.go`:

```go
// Package opcode is the 1.12.1 world opcode table, generated from the core's
// src/game/Protocol/Opcodes_1_12_1.h by tools/opcodegen (make opcodes).
package opcode

import "fmt"

// Name returns the core's name for op, or UNKNOWN_0x### for gaps and values past the table.
func Name(op uint16) string {
	if n, ok := names[op]; ok {
		return n
	}
	return fmt.Sprintf("UNKNOWN_0x%03X", op)
}
```

- [ ] **Step 4: Generate the table**

Run: `make -C ~/turtle-agent opcodes && head -5 ~/turtle-agent/internal/opcode/opcodes_gen.go && grep -c 'uint16 = 0x' ~/turtle-agent/internal/opcode/opcodes_gen.go`
Expected: the first line is `// Code generated by tools/opcodegen from Opcodes_1_12_1.h (sha256 0e12b66d7a3c643484789b6d9c3b489f4c0b1a657cd6babc1232ec7701768d43); DO NOT EDIT.` and the count is `827` (826 opcodes plus `NumMsgTypes`).

- [ ] **Step 5: Run the tests**

Run: `make -C ~/turtle-agent fmt test`
Expected: PASS for `tools/opcodegen` and `internal/opcode` (`TestGeneratedTableMatchesCore` runs, not skipped, because `~/tortoise-wow` exists).

- [ ] **Step 6: Record provenance and commit**

Append to `~/turtle-agent/NOTICE`:

```
tools/opcodegen/main.go | tools/opcodecatalog/main.go | ported (enum walk and assignment regex kept; 1.12 enum, gaps allowed, name table added)
```

```bash
cd ~/turtle-agent && git status --short
git add tools/opcodegen internal/opcode NOTICE
git commit -F - <<'EOF'
opcode: 1.12.1 table generated from the core's Opcodes_1_12_1.h

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01MKCBbD9VJLeKqwGUyyzxyw
EOF
```

---

## Task 3: Settings file with credentials

**Files:**
- Create: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces: `type Spot struct { Map uint32; X, Y, Z float32 }`; `type Settings struct { Realmd, Realm, WorldAddr, RPC string; Accounts map[string]string; Bots []string; TestSpell uint32; Spot *Spot }`; `const DefaultRPC = "127.0.0.1:8765"`; `func DefaultPath() string`; `func Load(path string) (Settings, error)`; `func (s Settings) Password(account string) (string, error)`; `func (s Settings) String() string`; `func (s Settings) GoString() string`.
- Consumes: nothing.

The file the owner writes (JSON object, mode 0600): `realmd` (`"192.0.2.10:<RealmServerPort>"`), `accounts` (account name → password), optional `realm` (realm name, empty = first realm), `world_addr` (overrides the address in the realm list), `rpc` (loopback `host:port`), and the live-test inputs `bots` (two bot character names), `test_spell` (a spell id the test character knows) and `spot` (`{"map":…,"x":…,"y":…,"z":…}` near hostile mobs).

- [ ] **Step 1: Write the failing test**

`~/turtle-agent/internal/config/config_test.go`:

```go
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSettings(t *testing.T, mode os.FileMode, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil { // WriteFile honours the umask
		t.Fatal(err)
	}
	return p
}

const good = `{"realmd":"127.0.0.1:3724","accounts":{"Harness":"s3cret-pw"},"bots":["Gorm","Lia"],
"test_spell":133,"spot":{"map":0,"x":1.5,"y":2.5,"z":3.5}}`

func TestLoadDefaultsAndLookup(t *testing.T) {
	s, err := Load(writeSettings(t, 0o600, good))
	if err != nil {
		t.Fatal(err)
	}
	if s.RPC != DefaultRPC || s.TestSpell != 133 || s.Spot == nil || s.Spot.Y != 2.5 || len(s.Bots) != 2 {
		t.Fatalf("loaded %v", s)
	}
	if pw, err := s.Password("HARNESS"); err != nil || pw != "s3cret-pw" {
		t.Fatalf("lookup: %v", err)
	}
	if _, err := s.Password("other"); err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("missing account: %v", err)
	}
}

func TestRejectsGroupReadableFile(t *testing.T) {
	_, err := Load(writeSettings(t, 0o644, good))
	if err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("err = %v", err)
	}
}

func TestRejectsBadSettings(t *testing.T) {
	for name, body := range map[string]string{
		"unknown field": `{"realmd":"h:1","accounts":{"a":"b"},"pasword":"x"}`,
		"no port":       `{"realmd":"192.0.2.10","accounts":{"a":"b"}}`,
		"public rpc":    `{"realmd":"h:1","accounts":{"a":"b"},"rpc":"0.0.0.0:8765"}`,
		"no accounts":   `{"realmd":"h:1","accounts":{}}`,
		"empty pw":      `{"realmd":"h:1","accounts":{"a":""}}`,
	} {
		if _, err := Load(writeSettings(t, 0o600, body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestPasswordsNeverFormatted(t *testing.T) {
	s, err := Load(writeSettings(t, 0o600, good))
	if err != nil {
		t.Fatal(err)
	}
	for _, out := range []string{fmt.Sprintf("%v", s), fmt.Sprintf("%+v", s), fmt.Sprintf("%#v", s), s.String()} {
		if strings.Contains(out, "s3cret") {
			t.Fatalf("password leaked: %s", out)
		}
	}
}

func TestParseErrorDoesNotEchoContent(t *testing.T) {
	_, err := Load(writeSettings(t, 0o600, `{"realmd":"h:1","accounts":{"a":s3cret-pw}}`))
	if err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("err = %v", err)
	}
}
```

- [ ] **Step 2: Run it and see it fail**

Run: `cd ~/turtle-agent && ~/.local/go/bin/go test ./internal/config/`
Expected: FAIL, `undefined: Load` (and `DefaultRPC`).

- [ ] **Step 3: Implement**

`~/turtle-agent/internal/config/config.go`:

```go
// Package config loads the local settings file. It holds account passwords, so no
// value read here may reach a log line, an error, an event or a capture.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Spot is a place near hostile mobs used by the live checks (map id and world coordinates).
type Spot struct {
	Map uint32  `json:"map"`
	X   float32 `json:"x"`
	Y   float32 `json:"y"`
	Z   float32 `json:"z"`
}

type Settings struct {
	Realmd    string            `json:"realmd"`
	Realm     string            `json:"realm,omitempty"`
	WorldAddr string            `json:"world_addr,omitempty"`
	RPC       string            `json:"rpc,omitempty"`
	Accounts  map[string]string `json:"accounts"`
	Bots      []string          `json:"bots,omitempty"`
	TestSpell uint32            `json:"test_spell,omitempty"`
	Spot      *Spot             `json:"spot,omitempty"`
}

const DefaultRPC = "127.0.0.1:8765"

func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".config", "turtle-agent", "settings.json")
	}
	return filepath.Join(home, ".config", "turtle-agent", "settings.json")
}

func Load(path string) (Settings, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Settings{}, fmt.Errorf("settings %s: %w", path, err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return Settings{}, fmt.Errorf("settings %s has mode %04o; it holds passwords, run: chmod 600 %s", path, perm, path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Settings{}, fmt.Errorf("settings %s: %w", path, err)
	}
	var s Settings
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		detail := ""
		if msg := err.Error(); strings.HasPrefix(msg, "json: unknown field ") {
			detail = ": " + strings.TrimPrefix(msg, "json: ") // field names are not secret
		}
		return Settings{}, fmt.Errorf("settings %s is not valid settings JSON%s", path, detail)
	}
	if s.RPC == "" {
		s.RPC = DefaultRPC
	}
	if err := s.validate(); err != nil {
		return Settings{}, fmt.Errorf("settings %s: %w", path, err)
	}
	return s, nil
}

func (s Settings) validate() error {
	if host, port, err := net.SplitHostPort(s.Realmd); err != nil || host == "" || port == "" {
		return errors.New(`"realmd" must be host:port (RealmServerPort from the server's realmd.conf)`)
	}
	if s.WorldAddr != "" {
		if _, _, err := net.SplitHostPort(s.WorldAddr); err != nil {
			return errors.New(`"world_addr" must be host:port`)
		}
	}
	h, _, err := net.SplitHostPort(s.RPC)
	if err != nil {
		return errors.New(`"rpc" must be host:port`)
	}
	if h == "localhost" {
		h = "127.0.0.1"
	}
	if ip := net.ParseIP(h); ip == nil || !ip.IsLoopback() {
		return errors.New(`"rpc" must be a loopback address (127.0.0.1 or ::1)`)
	}
	if len(s.Accounts) == 0 {
		return errors.New(`"accounts" must map at least one account name to its password`)
	}
	for name, pw := range s.Accounts {
		if name == "" || pw == "" {
			return errors.New(`"accounts" has an empty account name or password`)
		}
	}
	return nil
}

// Password looks the account up case-insensitively (realmd upper-cases account names).
func (s Settings) Password(account string) (string, error) {
	for name, pw := range s.Accounts {
		if strings.EqualFold(name, account) {
			return pw, nil
		}
	}
	return "", fmt.Errorf("account %q is not in the settings file", account)
}

// String keeps passwords out of %v, %+v and %s formatting.
func (s Settings) String() string {
	names := make([]string, 0, len(s.Accounts))
	for n := range s.Accounts {
		names = append(names, n)
	}
	sort.Strings(names)
	return fmt.Sprintf("Settings{realmd=%s realm=%q rpc=%s accounts=%v passwords=redacted}", s.Realmd, s.Realm, s.RPC, names)
}

// GoString keeps passwords out of %#v.
func (s Settings) GoString() string { return s.String() }
```

- [ ] **Step 4: Run the tests**

Run: `make -C ~/turtle-agent fmt test`
Expected: PASS, `ok  github.com/TortoiseBots/turtle-agent/internal/config`.

- [ ] **Step 5: Commit**

```bash
cd ~/turtle-agent && git status --short
git add internal/config
git commit -F - <<'EOF'
config: settings file with accounts, loopback RPC, redacted printing

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01MKCBbD9VJLeKqwGUyyzxyw
EOF
```

---

## Task 4: SRP6 with the server's unpadded BigNumber forms

**Files:**
- Create: `internal/auth/srp.go`
- Modify: `NOTICE` (append one row)
- Test: `internal/auth/srp_test.go`

**Interfaces:**
- Produces: `type Challenge struct { B [32]byte; Salt []byte }`; `type Proof struct { A [32]byte; M1, M2 [20]byte; K [40]byte }`; `func (p Proof) KeyBytes() []byte`; `func ComputeProof(user, pass string, c Challenge, a *big.Int) (Proof, error)`; `func NewProof(user, pass string, c Challenge) (Proof, error)`; unexported `newProofFrom(rng io.Reader, user, pass string, c Challenge) (Proof, error)`, `asBytes(le []byte) []byte`, `srpN`, `srpG`, `toLE32`, `fromLE`, `digest`.
- Consumes: nothing.

The server side being mirrored (all `~/tortoise-wow/src/realmd/AuthSocket.cpp` unless noted): N and g at :174-175; v from x = SHA1(s.AsByteArray(), SHA1(I:P)) at :316-330; B = 3v + g^b at :601-603; B padded to 32 bytes on the wire at :611, s unpadded at :616; u = SHA1(A, B) over unpadded forms at :801-805; S padded to 32 and interleaved into K at :808-841; M1 = SHA1(H(N) xor H(g), H(I), s, A, B, K) over unpadded forms at :845-873; the comparison `memcmp(M.AsByteArray().data(), lp.M1, 20)` at :920; M2 = SHA1(A, M, K) at :1005-1007. `AsByteArray()` is little-endian with high zero bytes dropped (`shared/Auth/BigNumber.cpp:164-183`).

- [ ] **Step 1: Write the failing test**

`~/turtle-agent/internal/auth/srp_test.go`:

```go
package auth

import (
	"bytes"
	"crypto/sha1"
	"math/big"
	"math/rand/v2"
	"strings"
	"testing"
)

// fakeRealm is the server side of AuthSocket.cpp with BigNumber semantics, written
// independently of srp.go so the tests compare two implementations.
type fakeRealm struct {
	user          string
	s, v, b, bigB *big.Int
}

// bnBytes is BigNumber::AsByteArray(minSize) (BigNumber.cpp:164-183).
func bnBytes(n *big.Int, minSize int) []byte {
	be := n.Bytes()
	if len(be) < minSize {
		be = append(make([]byte, minSize-len(be)), be...)
	}
	out := make([]byte, len(be))
	for i := range be {
		out[i] = be[len(be)-1-i]
	}
	return out
}

func bnFromLE(b []byte) *big.Int {
	be := make([]byte, len(b))
	for i := range b {
		be[i] = b[len(b)-1-i]
	}
	return new(big.Int).SetBytes(be)
}

func sh(parts ...[]byte) []byte {
	h := sha1.New()
	for _, p := range parts {
		h.Write(p)
	}
	return h.Sum(nil)
}

func newFakeRealm(user, pass string, salt, b *big.Int) *fakeRealm {
	f := &fakeRealm{user: strings.ToUpper(user), s: salt, b: b}
	x := bnFromLE(sh(bnBytes(salt, 0), sh([]byte(f.user+":"+strings.ToUpper(pass))))) // :316-329
	f.v = new(big.Int).Exp(srpG, x, srpN)
	gmod := new(big.Int).Exp(srpG, b, srpN)
	f.bigB = new(big.Int).Mod(new(big.Int).Add(new(big.Int).Mul(f.v, big.NewInt(3)), gmod), srpN) // :603
	return f
}

func (f *fakeRealm) challenge() Challenge {
	var c Challenge
	copy(c.B[:], bnBytes(f.bigB, 32)) // :611
	c.Salt = bnBytes(f.s, 0)          // :616
	return c
}

// verify is _HandleLogonProof (:790-873, :920, :1005-1009).
func (f *fakeRealm) verify(a32 [32]byte, m1 [20]byte) (ok bool, m2, key []byte) {
	A := bnFromLE(a32[:])
	u := bnFromLE(sh(bnBytes(A, 0), bnBytes(f.bigB, 0)))
	S := new(big.Int).Exp(new(big.Int).Mul(A, new(big.Int).Exp(f.v, u, srpN)), f.b, srpN)
	t := bnBytes(S, 32)
	var t1 [16]byte
	for i := range t1 {
		t1[i] = t[i*2]
	}
	even := sh(t1[:])
	for i := range t1 {
		t1[i] = t[i*2+1]
	}
	odd := sh(t1[:])
	vK := make([]byte, 40)
	for i := 0; i < 20; i++ {
		vK[i*2], vK[i*2+1] = even[i], odd[i]
	}
	K := bnFromLE(vK)
	hash := sh(bnBytes(srpN, 0))
	hg := sh(bnBytes(srpG, 0))
	for i := range hash {
		hash[i] ^= hg[i]
	}
	t3 := bnFromLE(hash)
	M := bnFromLE(sh(bnBytes(t3, 0), sh([]byte(f.user)), bnBytes(f.s, 0), bnBytes(A, 0), bnBytes(f.bigB, 0), bnBytes(K, 0)))
	mb := bnBytes(M, 0)
	if len(mb) != 20 || !bytes.Equal(mb, m1[:]) { // memcmp over M.AsByteArray(): a 19-byte M never matches
		return false, nil, nil
	}
	return true, sh(bnBytes(A, 0), bnBytes(M, 0), bnBytes(K, 0)), bnBytes(K, 0)
}

// bigFrom is a deterministic 256-bit value with the top bit set, like BN_rand(256, 0, 1).
func bigFrom(seed uint64) *big.Int {
	var key [32]byte
	key[0], key[1], key[2] = byte(seed), byte(seed>>8), byte(seed>>16)
	b := make([]byte, 32)
	rand.NewChaCha8(key).Read(b)
	b[0] |= 0x80
	return new(big.Int).SetBytes(b)
}

func TestProofMatchesServer(t *testing.T) {
	for i := uint64(1); i <= 50; i++ {
		f := newFakeRealm("harness", "pw", bigFrom(i), bigFrom(1000+i))
		p, err := ComputeProof("Harness", "PW", f.challenge(), bigFrom(2000+i))
		if err != nil {
			t.Fatal(err)
		}
		if p.M1[19] == 0 {
			continue // the server cannot match it; TestNewProofRedrawsShortM1 covers this
		}
		ok, m2, key := f.verify(p.A, p.M1)
		if !ok || !bytes.Equal(m2, p.M2[:]) || !bytes.Equal(key, p.KeyBytes()) {
			t.Fatalf("seed %d: proof, M2 or K differ from the server", i)
		}
	}
}

// Review Focus 1: A, B and K with a zero high byte are hashed without it.
func TestProofEdgeVectors(t *testing.T) {
	edges := []struct {
		name string
		hit  func(f *fakeRealm, p Proof) bool
	}{
		{"B high byte zero", func(f *fakeRealm, p Proof) bool { return bnBytes(f.bigB, 32)[31] == 0 }},
		{"A high byte zero", func(f *fakeRealm, p Proof) bool { return p.A[31] == 0 }},
		{"K high byte zero", func(f *fakeRealm, p Proof) bool { return p.K[39] == 0 }},
	}
	for _, e := range edges {
		found := false
		for i := uint64(1); i < 20000 && !found; i++ {
			f := newFakeRealm("harness", "pw", bigFrom(7), bigFrom(i))
			p, err := ComputeProof("harness", "pw", f.challenge(), bigFrom(i+50000))
			if err != nil {
				t.Fatal(err)
			}
			if !e.hit(f, p) || p.M1[19] == 0 {
				continue
			}
			found = true
			ok, m2, key := f.verify(p.A, p.M1)
			if !ok || !bytes.Equal(m2, p.M2[:]) || !bytes.Equal(key, p.KeyBytes()) {
				t.Fatalf("%s (seed %d): the server would reject this login", e.name, i)
			}
			if e.name == "K high byte zero" && len(p.KeyBytes()) != 39 {
				t.Fatalf("KeyBytes has %d bytes, want 39", len(p.KeyBytes()))
			}
		}
		if !found {
			t.Fatalf("%s: no vector in 20000 seeds", e.name)
		}
	}
}

func TestNewProofRedrawsShortM1(t *testing.T) {
	f := newFakeRealm("harness", "pw", bigFrom(3), bigFrom(4))
	found := false
	for i := uint64(1); i < 20000 && !found; i++ {
		p, err := ComputeProof("harness", "pw", f.challenge(), bigFrom(i+90000))
		if err != nil {
			t.Fatal(err)
		}
		if p.M1[19] == 0 {
			found = true
			if ok, _, _ := f.verify(p.A, p.M1); ok {
				t.Fatal("fake server accepted a 19-byte M; the mirror is wrong")
			}
		}
	}
	if !found {
		t.Fatal("no short-M1 vector in 20000 seeds")
	}
	for i := 0; i < 200; i++ {
		p, err := NewProof("harness", "pw", f.challenge())
		if err != nil {
			t.Fatal(err)
		}
		if ok, _, _ := f.verify(p.A, p.M1); !ok {
			t.Fatalf("draw %d: server rejected NewProof (M1[19]=%d)", i, p.M1[19])
		}
	}
}

func TestRejectsZeroB(t *testing.T) {
	if _, err := ComputeProof("a", "b", Challenge{Salt: make([]byte, 32)}, big.NewInt(5)); err == nil {
		t.Fatal("B = 0 accepted")
	}
}
```

- [ ] **Step 2: Run it and see it fail**

Run: `cd ~/turtle-agent && ~/.local/go/bin/go test ./internal/auth/`
Expected: FAIL, `undefined: srpG` (and `srpN`, `Challenge`, `ComputeProof`, `NewProof`, `Proof`).

- [ ] **Step 3: Implement**

`~/turtle-agent/internal/auth/srp.go`:

```go
// Copyright (c) 2026 agent-wow
// SPDX-License-Identifier: MIT
// Ported from github.com/agent-wow/agent-wow pkg/auth/srp.go at 5ef4916 (see NOTICE).
// Kept: the 256-bit group, the A/u/x/S/M1/M2 arithmetic and the SHA-1 helpers.
// Changed: every hash input uses the server's BigNumber::AsByteArray() form
// (little-endian, high zero bytes dropped), K comes from the full 32-byte S with no
// zero stripping, and NewProof redraws a private key whose M1 ends in a zero byte,
// matching the cmangos realmd in ~/tortoise-wow (AuthSocket.cpp:790-1009).

// Package auth logs in to realmd: SRP6, logon challenge and proof, realm list.
package auth

import (
	"crypto/rand"
	"crypto/sha1" // required by the WoW SRP6 protocol
	"errors"
	"io"
	"math/big"
	"slices"
	"strings"
)

var (
	srpN = mustHex("894B645E89E1535BBDAD5B8B290650530801B18EBFBF5E8FAB3C82872A3E9BB7") // AuthSocket.cpp:174
	srpG = big.NewInt(7)                                                                   // AuthSocket.cpp:175
)

func mustHex(s string) *big.Int {
	n, ok := new(big.Int).SetString(s, 16)
	if !ok {
		panic("bad SRP constant")
	}
	return n
}

// Challenge is the SRP part of realmd's logon-challenge reply (AuthSocket.cpp:608-617).
type Challenge struct {
	B    [32]byte // little-endian, zero-padded to 32 bytes by the server
	Salt []byte   // little-endian, as sent
}

// Proof is the client half of the exchange. K is secret: never print or store it.
type Proof struct {
	A  [32]byte // little-endian, zero-padded: the wire field is 32 bytes
	M1 [20]byte
	M2 [20]byte // the server proof we expect
	K  [40]byte // session key, full width
}

// KeyBytes is K as the server's BigNumber::AsByteArray() renders it. The world server
// hashes this form into CMSG_AUTH_SESSION and keys the header cipher with it
// (WorldSocket.cpp:296, :341); it has 39 bytes when K's top byte is zero.
func (p Proof) KeyBytes() []byte { return asBytes(p.K[:]) }

// ComputeProof runs the client side of SRP6 for the private key a.
func ComputeProof(user, pass string, c Challenge, a *big.Int) (Proof, error) {
	I, P := strings.ToUpper(user), strings.ToUpper(pass)
	B := fromLE(c.B[:])
	if B.Sign() == 0 || new(big.Int).Mod(B, srpN).Sign() == 0 {
		return Proof{}, errors.New("srp: server sent an invalid B")
	}
	var p Proof
	p.A = toLE32(new(big.Int).Exp(srpG, a, srpN))
	aB, bB, sB := asBytes(p.A[:]), asBytes(c.B[:]), asBytes(c.Salt)
	u := fromLE(digestSlice(aB, bB)) // AuthSocket.cpp:801-805
	if u.Sign() == 0 {
		return Proof{}, errors.New("srp: scrambler u is zero")
	}
	id := digest([]byte(I + ":" + P))
	x := fromLE(digestSlice(sB, id[:])) // AuthSocket.cpp:316-329
	v := new(big.Int).Exp(srpG, x, srpN)
	base := new(big.Int).Sub(B, new(big.Int).Mul(big.NewInt(3), v))
	base.Mod(base, srpN)
	S := new(big.Int).Exp(base, new(big.Int).Add(a, new(big.Int).Mul(u, x)), srpN)
	p.K = interleave(toLE32(S)) // AuthSocket.cpp:808-841
	nLE := toLE32(srpN)
	hN, hG := digest(asBytes(nLE[:])), digest([]byte{7})
	var t3 [20]byte
	for i := range t3 {
		t3[i] = hN[i] ^ hG[i] // AuthSocket.cpp:845-859
	}
	hI := digest([]byte(I))
	p.M1 = digest(asBytes(t3[:]), hI[:], sB, aB, bB, p.KeyBytes()) // AuthSocket.cpp:867-871
	p.M2 = digest(aB, asBytes(p.M1[:]), p.KeyBytes())              // AuthSocket.cpp:1005-1007
	return p, nil
}

// NewProof draws a private key, redrawing while M1's last byte is zero: realmd compares
// M.AsByteArray() (then only 19 bytes) against the 20-byte proof (AuthSocket.cpp:920).
func NewProof(user, pass string, c Challenge) (Proof, error) {
	return newProofFrom(rand.Reader, user, pass, c)
}

func newProofFrom(rng io.Reader, user, pass string, c Challenge) (Proof, error) {
	limit := new(big.Int).Sub(srpN, big.NewInt(1))
	for range 64 {
		a, err := rand.Int(rng, limit)
		if err != nil {
			return Proof{}, err
		}
		a.Add(a, big.NewInt(1))
		p, err := ComputeProof(user, pass, c, a)
		if err != nil {
			return Proof{}, err
		}
		if p.M1[19] != 0 {
			return p, nil
		}
	}
	return Proof{}, errors.New("srp: no usable private key in 64 draws")
}

// interleave builds the 40-byte session key from S (AuthSocket.cpp:813-841).
func interleave(s [32]byte) [40]byte {
	var even, odd [16]byte
	for i := 0; i < 16; i++ {
		even[i], odd[i] = s[2*i], s[2*i+1]
	}
	he, ho := digest(even[:]), digest(odd[:])
	var k [40]byte
	for i := 0; i < 20; i++ {
		k[2*i], k[2*i+1] = he[i], ho[i]
	}
	return k
}

// asBytes is BigNumber::AsByteArray() with no minimum size (BigNumber.cpp:164-183):
// little-endian with the high (trailing) zero bytes removed.
func asBytes(le []byte) []byte {
	n := len(le)
	for n > 0 && le[n-1] == 0 {
		n--
	}
	return slices.Clone(le[:n])
}

func digest(parts ...[]byte) [20]byte {
	h := sha1.New()
	for _, p := range parts {
		h.Write(p)
	}
	var out [20]byte
	copy(out[:], h.Sum(nil))
	return out
}

func digestSlice(parts ...[]byte) []byte { d := digest(parts...); return d[:] }

func fromLE(b []byte) *big.Int {
	r := slices.Clone(b)
	slices.Reverse(r)
	return new(big.Int).SetBytes(r)
}

func toLE32(n *big.Int) [32]byte {
	var out [32]byte
	n.FillBytes(out[:])
	slices.Reverse(out[:])
	return out
}
```

- [ ] **Step 4: Run the tests**

Run: `make -C ~/turtle-agent fmt test`
Expected: PASS, `ok  github.com/TortoiseBots/turtle-agent/internal/auth` (the edge searches finish in well under a second each).

- [ ] **Step 5: Record provenance and commit**

Append to `~/turtle-agent/NOTICE`:

```
internal/auth/srp.go | pkg/auth/srp.go | ported (group and SRP6 arithmetic kept; unpadded BigNumber hashing, no K zero-strip, short-M1 redraw)
```

```bash
cd ~/turtle-agent && git status --short
git add internal/auth/srp.go internal/auth/srp_test.go NOTICE
git commit -F - <<'EOF'
auth: SRP6 mirroring realmd's unpadded BigNumber hashing

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01MKCBbD9VJLeKqwGUyyzxyw
EOF
```

---

## Task 5: Realmd client (challenge, proof, realm list, one retry)

**Files:**
- Create: `internal/auth/realmd.go`
- Test: `internal/auth/realmd_test.go`

**Interfaces:**
- Produces: `const ClientBuild uint16 = 7272`; result-code constants `ResultSuccess`, `ResultBanned`, `ResultUnknownAccount`, `ResultIncorrectPassword`, `ResultAlreadyOnline`, `ResultNoTime`, `ResultDBBusy`, `ResultVersionInvalid`, `ResultVersionUpdate`, `ResultSuspended`, `ResultParentControl` (uint8); `var ErrPINRequired error`; `type AuthError struct { Stage string; Code uint8 }`; `type Realm struct { ID int; Icon uint32; Flags uint8; Name, Address string; Population float32; Characters, Timezone uint8 }` with `Offline() bool`; `type Ticket struct { Account string; Key [40]byte; Realms []Realm }` with `KeyBytes() []byte`, `String() string`, `Pick(name string) (Realm, error)`; `func Login(ctx context.Context, addr, account, password string) (*Ticket, error)`; `func BuildLogonChallenge(user string) []byte`; `func BuildLogonProof(p Proof) []byte`; `func ParseRealmList(body []byte) ([]Realm, error)`.
- Consumes: Task 4 (`Challenge`, `Proof`, `newProofFrom`, `asBytes`, `toLE32`, `srpN`), Task 1 (`packet.Reader`, `packet.Writer`).

Wire layouts (`~/tortoise-wow/src/realmd/AuthSocket.cpp`): the client challenge is `sAuthLogonChallenge_C` at :90-107 (cmd, error, u16 size of the rest, "WoW\0", 1, 18, 1, u16 build, platform "68x\0" = 'x86', os "niW\0" = 'Win' (AuthSocket.h:131-135), country "SUne", u32 timezone, u32 ip, u8 name length, name). The reply is cmd, 0, result, then on success B[32], u8 1, g, u8 32, N[32], salt[32], 16 version-challenge bytes and a security-flags byte (:608-667). The proof is 75 bytes (:109-121). A failed proof answers 2 bytes (:925, :951, :978, :1017), except a missing patch, which answers 3 bytes starting with the challenge command (:750-755); a good proof answers 26 bytes (:123-131, :340-349). The realm-list request is 5 bytes (the handler skips 5 including the command, :1191-1194) and the answer is cmd, u16 size, body (:1220-1225).

- [ ] **Step 1: Write the failing test**

`~/turtle-agent/internal/auth/realmd_test.go`:

```go
package auth

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

type fakeOpts struct {
	challengeCode uint8
	pin           bool
	rejects       *int // proofs to reject before accepting
	realms        []Realm
}

var testRealms = []Realm{{Icon: 1, Name: "Tortoise", Address: "127.0.0.1:8090", Population: 0.5, Characters: 2}}

// serveRealmd plays one realmd connection the way AuthSocket.cpp does.
func serveRealmd(t *testing.T, c net.Conn, f *fakeRealm, o fakeOpts) {
	defer c.Close()
	head := make([]byte, 4)
	if _, err := io.ReadFull(c, head); err != nil {
		return
	}
	body := make([]byte, binary.LittleEndian.Uint16(head[2:4]))
	if _, err := io.ReadFull(c, body); err != nil {
		return
	}
	if string(body[0:4]) != "WoW\x00" || body[4] != 1 || body[5] != 18 || body[6] != 1 ||
		binary.LittleEndian.Uint16(body[7:9]) != ClientBuild ||
		string(body[9:13]) != "68x\x00" || string(body[13:17]) != "niW\x00" {
		t.Errorf("bad challenge fields % X", body[:17])
	}
	if user := string(body[30 : 30+int(body[29])]); user != f.user {
		t.Errorf("user %q, want %q", user, f.user)
	}
	if o.challengeCode != 0 {
		c.Write([]byte{0, 0, o.challengeCode})
		return
	}
	w := packet.NewWriter().U8(0).U8(0).U8(0).Append(bnBytes(f.bigB, 32)).U8(1).U8(7).U8(32).
		Append(bnBytes(srpN, 32)).Append(bnBytes(f.s, 0)).Append(make([]byte, 16))
	if o.pin {
		w.U8(1).U32(1234).Append(make([]byte, 16))
		c.Write(w.Bytes())
		return
	}
	c.Write(w.U8(0).Bytes())
	proof := make([]byte, 75)
	if _, err := io.ReadFull(c, proof); err != nil {
		return
	}
	var a [32]byte
	var m1 [20]byte
	copy(a[:], proof[1:33])
	copy(m1[:], proof[33:53])
	ok, m2, _ := f.verify(a, m1)
	if !ok || (o.rejects != nil && *o.rejects > 0) {
		if o.rejects != nil && *o.rejects > 0 {
			*o.rejects--
		}
		c.Write([]byte{cmdLogonProof, ResultUnknownAccount})
		return
	}
	c.Write(packet.NewWriter().U8(cmdLogonProof).U8(0).Append(m2).U32(0).Bytes())
	req := make([]byte, 5)
	if _, err := io.ReadFull(c, req); err != nil || req[0] != cmdRealmList {
		t.Errorf("realm list request % X err %v", req, err)
		return
	}
	list := packet.NewWriter().U32(0).U8(uint8(len(o.realms)))
	for _, r := range o.realms {
		list.U32(r.Icon).U8(r.Flags).CString(r.Name).CString(r.Address).F32(r.Population).U8(r.Characters).U8(r.Timezone).U8(0)
	}
	list.U16(2)
	c.Write(packet.NewWriter().U8(cmdRealmList).U16(uint16(list.Len())).Append(list.Bytes()).Bytes())
}

// startRealmd serves connections one after another and counts them.
func startRealmd(t *testing.T, f *fakeRealm, o fakeOpts) (addr string, conns *atomic.Int32) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	n := new(atomic.Int32)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			n.Add(1)
			serveRealmd(t, c, f, o)
		}
	}()
	return ln.Addr().String(), n
}

func ctx5s(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestLoginSuccess(t *testing.T) {
	f := newFakeRealm("harness", "pw", bigFrom(11), bigFrom(12))
	addr, _ := startRealmd(t, f, fakeOpts{realms: testRealms})
	tk, err := Login(ctx5s(t), addr, "Harness", "pw")
	if err != nil {
		t.Fatal(err)
	}
	if tk.Account != "HARNESS" || len(tk.Realms) != 1 || tk.Realms[0].Name != "Tortoise" ||
		tk.Realms[0].ID != 1 || tk.Realms[0].Address != "127.0.0.1:8090" || tk.Realms[0].Characters != 2 {
		t.Fatalf("ticket %s realms %+v", tk, tk.Realms)
	}
	if n := len(tk.KeyBytes()); n != 39 && n != 40 {
		t.Fatalf("key bytes %d", n)
	}
	if !strings.Contains(tk.String(), "redacted") {
		t.Fatalf("String leaks: %s", tk)
	}
}

func TestLoginRetriesOnceAfterProofReject(t *testing.T) {
	f := newFakeRealm("harness", "pw", bigFrom(21), bigFrom(22))
	one := 1
	addr, conns := startRealmd(t, f, fakeOpts{rejects: &one, realms: testRealms})
	if _, err := Login(ctx5s(t), addr, "harness", "pw"); err != nil {
		t.Fatal(err)
	}
	if conns.Load() != 2 {
		t.Fatalf("%d connections, want 2", conns.Load())
	}
}

func TestLoginWrongPassword(t *testing.T) {
	f := newFakeRealm("harness", "right-one", bigFrom(31), bigFrom(32))
	addr, conns := startRealmd(t, f, fakeOpts{realms: testRealms})
	_, err := Login(ctx5s(t), addr, "harness", "wrong-one")
	var ae *AuthError
	if !errors.As(err, &ae) || ae.Stage != "proof" || ae.Code != ResultUnknownAccount {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "wrong-one") || conns.Load() != 2 {
		t.Fatalf("err %q after %d connections", err, conns.Load())
	}
}

func TestPINRequired(t *testing.T) {
	f := newFakeRealm("harness", "pw", bigFrom(41), bigFrom(42))
	addr, _ := startRealmd(t, f, fakeOpts{pin: true})
	if _, err := Login(ctx5s(t), addr, "harness", "pw"); !errors.Is(err, ErrPINRequired) {
		t.Fatalf("err = %v", err)
	}
}

func TestChallengeRejected(t *testing.T) {
	f := newFakeRealm("harness", "pw", bigFrom(51), bigFrom(52))
	addr, _ := startRealmd(t, f, fakeOpts{challengeCode: ResultDBBusy})
	_, err := Login(ctx5s(t), addr, "harness", "pw")
	var ae *AuthError
	if !errors.As(err, &ae) || ae.Stage != "challenge" || !strings.Contains(err.Error(), "15 per 300 s") {
		t.Fatalf("err = %v", err)
	}
}

func TestLogonChallengeLayout(t *testing.T) {
	b := BuildLogonChallenge("HARNESS")
	if len(b) != 4+30+7 || binary.LittleEndian.Uint16(b[2:4]) != 37 || b[33] != 7 || string(b[34:]) != "HARNESS" {
		t.Fatalf("challenge % X", b)
	}
}

func TestParseRealmListAndPick(t *testing.T) {
	list := packet.NewWriter().U32(0).U8(2).
		U32(1).U8(realmFlagOffline).CString("Old").CString("10.0.0.1:8085").F32(0).U8(0).U8(1).U8(0).
		U32(1).U8(0).CString("Tortoise").CString("192.0.2.10:8090").F32(1).U8(3).U8(1).U8(0).
		U16(2)
	realms, err := ParseRealmList(list.Bytes())
	if err != nil || len(realms) != 2 || realms[1].ID != 2 || realms[1].Characters != 3 {
		t.Fatalf("%+v %v", realms, err)
	}
	tk := &Ticket{Realms: realms}
	if _, err := tk.Pick(""); err == nil {
		t.Fatal("first realm is offline but Pick accepted it")
	}
	if r, err := tk.Pick("tortoise"); err != nil || r.Address != "192.0.2.10:8090" {
		t.Fatalf("%+v %v", r, err)
	}
}
```

- [ ] **Step 2: Run it and see it fail**

Run: `cd ~/turtle-agent && ~/.local/go/bin/go test ./internal/auth/`
Expected: FAIL, `undefined: Login` (and `ClientBuild`, `cmdLogonProof`, `Realm`, `Ticket`, `BuildLogonChallenge`, `ParseRealmList`, `realmFlagOffline`, `ErrPINRequired`, `AuthError`).

- [ ] **Step 3: Implement**

`~/turtle-agent/internal/auth/realmd.go`:

```go
package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

// ClientBuild is the only build realmd knows (RealmList.cpp:39); realms show offline
// for any other build (AuthSocket.cpp:1249, :1266-1268).
const ClientBuild uint16 = 7272

const (
	cmdLogonChallenge = 0x00 // AuthCodes.h:31
	cmdLogonProof     = 0x01 // AuthCodes.h:32
	cmdRealmList      = 0x10 // AuthCodes.h:35
	realmFlagOffline  = 0x02 // REALM_FLAG_OFFLINE, shared/Common.h:200
)

// Result codes (AuthCodes.h:60-92).
const (
	ResultSuccess           uint8 = 0x00
	ResultBanned            uint8 = 0x03
	ResultUnknownAccount    uint8 = 0x04 // also the wrong-password answer (AuthSocket.cpp:1016-1017)
	ResultIncorrectPassword uint8 = 0x05
	ResultAlreadyOnline     uint8 = 0x06
	ResultNoTime            uint8 = 0x07
	ResultDBBusy            uint8 = 0x08 // also IP bans and the 15-accounts-per-IP throttle (AuthSocket.cpp:428-456)
	ResultVersionInvalid    uint8 = 0x09
	ResultVersionUpdate     uint8 = 0x0A
	ResultSuspended         uint8 = 0x0C
	ResultParentControl     uint8 = 0x0F // email not verified or geolocked (AuthSocket.cpp:485, :978)
)

// ErrPINRequired means realmd set security flag 1 (AuthSocket.cpp:652-663); v1 cannot answer a PIN grid.
var ErrPINRequired = errors.New("realmd asks for a PIN: raise ForcePinAccountRank above the account's rank or add an account_twofactor_allowed row for this IP")

// AuthError is a realmd rejection. It never carries the password.
type AuthError struct {
	Stage string // "challenge" or "proof"
	Code  uint8
}

func (e *AuthError) Error() string {
	return fmt.Sprintf("realmd %s rejected: %s (code 0x%02X)", e.Stage, resultText(e.Code), e.Code)
}

func resultText(c uint8) string {
	switch c {
	case ResultBanned:
		return "account banned"
	case ResultUnknownAccount, ResultIncorrectPassword:
		return "unknown account or wrong password"
	case ResultAlreadyOnline:
		return "account already online"
	case ResultNoTime:
		return "account blocked (negative shop balance)"
	case ResultDBBusy:
		return "server busy, IP banned, or too many accounts from this IP (15 per 300 s)"
	case ResultVersionInvalid, ResultVersionUpdate:
		return "client build rejected"
	case ResultSuspended:
		return "account suspended or IP-locked"
	case ResultParentControl:
		return "email not verified or account geolocked"
	}
	return "rejected"
}

type Realm struct {
	ID         int     `json:"id"` // position in the list, from 1: the vanilla list sends no id (AuthSocket.cpp:1277)
	Icon       uint32  `json:"icon"`
	Flags      uint8   `json:"flags"`
	Name       string  `json:"name"`
	Address    string  `json:"address"`
	Population float32 `json:"population"`
	Characters uint8   `json:"characters"`
	Timezone   uint8   `json:"timezone"`
}

func (r Realm) Offline() bool { return r.Flags&realmFlagOffline != 0 }

// Ticket is a successful realmd login. Key is secret: never print or store it.
type Ticket struct {
	Account string
	Key     [40]byte
	Realms  []Realm
}

// KeyBytes is the key in the form the world server uses (see Proof.KeyBytes).
func (t *Ticket) KeyBytes() []byte { return asBytes(t.Key[:]) }

func (t *Ticket) String() string {
	return fmt.Sprintf("Ticket{account=%s realms=%d key=redacted}", t.Account, len(t.Realms))
}

// Pick returns the realm called name (case-insensitive), or the first realm when name is empty.
func (t *Ticket) Pick(name string) (Realm, error) {
	for _, r := range t.Realms {
		if name == "" || strings.EqualFold(r.Name, name) {
			if r.Offline() {
				return Realm{}, fmt.Errorf("realm %q is offline for build %d (flags 0x%02X)", r.Name, ClientBuild, r.Flags)
			}
			return r, nil
		}
	}
	return Realm{}, fmt.Errorf("realm %q is not in the realm list (%d realms)", name, len(t.Realms))
}

// Login runs challenge, proof and realm list. A proof rejected with 0x04 is retried
// once on a fresh connection (spec: the rare unpadded-BigNumber mismatch); a real
// wrong password therefore costs two of the server's WrongPass.MaxCount attempts.
func Login(ctx context.Context, addr, account, password string) (*Ticket, error) {
	t, err := loginOnce(ctx, addr, account, password)
	var ae *AuthError
	if errors.As(err, &ae) && ae.Stage == "proof" && ae.Code == ResultUnknownAccount {
		t, err = loginOnce(ctx, addr, account, password)
	}
	return t, err
}

func loginOnce(ctx context.Context, addr, account, password string) (*Ticket, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("realmd: connect %s: %w", addr, err)
	}
	defer conn.Close()
	deadline := time.Now().Add(30 * time.Second)
	if dl, ok := ctx.Deadline(); ok {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)
	return handshake(conn, account, password, rand.Reader)
}

func handshake(rw io.ReadWriter, account, password string, rng io.Reader) (*Ticket, error) {
	user := strings.ToUpper(account)
	if user == "" || len(user) > 16 {
		return nil, errors.New("realmd: account name must be 1-16 characters")
	}
	if _, err := rw.Write(BuildLogonChallenge(user)); err != nil {
		return nil, fmt.Errorf("realmd: send challenge: %w", err)
	}
	c, err := readChallenge(rw)
	if err != nil {
		return nil, err
	}
	p, err := newProofFrom(rng, user, password, c)
	if err != nil {
		return nil, err
	}
	if _, err := rw.Write(BuildLogonProof(p)); err != nil {
		return nil, fmt.Errorf("realmd: send proof: %w", err)
	}
	if err := readProof(rw, p); err != nil {
		return nil, err
	}
	realms, err := requestRealms(rw)
	if err != nil {
		return nil, err
	}
	return &Ticket{Account: user, Key: p.K, Realms: realms}, nil
}

// BuildLogonChallenge lays out sAuthLogonChallenge_C (AuthSocket.cpp:90-107); size counts
// the bytes after the 4-byte header: 30 fixed bytes plus the name.
func BuildLogonChallenge(user string) []byte {
	w := packet.NewWriter().U8(cmdLogonChallenge).U8(0x03).U16(uint16(30 + len(user)))
	w.Append([]byte("WoW\x00")).U8(1).U8(18).U8(1).U16(ClientBuild)
	w.Append([]byte("68x\x00")) // 'x86' (AuthSocket.h:134), checked by VerifyVersion (AuthSocket.cpp:1533)
	w.Append([]byte("niW\x00")) // 'Win' (AuthSocket.h:131)
	w.Append([]byte("SUne"))    // 'enUS' reversed; realmd reverses it back (AuthSocket.cpp:669-671)
	w.U32(0)                    // timezone bias
	w.U32(0x0100007F)           // ip, informational only
	w.U8(uint8(len(user))).Append([]byte(user))
	return w.Bytes()
}

// BuildLogonProof lays out the 75-byte sAuthLogonProof_C_1_11 (AuthSocket.cpp:109-121).
// The version hash for build 7272 is all zeros (RealmList.cpp:39), so VerifyVersion
// passes without a CRC (AuthSocket.cpp:1552-1553).
func BuildLogonProof(p Proof) []byte {
	return packet.NewWriter().U8(cmdLogonProof).Append(p.A[:]).Append(p.M1[:]).
		Append(make([]byte, 20)).U8(0).U8(0).Bytes()
}

func readChallenge(r io.Reader) (Challenge, error) {
	var head [3]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return Challenge{}, fmt.Errorf("realmd: read challenge: %w", err)
	}
	if head[0] != cmdLogonChallenge {
		return Challenge{}, fmt.Errorf("realmd: unexpected command 0x%02X", head[0])
	}
	if head[2] != ResultSuccess {
		return Challenge{}, &AuthError{Stage: "challenge", Code: head[2]}
	}
	var c Challenge
	g, n := []byte{}, []byte{}
	var version [16]byte
	var flags [1]byte
	c.Salt = make([]byte, 32)
	err := readAll(r, c.B[:])
	if err == nil {
		g, err = readLenPrefixed(r)
	}
	if err == nil {
		n, err = readLenPrefixed(r)
	}
	if err == nil {
		err = readAll(r, c.Salt, version[:], flags[:])
	}
	if err != nil {
		return Challenge{}, fmt.Errorf("realmd: read challenge: %w", err)
	}
	nLE := toLE32(srpN)
	if !bytes.Equal(g, []byte{7}) || !bytes.Equal(n, nLE[:]) {
		return Challenge{}, errors.New("realmd: unexpected SRP group")
	}
	if flags[0] != 0 {
		return Challenge{}, ErrPINRequired
	}
	return c, nil
}

func readProof(r io.Reader, p Proof) error {
	var head [2]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return fmt.Errorf("realmd: read proof reply: %w", err)
	}
	if head[0] == cmdLogonChallenge { // "no patch" answer: cmd, 0, code (AuthSocket.cpp:750-755)
		var code [1]byte
		if _, err := io.ReadFull(r, code[:]); err != nil {
			return fmt.Errorf("realmd: read proof reply: %w", err)
		}
		return &AuthError{Stage: "proof", Code: code[0]}
	}
	if head[0] != cmdLogonProof {
		return fmt.Errorf("realmd: unexpected command 0x%02X after proof", head[0])
	}
	if head[1] != ResultSuccess {
		return &AuthError{Stage: "proof", Code: head[1]}
	}
	var rest [24]byte // M2[20] + uint32
	if _, err := io.ReadFull(r, rest[:]); err != nil {
		return fmt.Errorf("realmd: read proof reply: %w", err)
	}
	if !bytes.Equal(rest[:20], p.M2[:]) {
		return errors.New("realmd: server proof mismatch")
	}
	return nil
}

func requestRealms(rw io.ReadWriter) ([]Realm, error) {
	if _, err := rw.Write([]byte{cmdRealmList, 0, 0, 0, 0}); err != nil {
		return nil, fmt.Errorf("realmd: request realm list: %w", err)
	}
	var head [3]byte
	if _, err := io.ReadFull(rw, head[:]); err != nil {
		return nil, fmt.Errorf("realmd: read realm list: %w", err)
	}
	if head[0] != cmdRealmList {
		return nil, fmt.Errorf("realmd: unexpected command 0x%02X for realm list", head[0])
	}
	body := make([]byte, binary.LittleEndian.Uint16(head[1:3]))
	if _, err := io.ReadFull(rw, body); err != nil {
		return nil, fmt.Errorf("realmd: read realm list: %w", err)
	}
	return ParseRealmList(body)
}

// ParseRealmList decodes AuthSocket::LoadRealmlist (AuthSocket.cpp:1230-1281).
func ParseRealmList(body []byte) ([]Realm, error) {
	r := packet.NewReader(body)
	r.U32()
	n := int(r.U8())
	realms := make([]Realm, 0, n)
	for i := 0; i < n; i++ {
		var rl Realm
		rl.ID = i + 1
		rl.Icon, rl.Flags = r.U32(), r.U8()
		rl.Name, rl.Address = r.CString(), r.CString()
		rl.Population, rl.Characters, rl.Timezone = r.F32(), r.U8(), r.U8()
		r.U8()
		realms = append(realms, rl)
	}
	r.U16()
	if err := r.Err(); err != nil {
		return nil, fmt.Errorf("realmd: realm list: %w", err)
	}
	return realms, nil
}

func readAll(r io.Reader, bufs ...[]byte) error {
	for _, b := range bufs {
		if _, err := io.ReadFull(r, b); err != nil {
			return err
		}
	}
	return nil
}

func readLenPrefixed(r io.Reader) ([]byte, error) {
	var n [1]byte
	if _, err := io.ReadFull(r, n[:]); err != nil {
		return nil, err
	}
	b := make([]byte, n[0])
	_, err := io.ReadFull(r, b)
	return b, err
}
```

- [ ] **Step 4: Run the tests**

Run: `make -C ~/turtle-agent fmt test`
Expected: PASS for `internal/auth` (`TestLoginRetriesOnceAfterProofReject` sees 2 connections).

- [ ] **Step 5: Commit**

```bash
cd ~/turtle-agent && git status --short
git add internal/auth/realmd.go internal/auth/realmd_test.go
git commit -F - <<'EOF'
auth: realmd login with build 7272, vanilla realm list, one proof retry

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01MKCBbD9VJLeKqwGUyyzxyw
EOF
```

---

## Task 6: World connection (XOR header cipher, framing, auth session)

**Files:**
- Create: `internal/worldconn/crypt.go`, `internal/worldconn/conn.go`, `internal/worldconn/addon.go`, `internal/worldconn/worldtest/server.go`
- Modify: `NOTICE` (append one row)
- Test: `internal/worldconn/crypt_test.go` (package `worldconn`), `internal/worldconn/conn_test.go` (package `worldconn_test`, to avoid an import cycle with `worldtest`)

**Interfaces:**
- Produces (package `worldconn`): `type HeaderCrypt`; `func NewHeaderCrypt(key []byte) *HeaderCrypt`; `(*HeaderCrypt).Encrypt(h []byte)`, `(*HeaderCrypt).Decrypt(h []byte)`; `type Packet struct { Op uint16; Body []byte }`; `type Direction uint8` with `ServerToClient`, `ClientToServer`; `type Tap interface { Packet(dir Direction, op uint16, body []byte) }`; `const WorldBuild uint32 = 5875`; auth codes `AuthOK`, `AuthFailed`, `AuthReject`, `AuthUnavailable`, `AuthSystemError`, `AuthVersionMismatch`, `AuthUnknownAccount`, `AuthSessionExpired`, `AuthServerShutdown`, `AuthWaitQueue`, `AuthBanned`, `AuthAlreadyOnline` (uint8); `type AuthError struct { Code uint8 }`; `type Conn`; `func Dial(ctx context.Context, addr, account string, key []byte, tap Tap) (*Conn, error)`; `func Client(nc net.Conn, account string, key []byte, tap Tap) (*Conn, error)`; `(*Conn).ReadPacket() (Packet, error)`; `(*Conn).WritePacket(op uint16, body []byte) error`; `(*Conn).Close() error`; `func BuildAuthSession(account string, clientSeed uint32, serverSeed [4]byte, key []byte) []byte`; `func AddonBlock() []byte`.
- Produces (package `worldtest`): `type Options struct { Seed [4]byte; Reject uint8; Queue bool; ShortOK bool; Early []worldconn.Packet }`; `type Server` with fields `Account string`, `ClientSeed uint32`, `Build uint32`, `AddonBlock []byte`; `func Accept(nc net.Conn, key []byte, o Options) (*Server, error)`; `(*Server).Send(op uint16, body []byte) error`; `(*Server).Expect(op uint16, timeout time.Duration) (worldconn.Packet, error)`; `(*Server).Skipped() []worldconn.Packet`; `(*Server).Close() error`.
- Consumes: Task 1 (`packet`), Task 2 (`opcode`).

Server facts mirrored (all `~/tortoise-wow/src`): `AuthCrypt::EncryptSend`/`DecryptRecv` at `shared/Auth/AuthCrypt.cpp:39-66`, 4 bytes encrypted on send and 6 on receive (`AuthCrypt.h:136-137`); server header `u16 size + u16 cmd`, client header `u16 size + u32 cmd` (`framework/Network/MangosSocket.h:33-43`), size big-endian and counting the opcode bytes (`MangosSocketImpl.h:307-331`, `:504-513`), client packets above 10240 bytes are rejected (`MangosSocketImpl.h:316`); the challenge is a 4-byte seed (`game/Protocol/WorldSocket.cpp:480-486`); `CMSG_AUTH_SESSION` is `u32 build, u32 serverId, cstring account, u32 clientSeed, digest[20]`, then the addon block (`WorldSocket.cpp:144-149`, `:379-386`); digest = SHA1(account, u32 0, clientSeed, serverSeed, K unpadded) (`:287-299`); rejections before `:341` are sent with a plain header, the cipher starts at `:341-342`, `AUTH_OK` is 10 bytes (`game/World.cpp:367-371`) or 1 byte after a queue (`game/WorldSession.cpp:949-952`), the queue answer is 14 bytes (`World.cpp:471-477`) or 5 (`WorldSession.cpp:955-958`); the AUTH_UNAVAILABLE branch sends an empty default packet by mistake (`WorldSocket.cpp:275-283`). The addon block is a u32 uncompressed size and a zlib stream of `cstring name, u8 flags, u32 modulus CRC, u32 URL CRC` entries (`game/Anticheat/AddonHandler.cpp:70-76`, `:114-139`, `:196-200`); an empty block kicks the session (`:116-118`); flags 0x01 with CRC 0x4C1C776D (`:141`) is the standard answer.

- [ ] **Step 1: Write the failing tests**

`~/turtle-agent/internal/worldconn/crypt_test.go`:

```go
package worldconn

import (
	"bytes"
	"testing"
)

// Hand-computed from AuthCrypt.cpp:54-66 (encrypt) and :39-52 (decrypt) with key 01 02 03.
func TestHeaderCryptKnownVector(t *testing.T) {
	h := []byte{0x00, 0x04, 0xEE, 0x01}
	NewHeaderCrypt([]byte{1, 2, 3}).Encrypt(h)
	if !bytes.Equal(h, []byte{0x01, 0x07, 0xF4, 0xF4}) {
		t.Fatalf("encrypt % X", h)
	}
	NewHeaderCrypt([]byte{1, 2, 3}).Decrypt(h)
	if !bytes.Equal(h, []byte{0x00, 0x04, 0xEE, 0x01}) {
		t.Fatalf("decrypt % X", h)
	}
}

func TestHeaderCryptStreamsWith40And39ByteKeys(t *testing.T) {
	for _, n := range []int{40, 39} {
		key := bytes.Repeat([]byte{0xA5, 0x3C}, 20)[:n]
		send, recv := NewHeaderCrypt(key), NewHeaderCrypt(key)
		for i := 0; i < 1000; i++ {
			h := []byte{byte(i), byte(i >> 8), 0x95, 0x00, 0x00, 0x00}
			want := bytes.Clone(h)
			send.Encrypt(h)
			recv.Decrypt(h)
			if !bytes.Equal(h, want) {
				t.Fatalf("key %d bytes, header %d: got % X want % X", n, i, h, want)
			}
		}
	}
}
```

`~/turtle-agent/internal/worldconn/conn_test.go`:

```go
package worldconn_test

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/opcode"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
	"github.com/TortoiseBots/turtle-agent/internal/worldconn"
	"github.com/TortoiseBots/turtle-agent/internal/worldconn/worldtest"
)

var key40 = bytes.Repeat([]byte{0x11, 0x22, 0x33, 0x44}, 10)

func pipeAuth(t *testing.T, key []byte, o worldtest.Options) (*worldconn.Conn, *worldtest.Server, error) {
	t.Helper()
	cli, srv := net.Pipe()
	t.Cleanup(func() { cli.Close(); srv.Close() })
	type res struct {
		s   *worldtest.Server
		err error
	}
	ch := make(chan res, 1)
	go func() { s, err := worldtest.Accept(srv, key, o); ch <- res{s, err} }()
	c, err := worldconn.Client(cli, "HARNESS", key, nil)
	r := <-ch
	if r.err != nil {
		t.Fatalf("server: %v", r.err)
	}
	return c, r.s, err
}

func TestAuthSessionLayout(t *testing.T) {
	seed := [4]byte{9, 8, 7, 6}
	body := worldconn.BuildAuthSession("HARNESS", 0x01020304, seed, key40)
	r := packet.NewReader(body)
	if r.U32() != worldconn.WorldBuild || r.U32() != 0 || r.CString() != "HARNESS" || r.U32() != 0x01020304 {
		t.Fatal("header fields wrong")
	}
	h := sha1.New()
	h.Write([]byte("HARNESS"))
	h.Write([]byte{0, 0, 0, 0})
	h.Write([]byte{4, 3, 2, 1})
	h.Write(seed[:])
	h.Write(key40)
	if !bytes.Equal(r.Bytes(20), h.Sum(nil)) {
		t.Fatal("digest differs from WorldSocket.cpp:287-299")
	}
	size := r.U32()
	zr, err := zlib.NewReader(bytes.NewReader(r.Bytes(r.Remaining())))
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := io.ReadAll(zr)
	if uint32(len(plain)) != size {
		t.Fatalf("declared %d bytes, inflated %d", size, len(plain))
	}
	pr := packet.NewReader(plain)
	var names []string
	for pr.Remaining() > 0 {
		names = append(names, pr.CString())
		if pr.U8() != 0x01 || pr.U32() != 0x4C1C776D || pr.U32() != 0 {
			t.Fatal("addon entry fields wrong")
		}
	}
	if len(names) != 4 || names[0] != "Blizzard_BindingUI" || names[3] != "Blizzard_RaidUI" || pr.Err() != nil {
		t.Fatalf("addons %v err %v", names, pr.Err())
	}
}

func TestClientAuthenticatesAndFrames(t *testing.T) {
	early := []worldconn.Packet{{Op: opcode.SMSGAddonInfo, Body: []byte{2, 1, 0, 0, 0, 0, 0, 0}}}
	c, srv, err := pipeAuth(t, key40, worldtest.Options{Early: early})
	if err != nil {
		t.Fatal(err)
	}
	if srv.Build != 5875 || srv.Account != "HARNESS" || len(srv.AddonBlock) < 8 {
		t.Fatalf("server saw build %d account %q addon %d bytes", srv.Build, srv.Account, len(srv.AddonBlock))
	}
	p, err := c.ReadPacket()
	if err != nil || p.Op != opcode.SMSGAddonInfo {
		t.Fatalf("first packet %s %v", opcode.Name(p.Op), err)
	}
	go srv.Send(opcode.SMSGPong, []byte{7, 0, 0, 0})
	if p, err = c.ReadPacket(); err != nil || p.Op != opcode.SMSGPong || binary.LittleEndian.Uint32(p.Body) != 7 {
		t.Fatalf("pong %v %v", p, err)
	}
	if err := c.WritePacket(opcode.CMSGPing, []byte{1, 0, 0, 0, 0, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	if p, err := srv.Expect(opcode.CMSGPing, time.Second); err != nil || p.Body[0] != 1 {
		t.Fatalf("ping %v %v", p, err)
	}
}

func TestQueueThenShortOK(t *testing.T) {
	if _, _, err := pipeAuth(t, key40, worldtest.Options{Queue: true, ShortOK: true}); err != nil {
		t.Fatal(err)
	}
}

func TestPlainTextRejection(t *testing.T) {
	_, _, err := pipeAuth(t, key40, worldtest.Options{Reject: worldconn.AuthUnknownAccount})
	var ae *worldconn.AuthError
	if !errors.As(err, &ae) || ae.Code != worldconn.AuthUnknownAccount {
		t.Fatalf("err = %v", err)
	}
}

// Review Focus 1: K with a zero top byte is 39 bytes on the server; digest and cipher use that.
func TestAuthWith39ByteKey(t *testing.T) {
	key := key40[:39]
	c, srv, err := pipeAuth(t, key, worldtest.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		if err := c.WritePacket(opcode.CMSGPing, []byte{byte(i), 0, 0, 0, 0, 0, 0, 0}); err != nil {
			t.Fatal(err)
		}
		if p, err := srv.Expect(opcode.CMSGPing, time.Second); err != nil || p.Body[0] != byte(i) {
			t.Fatalf("ping %d: %v %v", i, p, err)
		}
	}
}

func TestWriteRefusesBadOpcodeAndSize(t *testing.T) {
	c, _, err := pipeAuth(t, key40, worldtest.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.WritePacket(opcode.NumMsgTypes, nil); err == nil {
		t.Fatal("opcode NUM_MSG_TYPES accepted")
	}
	if err := c.WritePacket(opcode.CMSGMessagechat, make([]byte, 10237)); err == nil {
		t.Fatal("oversized packet accepted")
	}
}
```

- [ ] **Step 2: Run them and see them fail**

Run: `cd ~/turtle-agent && ~/.local/go/bin/go test ./internal/worldconn/...`
Expected: FAIL, `undefined: NewHeaderCrypt` and `no non-test Go files in .../worldconn/worldtest` (package `worldtest` does not exist yet).

- [ ] **Step 3: Implement the cipher**

`~/turtle-agent/internal/worldconn/crypt.go`:

```go
package worldconn

// HeaderCrypt is the vanilla rolling-XOR header cipher (shared/Auth/AuthCrypt.cpp:39-66).
// Encrypt is the sending direction and Decrypt the receiving one, each with its own
// index and carry; the client encrypts 6-byte headers and decrypts 4-byte ones, the
// server (worldtest) the other way round.
type HeaderCrypt struct {
	key          []byte
	sendI, sendJ uint8
	recvI, recvJ uint8
}

// NewHeaderCrypt keys the cipher with K as the server holds it (WorldSocket.cpp:341).
func NewHeaderCrypt(key []byte) *HeaderCrypt {
	k := append([]byte(nil), key...)
	if len(k) == 0 {
		k = []byte{0} // AuthCrypt::SetKey resizes an empty key to one byte (AuthCrypt.cpp:72-73)
	}
	return &HeaderCrypt{key: k}
}

// Encrypt mirrors AuthCrypt::EncryptSend: x = (d ^ K[i]) + j; j = x.
func (c *HeaderCrypt) Encrypt(h []byte) {
	for t := range h {
		c.sendI %= uint8(len(c.key))
		x := (h[t] ^ c.key[c.sendI]) + c.sendJ
		c.sendI++
		h[t], c.sendJ = x, x
	}
}

// Decrypt mirrors AuthCrypt::DecryptRecv: x = (d - j) ^ K[i]; j = d.
func (c *HeaderCrypt) Decrypt(h []byte) {
	for t := range h {
		c.recvI %= uint8(len(c.key))
		x := (h[t] - c.recvJ) ^ c.key[c.recvI]
		c.recvI++
		c.recvJ = h[t]
		h[t] = x
	}
}
```

- [ ] **Step 4: Implement the addon block, connection and auth**

`~/turtle-agent/internal/worldconn/addon.go`:

```go
package worldconn

import (
	"bytes"
	"compress/zlib"

	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

// fingerprintAddons are the addons the anticheat reads its fingerprint from
// (Anticheat/AddonHandler.cpp:80-86). Flags 0x01 on all four means "no fingerprint
// yet": the server generates one and answers with SMSG_ADDON_INFO (:217-253).
var fingerprintAddons = []string{"Blizzard_BindingUI", "Blizzard_InspectUI", "Blizzard_MacroUI", "Blizzard_RaidUI"}

// AddonBlock is the tail of CMSG_AUTH_SESSION: u32 uncompressed size, then a zlib
// stream of name, u8 flags, u32 modulus CRC, u32 URL CRC (AddonHandler.cpp:114-139, :200).
func AddonBlock() []byte {
	plain := packet.NewWriter()
	for _, name := range fingerprintAddons {
		plain.CString(name).U8(0x01).U32(0x4C1C776D).U32(0) // standard modulus CRC (AddonHandler.cpp:141)
	}
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	zw.Write(plain.Bytes())
	zw.Close()
	return packet.NewWriter().U32(uint32(plain.Len())).Append(z.Bytes()).Bytes()
}
```

`~/turtle-agent/internal/worldconn/conn.go`:

```go
// Copyright (c) 2026 agent-wow
// SPDX-License-Identifier: MIT
// Framing skeleton ported from github.com/agent-wow/agent-wow pkg/worldconn/protocol.go
// and pkg/worldconn/conn.go at 5ef4916 (see NOTICE): io.ReadFull framing, serialized
// writes, Dial-then-authenticate. Header layout, cipher, auth session and auth response
// are rewritten for the 1.12 core in ~/tortoise-wow.

// Package worldconn owns one world-server socket: framing, header cipher and the
// authentication handshake. One goroutine may read while others write.
package worldconn

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/opcode"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

// WorldBuild is the only build the world server accepts (game/Database/DBCStores.cpp:121-129).
const WorldBuild uint32 = 5875

// maxClientPacket is the server's limit on a client packet, opcode included (MangosSocketImpl.h:316).
const maxClientPacket = 10240

// SMSG_AUTH_RESPONSE codes: ResponseCodes ordinals (SharedDefines.h:1629-1666).
const (
	AuthOK              uint8 = 0x0C
	AuthFailed          uint8 = 0x0D
	AuthReject          uint8 = 0x0E
	AuthUnavailable     uint8 = 0x10
	AuthSystemError     uint8 = 0x11
	AuthVersionMismatch uint8 = 0x14
	AuthUnknownAccount  uint8 = 0x15
	AuthSessionExpired  uint8 = 0x17
	AuthServerShutdown  uint8 = 0x18
	AuthWaitQueue       uint8 = 0x1B
	AuthBanned          uint8 = 0x1C
	AuthAlreadyOnline   uint8 = 0x1D
)

type AuthError struct{ Code uint8 }

func (e *AuthError) Error() string {
	text := "rejected"
	switch e.Code {
	case AuthFailed:
		text = "session key mismatch (realmd login stale, or K hashed in the wrong form)"
	case AuthUnavailable:
		text = "server is locked to a higher security level"
	case AuthVersionMismatch:
		text = "build rejected (the world server wants 5875)"
	case AuthUnknownAccount:
		text = "account unknown to the world server"
	case AuthBanned:
		text = "account or IP banned"
	case AuthAlreadyOnline:
		text = "account already online"
	case AuthServerShutdown:
		text = "server shutting down"
	}
	return fmt.Sprintf("world auth: %s (code 0x%02X)", text, e.Code)
}

type Packet struct {
	Op   uint16
	Body []byte
}

type Direction uint8

const (
	ServerToClient Direction = 0
	ClientToServer Direction = 1
)

// Tap sees every packet after decryption (capture mode); it must be safe for
// concurrent use because reads and writes happen on different goroutines.
type Tap interface {
	Packet(dir Direction, op uint16, body []byte)
}

type Conn struct {
	nc        net.Conn
	tap       Tap
	crypt     *HeaderCrypt
	early     []Packet // packets that arrived before SMSG_AUTH_RESPONSE
	writeMu   sync.Mutex
	closeOnce sync.Once
}

// Dial connects to the world server and authenticates; ctx bounds the handshake only.
func Dial(ctx context.Context, addr, account string, key []byte, tap Tap) (*Conn, error) {
	var d net.Dialer
	nc, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("world: connect %s: %w", addr, err)
	}
	deadline := time.Now().Add(30 * time.Second)
	if dl, ok := ctx.Deadline(); ok {
		deadline = dl
	}
	_ = nc.SetDeadline(deadline)
	c, err := Client(nc, account, key, tap)
	if err != nil {
		nc.Close()
		return nil, err
	}
	_ = nc.SetDeadline(time.Time{})
	return c, nil
}

// Client authenticates over an already open connection (tests pass a net.Pipe).
func Client(nc net.Conn, account string, key []byte, tap Tap) (*Conn, error) {
	c := &Conn{nc: nc, tap: tap}
	if err := c.authenticate(account, key); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Conn) authenticate(account string, key []byte) error {
	p, err := c.readRaw()
	if err != nil {
		return fmt.Errorf("world: read auth challenge: %w", err)
	}
	if p.Op != opcode.SMSGAuthChallenge || len(p.Body) != 4 {
		return fmt.Errorf("world: expected a 4-byte SMSG_AUTH_CHALLENGE, got %s with %d bytes", opcode.Name(p.Op), len(p.Body))
	}
	var serverSeed, cs [4]byte
	copy(serverSeed[:], p.Body)
	if _, err := rand.Read(cs[:]); err != nil {
		return err
	}
	body := BuildAuthSession(account, binary.LittleEndian.Uint32(cs[:]), serverSeed, key)
	if err := c.WritePacket(opcode.CMSGAuthSession, body); err != nil {
		return fmt.Errorf("world: send auth session: %w", err)
	}
	c.crypt = NewHeaderCrypt(key) // the server starts its cipher after the digest check (WorldSocket.cpp:341-342)
	return c.readAuthResponse()
}

// BuildAuthSession lays out CMSG_AUTH_SESSION (WorldSocket.cpp:144-149, :287-299).
func BuildAuthSession(account string, clientSeed uint32, serverSeed [4]byte, key []byte) []byte {
	d := sha1.New()
	d.Write([]byte(account))
	d.Write([]byte{0, 0, 0, 0})
	d.Write(binary.LittleEndian.AppendUint32(nil, clientSeed))
	d.Write(serverSeed[:])
	d.Write(key)
	return packet.NewWriter().U32(WorldBuild).U32(0).CString(account).U32(clientSeed).
		Append(d.Sum(nil)).Append(AddonBlock()).Bytes()
}

// readAuthResponse waits for SMSG_AUTH_RESPONSE. A rejection is sent before the server
// starts its cipher, so its header is plain (WorldSocket.cpp:158-308); success and queue
// updates are encrypted. Other packets that arrive first (SMSG_ADDON_INFO is sent right
// after the session is queued, WorldSocket.cpp:380-390) are kept for ReadPacket.
func (c *Conn) readAuthResponse() error {
	for range 1000 {
		var h [4]byte
		if _, err := io.ReadFull(c.nc, h[:]); err != nil {
			return fmt.Errorf("world: read auth response: %w", err)
		}
		if plainSize(h) == 2 && plainOp(h) == 0 { // AUTH_UNAVAILABLE sends an empty packet (WorldSocket.cpp:275-283)
			return &AuthError{Code: AuthUnavailable}
		}
		trial := *c.crypt
		dec := h
		trial.Decrypt(dec[:])
		size, op := plainSize(dec), plainOp(dec)
		switch {
		case op == opcode.SMSGAuthResponse:
			*c.crypt = trial
		case plainSize(h) == 3 && plainOp(h) == opcode.SMSGAuthResponse:
			size, op = 3, opcode.SMSGAuthResponse
		case op < opcode.NumMsgTypes:
			*c.crypt = trial
		default:
			return fmt.Errorf("world: undecodable header % X after the auth session", h)
		}
		if size < 2 {
			return fmt.Errorf("world: bad header size %d", size)
		}
		body := make([]byte, size-2)
		if _, err := io.ReadFull(c.nc, body); err != nil {
			return fmt.Errorf("world: read auth response: %w", err)
		}
		if c.tap != nil {
			c.tap.Packet(ServerToClient, op, body)
		}
		if op != opcode.SMSGAuthResponse {
			c.early = append(c.early, Packet{Op: op, Body: body})
			continue
		}
		if len(body) == 0 {
			return errors.New("world: empty SMSG_AUTH_RESPONSE")
		}
		switch body[0] {
		case AuthOK: // 10 bytes (World.cpp:367-371) or 1 byte after a queue (WorldSession.cpp:949-952)
			return nil
		case AuthWaitQueue: // 14 bytes (World.cpp:471-477) or 5 (WorldSession.cpp:955-958); keep waiting
			continue
		default:
			return &AuthError{Code: body[0]}
		}
	}
	return errors.New("world: no SMSG_AUTH_RESPONSE within 1000 packets")
}

func plainSize(h [4]byte) int   { return int(binary.BigEndian.Uint16(h[0:2])) }
func plainOp(h [4]byte) uint16  { return binary.LittleEndian.Uint16(h[2:4]) }

// ReadPacket returns the next server packet. Only one goroutine may read.
func (c *Conn) ReadPacket() (Packet, error) {
	if len(c.early) > 0 {
		p := c.early[0]
		c.early = c.early[1:]
		return p, nil
	}
	return c.readRaw()
}

// readRaw reads one server packet: u16 big-endian size (opcode included), u16 opcode
// (MangosSocket.h:33-37, MangosSocketImpl.h:504-513).
func (c *Conn) readRaw() (Packet, error) {
	var h [4]byte
	if _, err := io.ReadFull(c.nc, h[:]); err != nil {
		return Packet{}, err
	}
	if c.crypt != nil {
		c.crypt.Decrypt(h[:])
	}
	size, op := plainSize(h), plainOp(h)
	if size < 2 {
		return Packet{}, fmt.Errorf("world: bad header size %d", size)
	}
	body := make([]byte, size-2)
	if _, err := io.ReadFull(c.nc, body); err != nil {
		return Packet{}, err
	}
	if c.tap != nil {
		c.tap.Packet(ServerToClient, op, body)
	}
	return Packet{Op: op, Body: body}, nil
}

// WritePacket frames one client packet: u16 big-endian size (body + 4), u32 opcode,
// first 6 bytes encrypted (MangosSocket.h:39-43, MangosSocketImpl.h:307-331).
func (c *Conn) WritePacket(op uint16, body []byte) error {
	if op >= opcode.NumMsgTypes {
		return fmt.Errorf("world: opcode 0x%03X is at or past NUM_MSG_TYPES; the server would close the socket (WorldSocket.cpp:55-59)", op)
	}
	if len(body)+4 > maxClientPacket {
		return fmt.Errorf("world: %s body is %d bytes; the server limit is %d", opcode.Name(op), len(body), maxClientPacket-4)
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	buf := make([]byte, 6, 6+len(body))
	binary.BigEndian.PutUint16(buf[0:2], uint16(len(body)+4))
	binary.LittleEndian.PutUint32(buf[2:6], uint32(op))
	if c.crypt != nil {
		c.crypt.Encrypt(buf[:6])
	}
	buf = append(buf, body...)
	if c.tap != nil {
		c.tap.Packet(ClientToServer, op, body)
	}
	_, err := c.nc.Write(buf)
	return err
}

func (c *Conn) Close() error {
	var err error
	c.closeOnce.Do(func() { err = c.nc.Close() })
	return err
}
```

- [ ] **Step 5: Implement the test server**

`~/turtle-agent/internal/worldconn/worldtest/server.go`:

```go
// Package worldtest is a scripted world server for tests: the server side of
// WorldSocket.cpp's handshake (challenge, digest check, cipher start, auth response)
// and of MangosSocket framing. Tests only; it never listens on a port.
package worldtest

import (
	"bytes"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/opcode"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
	"github.com/TortoiseBots/turtle-agent/internal/worldconn"
)

type Options struct {
	Seed    [4]byte            // zero: DE AD BE EF
	Reject  uint8              // non-zero: answer with this code in a plain header
	Queue   bool               // send a 14-byte AUTH_WAIT_QUEUE first
	ShortOK bool               // 1-byte AUTH_OK instead of 10 bytes
	Early   []worldconn.Packet // sent after the cipher starts, before AUTH_OK
}

type Server struct {
	Account    string
	ClientSeed uint32
	Build      uint32
	AddonBlock []byte

	nc      net.Conn
	crypt   *worldconn.HeaderCrypt
	in      chan worldconn.Packet
	writeMu sync.Mutex
	mu      sync.Mutex
	skipped []worldconn.Packet
}

func Accept(nc net.Conn, key []byte, o Options) (*Server, error) {
	s := &Server{nc: nc, in: make(chan worldconn.Packet, 1024)}
	if o.Seed == ([4]byte{}) {
		o.Seed = [4]byte{0xDE, 0xAD, 0xBE, 0xEF}
	}
	if err := s.sendPlain(opcode.SMSGAuthChallenge, o.Seed[:]); err != nil {
		return nil, err
	}
	p, err := s.recvPlain()
	if err != nil {
		return nil, err
	}
	if p.Op != opcode.CMSGAuthSession {
		return nil, fmt.Errorf("worldtest: got %s, want CMSG_AUTH_SESSION", opcode.Name(p.Op))
	}
	r := packet.NewReader(p.Body)
	s.Build = r.U32()
	r.U32()
	s.Account = r.CString()
	s.ClientSeed = r.U32()
	digest := r.Bytes(20)
	s.AddonBlock = r.Bytes(r.Remaining())
	if err := r.Err(); err != nil {
		return nil, err
	}
	want := sha1.New()
	want.Write([]byte(s.Account))
	want.Write([]byte{0, 0, 0, 0})
	want.Write(binary.LittleEndian.AppendUint32(nil, s.ClientSeed))
	want.Write(o.Seed[:])
	want.Write(key)
	if !bytes.Equal(want.Sum(nil), digest) {
		_ = s.sendPlain(opcode.SMSGAuthResponse, []byte{worldconn.AuthFailed})
		return nil, errors.New("worldtest: digest mismatch")
	}
	if o.Reject != 0 {
		return s, s.sendPlain(opcode.SMSGAuthResponse, []byte{o.Reject})
	}
	s.crypt = worldconn.NewHeaderCrypt(key)
	for _, e := range o.Early {
		if err := s.Send(e.Op, e.Body); err != nil {
			return nil, err
		}
	}
	if o.Queue {
		q := packet.NewWriter().U8(worldconn.AuthWaitQueue).U32(0).U8(0).U32(0).U32(3) // World.cpp:471-477
		if err := s.Send(opcode.SMSGAuthResponse, q.Bytes()); err != nil {
			return nil, err
		}
	}
	ok := packet.NewWriter().U8(worldconn.AuthOK)
	if !o.ShortOK {
		ok.U32(0).U8(0).U32(0) // World.cpp:367-371
	}
	if err := s.Send(opcode.SMSGAuthResponse, ok.Bytes()); err != nil {
		return nil, err
	}
	go s.readLoop()
	return s, nil
}

func (s *Server) sendPlain(op uint16, body []byte) error {
	h := binary.BigEndian.AppendUint16(nil, uint16(len(body)+2))
	h = binary.LittleEndian.AppendUint16(h, op)
	_, err := s.nc.Write(append(h, body...))
	return err
}

func (s *Server) recvPlain() (worldconn.Packet, error) {
	h := make([]byte, 6)
	if _, err := io.ReadFull(s.nc, h); err != nil {
		return worldconn.Packet{}, err
	}
	body := make([]byte, int(binary.BigEndian.Uint16(h))-4)
	_, err := io.ReadFull(s.nc, body)
	return worldconn.Packet{Op: uint16(binary.LittleEndian.Uint32(h[2:])), Body: body}, err
}

// Send writes one server packet with an encrypted 4-byte header (AuthCrypt::EncryptSend).
func (s *Server) Send(op uint16, body []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	h := binary.BigEndian.AppendUint16(nil, uint16(len(body)+2))
	h = binary.LittleEndian.AppendUint16(h, op)
	s.crypt.Encrypt(h)
	_, err := s.nc.Write(append(h, body...))
	return err
}

func (s *Server) readLoop() {
	defer close(s.in)
	for {
		h := make([]byte, 6)
		if _, err := io.ReadFull(s.nc, h); err != nil {
			return
		}
		s.crypt.Decrypt(h) // AuthCrypt::DecryptRecv
		body := make([]byte, int(binary.BigEndian.Uint16(h))-4)
		if _, err := io.ReadFull(s.nc, body); err != nil {
			return
		}
		s.in <- worldconn.Packet{Op: uint16(binary.LittleEndian.Uint32(h[2:])), Body: body}
	}
}

// Expect returns the next client packet with opcode op; packets with other opcodes
// are kept in Skipped.
func (s *Server) Expect(op uint16, timeout time.Duration) (worldconn.Packet, error) {
	deadline := time.After(timeout)
	for {
		select {
		case p, ok := <-s.in:
			if !ok {
				return worldconn.Packet{}, errors.New("worldtest: client connection closed")
			}
			if p.Op == op {
				return p, nil
			}
			s.mu.Lock()
			s.skipped = append(s.skipped, p)
			s.mu.Unlock()
		case <-deadline:
			return worldconn.Packet{}, fmt.Errorf("worldtest: no %s within %s", opcode.Name(op), timeout)
		}
	}
}

func (s *Server) Skipped() []worldconn.Packet {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]worldconn.Packet(nil), s.skipped...)
}

func (s *Server) Close() error { return s.nc.Close() }
```

- [ ] **Step 6: Run the tests**

Run: `make -C ~/turtle-agent fmt test`
Expected: PASS for `internal/worldconn` and `internal/worldconn/worldtest` (`[no test files]` for the latter).

- [ ] **Step 7: Record provenance and commit**

Append to `~/turtle-agent/NOTICE`:

```
internal/worldconn/conn.go | pkg/worldconn/protocol.go, pkg/worldconn/conn.go | ported (ReadFull framing, serialized writes, Dial-then-authenticate kept; 4-byte server header, XOR cipher, 1.12 auth session and response rewritten)
```

```bash
cd ~/turtle-agent && git status --short
git add internal/worldconn NOTICE
git commit -F - <<'EOF'
worldconn: XOR header cipher, 1.12 framing, auth session with addon block

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01MKCBbD9VJLeKqwGUyyzxyw
EOF
```

---

## Task 7: Ordered event log

**Files:**
- Create: `internal/events/log.go`
- Test: `internal/events/log_test.go`

**Interfaces:**
- Produces: kind constants `KindSession`, `KindChat`, `KindWhisper`, `KindSystem`, `KindParty`, `KindPartyStats`, `KindMelee`, `KindSpellDamage`, `KindPeriodic`, `KindHeal`, `KindEnergize`, `KindSpellStart`, `KindSpellGo`, `KindSpellFail`, `KindSpellMiss`, `KindAuraApplied`, `KindAuraRemoved`, `KindDeath`, `KindKill`, `KindAttackStart`, `KindAttackStop`, `KindAttackError`, `KindParseError`; `var ChatKinds []string`; `type Event struct { Seq uint64; Time time.Time; Kind, Source string; SourceGUID packet.GUID; Target string; TargetGUID packet.GUID; SpellID uint32; Amount int64; Crit, Party bool; Text string; Data map[string]any }`; `const DefaultLimit = 500`, `const MaxLimit = 5000`; `type Query struct { Since uint64; Kinds []string; PartyOnly bool; Limit int }`; `type Page struct { Events []Event; Next, Oldest uint64; Dropped, More bool }`; `type Log`; `func New(capacity int) *Log`; `(*Log).Append(e Event) Event`; `(*Log).Read(q Query) Page`; `(*Log).Wait(ctx context.Context, since uint64) error`; `(*Log).Last() uint64`.
- Consumes: Task 1 (`packet.GUID`).

Semantics: `Seq` starts at 1 and grows by one per `Append` across all kinds. `Since` is exclusive. `Next` is the `Seq` of the last event scanned, including events a `Kinds` or `PartyOnly` filter skipped, so the next call never rescans them. `Oldest` is the smallest `Seq` still held; `Dropped` is true when entries after `Since` were evicted. A `Since` above `Last()` (a cursor from an earlier process) returns no events and `Next = Last()`.

- [ ] **Step 1: Write the failing test**

`~/turtle-agent/internal/events/log_test.go`:

```go
package events

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func appendKinds(l *Log, kinds ...string) {
	for _, k := range kinds {
		l.Append(Event{Kind: k})
	}
}

func TestSinceIsExclusiveAndContiguous(t *testing.T) {
	l := New(100)
	appendKinds(l, KindChat, KindMelee, KindChat, KindHeal)
	p := l.Read(Query{Since: 1})
	if len(p.Events) != 3 || p.Events[0].Seq != 2 || p.Events[2].Seq != 4 || p.Next != 4 || p.Dropped || p.More {
		t.Fatalf("%+v", p)
	}
	if p := l.Read(Query{Since: 4}); len(p.Events) != 0 || p.Next != 4 {
		t.Fatalf("%+v", p)
	}
}

func TestKindsFilterAdvancesNext(t *testing.T) {
	l := New(100)
	appendKinds(l, KindMelee, KindChat, KindMelee)
	p := l.Read(Query{Kinds: []string{KindChat}})
	if len(p.Events) != 1 || p.Events[0].Seq != 2 || p.Next != 3 {
		t.Fatalf("%+v", p)
	}
}

func TestPartyOnly(t *testing.T) {
	l := New(100)
	l.Append(Event{Kind: KindMelee, Party: true})
	l.Append(Event{Kind: KindMelee})
	if p := l.Read(Query{PartyOnly: true}); len(p.Events) != 1 || p.Events[0].Seq != 1 || p.Next != 2 {
		t.Fatalf("%+v", p)
	}
}

func TestLimitAndMore(t *testing.T) {
	l := New(100)
	for i := 0; i < 10; i++ {
		l.Append(Event{Kind: KindChat})
	}
	p := l.Read(Query{Limit: 4})
	if len(p.Events) != 4 || !p.More || p.Next != 4 {
		t.Fatalf("%+v", p)
	}
	p = l.Read(Query{Since: p.Next, Limit: 100})
	if len(p.Events) != 6 || p.More || p.Next != 10 {
		t.Fatalf("%+v", p)
	}
}

func TestEvictionReportsDropped(t *testing.T) {
	l := New(10)
	for i := 0; i < 25; i++ {
		l.Append(Event{Kind: KindChat})
	}
	p := l.Read(Query{})
	if !p.Dropped || p.Oldest <= 1 || p.Events[0].Seq != p.Oldest || p.Next != 25 || len(p.Events) > 10 {
		t.Fatalf("%+v", p)
	}
	if p := l.Read(Query{Since: p.Oldest}); p.Dropped {
		t.Fatalf("cursor at Oldest must not report drops: %+v", p)
	}
}

func TestConcurrentAppend(t *testing.T) {
	l := New(100000)
	var writers, reader sync.WaitGroup
	for g := 0; g < 8; g++ {
		writers.Add(1)
		go func() {
			defer writers.Done()
			for i := 0; i < 1000; i++ {
				l.Append(Event{Kind: KindChat})
			}
		}()
	}
	done := make(chan struct{})
	reader.Add(1)
	go func() {
		defer reader.Done()
		var next uint64
		for {
			p := l.Read(Query{Since: next, Limit: MaxLimit})
			for _, e := range p.Events {
				if e.Seq != next+1 {
					t.Errorf("gap: %d after %d", e.Seq, next)
				}
				next = e.Seq
			}
			select {
			case <-done:
				if next == 8000 {
					return
				}
			default:
			}
		}
	}()
	writers.Wait()
	close(done)
	reader.Wait()
	if l.Last() != 8000 {
		t.Fatalf("last %d", l.Last())
	}
}

func TestCursorAheadOfLog(t *testing.T) {
	l := New(10)
	appendKinds(l, KindChat)
	if p := l.Read(Query{Since: 99}); p.Next != 1 || len(p.Events) != 0 {
		t.Fatalf("%+v", p)
	}
}

func TestWaitWakesOnAppendAndHonoursContext(t *testing.T) {
	l := New(10)
	go func() { time.Sleep(20 * time.Millisecond); l.Append(Event{Kind: KindChat}) }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := l.Wait(ctx, 0); err != nil {
		t.Fatal(err)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel2()
	if err := l.Wait(ctx2, 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}

func TestEventJSONShape(t *testing.T) {
	b, _ := json.Marshal(Event{Seq: 1, Kind: KindMelee, SourceGUID: 0xF130000000001234, Amount: 12})
	s := string(b)
	if !strings.Contains(s, `"source_guid":"0xF130000000001234"`) || strings.Contains(s, "target_guid") || !strings.Contains(s, `"amount":12`) {
		t.Fatal(s)
	}
}
```

- [ ] **Step 2: Run it and see it fail**

Run: `cd ~/turtle-agent && ~/.local/go/bin/go test ./internal/events/`
Expected: FAIL, `undefined: Log` (and `New`, `Event`, `Query`, kind constants).

- [ ] **Step 3: Implement**

`~/turtle-agent/internal/events/log.go`:

```go
// Package events is the ordered event log the session appends to and RPC pages through.
package events

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

const (
	KindSession     = "session"      // state changes: in_world, logout_pending, logged_out, disconnected, teleported
	KindChat        = "chat"         // say, party, yell, emote, channel, monster speech
	KindWhisper     = "whisper"      // whispers in and out
	KindSystem      = "system"       // system messages, GM command output, notifications
	KindParty       = "party"        // invites, command results, joins and leaves
	KindPartyStats  = "party_stats"  // SMSG_PARTY_MEMBER_STATS
	KindMelee       = "melee"        // SMSG_ATTACKERSTATEUPDATE
	KindSpellDamage = "spell_damage" // SMSG_SPELLNONMELEEDAMAGELOG
	KindPeriodic    = "periodic"     // SMSG_PERIODICAURALOG
	KindHeal        = "heal"         // SMSG_SPELLHEALLOG
	KindEnergize    = "energize"     // SMSG_SPELLENERGIZELOG
	KindSpellStart  = "spell_start"
	KindSpellGo     = "spell_go"
	KindSpellFail   = "spell_fail"
	KindSpellMiss   = "spell_miss"
	KindAuraApplied = "aura_applied"
	KindAuraRemoved = "aura_removed"
	KindDeath       = "death" // health reached zero
	KindKill        = "kill"  // SMSG_PARTYKILLLOG
	KindAttackStart = "attack_start"
	KindAttackStop  = "attack_stop"
	KindAttackError = "attack_error"
	KindParseError  = "parse_error"
)

// ChatKinds is what the chat_log method returns.
var ChatKinds = []string{KindChat, KindWhisper, KindSystem}

// Event is one log entry. Names are resolved by the session before Append.
type Event struct {
	Seq        uint64         `json:"seq"`
	Time       time.Time      `json:"time"`
	Kind       string         `json:"kind"`
	Source     string         `json:"source,omitempty"`
	SourceGUID packet.GUID    `json:"source_guid,omitempty"`
	Target     string         `json:"target,omitempty"`
	TargetGUID packet.GUID    `json:"target_guid,omitempty"`
	SpellID    uint32         `json:"spell_id,omitempty"`
	Amount     int64          `json:"amount,omitempty"`
	Crit       bool           `json:"crit,omitempty"`
	Party      bool           `json:"party,omitempty"` // source or target is the player or a party member
	Text       string         `json:"text,omitempty"`
	Data       map[string]any `json:"data,omitempty"`
}

const (
	DefaultLimit = 500
	MaxLimit     = 5000
)

type Query struct {
	Since     uint64
	Kinds     []string
	PartyOnly bool
	Limit     int
}

type Page struct {
	Events  []Event `json:"events"`
	Next    uint64  `json:"next"`
	Oldest  uint64  `json:"oldest"`
	Dropped bool    `json:"dropped"`
	More    bool    `json:"more"`
}

// Log is a bounded ring of events; when full it drops the oldest tenth.
type Log struct {
	mu      sync.Mutex
	limit   int
	buf     []Event
	last    uint64
	changed chan struct{}
}

func New(capacity int) *Log {
	if capacity < 10 {
		capacity = 10
	}
	return &Log{limit: capacity, changed: make(chan struct{})}
}

// Append stores e with the next sequence number, stamps Time if unset and wakes waiters.
func (l *Log) Append(e Event) Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.last++
	e.Seq = l.last
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	if len(l.buf) >= l.limit {
		n := copy(l.buf, l.buf[l.limit/10:])
		clear(l.buf[n:])
		l.buf = l.buf[:n]
	}
	l.buf = append(l.buf, e)
	close(l.changed)
	l.changed = make(chan struct{})
	return e
}

func (l *Log) Last() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.last
}

func (l *Log) Read(q Query) Page {
	l.mu.Lock()
	defer l.mu.Unlock()
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	p := Page{Events: []Event{}, Next: q.Since, Oldest: l.last + 1}
	if len(l.buf) > 0 {
		p.Oldest = l.buf[0].Seq
	}
	if q.Since > l.last {
		p.Next = l.last
		return p
	}
	p.Dropped = q.Since+1 < p.Oldest
	kinds := make(map[string]bool, len(q.Kinds))
	for _, k := range q.Kinds {
		kinds[k] = true
	}
	for i := sort.Search(len(l.buf), func(i int) bool { return l.buf[i].Seq > q.Since }); i < len(l.buf); i++ {
		if len(p.Events) == limit {
			p.More = true
			break
		}
		e := l.buf[i]
		p.Next = e.Seq
		if (len(kinds) > 0 && !kinds[e.Kind]) || (q.PartyOnly && !e.Party) {
			continue
		}
		p.Events = append(p.Events, e)
	}
	return p
}

// Wait returns once an event with Seq > since exists, or when ctx ends.
func (l *Log) Wait(ctx context.Context, since uint64) error {
	for {
		l.mu.Lock()
		if l.last > since {
			l.mu.Unlock()
			return nil
		}
		ch := l.changed
		l.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
```

- [ ] **Step 4: Run the tests**

Run: `make -C ~/turtle-agent fmt test`
Expected: PASS, `ok  github.com/TortoiseBots/turtle-agent/internal/events` (with `-race`).

- [ ] **Step 5: Commit**

```bash
cd ~/turtle-agent && git status --short
git add internal/events
git commit -F - <<'EOF'
events: ordered event log with exclusive cursors, filters and eviction

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01MKCBbD9VJLeKqwGUyyzxyw
EOF
```

---

## Task 8: Capture files and the scrub check

**Files:**
- Create: `internal/capture/capture.go`
- Test: `internal/capture/capture_test.go`

**Interfaces:**
- Produces: `type Record struct { Dir worldconn.Direction; Time time.Time; Op uint16; Body []byte; Redacted bool }`; `type Writer` implementing `worldconn.Tap`; `func Create(path string, secrets ...[]byte) (*Writer, error)`; `(*Writer).Packet(dir worldconn.Direction, op uint16, body []byte)`; `(*Writer).Counts() (records, redacted int)`; `(*Writer).Close() error`; `func ReadFile(path string) ([]Record, error)`; `type Finding struct { Index int; Op uint16; Needle int }`; `func Scan(recs []Record, needles ...[]byte) []Finding`; `func Bodies(recs []Record, op uint16) [][]byte`; `func Stats(recs []Record) map[string]int`.
- Consumes: Task 1 (`packet`), Task 2 (`opcode`), Task 6 (`worldconn.Direction`, `worldconn.Tap`).

File format `TWCAP1`: the 7 bytes `TWCAP1\n`, then records of `u8 flags` (bit 0 direction, bit 7 redacted), `u64` Unix nanoseconds, `u16` opcode, `u32` body length, body. Bodies are payloads after header decryption; payloads are never encrypted in 1.12. A redacted record keeps opcode and time and has an empty body. Secrets shorter than 3 bytes are ignored (they would match everywhere). Matching is case-sensitive so a character name like `Harnessb` is not redacted for an account named `HARNESS`.

- [ ] **Step 1: Write the failing test**

`~/turtle-agent/internal/capture/capture_test.go`:

```go
package capture

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/TortoiseBots/turtle-agent/internal/opcode"
	"github.com/TortoiseBots/turtle-agent/internal/worldconn"
)

func TestRoundTripAndMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.twcap")
	w, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w.Packet(worldconn.ServerToClient, opcode.SMSGCharEnum, []byte{1, 2, 3})
	w.Packet(worldconn.ClientToServer, opcode.CMSGPing, []byte{4})
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode())
	}
	recs, err := ReadFile(path)
	if err != nil || len(recs) != 2 {
		t.Fatalf("%v %v", recs, err)
	}
	if recs[0].Op != opcode.SMSGCharEnum || recs[0].Dir != worldconn.ServerToClient || !bytes.Equal(recs[0].Body, []byte{1, 2, 3}) ||
		recs[1].Dir != worldconn.ClientToServer || recs[1].Redacted {
		t.Fatalf("%+v", recs)
	}
	if got := Bodies(recs, opcode.SMSGCharEnum); len(got) != 1 {
		t.Fatalf("Bodies %v", got)
	}
	if Stats(recs)["SMSG_CHAR_ENUM"] != 1 {
		t.Fatalf("stats %v", Stats(recs))
	}
}

func TestNeverStoresAuthSessionOrWarden(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.twcap")
	w, _ := Create(path)
	w.Packet(worldconn.ClientToServer, opcode.CMSGAuthSession, []byte("HARNESS\x00digest"))
	w.Packet(worldconn.ServerToClient, opcode.SMSGWardenData, []byte{9, 9, 9})
	w.Close()
	recs, _ := ReadFile(path)
	for _, r := range recs {
		if !r.Redacted || len(r.Body) != 0 {
			t.Fatalf("%s stored: %+v", opcode.Name(r.Op), r)
		}
	}
}

func TestSecretsRedacted(t *testing.T) {
	key := bytes.Repeat([]byte{0xAB, 0xCD}, 20)
	path := filepath.Join(t.TempDir(), "c.twcap")
	w, _ := Create(path, []byte("HARNESS"), []byte("pw-secret"), key, []byte("x"))
	w.Packet(worldconn.ServerToClient, opcode.SMSGMessagechat, []byte("say pw-secret now"))
	w.Packet(worldconn.ServerToClient, opcode.SMSGMessagechat, append([]byte{1}, key...))
	w.Packet(worldconn.ServerToClient, opcode.SMSGNameQueryResponse, []byte("Harnessb\x00"))
	if n, red := w.Counts(); n != 3 || red != 2 {
		t.Fatalf("counts %d %d", n, red)
	}
	w.Close()
	recs, _ := ReadFile(path)
	if !recs[0].Redacted || !recs[1].Redacted || recs[2].Redacted {
		t.Fatalf("%+v", recs)
	}
	if f := Scan(recs, []byte("Harnessb")); len(f) != 1 || f[0].Index != 2 {
		t.Fatalf("scan %v", f)
	}
}

func TestCreateRefusesOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.twcap")
	w, _ := Create(path)
	w.Close()
	if _, err := Create(path); err == nil {
		t.Fatal("overwrote an existing capture")
	}
}

func TestReadFileRejectsGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad")
	os.WriteFile(path, []byte("TWCAP1\n\x00\x01"), 0o600)
	if _, err := ReadFile(path); err == nil {
		t.Fatal("truncated record accepted")
	}
}
```

- [ ] **Step 2: Run it and see it fail**

Run: `cd ~/turtle-agent && ~/.local/go/bin/go test ./internal/capture/`
Expected: FAIL, `undefined: Create` (and `ReadFile`, `Bodies`, `Stats`, `Scan`).

- [ ] **Step 3: Implement**

`~/turtle-agent/internal/capture/capture.go`:

```go
// Package capture records decrypted world packets for test fixtures. A capture never
// holds realmd traffic, CMSG_AUTH_SESSION or Warden bodies, or any registered secret.
package capture

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/opcode"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
	"github.com/TortoiseBots/turtle-agent/internal/worldconn"
)

const (
	magic       = "TWCAP1\n"
	redactedBit = 0x80
)

// neverStored bodies are dropped whatever they contain.
var neverStored = map[uint16]bool{
	opcode.CMSGAuthSession: true, // account name and a key-derived digest (WorldSocket.cpp:144-149)
	opcode.SMSGWardenData:  true, // key-derived Warden payloads
	opcode.CMSGWardenData:  true,
}

type Record struct {
	Dir      worldconn.Direction
	Time     time.Time
	Op       uint16
	Body     []byte
	Redacted bool
}

type Writer struct {
	mu       sync.Mutex
	f        *os.File
	bw       *bufio.Writer
	secrets  [][]byte
	count    int
	redacted int
	err      error
}

// Create opens a new capture (never overwrites, mode 0600). Secrets are byte strings
// that must not appear in any stored body: account names, passwords, the session key.
func Create(path string, secrets ...[]byte) (*Writer, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("capture: %w", err)
	}
	w := &Writer{f: f, bw: bufio.NewWriter(f)}
	for _, s := range secrets {
		if len(s) >= 3 {
			w.secrets = append(w.secrets, bytes.Clone(s))
		}
	}
	if _, err := w.bw.WriteString(magic); err != nil {
		f.Close()
		return nil, fmt.Errorf("capture: %w", err)
	}
	return w, nil
}

// Packet implements worldconn.Tap.
func (w *Writer) Packet(dir worldconn.Direction, op uint16, body []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return
	}
	flags := uint8(dir)
	if neverStored[op] || w.hasSecret(body) {
		flags |= redactedBit
		body = nil
		w.redacted++
	}
	hdr := packet.NewWriter().U8(flags).U64(uint64(time.Now().UnixNano())).U16(op).U32(uint32(len(body)))
	if _, err := w.bw.Write(hdr.Bytes()); err != nil {
		w.err = err
		return
	}
	if _, err := w.bw.Write(body); err != nil {
		w.err = err
		return
	}
	w.count++
}

func (w *Writer) hasSecret(body []byte) bool {
	for _, s := range w.secrets {
		if bytes.Contains(body, s) {
			return true
		}
	}
	return false
}

func (w *Writer) Counts() (records, redacted int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.count, w.redacted
}

func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.bw.Flush(); err != nil && w.err == nil {
		w.err = err
	}
	if err := w.f.Close(); err != nil && w.err == nil {
		w.err = err
	}
	return w.err
}

func ReadFile(path string) ([]Record, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("capture: %w", err)
	}
	if !bytes.HasPrefix(raw, []byte(magic)) {
		return nil, fmt.Errorf("capture %s: not a TWCAP1 file", path)
	}
	r := packet.NewReader(raw[len(magic):])
	var recs []Record
	for r.Remaining() > 0 {
		flags, ts, op, n := r.U8(), r.U64(), r.U16(), r.U32()
		body := r.Bytes(int(n))
		if err := r.Err(); err != nil {
			return nil, fmt.Errorf("capture %s: record %d: %w", path, len(recs), err)
		}
		recs = append(recs, Record{
			Dir: worldconn.Direction(flags &^ redactedBit), Time: time.Unix(0, int64(ts)),
			Op: op, Body: body, Redacted: flags&redactedBit != 0,
		})
	}
	return recs, nil
}

type Finding struct {
	Index  int    // record index
	Op     uint16 // record opcode
	Needle int    // index into the needles argument
}

// Scan reports every record whose body contains one of the needles (3 bytes or longer).
func Scan(recs []Record, needles ...[]byte) []Finding {
	var out []Finding
	for i, rec := range recs {
		for j, n := range needles {
			if len(n) >= 3 && bytes.Contains(rec.Body, n) {
				out = append(out, Finding{Index: i, Op: rec.Op, Needle: j})
			}
		}
	}
	return out
}

// Bodies returns, in order, the bodies of server packets with opcode op.
func Bodies(recs []Record, op uint16) [][]byte {
	var out [][]byte
	for _, r := range recs {
		if r.Op == op && r.Dir == worldconn.ServerToClient && !r.Redacted {
			out = append(out, r.Body)
		}
	}
	return out
}

// Stats counts records per opcode name.
func Stats(recs []Record) map[string]int {
	out := map[string]int{}
	for _, r := range recs {
		out[opcode.Name(r.Op)]++
	}
	return out
}
```

- [ ] **Step 4: Run the tests**

Run: `make -C ~/turtle-agent fmt test`
Expected: PASS, `ok  github.com/TortoiseBots/turtle-agent/internal/capture`.

- [ ] **Step 5: Commit**

```bash
cd ~/turtle-agent && git status --short
git add internal/capture
git commit -F - <<'EOF'
capture: TWCAP1 packet recorder with secret redaction and scan

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01MKCBbD9VJLeKqwGUyyzxyw
EOF
```

---

## Task 9: Session core (char enum, enter world, ping, logout, disconnect)

**Files:**
- Create: `internal/session/session.go`, `internal/session/charenum.go`
- Modify: `NOTICE` (append one row)
- Test: `internal/session/session_test.go`

**Interfaces:**
- Produces: `type Status string` with `StatusConnecting`, `StatusCharSelect`, `StatusLoggingIn`, `StatusInWorld`, `StatusLoggingOut`, `StatusLoggedOut`, `StatusDisconnected`; `var ErrClosed, ErrDisconnected, ErrNotInWorld error`; `type LogoutError struct { Reason uint32 }`; `type Options struct { Character string; Log *events.Log; Logger io.Writer; PingInterval, PongTimeout, LogoutTimeout time.Duration }`; `type StatusInfo struct { State Status; Character string; GUID packet.GUID; Level uint8; Map uint32; X, Y, Z, O float32; LatencyMS uint32; Unknown map[string]int; ParseErrors int; LastSeq uint64; Error string }`; `type Character struct { GUID packet.GUID; Name string; Race, Class, Gender, Level uint8; Zone, Map uint32; X, Y, Z float32; Guild, Flags uint32; FirstLogin bool }`; `func ParseCharEnum(body []byte) ([]Character, error)`; `type Session`; `func New(conn *worldconn.Conn, opts Options) *Session`; `(*Session).Run(ctx context.Context) error`; `Ready() <-chan struct{}`; `Done() <-chan struct{}`; `Log() *events.Log`; `State() Status`; `Status() StatusInfo`; `Logout(ctx context.Context) error`.
- Internal API later tasks extend: `type handler func(body []byte) error`; `s.handlers map[uint16]handler`; `func (s *Session) register()` (later tasks add one `s.registerX()` line each); `func (s *Session) do(ctx context.Context, fn func() (any, error)) (any, error)` (runs fn on the owner goroutine); `func (s *Session) send(ctx context.Context, op uint16, body []byte) error` (writes when in world); `func (s *Session) emit(e events.Event)`; `func (s *Session) flushPending()` (no-op here, Task 13 fills it); `func (s *Session) onTick(now time.Time) error` (called every 100 ms by `Run`; later tasks add lines); `type fatalError struct{ err error }` (a handler returning it ends the session).
- Consumes: Task 1, 2, 6 (`worldconn.Conn`, `worldtest`), Task 7 (`events.Log`).

Server facts mirrored (`~/tortoise-wow/src/game`): the client asks for the list with an empty `CMSG_CHAR_ENUM` (`Handlers/CharacterHandler.cpp:203`); `SMSG_CHAR_ENUM` is a u8 count (`CharacterHandler.cpp:164-169`, `:197`) then per character the record of `Player::BuildEnumData` (`Objects/Player.cpp:2198-2285`: u64 guid, name, race, class, gender, skin, face, hair style, hair colour, facial hair, level, u32 zone, u32 map, x, y, z, u32 guild, u32 flags, u8 first login, 3×u32 pet, then `INVENTORY_SLOT_BAG_START + 1` = 20 slots (`Objects/Player.h:612`) of u32 display + u8 inventory type), which is 150 bytes after the name (the spike's 151 is a miscount; Task 11's capture test pins it against the live server); `CMSG_PLAYER_LOGIN` is the u64 guid (`CharacterHandler.cpp:500-503`); `SMSG_CHARACTER_LOGIN_FAILED` is one byte (`:505-511`); `SMSG_LOGIN_VERIFY_WORLD` is u32 map + x, y, z, o (`:684-690`); the server sets the client mover at login (`:937`) and `CMSG_SET_ACTIVE_MOVER` carries the u64 guid (`Handlers/MovementHandler.cpp:838-876`); `CMSG_PING` is u32 sequence + u32 latency and `SMSG_PONG` echoes the sequence (`Protocol/WorldSocket.cpp:395-456`); `CMSG_LOGOUT_REQUEST` is empty and `SMSG_LOGOUT_RESPONSE` is u32 reason + u8 instant, reason 1 in combat, 2 frozen, 3 falling (`Handlers/MiscHandler.cpp:342-395`); `SMSG_LOGOUT_COMPLETE` is empty (`WorldSession.cpp:815`).

- [ ] **Step 1: Write the failing tests**

`~/turtle-agent/internal/session/session_test.go`:

```go
package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/events"
	"github.com/TortoiseBots/turtle-agent/internal/opcode"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
	"github.com/TortoiseBots/turtle-agent/internal/worldconn"
	"github.com/TortoiseBots/turtle-agent/internal/worldconn/worldtest"
)

var testKey = bytes.Repeat([]byte{0x5A, 0xA5}, 20)

var me = Character{GUID: 0x2A, Name: "Harnessb", Race: 1, Class: 1, Gender: 0, Level: 60, Zone: 12, Map: 0,
	X: -8913, Y: -136, Z: 82, Guild: 0, Flags: 0}

type harness struct {
	t   *testing.T
	s   *Session
	srv *worldtest.Server
	run chan error
}

func start(t *testing.T, opts Options) *harness {
	t.Helper()
	cli, srvConn := net.Pipe()
	t.Cleanup(func() { cli.Close(); srvConn.Close() })
	ch := make(chan *worldtest.Server, 1)
	go func() {
		srv, err := worldtest.Accept(srvConn, testKey, worldtest.Options{})
		if err != nil {
			t.Error(err)
		}
		ch <- srv
	}()
	conn, err := worldconn.Client(cli, "TESTACC", testKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	if opts.Character == "" {
		opts.Character = "Harnessb"
	}
	h := &harness{t: t, srv: <-ch, run: make(chan error, 1)}
	h.s = New(conn, opts)
	go func() { h.run <- h.s.Run(context.Background()) }()
	return h
}

// charEnumBody builds SMSG_CHAR_ENUM the way Player::BuildEnumData writes it (Player.cpp:2198-2285).
func charEnumBody(chars ...Character) []byte {
	w := packet.NewWriter().U8(uint8(len(chars)))
	for _, c := range chars {
		w.GUID(c.GUID).CString(c.Name).U8(c.Race).U8(c.Class).U8(c.Gender)
		w.U8(1).U8(2).U8(3).U8(4).U8(5) // skin, face, hair style, hair colour, facial hair
		w.U8(c.Level).U32(c.Zone).U32(c.Map).F32(c.X).F32(c.Y).F32(c.Z).U32(c.Guild).U32(c.Flags)
		if c.FirstLogin {
			w.U8(1)
		} else {
			w.U8(0)
		}
		w.U32(0).U32(0).U32(0) // pet display, level, family
		for slot := 0; slot < 20; slot++ {
			w.U32(uint32(1000 + slot)).U8(uint8(slot))
		}
	}
	return w.Bytes()
}

func (h *harness) expect(op uint16) worldconn.Packet {
	h.t.Helper()
	p, err := h.srv.Expect(op, 2*time.Second)
	if err != nil {
		h.t.Fatal(err)
	}
	return p
}

func (h *harness) send(op uint16, body []byte) {
	h.t.Helper()
	if err := h.srv.Send(op, body); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) enterWorld() {
	h.t.Helper()
	h.expect(opcode.CMSGCharEnum)
	h.send(opcode.SMSGCharEnum, charEnumBody(Character{GUID: 0x2B, Name: "Alt", Level: 1}, me))
	if p := h.expect(opcode.CMSGPlayerLogin); packet.NewReader(p.Body).GUID() != me.GUID {
		h.t.Fatalf("logged in % X", p.Body)
	}
	h.send(opcode.SMSGLoginVerifyWorld, packet.NewWriter().U32(0).F32(me.X).F32(me.Y).F32(me.Z).F32(1.5).Bytes())
	h.expect(opcode.CMSGSetActiveMover)
	select {
	case <-h.s.Ready():
	case <-time.After(2 * time.Second):
		h.t.Fatal("not ready")
	}
}

func (h *harness) logout() {
	h.t.Helper()
	done := make(chan error, 1)
	go func() { done <- h.s.Logout(context.Background()) }()
	h.expect(opcode.CMSGLogoutRequest)
	h.send(opcode.SMSGLogoutResponse, packet.NewWriter().U32(0).U8(1).Bytes())
	h.send(opcode.SMSGLogoutComplete, nil)
	if err := <-done; err != nil {
		h.t.Fatal(err)
	}
	if err := <-h.run; err != nil {
		h.t.Fatal(err)
	}
}

func ofKind(l *events.Log, kind string) []events.Event {
	return l.Read(events.Query{Kinds: []string{kind}, Limit: events.MaxLimit}).Events
}

func sessionStates(l *events.Log) []string {
	var out []string
	for _, e := range ofKind(l, events.KindSession) {
		out = append(out, fmt.Sprint(e.Data["state"]))
	}
	return out
}

func TestCharEnumTailIs150Bytes(t *testing.T) {
	body := charEnumBody(me)
	if len(body) != 1+8+len(me.Name)+1+150 {
		t.Fatalf("builder wrote %d bytes", len(body))
	}
	chars, err := ParseCharEnum(body)
	if err != nil || len(chars) != 1 || chars[0] != me {
		t.Fatalf("%+v %v", chars, err)
	}
	if _, err := ParseCharEnum(append(body, 0)); err == nil || !strings.Contains(err.Error(), "unread") {
		t.Fatalf("trailing byte: %v", err)
	}
}

func TestLoginToWorldAndLogout(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	if st := h.s.Status(); st.State != StatusInWorld || st.Character != "Harnessb" || st.GUID != me.GUID || st.X != me.X || st.O != 1.5 {
		t.Fatalf("%+v", st)
	}
	h.logout()
	if st := h.s.Status(); st.State != StatusLoggedOut {
		t.Fatalf("%+v", st)
	}
	if got := strings.Join(sessionStates(h.s.Log()), ","); got != "in_world,logout_pending,logged_out" {
		t.Fatalf("session events %s", got)
	}
}

func TestMissingCharacterEndsSession(t *testing.T) {
	h := start(t, Options{})
	h.expect(opcode.CMSGCharEnum)
	h.send(opcode.SMSGCharEnum, charEnumBody(Character{GUID: 0x2B, Name: "Alt"}))
	if err := <-h.run; err == nil || !strings.Contains(err.Error(), "Alt") {
		t.Fatalf("err = %v", err)
	}
}

// Review Focus 4.
func TestDisconnectInWorld(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	h.srv.Close()
	if err := <-h.run; !errors.Is(err, ErrDisconnected) {
		t.Fatalf("run err = %v", err)
	}
	if st := h.s.Status(); st.State != StatusDisconnected || st.Error == "" {
		t.Fatalf("%+v", st)
	}
	if states := sessionStates(h.s.Log()); states[len(states)-1] != "disconnected" {
		t.Fatalf("events %v", states)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := h.s.Logout(ctx); !errors.Is(err, ErrClosed) {
		t.Fatalf("logout after disconnect: %v", err)
	}
}

func TestDisconnectDuringLogout(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	done := make(chan error, 1)
	go func() { done <- h.s.Logout(context.Background()) }()
	h.expect(opcode.CMSGLogoutRequest)
	h.srv.Close()
	if err := <-done; !errors.Is(err, ErrDisconnected) {
		t.Fatalf("logout err = %v", err)
	}
	if err := <-h.run; !errors.Is(err, ErrDisconnected) {
		t.Fatalf("run err = %v", err)
	}
}

func TestLogoutRefusedInCombat(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	done := make(chan error, 1)
	go func() { done <- h.s.Logout(context.Background()) }()
	h.expect(opcode.CMSGLogoutRequest)
	h.send(opcode.SMSGLogoutResponse, packet.NewWriter().U32(1).U8(0).Bytes())
	var le *LogoutError
	if err := <-done; !errors.As(err, &le) || le.Reason != 1 {
		t.Fatalf("err = %v", err)
	}
	if h.s.State() != StatusInWorld {
		t.Fatalf("state %s", h.s.State())
	}
	h.logout()
}

// Review Focus 3: unknown opcodes are counted, a bad packet is logged, the session lives on.
func TestUnknownOpcodeCountedAndParseErrorSurvives(t *testing.T) {
	var logBuf bytes.Buffer
	h := start(t, Options{Logger: &logBuf})
	h.enterWorld()
	h.send(opcode.SMSGInitWorldStates, []byte{1, 2, 3})
	h.send(opcode.SMSGWardenData, []byte{0, 1, 2})
	h.send(opcode.SMSGPong, nil) // too short for its u32
	h.logout()
	st := h.s.Status()
	if st.Unknown["SMSG_INIT_WORLD_STATES"] != 1 || st.Unknown["SMSG_WARDEN_DATA"] != 0 || st.ParseErrors != 1 {
		t.Fatalf("%+v", st)
	}
	if pe := ofKind(h.s.Log(), events.KindParseError); len(pe) != 1 || pe[0].Data["opcode"] != "SMSG_PONG" {
		t.Fatalf("parse events %+v", pe)
	}
	if !strings.Contains(logBuf.String(), "SMSG_PONG") {
		t.Fatalf("log %q", logBuf.String())
	}
}

func TestPingCadence(t *testing.T) {
	h := start(t, Options{PingInterval: 50 * time.Millisecond})
	h.enterWorld()
	p := h.expect(opcode.CMSGPing)
	if packet.NewReader(p.Body).U32() != 1 {
		t.Fatalf("first ping % X", p.Body)
	}
	h.send(opcode.SMSGPong, packet.NewWriter().U32(1).Bytes())
	if p := h.expect(opcode.CMSGPing); packet.NewReader(p.Body).U32() != 2 {
		t.Fatalf("second ping % X", p.Body)
	}
	h.logout()
}

func TestPongTimeoutDisconnects(t *testing.T) {
	h := start(t, Options{PingInterval: 30 * time.Millisecond, PongTimeout: 100 * time.Millisecond})
	h.enterWorld()
	select {
	case err := <-h.run:
		if !errors.Is(err, ErrDisconnected) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no disconnect without pongs")
	}
}
```

- [ ] **Step 2: Run them and see them fail**

Run: `cd ~/turtle-agent && ~/.local/go/bin/go test ./internal/session/`
Expected: FAIL, `undefined: Character` (and `Options`, `Session`, `New`, `ParseCharEnum`, `ErrDisconnected`, `LogoutError`, status constants).

- [ ] **Step 3: Implement the char enum parser**

`~/turtle-agent/internal/session/charenum.go`:

```go
package session

import (
	"fmt"

	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

// Character is one SMSG_CHAR_ENUM entry (Player::BuildEnumData, Player.cpp:2176-2288).
type Character struct {
	GUID       packet.GUID `json:"guid"`
	Name       string      `json:"name"`
	Race       uint8       `json:"race"`
	Class      uint8       `json:"class"`
	Gender     uint8       `json:"gender"`
	Level      uint8       `json:"level"`
	Zone       uint32      `json:"zone"`
	Map        uint32      `json:"map"`
	X          float32     `json:"x"`
	Y          float32     `json:"y"`
	Z          float32     `json:"z"`
	Guild      uint32      `json:"guild"`
	Flags      uint32      `json:"flags"`
	FirstLogin bool        `json:"first_login"`
}

// ParseCharEnum reads a u8 count (CharacterHandler.cpp:164-169) and per character the
// guid, the name and a 150-byte tail (Player.cpp:2198-2285). Bytes left over mean the
// layout drifted, which is an error rather than a silent misparse.
func ParseCharEnum(body []byte) ([]Character, error) {
	r := packet.NewReader(body)
	n := int(r.U8())
	chars := make([]Character, 0, n)
	for i := 0; i < n; i++ {
		var c Character
		c.GUID, c.Name = r.GUID(), r.CString()
		c.Race, c.Class, c.Gender = r.U8(), r.U8(), r.U8()
		r.Skip(5) // skin, face, hair style, hair colour, facial hair
		c.Level = r.U8()
		c.Zone, c.Map = r.U32(), r.U32()
		c.X, c.Y, c.Z = r.F32(), r.F32(), r.F32()
		c.Guild, c.Flags = r.U32(), r.U32()
		c.FirstLogin = r.U8() != 0
		r.Skip(12)     // pet display id, level, family
		r.Skip(20 * 5) // 20 equipment slots: u32 display id + u8 inventory type
		chars = append(chars, c)
	}
	if err := r.Err(); err != nil {
		return nil, fmt.Errorf("SMSG_CHAR_ENUM: %w", err)
	}
	if r.Remaining() != 0 {
		return nil, fmt.Errorf("SMSG_CHAR_ENUM: %d unread bytes after %d characters (layout drift)", r.Remaining(), n)
	}
	return chars, nil
}
```

- [ ] **Step 4: Implement the session**

`~/turtle-agent/internal/session/session.go`:

```go
// Copyright (c) 2026 agent-wow
// SPDX-License-Identifier: MIT
// The single-owner loop shape (one select over packets, requests and timers; ping
// cadence; logout wait) is ported from github.com/agent-wow/agent-wow pkg/world/engine.go
// at 5ef4916 (see NOTICE). The login sequence, readiness rule and handlers are new.

// Package session owns one logged-in character. Run is the only goroutine that writes
// packets and changes state; other goroutines call methods that queue work for it.
package session

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/events"
	"github.com/TortoiseBots/turtle-agent/internal/opcode"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
	"github.com/TortoiseBots/turtle-agent/internal/worldconn"
)

type Status string

const (
	StatusConnecting   Status = "connecting"
	StatusCharSelect   Status = "char_select"
	StatusLoggingIn    Status = "logging_in"
	StatusInWorld      Status = "in_world"
	StatusLoggingOut   Status = "logging_out"
	StatusLoggedOut    Status = "logged_out"
	StatusDisconnected Status = "disconnected"
)

var (
	ErrClosed       = errors.New("session closed")
	ErrDisconnected = errors.New("disconnected by the server")
	ErrNotInWorld   = errors.New("not in world")
)

// LogoutError is a refused logout (MiscHandler.cpp:350-365).
type LogoutError struct{ Reason uint32 }

func (e *LogoutError) Error() string {
	reason := "unknown reason"
	switch e.Reason {
	case 1:
		reason = "in combat"
	case 2:
		reason = "frozen by a GM"
	case 3:
		reason = "jumping or falling"
	}
	return fmt.Sprintf("logout refused: %s (reason %d)", reason, e.Reason)
}

type Options struct {
	Character     string        // character name, case-insensitive
	Log           *events.Log   // nil: a new 100000-event log
	Logger        io.Writer     // diagnostics such as parse errors with hex dumps; nil: discarded
	PingInterval  time.Duration // 0: 30 s (the server flags pings under 27 s, WorldSocket.cpp:413)
	PongTimeout   time.Duration // 0: 90 s
	LogoutTimeout time.Duration // 0: 30 s
}

type StatusInfo struct {
	State       Status         `json:"state"`
	Character   string         `json:"character,omitempty"`
	GUID        packet.GUID    `json:"guid,omitempty"`
	Level       uint8          `json:"level,omitempty"`
	Map         uint32         `json:"map"`
	X           float32        `json:"x"`
	Y           float32        `json:"y"`
	Z           float32        `json:"z"`
	O           float32        `json:"o"`
	LatencyMS   uint32         `json:"latency_ms"`
	Unknown     map[string]int `json:"unknown_opcodes"`
	ParseErrors int            `json:"parse_errors"`
	LastSeq     uint64         `json:"last_seq"`
	Error       string         `json:"error,omitempty"`
}

type handler func(body []byte) error

// fatalError, returned by a handler, ends the session (login failures).
type fatalError struct{ err error }

func (f *fatalError) Error() string { return f.err.Error() }

type request struct {
	fn    func() (any, error)
	reply chan result
}

type result struct {
	v   any
	err error
}

type logoutOp struct {
	done chan struct{}
	err  error
}

func (op *logoutOp) finish(err error) { op.err = err; close(op.done) }

type Session struct {
	conn     *worldconn.Conn
	opts     Options
	log      *events.Log
	logger   io.Writer
	handlers map[uint16]handler
	reqs     chan request
	done     chan struct{}
	ready    chan struct{}
	start    time.Time

	mu        sync.Mutex // guards these fields for readers outside the loop
	state     Status
	self      Character
	mapID     uint32
	pos       [4]float32
	latency   uint32
	unknown   map[uint16]int
	parseErrs int
	lastErr   string

	// owner-loop only
	finished       bool
	nextPing       time.Time
	pingSent       time.Time
	pingSeq        uint32
	pongWait       bool
	logout         *logoutOp
	logoutDeadline time.Time
}

func New(conn *worldconn.Conn, opts Options) *Session {
	if opts.Log == nil {
		opts.Log = events.New(100000)
	}
	if opts.Logger == nil {
		opts.Logger = io.Discard
	}
	if opts.PingInterval == 0 {
		opts.PingInterval = 30 * time.Second
	}
	if opts.PongTimeout == 0 {
		opts.PongTimeout = 90 * time.Second
	}
	if opts.LogoutTimeout == 0 {
		opts.LogoutTimeout = 30 * time.Second
	}
	s := &Session{
		conn: conn, opts: opts, log: opts.Log, logger: opts.Logger,
		handlers: map[uint16]handler{}, reqs: make(chan request), done: make(chan struct{}),
		ready: make(chan struct{}), start: time.Now(), state: StatusConnecting, unknown: map[uint16]int{},
	}
	s.register()
	return s
}

// register installs every packet handler; each feature adds its own register call here.
func (s *Session) register() {
	s.registerCore()
}

func (s *Session) registerCore() {
	s.handlers[opcode.SMSGCharEnum] = s.onCharEnum
	s.handlers[opcode.SMSGCharacterLoginFailed] = s.onLoginFailed
	s.handlers[opcode.SMSGLoginVerifyWorld] = s.onVerifyWorld
	s.handlers[opcode.SMSGPong] = s.onPong
	s.handlers[opcode.SMSGLogoutResponse] = s.onLogoutResponse
	s.handlers[opcode.SMSGLogoutComplete] = s.onLogoutComplete
	// Warden is ignored: the timeout kick is commented out (Warden.cpp:498).
	s.handlers[opcode.SMSGWardenData] = func([]byte) error { return nil }
	s.handlers[opcode.SMSGAddonInfo] = func([]byte) error { return nil }
}

func (s *Session) Ready() <-chan struct{} { return s.ready }
func (s *Session) Done() <-chan struct{}  { return s.done }
func (s *Session) Log() *events.Log       { return s.log }

func (s *Session) State() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func (s *Session) setState(st Status) {
	s.mu.Lock()
	s.state = st
	s.mu.Unlock()
}

func (s *Session) Status() StatusInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	unknown := make(map[string]int, len(s.unknown))
	for op, n := range s.unknown {
		unknown[opcode.Name(op)] = n
	}
	return StatusInfo{
		State: s.state, Character: s.self.Name, GUID: s.self.GUID, Level: s.self.Level, Map: s.mapID,
		X: s.pos[0], Y: s.pos[1], Z: s.pos[2], O: s.pos[3], LatencyMS: s.latency,
		Unknown: unknown, ParseErrors: s.parseErrs, LastSeq: s.log.Last(), Error: s.lastErr,
	}
}

// Run drives the character from the character list to logout. It returns nil after a
// clean logout and an error wrapping ErrDisconnected when the server closes the socket.
func (s *Session) Run(ctx context.Context) error {
	defer close(s.done)
	defer s.conn.Close()
	packets := make(chan worldconn.Packet, 256)
	readErr := make(chan error, 1)
	go func() {
		for {
			p, err := s.conn.ReadPacket()
			if err != nil {
				readErr <- err
				return
			}
			select {
			case packets <- p:
			case <-s.done:
				return
			}
		}
	}()
	if err := s.conn.WritePacket(opcode.CMSGCharEnum, nil); err != nil { // CharacterHandler.cpp:203
		return s.connLost(err)
	}
	s.setState(StatusCharSelect)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return s.end(ctx.Err())
		case err := <-readErr:
			return s.connLost(err)
		case p := <-packets:
			if err := s.dispatch(p); err != nil {
				return s.end(err)
			}
			if s.finished {
				return nil
			}
		case r := <-s.reqs:
			v, err := r.fn()
			r.reply <- result{v, err}
		case now := <-tick.C:
			if err := s.onTick(now); err != nil {
				return s.connLost(err)
			}
		}
	}
}

// do runs fn on the owner goroutine and returns its result.
func (s *Session) do(ctx context.Context, fn func() (any, error)) (any, error) {
	r := request{fn: fn, reply: make(chan result, 1)}
	select {
	case s.reqs <- r:
	case <-s.done:
		return nil, ErrClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case res := <-r.reply:
		return res.v, res.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// send writes one packet from the owner goroutine once the character is in world.
func (s *Session) send(ctx context.Context, op uint16, body []byte) error {
	_, err := s.do(ctx, func() (any, error) {
		if st := s.State(); st != StatusInWorld {
			return nil, fmt.Errorf("%w (state %s)", ErrNotInWorld, st)
		}
		return nil, s.conn.WritePacket(op, body)
	})
	return err
}

// emit appends an event (Task 13 makes it wait for names first).
func (s *Session) emit(e events.Event) { s.log.Append(e) }

// flushPending appends events still waiting for names (filled in by Task 13).
func (s *Session) flushPending() {}

func (s *Session) dispatch(p worldconn.Packet) error {
	h, ok := s.handlers[p.Op]
	if !ok {
		s.mu.Lock()
		s.unknown[p.Op]++
		s.mu.Unlock()
		return nil
	}
	err := h(p.Body)
	if err == nil {
		return nil
	}
	var fatal *fatalError
	if errors.As(err, &fatal) {
		return fatal.err
	}
	s.mu.Lock()
	s.parseErrs++
	s.mu.Unlock()
	dump := p.Body
	if len(dump) > 256 {
		dump = dump[:256]
	}
	fmt.Fprintf(s.logger, "parse error in %s (0x%03X, %d bytes): %v\n%s", opcode.Name(p.Op), p.Op, len(p.Body), err, hex.Dump(dump))
	s.emit(events.Event{Kind: events.KindParseError, Text: err.Error(),
		Data: map[string]any{"opcode": opcode.Name(p.Op), "length": len(p.Body)}})
	return nil
}

func (s *Session) onTick(now time.Time) error {
	if st := s.State(); st == StatusInWorld || st == StatusLoggingOut {
		if s.pongWait && now.Sub(s.pingSent) > s.opts.PongTimeout {
			return errors.New("no SMSG_PONG within the pong timeout")
		}
		if !s.nextPing.IsZero() && !now.Before(s.nextPing) {
			s.pingSeq++
			if !s.pongWait { // the timeout runs from the oldest unanswered ping
				s.pingSent, s.pongWait = now, true
			}
			s.nextPing = now.Add(s.opts.PingInterval)
			s.mu.Lock()
			latency := s.latency
			s.mu.Unlock()
			if err := s.conn.WritePacket(opcode.CMSGPing, packet.NewWriter().U32(s.pingSeq).U32(latency).Bytes()); err != nil {
				return err
			}
		}
	}
	if s.logout != nil && now.After(s.logoutDeadline) {
		s.logout.finish(errors.New("logout timed out without SMSG_LOGOUT_COMPLETE"))
		s.logout = nil
		s.setState(StatusInWorld)
	}
	return nil
}

// end records why the session stopped, emits it and wakes a waiting Logout.
func (s *Session) end(err error) error {
	s.mu.Lock()
	s.state = StatusDisconnected
	s.lastErr = err.Error()
	s.mu.Unlock()
	s.flushPending()
	s.emit(events.Event{Kind: events.KindSession, Text: err.Error(), Data: map[string]any{"state": string(StatusDisconnected)}})
	if s.logout != nil {
		s.logout.finish(err)
		s.logout = nil
	}
	return err
}

func (s *Session) connLost(err error) error {
	if s.State() == StatusLoggedOut {
		return nil
	}
	return s.end(fmt.Errorf("%w: %v", ErrDisconnected, err))
}

func (s *Session) onCharEnum(body []byte) error {
	chars, err := ParseCharEnum(body)
	if err != nil {
		return &fatalError{err}
	}
	var names []string
	for _, c := range chars {
		if strings.EqualFold(c.Name, s.opts.Character) {
			s.mu.Lock()
			s.self, s.state = c, StatusLoggingIn
			s.mu.Unlock()
			if err := s.conn.WritePacket(opcode.CMSGPlayerLogin, packet.NewWriter().GUID(c.GUID).Bytes()); err != nil {
				return &fatalError{err}
			}
			return nil
		}
		names = append(names, c.Name)
	}
	return &fatalError{fmt.Errorf("character %q is not on this account (characters: %s)", s.opts.Character, strings.Join(names, ", "))}
}

func (s *Session) onLoginFailed(body []byte) error {
	code := -1
	if len(body) > 0 {
		code = int(body[0])
	}
	return &fatalError{fmt.Errorf("SMSG_CHARACTER_LOGIN_FAILED (code %d): character locked or already loading", code)}
}

func (s *Session) onVerifyWorld(body []byte) error {
	r := packet.NewReader(body)
	m, x, y, z, o := r.U32(), r.F32(), r.F32(), r.F32(), r.F32()
	if err := r.Err(); err != nil {
		return &fatalError{fmt.Errorf("SMSG_LOGIN_VERIFY_WORLD: %w", err)}
	}
	s.mu.Lock()
	s.mapID, s.pos, s.state = m, [4]float32{x, y, z, o}, StatusInWorld
	self := s.self
	s.mu.Unlock()
	s.nextPing = time.Now().Add(s.opts.PingInterval)
	if err := s.conn.WritePacket(opcode.CMSGSetActiveMover, packet.NewWriter().GUID(self.GUID).Bytes()); err != nil {
		return &fatalError{err}
	}
	s.emit(events.Event{Kind: events.KindSession, Source: self.Name, SourceGUID: self.GUID,
		Data: map[string]any{"state": string(StatusInWorld), "map": m}})
	select {
	case <-s.ready:
	default:
		close(s.ready)
	}
	return nil
}

func (s *Session) onPong(body []byte) error {
	r := packet.NewReader(body)
	seq := r.U32()
	if err := r.Err(); err != nil {
		return err
	}
	if s.pongWait && seq <= s.pingSeq {
		s.pongWait = false
		s.mu.Lock()
		s.latency = uint32(time.Since(s.pingSent).Milliseconds())
		s.mu.Unlock()
	}
	return nil
}

// Logout asks for a logout and waits for SMSG_LOGOUT_COMPLETE.
func (s *Session) Logout(ctx context.Context) error {
	v, err := s.do(ctx, func() (any, error) {
		if s.logout != nil {
			return s.logout, nil
		}
		if st := s.State(); st != StatusInWorld {
			return nil, fmt.Errorf("%w (state %s)", ErrNotInWorld, st)
		}
		if err := s.conn.WritePacket(opcode.CMSGLogoutRequest, nil); err != nil {
			return nil, err
		}
		s.logout = &logoutOp{done: make(chan struct{})}
		s.logoutDeadline = time.Now().Add(s.opts.LogoutTimeout)
		s.setState(StatusLoggingOut)
		return s.logout, nil
	})
	if err != nil {
		return err
	}
	op := v.(*logoutOp)
	select {
	case <-op.done:
		return op.err
	case <-s.done:
		select {
		case <-op.done:
			return op.err
		default:
			return ErrClosed
		}
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Session) onLogoutResponse(body []byte) error {
	r := packet.NewReader(body)
	reason, instant := r.U32(), r.U8()
	if err := r.Err(); err != nil {
		return err
	}
	if reason != 0 {
		if s.logout != nil {
			s.logout.finish(&LogoutError{Reason: reason})
			s.logout = nil
		}
		s.setState(StatusInWorld)
		s.emit(events.Event{Kind: events.KindSession, Data: map[string]any{"state": "logout_refused", "reason": reason}})
		return nil
	}
	s.emit(events.Event{Kind: events.KindSession, Data: map[string]any{"state": "logout_pending", "instant": instant == 1}})
	return nil
}

func (s *Session) onLogoutComplete([]byte) error {
	s.setState(StatusLoggedOut)
	s.flushPending()
	s.emit(events.Event{Kind: events.KindSession, Data: map[string]any{"state": string(StatusLoggedOut)}})
	if s.logout != nil {
		s.logout.finish(nil)
		s.logout = nil
	}
	s.finished = true
	return nil
}
```

- [ ] **Step 5: Run the tests**

Run: `make -C ~/turtle-agent fmt test`
Expected: PASS, `ok  github.com/TortoiseBots/turtle-agent/internal/session` (with `-race`).

- [ ] **Step 6: Record provenance and commit**

Append to `~/turtle-agent/NOTICE`:

```
internal/session/session.go | pkg/world/engine.go | ported (single-owner select loop, ping cadence, logout wait kept; 1.12 login sequence, readiness on SMSG_LOGIN_VERIFY_WORLD and all handlers new)
```

```bash
cd ~/turtle-agent && git status --short
git add internal/session NOTICE
git commit -F - <<'EOF'
session: char enum, enter world, ping, logout and disconnect handling

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01MKCBbD9VJLeKqwGUyyzxyw
EOF
```

---

## Task 10: CLI `login`, `scrub-check` and `capture-stats`

**Files:**
- Create: `cmd/turtle-agent/login.go`, `cmd/turtle-agent/capturecmd.go`
- Modify: `cmd/turtle-agent/main.go` (subcommand switch)
- Test: `cmd/turtle-agent/login_test.go`

**Interfaces:**
- Produces: `func loginCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int`; `func logoutAndWait(s *session.Session, runErr <-chan error, stderr io.Writer) int`; `func sessionExit(err error, stderr io.Writer) int`; `func scrubCmd(args []string, stdout, stderr io.Writer) int`; `func statsCmd(args []string, stdout, stderr io.Writer) int`.
- Consumes: Task 3 (`config.Load`, `Settings.Password`), Task 5 (`auth.Login`, `Ticket.Pick`, `Ticket.KeyBytes`), Task 6 (`worldconn.Dial`, `worldconn.Tap`), Task 7 (`events.New`), Task 8 (`capture.Create`, `ReadFile`, `Scan`, `Stats`), Task 9 (`session.New`, `Run`, `Ready`, `Status`, `Logout`, `ErrDisconnected`).

Progress goes to stderr, one line per stage: `realmd: authenticated as ACCOUNT, N realm(s)`, `world: authenticated on REALM (ADDR)`, `in world: NAME guid=0x… map=M x=… y=… z=…`, `logout: complete`, `capture: N records, R redacted, written to FILE`. No line ever contains the password or the key.

- [ ] **Step 1: Write the failing test**

`~/turtle-agent/cmd/turtle-agent/login_test.go`:

```go
package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TortoiseBots/turtle-agent/internal/capture"
	"github.com/TortoiseBots/turtle-agent/internal/opcode"
	"github.com/TortoiseBots/turtle-agent/internal/worldconn"
)

func settingsFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Chmod(p, 0o600)
	return p
}

const unreachable = `{"realmd":"127.0.0.1:1","accounts":{"harness":"pw-secret"}}`

func runCLI(args ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	code := run(context.Background(), args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestLoginUsage(t *testing.T) {
	if code, _, _ := runCLI("login"); code != exitUsage {
		t.Fatalf("code %d", code)
	}
}

func TestLoginAccountNotInSettings(t *testing.T) {
	code, _, errOut := runCLI("login", "--config", settingsFile(t, unreachable), "nobody", "Harnessb")
	if code != exitUsage || !strings.Contains(errOut, "nobody") || strings.Contains(errOut, "pw-secret") {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
}

func TestLoginRealmdUnreachable(t *testing.T) {
	code, _, errOut := runCLI("login", "--config", settingsFile(t, unreachable), "harness", "Harnessb")
	if code != exitAuth || !strings.Contains(errOut, "realmd:") || strings.Contains(errOut, "pw-secret") {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
}

func TestScrubCheck(t *testing.T) {
	dir := t.TempDir()
	dirty, clean := filepath.Join(dir, "dirty.twcap"), filepath.Join(dir, "clean.twcap")
	w, _ := capture.Create(dirty)
	w.Packet(worldconn.ServerToClient, opcode.SMSGMessagechat, []byte("text HARNESS text"))
	w.Close()
	w, _ = capture.Create(clean)
	w.Packet(worldconn.ServerToClient, opcode.SMSGMessagechat, []byte("Harnessb says hi"))
	w.Close()
	cfg := settingsFile(t, unreachable)
	if code, out, _ := runCLI("scrub-check", "--config", cfg, dirty); code != exitScrub || !strings.Contains(out, "record 0") {
		t.Fatalf("dirty: %d %q", code, out)
	}
	if code, out, _ := runCLI("scrub-check", "--config", cfg, clean); code != exitOK || !strings.Contains(out, "clean, 1 records") {
		t.Fatalf("clean: %d %q", code, out)
	}
}

func TestCaptureStats(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.twcap")
	w, _ := capture.Create(path)
	w.Packet(worldconn.ServerToClient, opcode.SMSGCharEnum, []byte{0})
	w.Close()
	code, out, _ := runCLI("capture-stats", path)
	if code != exitOK || !strings.Contains(out, "SMSG_CHAR_ENUM") || !strings.Contains(out, "TOTAL") {
		t.Fatalf("%d %q", code, out)
	}
}
```

- [ ] **Step 2: Run it and see it fail**

Run: `cd ~/turtle-agent && ~/.local/go/bin/go test ./cmd/turtle-agent/`
Expected: FAIL: `TestLoginAccountNotInSettings`, `TestLoginRealmdUnreachable`, `TestScrubCheck`, `TestCaptureStats` get exit code 2 (usage) because `run` does not know the subcommands yet.

- [ ] **Step 3: Implement**

`~/turtle-agent/cmd/turtle-agent/login.go`:

```go
package main

import (
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/auth"
	"github.com/TortoiseBots/turtle-agent/internal/capture"
	"github.com/TortoiseBots/turtle-agent/internal/config"
	"github.com/TortoiseBots/turtle-agent/internal/events"
	"github.com/TortoiseBots/turtle-agent/internal/session"
	"github.com/TortoiseBots/turtle-agent/internal/worldconn"
)

func loginCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", config.DefaultPath(), "settings file (realmd address, accounts)")
	capPath := fs.String("capture", "", "record decrypted world packets to this new file")
	stay := fs.Duration("stay", 0, "log out after this long in world (0: until interrupted or the logout RPC)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 2 {
		fmt.Fprintln(stderr, "usage: turtle-agent login [flags] <account> <character>")
		return exitUsage
	}
	account, character := fs.Arg(0), fs.Arg(1)
	st, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "config:", err)
		return exitUsage
	}
	password, err := st.Password(account)
	if err != nil {
		fmt.Fprintln(stderr, "config:", err)
		return exitUsage
	}

	actx, cancel := context.WithTimeout(ctx, 30*time.Second)
	ticket, err := auth.Login(actx, st.Realmd, account, password)
	cancel()
	if err != nil {
		fmt.Fprintln(stderr, "realmd:", err)
		return exitAuth
	}
	fmt.Fprintf(stderr, "realmd: authenticated as %s, %d realm(s)\n", ticket.Account, len(ticket.Realms))
	realm, err := ticket.Pick(st.Realm)
	if err != nil {
		fmt.Fprintln(stderr, "realmd:", err)
		return exitRealm
	}
	addr := realm.Address
	if st.WorldAddr != "" {
		addr = st.WorldAddr
	}

	var tap worldconn.Tap
	if *capPath != "" {
		key := ticket.KeyBytes()
		hexKey := hex.EncodeToString(key)
		w, err := capture.Create(*capPath, []byte(ticket.Account), []byte(password), []byte(strings.ToUpper(password)),
			ticket.Key[:], key, []byte(hexKey), []byte(strings.ToUpper(hexKey)))
		if err != nil {
			fmt.Fprintln(stderr, err)
			return exitUsage
		}
		defer func() {
			n, redacted := w.Counts()
			if err := w.Close(); err != nil {
				fmt.Fprintln(stderr, "capture:", err)
			}
			fmt.Fprintf(stderr, "capture: %d records, %d redacted, written to %s\n", n, redacted, *capPath)
		}()
		tap = w
	}

	wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	conn, err := worldconn.Dial(wctx, addr, ticket.Account, ticket.KeyBytes(), tap)
	cancel()
	if err != nil {
		fmt.Fprintln(stderr, "world:", err)
		return exitWorld
	}
	fmt.Fprintf(stderr, "world: authenticated on %s (%s)\n", realm.Name, addr)

	log := events.New(100000)
	s := session.New(conn, session.Options{Character: character, Log: log, Logger: stderr})
	runErr := make(chan error, 1)
	go func() { runErr <- s.Run(context.Background()) }()
	select {
	case <-s.Ready():
		info := s.Status()
		fmt.Fprintf(stderr, "in world: %s guid=%s map=%d x=%.1f y=%.1f z=%.1f\n", info.Character, info.GUID, info.Map, info.X, info.Y, info.Z)
	case err := <-runErr:
		return sessionExit(err, stderr)
	case <-ctx.Done():
		conn.Close()
		<-runErr
		return exitWorld
	}

	var stayC <-chan time.Time
	if *stay > 0 {
		stayC = time.After(*stay)
	}
	select {
	case <-stayC:
		return logoutAndWait(s, runErr, stderr)
	case <-ctx.Done():
		return logoutAndWait(s, runErr, stderr)
	case err := <-runErr:
		return sessionExit(err, stderr)
	}
}

func logoutAndWait(s *session.Session, runErr <-chan error, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	if err := s.Logout(ctx); err != nil {
		fmt.Fprintln(stderr, "logout:", err)
	}
	select {
	case err := <-runErr:
		return sessionExit(err, stderr)
	case <-ctx.Done():
		fmt.Fprintln(stderr, "logout: timed out")
		return exitDisconnected
	}
}

func sessionExit(err error, stderr io.Writer) int {
	if err == nil {
		fmt.Fprintln(stderr, "logout: complete")
		return exitOK
	}
	fmt.Fprintln(stderr, "session:", err)
	if errors.Is(err, session.ErrDisconnected) {
		return exitDisconnected
	}
	return exitWorld
}
```

`~/turtle-agent/cmd/turtle-agent/capturecmd.go`:

```go
package main

import (
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/TortoiseBots/turtle-agent/internal/capture"
	"github.com/TortoiseBots/turtle-agent/internal/config"
	"github.com/TortoiseBots/turtle-agent/internal/opcode"
)

// scrubCmd fails when a capture contains an account name (upper case, as on the wire)
// or a password from the settings file. Run it on every fixture before committing.
func scrubCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("scrub-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", config.DefaultPath(), "settings file whose accounts and passwords must not appear")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(stderr, "usage: turtle-agent scrub-check [--config FILE] <capture>...")
		return exitUsage
	}
	st, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "config:", err)
		return exitUsage
	}
	var needles [][]byte
	for name, pw := range st.Accounts {
		needles = append(needles, []byte(strings.ToUpper(name)), []byte(pw), []byte(strings.ToUpper(pw)))
	}
	code := exitOK
	for _, path := range fs.Args() {
		recs, err := capture.ReadFile(path)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return exitUsage
		}
		found := capture.Scan(recs, needles...)
		for _, f := range found {
			what := "a password"
			if f.Needle%3 == 0 {
				what = "an account name"
			}
			fmt.Fprintf(stdout, "%s: record %d (%s) contains %s from the settings file\n", path, f.Index, opcode.Name(f.Op), what)
		}
		if len(found) > 0 {
			code = exitScrub
			continue
		}
		fmt.Fprintf(stdout, "%s: clean, %d records\n", path, len(recs))
	}
	return code
}

func statsCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: turtle-agent capture-stats <capture>")
		return exitUsage
	}
	recs, err := capture.ReadFile(args[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	st := capture.Stats(recs)
	names := make([]string, 0, len(st))
	for n := range st {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(stdout, "%-36s %d\n", n, st[n])
	}
	fmt.Fprintf(stdout, "%-36s %d\n", "TOTAL", len(recs))
	return exitOK
}
```

In `~/turtle-agent/cmd/turtle-agent/main.go`, replace the `switch` in `run` with:

```go
	switch args[0] {
	case "login":
		return loginCmd(ctx, args[1:], stdout, stderr)
	case "scrub-check":
		return scrubCmd(args[1:], stdout, stderr)
	case "capture-stats":
		return statsCmd(args[1:], stdout, stderr)
	case "version":
		fmt.Fprintln(stdout, "turtle-agent", version)
		return exitOK
	default:
		usage(stderr)
		return exitUsage
	}
```

- [ ] **Step 4: Run the tests**

Run: `make -C ~/turtle-agent fmt test && make -C ~/turtle-agent build`
Expected: PASS for every package; `bin/turtle-agent` is built.

- [ ] **Step 5: Commit**

```bash
cd ~/turtle-agent && git status --short
git add cmd/turtle-agent
git commit -F - <<'EOF'
cli: login with capture mode and --stay; scrub-check and capture-stats

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01MKCBbD9VJLeKqwGUyyzxyw
EOF
```

---

## Task 11: Milestone 1 live check on m910q and the login fixture

**Files:**
- Create: `testdata/captures/m1-login.twcap` (recorded), `internal/session/capture_test.go`
- Test: `internal/session/capture_test.go`

**Interfaces:**
- Produces: test helper `func fixture(t *testing.T, name string) []capture.Record` in package `session` (later fixture tests in this package reuse it).
- Consumes: Tasks 8-10.

How the capture is taken: `turtle-agent login --capture FILE` installs `capture.Writer` as the world connection's tap. It records every world packet after header decryption, both directions, from `SMSG_AUTH_CHALLENGE` to the socket close; realmd traffic is never tapped. The bodies of `CMSG_AUTH_SESSION` and Warden packets are never stored, and any body containing the upper-case account name, the password (as typed and upper-cased) or the session key (raw, trimmed, and hex in both cases) is stored as an empty redacted record. Raw captures go to `~/turtle-agent/captures/` (git-ignored); only a capture that passes `scrub-check` is copied into `testdata/captures/`.

- [ ] **Step 1: Owner prerequisite: the settings file**

The owner creates `~/.config/turtle-agent/settings.json` (keys described in Task 3) with `realmd` set to `192.0.2.10:` plus `RealmServerPort` from m910q's `realmd.conf`, the test account and its password under `accounts`, the two bot names under `bots`, a known spell id under `test_spell` and a `spot` near hostile mobs. Never print the file. Check its shape without showing secrets:

Run: `stat -c %a ~/.config/turtle-agent/settings.json && python3 -I -c 'import json,sys; d=json.load(open(sys.argv[1])); print(sorted(d), sorted(d["accounts"]))' ~/.config/turtle-agent/settings.json`
Expected: `600`, then the key list (containing `accounts`, `realmd`, and for later milestones `bots`, `spot`, `test_spell`) and the account names only.

Every live step from here on uses these shell variables (they print no password; if the file lists several accounts, set `ACCOUNT` by hand to the one that owns Harnessb):

```bash
CFG=~/.config/turtle-agent/settings.json
ACCOUNT=$(python3 -I -c 'import json,sys; print(next(iter(json.load(open(sys.argv[1]))["accounts"])))' $CFG)
BOT1=$(python3 -I -c 'import json,sys; print(json.load(open(sys.argv[1]))["bots"][0])' $CFG)
BOT2=$(python3 -I -c 'import json,sys; print(json.load(open(sys.argv[1]))["bots"][1])' $CFG)
SPOT=$(python3 -I -c 'import json,sys; print(json.dumps(json.load(open(sys.argv[1]))["spot"]))' $CFG)
SPELL=$(python3 -I -c 'import json,sys; print(json.load(open(sys.argv[1]))["test_spell"])' $CFG)
```

- [ ] **Step 2: Log in, stay 20 s, log out, with a capture**

Run: `cd ~/turtle-agent && make build && mkdir -p captures && ./bin/turtle-agent login --capture captures/m1-login.twcap --stay 20s $ACCOUNT Harnessb; echo "exit=$?"`
Expected on stderr, in order: `realmd: authenticated as <ACCOUNT upper-cased>, 1 realm(s)` (or the realm count m910q has), `world: authenticated on <realm name> (<host:port>)`, `in world: Harnessb guid=0x… map=… x=… y=… z=…`, `logout: complete`, `capture: N records, R redacted, written to captures/m1-login.twcap` with R ≥ 1 (the auth session), and `exit=0`. Running the same command again right away also reaches `in world` (the first session really logged out).

If it fails: `exit=3` with `code 0x04` twice means the password or realmd address is wrong; `ErrPINRequired` text means the account's rank is at or above `ForcePinAccountRank` on m910q; `exit=4` means the realm is offline for build 7272; `exit=5` with `world auth: session key mismatch` means K is hashed in the wrong form (Review Focus 1); `exit=6` means the server closed the socket (check the anticheat log on m910q for an addon-info kick).

- [ ] **Step 3: Inspect and scrub the capture**

Run: `cd ~/turtle-agent && ./bin/turtle-agent capture-stats captures/m1-login.twcap && ./bin/turtle-agent scrub-check captures/m1-login.twcap`
Expected: the stats list `SMSG_AUTH_CHALLENGE 1`, `CMSG_AUTH_SESSION 1`, `SMSG_AUTH_RESPONSE 1`, `SMSG_CHAR_ENUM 1`, `SMSG_LOGIN_VERIFY_WORLD 1`, at least one `SMSG_UPDATE_OBJECT` and one `SMSG_COMPRESSED_UPDATE_OBJECT`, `CMSG_LOGOUT_REQUEST 1`, `SMSG_LOGOUT_COMPLETE 1`; scrub-check prints `captures/m1-login.twcap: clean, N records` and exits 0. If scrub-check reports a record, do not copy the file; delete it and find out which packet carried the secret.

- [ ] **Step 4: Promote the fixture and write the capture-driven test**

Run: `mkdir -p ~/turtle-agent/testdata/captures && cp ~/turtle-agent/captures/m1-login.twcap ~/turtle-agent/testdata/captures/m1-login.twcap`

`~/turtle-agent/internal/session/capture_test.go`:

```go
package session

import (
	"path/filepath"
	"testing"

	"github.com/TortoiseBots/turtle-agent/internal/capture"
	"github.com/TortoiseBots/turtle-agent/internal/opcode"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

// fixture loads a capture recorded on m910q by a milestone's live step.
func fixture(t *testing.T, name string) []capture.Record {
	t.Helper()
	recs, err := capture.ReadFile(filepath.Join("..", "..", "testdata", "captures", name))
	if err != nil {
		t.Fatalf("fixture %s: %v (record it with the milestone's live step)", name, err)
	}
	return recs
}

// Pins the SMSG_CHAR_ENUM layout: the source gives a 150-byte tail after the name
// (Player.cpp:2198-2285; the spike guessed 151). It must parse with no bytes left.
func TestM1CharEnumFromCapture(t *testing.T) {
	bodies := capture.Bodies(fixture(t, "m1-login.twcap"), opcode.SMSGCharEnum)
	if len(bodies) != 1 {
		t.Fatalf("%d SMSG_CHAR_ENUM records", len(bodies))
	}
	chars, err := ParseCharEnum(bodies[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range chars {
		if c.Name == "Harnessb" && c.GUID.IsPlayer() && c.Level > 0 {
			return
		}
	}
	t.Fatalf("Harnessb not found in %+v", chars)
}

func TestM1HandshakeShapesFromCapture(t *testing.T) {
	recs := fixture(t, "m1-login.twcap")
	if b := capture.Bodies(recs, opcode.SMSGAuthChallenge); len(b) != 1 || len(b[0]) != 4 {
		t.Fatalf("auth challenge %v", b)
	}
	if b := capture.Bodies(recs, opcode.SMSGAuthResponse); len(b) == 0 || b[len(b)-1][0] != 0x0C || (len(b[len(b)-1]) != 10 && len(b[len(b)-1]) != 1) {
		t.Fatalf("auth response %v", b)
	}
	b := capture.Bodies(recs, opcode.SMSGLoginVerifyWorld)
	if len(b) != 1 || len(b[0]) != 20 {
		t.Fatalf("verify world %v", b)
	}
	r := packet.NewReader(b[0])
	r.U32()
	if x := r.F32(); x == 0 || r.Err() != nil {
		t.Fatalf("verify world position %v %v", x, r.Err())
	}
}
```

- [ ] **Step 5: Run the tests**

Run: `make -C ~/turtle-agent fmt test`
Expected: PASS. If `TestM1CharEnumFromCapture` fails with `N unread bytes`, the live layout differs from the source count: change the `r.Skip(20 * 5)` / tail reads in `ParseCharEnum` to match what the fixture shows (hex-dump it with `capture.Bodies`), update the builder in `session_test.go` and the 150 in `TestCharEnumTailIs150Bytes` to the same layout, and say so in the commit message.

- [ ] **Step 6: Commit**

```bash
cd ~/turtle-agent && git status --short
git add testdata/captures/m1-login.twcap internal/session/capture_test.go
git commit -F - <<'EOF'
Milestone 1 on m910q: login, in world, clean logout; scrubbed login capture

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01MKCBbD9VJLeKqwGUyyzxyw
EOF
```

---

# Milestone 2: chat, whispers, GM and `.bot` commands

## Task 12: Chat and query codecs

**Files:**
- Create: `internal/game/chat.go`, `internal/game/query.go`
- Test: `internal/game/chat_test.go`, `internal/game/query_test.go`

**Interfaces:**
- Produces: `type ChatType uint8` with `String() string` and constants `ChatSay`, `ChatParty`, `ChatRaid`, `ChatGuild`, `ChatOfficer`, `ChatYell`, `ChatWhisper`, `ChatWhisperInform`, `ChatEmote`, `ChatTextEmote`, `ChatSystem`, `ChatMonsterSay`, `ChatMonsterYell`, `ChatMonsterEmote`, `ChatChannel`, `ChatMonsterWhisper`, `ChatRaidLeader`, `ChatRaidWarning`, `ChatRaidBossWhisper`, `ChatRaidBossEmote`, `ChatBattleground`, `ChatBattlegroundLeader`, `ChatHardcore`; `const LangUniversal uint32 = 0`; `func ChatTypeByName(name string) (ChatType, error)`; `func BuildMessageChat(t ChatType, to, text string) ([]byte, error)`; `type ChatMessage struct { Type ChatType; Lang uint32; Sender packet.GUID; SenderName string; Target packet.GUID; Channel, Raw, Text string; Tag uint8 }`; `func ParseMessageChat(body []byte) (ChatMessage, error)`; `func StripColors(s string) string`; `func ParseCString(body []byte) (string, error)` (for `SMSG_NOTIFICATION`, `SMSG_CHAT_PLAYER_NOT_FOUND`); `func BuildNameQuery(g packet.GUID) []byte`; `type NameInfo struct { GUID packet.GUID; Name string; Race, Gender, Class uint32 }`; `func ParseNameQueryResponse(body []byte) (NameInfo, error)`; `func BuildCreatureQuery(entry uint32, g packet.GUID) []byte`; `type CreatureInfo struct { Entry uint32; Found bool; Name, SubName string; Rank uint32 }`; `func ParseCreatureQueryResponse(body []byte) (CreatureInfo, error)`.
- Consumes: Task 1 (`packet`).

Layouts (`~/tortoise-wow/src/game`): chat types `SharedDefines.h:1358-1455` (Turtle's `CHAT_MSG_HARDCORE = 0x5E` at :1455), `LANG_UNIVERSAL = 0` at `SharedDefines.h:206`, accepted for every player because its skill id is 0 (`ObjectMgr.cpp:93`, check at `Handlers/ChatHandler.cpp:194-205`). `CMSG_MESSAGECHAT` is u32 type, u32 language, then for whispers the target name and for say/party/yell/emote/guild/officer/raid/battleground/hardcore just the message (`Handlers/ChatHandler.cpp:176-300`); `.`-prefixed text is run as a command (`ChatHandler.cpp:83-96`). `SMSG_MESSAGECHAT` is `ChatHandler::BuildChatPacket` (`Chat/Chat.cpp:2251-2314`): u8 type, u32 language; then monster whisper/raid boss whisper/raid boss emote/monster emote: u32 name length, name, u64 target (:2270-2278); say/party/yell: u64 sender twice (:2280-2285); monster say/yell: u64 sender, u32 name length, name, u64 target (:2287-2294); channel: channel name, u32 rank, u64 sender (:2296-2301); others: u64 sender (:2303-2305); then, unless the message is empty (:2308-2309), u32 length, message, u8 chat tag (:2311-2313); GM-tagged text is wrapped in `|c1049e6ff…|r` (:2257-2260). `SMSG_NOTIFICATION` and `SMSG_CHAT_PLAYER_NOT_FOUND` are one string (`WorldSession.cpp:888-889`, `ChatHandler.cpp:998`). Name query: request u64 guid (`Handlers/QueryHandler.cpp:121-125`); response u64 guid, name, realm name (a lone 0), u32 race, gender, class (`QueryHandler.cpp:45-52`). Creature query: request u32 entry + u64 guid (`QueryHandler.cpp:141-147`); response u32 entry, name, three empty names, subname, u32 type flags, type, family, rank, unknown, pet spell list, display id, u8 civilian, u8 racial leader (`:186-200`), or just entry | 0x80000000 when unknown (`:207-209`).

- [ ] **Step 1: Write the failing tests**

`~/turtle-agent/internal/game/chat_test.go`:

```go
package game

import (
	"bytes"
	"strings"
	"testing"

	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

// buildChat transcribes ChatHandler::BuildChatPacket (Chat.cpp:2251-2314) for tests.
func buildChat(t ChatType, sender packet.GUID, name string, target packet.GUID, channel, msg string, tag uint8) []byte {
	w := packet.NewWriter().U8(uint8(t)).U32(LangUniversal)
	switch t {
	case ChatMonsterWhisper, ChatRaidBossWhisper, ChatRaidBossEmote, ChatMonsterEmote:
		w.U32(uint32(len(name) + 1)).CString(name).GUID(target)
	case ChatSay, ChatParty, ChatYell:
		w.GUID(sender).GUID(sender)
	case ChatMonsterSay, ChatMonsterYell:
		w.GUID(sender).U32(uint32(len(name) + 1)).CString(name).GUID(target)
	case ChatChannel:
		w.CString(channel).U32(0).GUID(sender)
	default:
		w.GUID(sender)
	}
	if msg == "" {
		return w.Bytes()
	}
	if tag == 3 { // CHAT_TAG_GM (Chat.h:88)
		msg = "|c1049e6ff" + msg + "|r"
	}
	return w.U32(uint32(len(msg) + 1)).CString(msg).U8(tag).Bytes()
}

func TestParseMessageChatEveryBranch(t *testing.T) {
	cases := []struct {
		body []byte
		want ChatMessage
	}{
		{buildChat(ChatSay, 0x2A, "", 0, "", "hello", 0), ChatMessage{Type: ChatSay, Sender: 0x2A, Text: "hello"}},
		{buildChat(ChatWhisper, 0x2B, "", 0, "", "attack!", 0), ChatMessage{Type: ChatWhisper, Sender: 0x2B, Text: "attack!"}},
		{buildChat(ChatSystem, 0, "", 0, "", "TBM:VERSION|2026-10-10-v3", 0), ChatMessage{Type: ChatSystem, Text: "TBM:VERSION|2026-10-10-v3"}},
		{buildChat(ChatMonsterSay, 0xF130000000001234, "Hogger", 0x2A, "", "Grr", 0), ChatMessage{Type: ChatMonsterSay, Sender: 0xF130000000001234, SenderName: "Hogger", Target: 0x2A, Text: "Grr"}},
		{buildChat(ChatMonsterEmote, 0, "Hogger", 0x2A, "", "growls", 0), ChatMessage{Type: ChatMonsterEmote, SenderName: "Hogger", Target: 0x2A, Text: "growls"}},
		{buildChat(ChatChannel, 0x2C, "", 0, "world", "lfg", 0), ChatMessage{Type: ChatChannel, Sender: 0x2C, Channel: "world", Text: "lfg"}},
		{buildChat(ChatHardcore, 0x2D, "", 0, "", "hc", 0), ChatMessage{Type: ChatHardcore, Sender: 0x2D, Text: "hc"}},
		{buildChat(ChatSay, 0x2A, "", 0, "", "gm text", 3), ChatMessage{Type: ChatSay, Sender: 0x2A, Text: "gm text", Tag: 3}},
		{buildChat(ChatSystem, 0, "", 0, "", "", 0), ChatMessage{Type: ChatSystem}},
	}
	for i, c := range cases {
		got, err := ParseMessageChat(c.body)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		got.Raw = ""
		if got != c.want {
			t.Errorf("case %d: got %+v want %+v", i, got, c.want)
		}
	}
	if _, err := ParseMessageChat([]byte{0, 0, 0}); err == nil {
		t.Fatal("truncated packet accepted")
	}
}

func TestBuildMessageChat(t *testing.T) {
	b, err := BuildMessageChat(ChatWhisper, "Gorm", "hi")
	want := packet.NewWriter().U32(6).U32(0).CString("Gorm").CString("hi").Bytes()
	if err != nil || !bytes.Equal(b, want) {
		t.Fatalf("% X %v", b, err)
	}
	b, _ = BuildMessageChat(ChatSay, "", ".gm visible on")
	if !bytes.Equal(b, packet.NewWriter().U32(0).U32(0).CString(".gm visible on").Bytes()) {
		t.Fatalf("% X", b)
	}
	for name, fn := range map[string]func() error{
		"empty":       func() error { _, err := BuildMessageChat(ChatSay, "", ""); return err },
		"too long":    func() error { _, err := BuildMessageChat(ChatSay, "", strings.Repeat("x", 256)); return err },
		"whisper, no": func() error { _, err := BuildMessageChat(ChatWhisper, "", "x"); return err },
		"system":      func() error { _, err := BuildMessageChat(ChatSystem, "", "x"); return err },
	} {
		if fn() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestChatTypeByName(t *testing.T) {
	if ct, err := ChatTypeByName("Party"); err != nil || ct != ChatParty {
		t.Fatalf("%v %v", ct, err)
	}
	if _, err := ChatTypeByName("system"); err == nil {
		t.Fatal("system is not sendable")
	}
	if ChatHardcore.String() != "hardcore" || ChatType(0x42).String() != "chat_0x42" {
		t.Fatal("names")
	}
}

func TestStripColorsAndLinks(t *testing.T) {
	in := "|cffff0000Red|r and |cff71d5ff|Hspell:133|h[Fireball]|h|r"
	if got := StripColors(in); got != "Red and [Fireball]" {
		t.Fatalf("%q", got)
	}
}
```

`~/turtle-agent/internal/game/query_test.go`:

```go
package game

import (
	"bytes"
	"testing"

	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

func TestNameQuery(t *testing.T) {
	if b := BuildNameQuery(0x2B); !bytes.Equal(b, packet.NewWriter().GUID(0x2B).Bytes()) {
		t.Fatalf("% X", b)
	}
	resp := packet.NewWriter().GUID(0x2B).CString("Gorm").U8(0).U32(2).U32(0).U32(1).Bytes() // QueryHandler.cpp:45-52
	info, err := ParseNameQueryResponse(resp)
	if err != nil || info != (NameInfo{GUID: 0x2B, Name: "Gorm", Race: 2, Gender: 0, Class: 1}) {
		t.Fatalf("%+v %v", info, err)
	}
}

func TestCreatureQuery(t *testing.T) {
	if b := BuildCreatureQuery(299, 0xF13000012B000001); !bytes.Equal(b, packet.NewWriter().U32(299).GUID(0xF13000012B000001).Bytes()) {
		t.Fatalf("% X", b)
	}
	resp := packet.NewWriter().U32(299).CString("Young Wolf").U8(0).U8(0).U8(0).CString("").
		U32(0).U32(1).U32(1).U32(0).U32(0).U32(0).U32(161).U8(0).U8(0).Bytes() // QueryHandler.cpp:187-200
	info, err := ParseCreatureQueryResponse(resp)
	if err != nil || !info.Found || info.Entry != 299 || info.Name != "Young Wolf" {
		t.Fatalf("%+v %v", info, err)
	}
	info, err = ParseCreatureQueryResponse(packet.NewWriter().U32(299 | 0x80000000).Bytes()) // :207-209
	if err != nil || info.Found || info.Entry != 299 {
		t.Fatalf("unknown entry: %+v %v", info, err)
	}
}

func TestParseCString(t *testing.T) {
	if s, err := ParseCString([]byte("You are now invisible.\x00")); err != nil || s != "You are now invisible." {
		t.Fatalf("%q %v", s, err)
	}
}
```

- [ ] **Step 2: Run them and see them fail**

Run: `cd ~/turtle-agent && ~/.local/go/bin/go test ./internal/game/`
Expected: FAIL, `undefined: ChatType` (and `LangUniversal`, `ParseMessageChat`, `BuildNameQuery`, …).

- [ ] **Step 3: Implement**

`~/turtle-agent/internal/game/chat.go`:

```go
// Package game holds pure codecs for 1.12 gameplay packets: no state and no I/O.
package game

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

// ChatType is ChatMsg (SharedDefines.h:1358-1455).
type ChatType uint8

const (
	ChatSay                ChatType = 0x00
	ChatParty              ChatType = 0x01
	ChatRaid               ChatType = 0x02
	ChatGuild              ChatType = 0x03
	ChatOfficer            ChatType = 0x04
	ChatYell               ChatType = 0x05
	ChatWhisper            ChatType = 0x06
	ChatWhisperInform      ChatType = 0x07
	ChatEmote              ChatType = 0x08
	ChatTextEmote          ChatType = 0x09
	ChatSystem             ChatType = 0x0A
	ChatMonsterSay         ChatType = 0x0B
	ChatMonsterYell        ChatType = 0x0C
	ChatMonsterEmote       ChatType = 0x0D
	ChatChannel            ChatType = 0x0E
	ChatMonsterWhisper     ChatType = 0x1A
	ChatRaidLeader         ChatType = 0x57
	ChatRaidWarning        ChatType = 0x58
	ChatRaidBossWhisper    ChatType = 0x59
	ChatRaidBossEmote      ChatType = 0x5A
	ChatBattleground       ChatType = 0x5C
	ChatBattlegroundLeader ChatType = 0x5D
	ChatHardcore           ChatType = 0x5E // Turtle (SharedDefines.h:1455)
)

// LangUniversal (SharedDefines.h:206) needs no language skill (ObjectMgr.cpp:93).
const LangUniversal uint32 = 0

var chatNames = map[ChatType]string{
	ChatSay: "say", ChatParty: "party", ChatRaid: "raid", ChatGuild: "guild", ChatOfficer: "officer",
	ChatYell: "yell", ChatWhisper: "whisper", ChatWhisperInform: "whisper_inform", ChatEmote: "emote",
	ChatTextEmote: "text_emote", ChatSystem: "system", ChatMonsterSay: "monster_say", ChatMonsterYell: "monster_yell",
	ChatMonsterEmote: "monster_emote", ChatChannel: "channel", ChatMonsterWhisper: "monster_whisper",
	ChatRaidLeader: "raid_leader", ChatRaidWarning: "raid_warning", ChatRaidBossWhisper: "raid_boss_whisper",
	ChatRaidBossEmote: "raid_boss_emote", ChatBattleground: "battleground", ChatBattlegroundLeader: "battleground_leader",
	ChatHardcore: "hardcore",
}

// sendable are the types HandleMessagechatOpcode reads as [target,] message (ChatHandler.cpp:276-300).
var sendable = map[ChatType]bool{
	ChatSay: true, ChatParty: true, ChatRaid: true, ChatGuild: true, ChatOfficer: true, ChatYell: true,
	ChatWhisper: true, ChatEmote: true, ChatRaidLeader: true, ChatRaidWarning: true, ChatBattleground: true,
	ChatBattlegroundLeader: true, ChatHardcore: true,
}

func (t ChatType) String() string {
	if n, ok := chatNames[t]; ok {
		return n
	}
	return fmt.Sprintf("chat_0x%02X", uint8(t))
}

func ChatTypeByName(name string) (ChatType, error) {
	name = strings.ToLower(name)
	for t, n := range chatNames {
		if n == name && sendable[t] {
			return t, nil
		}
	}
	return 0, fmt.Errorf("chat type %q cannot be sent; use say, party, raid, guild, officer, yell, whisper, emote, raid_leader, raid_warning, battleground, battleground_leader or hardcore", name)
}

// BuildMessageChat lays out CMSG_MESSAGECHAT (ChatHandler.cpp:176-300).
func BuildMessageChat(t ChatType, to, text string) ([]byte, error) {
	if text == "" {
		return nil, errors.New("chat: empty message")
	}
	if len(text) > 255 {
		return nil, fmt.Errorf("chat: message is %d bytes; the client limit is 255", len(text))
	}
	if !sendable[t] {
		return nil, fmt.Errorf("chat: type %s cannot be sent", t)
	}
	w := packet.NewWriter().U32(uint32(t)).U32(LangUniversal)
	if t == ChatWhisper {
		if to == "" {
			return nil, errors.New("chat: a whisper needs a target name")
		}
		w.CString(to)
	}
	return w.CString(text).Bytes(), nil
}

type ChatMessage struct {
	Type       ChatType
	Lang       uint32
	Sender     packet.GUID
	SenderName string // only monster and boss messages carry a name
	Target     packet.GUID
	Channel    string
	Raw        string // as sent, colour codes included
	Text       string // StripColors(Raw)
	Tag        uint8  // CHAT_TAG_* (Chat.h:85-88)
}

// ParseMessageChat decodes ChatHandler::BuildChatPacket (Chat.cpp:2251-2314).
func ParseMessageChat(body []byte) (ChatMessage, error) {
	r := packet.NewReader(body)
	var m ChatMessage
	m.Type, m.Lang = ChatType(r.U8()), r.U32()
	switch m.Type {
	case ChatMonsterWhisper, ChatRaidBossWhisper, ChatRaidBossEmote, ChatMonsterEmote:
		r.U32()
		m.SenderName, m.Target = r.CString(), r.GUID()
	case ChatSay, ChatParty, ChatYell:
		m.Sender = r.GUID()
		r.GUID()
	case ChatMonsterSay, ChatMonsterYell:
		m.Sender = r.GUID()
		r.U32()
		m.SenderName, m.Target = r.CString(), r.GUID()
	case ChatChannel:
		m.Channel = r.CString()
		r.U32()
		m.Sender = r.GUID()
	default:
		m.Sender = r.GUID()
	}
	if r.Err() == nil && r.Remaining() > 0 {
		r.U32()
		m.Raw, m.Tag = r.CString(), r.U8()
	}
	if err := r.Err(); err != nil {
		return ChatMessage{}, fmt.Errorf("SMSG_MESSAGECHAT: %w", err)
	}
	m.Text = StripColors(m.Raw)
	return m, nil
}

var (
	colourCode = regexp.MustCompile(`\|c[0-9A-Fa-f]{8}|\|r`)
	linkCode   = regexp.MustCompile(`\|H[^|]*\|h(.*?)\|h`)
)

// StripColors removes |cAARRGGBB and |r codes and unwraps |H…|h[text]|h links.
func StripColors(s string) string {
	return colourCode.ReplaceAllString(linkCode.ReplaceAllString(s, "$1"), "")
}

// ParseCString reads a body that is one string (SMSG_NOTIFICATION, SMSG_CHAT_PLAYER_NOT_FOUND).
func ParseCString(body []byte) (string, error) {
	r := packet.NewReader(body)
	s := r.CString()
	return s, r.Err()
}
```

`~/turtle-agent/internal/game/query.go`:

```go
package game

import (
	"fmt"

	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

// BuildNameQuery is CMSG_NAME_QUERY (QueryHandler.cpp:121-125).
func BuildNameQuery(g packet.GUID) []byte { return packet.NewWriter().GUID(g).Bytes() }

type NameInfo struct {
	GUID                packet.GUID
	Name                string
	Race, Gender, Class uint32
}

// ParseNameQueryResponse decodes SMSG_NAME_QUERY_RESPONSE (QueryHandler.cpp:45-52).
func ParseNameQueryResponse(body []byte) (NameInfo, error) {
	r := packet.NewReader(body)
	var n NameInfo
	n.GUID, n.Name = r.GUID(), r.CString()
	r.CString() // realm name, empty
	n.Race, n.Gender, n.Class = r.U32(), r.U32(), r.U32()
	if err := r.Err(); err != nil {
		return NameInfo{}, fmt.Errorf("SMSG_NAME_QUERY_RESPONSE: %w", err)
	}
	return n, nil
}

// BuildCreatureQuery is CMSG_CREATURE_QUERY (QueryHandler.cpp:141-147).
func BuildCreatureQuery(entry uint32, g packet.GUID) []byte {
	return packet.NewWriter().U32(entry).GUID(g).Bytes()
}

type CreatureInfo struct {
	Entry   uint32
	Found   bool
	Name    string
	SubName string
	Rank    uint32
}

// ParseCreatureQueryResponse decodes SMSG_CREATURE_QUERY_RESPONSE (QueryHandler.cpp:186-209).
func ParseCreatureQueryResponse(body []byte) (CreatureInfo, error) {
	r := packet.NewReader(body)
	entry := r.U32()
	if entry&0x80000000 != 0 {
		return CreatureInfo{Entry: entry &^ 0x80000000}, r.Err()
	}
	c := CreatureInfo{Entry: entry, Found: true}
	c.Name = r.CString()
	r.CString()
	r.CString()
	r.CString()
	c.SubName = r.CString()
	r.U32() // type flags
	r.U32() // type
	r.U32() // family
	c.Rank = r.U32()
	if err := r.Err(); err != nil {
		return CreatureInfo{}, fmt.Errorf("SMSG_CREATURE_QUERY_RESPONSE: %w", err)
	}
	return c, nil
}
```

- [ ] **Step 4: Run the tests**

Run: `make -C ~/turtle-agent fmt test`
Expected: PASS, `ok  github.com/TortoiseBots/turtle-agent/internal/game`.

- [ ] **Step 5: Commit**

```bash
cd ~/turtle-agent && git status --short
git add internal/game
git commit -F - <<'EOF'
game: chat codecs (incl. Turtle hardcore channel) and name/creature queries

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01MKCBbD9VJLeKqwGUyyzxyw
EOF
```

---

## Task 13: Session chat, GM commands and order-preserving names

**Files:**
- Create: `internal/session/names.go`, `internal/session/chat.go`
- Modify: `internal/session/session.go` (struct field, `New`, `register`, `emit`, `flushPending`, `onTick`, `onVerifyWorld`)
- Test: `internal/session/chat_test.go`

**Interfaces:**
- Produces: `(*Session).Chat(ctx context.Context, typ, text, to string) error`; `(*Session).GM(ctx context.Context, text string) error`; internal `var namesWait = 2 * time.Second`; `type nameCache` with `setPlayer`, `setCreature`, `lookup(g packet.GUID) (string, bool)`; `(*Session).nameOf(g packet.GUID) (string, bool)`; `(*Session).appendEvent(e events.Event)`; `(*Session).flushNames(now time.Time)`.
- Consumes: Task 9 internals (`register`, `emit`, `flushPending`, `onTick`, `send`), Task 12 codecs.

Event mapping: whispers in and out and monster/boss whispers → `whisper`; system messages, notifications and "player not found" → `system`; everything else → `chat`. `Data["type"]` keeps the chat type name (`say`, `party`, `whisper_inform`, `notification`, …). Every event goes through a FIFO: an event waits until the names of its source and target GUIDs are known (a name or creature query is sent once per 5 s per GUID or entry), at most `namesWait`, and nothing behind it overtakes it.

- [ ] **Step 1: Write the failing tests**

`~/turtle-agent/internal/session/chat_test.go`:

```go
package session

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/events"
	"github.com/TortoiseBots/turtle-agent/internal/game"
	"github.com/TortoiseBots/turtle-agent/internal/opcode"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

// chatBody transcribes ChatHandler::BuildChatPacket's default and say branches (Chat.cpp:2251-2314).
func chatBody(t game.ChatType, sender packet.GUID, msg string) []byte {
	w := packet.NewWriter().U8(uint8(t)).U32(0).GUID(sender)
	if t == game.ChatSay || t == game.ChatParty || t == game.ChatYell {
		w.GUID(sender)
	}
	return w.U32(uint32(len(msg) + 1)).CString(msg).U8(0).Bytes()
}

func nameResponse(g packet.GUID, name string) []byte {
	return packet.NewWriter().GUID(g).CString(name).U8(0).U32(1).U32(0).U32(1).Bytes()
}

func TestChatAndGMSendMessagechat(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	if err := h.s.Chat(context.Background(), "party", "hello", ""); err != nil {
		t.Fatal(err)
	}
	if p := h.expect(opcode.CMSGMessagechat); !bytes.Equal(p.Body, packet.NewWriter().U32(1).U32(0).CString("hello").Bytes()) {
		t.Fatalf("% X", p.Body)
	}
	if err := h.s.GM(context.Background(), "gm visible on"); err != nil {
		t.Fatal(err)
	}
	if p := h.expect(opcode.CMSGMessagechat); !bytes.Equal(p.Body, packet.NewWriter().U32(0).U32(0).CString(".gm visible on").Bytes()) {
		t.Fatalf("% X", p.Body)
	}
	h.logout()
}

func TestChatBeforeWorldFails(t *testing.T) {
	h := start(t, Options{})
	h.expect(opcode.CMSGCharEnum)
	if err := h.s.Chat(context.Background(), "say", "x", ""); !errors.Is(err, ErrNotInWorld) {
		t.Fatalf("err = %v", err)
	}
}

func TestWhisperNameComesFromQuery(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	h.send(opcode.SMSGMessagechat, chatBody(game.ChatWhisper, 0x99, "attacking"))
	if p := h.expect(opcode.CMSGNameQuery); packet.NewReader(p.Body).GUID() != 0x99 {
		t.Fatalf("query % X", p.Body)
	}
	h.send(opcode.SMSGNameQueryResponse, nameResponse(0x99, "Gorm"))
	h.send(opcode.SMSGNotification, []byte("Hello\x00"))
	h.logout()
	w := ofKind(h.s.Log(), events.KindWhisper)
	if len(w) != 1 || w[0].Source != "Gorm" || w[0].Text != "attacking" || w[0].Data["type"] != "whisper" {
		t.Fatalf("%+v", w)
	}
	if sys := ofKind(h.s.Log(), events.KindSystem); len(sys) != 1 || sys[0].Text != "Hello" {
		t.Fatalf("%+v", sys)
	}
}

// Review Focus 5: an event waiting for a name is not overtaken, and it is logged
// with what is known once namesWait runs out.
func TestNameWaitKeepsOrder(t *testing.T) {
	old := namesWait
	namesWait = 150 * time.Millisecond
	defer func() { namesWait = old }()
	h := start(t, Options{})
	h.enterWorld()
	h.send(opcode.SMSGMessagechat, chatBody(game.ChatWhisper, 0x77, "first"))
	h.send(opcode.SMSGMessagechat, chatBody(game.ChatSystem, 0, "second"))
	h.expect(opcode.CMSGNameQuery) // never answered
	time.Sleep(400 * time.Millisecond)
	h.send(opcode.SMSGChatPlayerNotFound, []byte("Nobody\x00"))
	h.logout()
	p := h.s.Log().Read(events.Query{Kinds: events.ChatKinds})
	if len(p.Events) != 3 || p.Events[0].Text != "first" || p.Events[0].Source != "" ||
		p.Events[1].Text != "second" || p.Events[2].Data["type"] != "player_not_found" {
		t.Fatalf("%+v", p.Events)
	}
	for i := 1; i < len(p.Events); i++ {
		if p.Events[i].Seq <= p.Events[i-1].Seq {
			t.Fatal("sequence not increasing")
		}
	}
}
```

- [ ] **Step 2: Run them and see them fail**

Run: `cd ~/turtle-agent && ~/.local/go/bin/go test ./internal/session/`
Expected: FAIL, `h.s.Chat undefined`, `h.s.GM undefined`, `undefined: namesWait`.

- [ ] **Step 3: Implement the name cache**

`~/turtle-agent/internal/session/names.go`:

```go
package session

import (
	"fmt"
	"sync"
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/events"
	"github.com/TortoiseBots/turtle-agent/internal/game"
	"github.com/TortoiseBots/turtle-agent/internal/opcode"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

// namesWait bounds how long an event waits for a name before it is logged with the
// names known so far. A var so tests can shorten it.
var namesWait = 2 * time.Second

// nameCache maps player GUIDs and creature entries to names. mu guards the two maps,
// which RPC goroutines read; the rest belongs to the owner loop.
type nameCache struct {
	mu         sync.RWMutex
	players    map[packet.GUID]string
	creatures  map[uint32]string
	askedGUID  map[packet.GUID]time.Time
	askedEntry map[uint32]time.Time
	waiting    []pendingEvent
}

type pendingEvent struct {
	e     events.Event
	until time.Time
}

func newNameCache() *nameCache {
	return &nameCache{
		players: map[packet.GUID]string{}, creatures: map[uint32]string{},
		askedGUID: map[packet.GUID]time.Time{}, askedEntry: map[uint32]time.Time{},
	}
}

func (c *nameCache) setPlayer(g packet.GUID, name string) {
	c.mu.Lock()
	c.players[g] = name
	c.mu.Unlock()
}

func (c *nameCache) setCreature(entry uint32, name string) {
	c.mu.Lock()
	c.creatures[entry] = name
	c.mu.Unlock()
}

// lookup returns the cached name; ok is false while a query is owed.
func (c *nameCache) lookup(g packet.GUID) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	switch {
	case g == 0:
		return "", true
	case g.IsPlayer():
		n, ok := c.players[g]
		return n, ok
	case g.IsCreature():
		n, ok := c.creatures[g.Entry()]
		return n, ok
	}
	return "", true // game objects and corpses have no name query in v1
}

// nameOf returns g's name and sends a name or creature query when it is unknown.
func (s *Session) nameOf(g packet.GUID) (string, bool) {
	n, ok := s.names.lookup(g)
	if !ok {
		s.askName(g)
	}
	return n, ok
}

func (s *Session) askName(g packet.GUID) {
	now := time.Now()
	if g.IsPlayer() {
		if t, ok := s.names.askedGUID[g]; ok && now.Sub(t) < 5*time.Second {
			return
		}
		s.names.askedGUID[g] = now
		_ = s.conn.WritePacket(opcode.CMSGNameQuery, game.BuildNameQuery(g))
		return
	}
	entry := g.Entry()
	if t, ok := s.names.askedEntry[entry]; ok && now.Sub(t) < 5*time.Second {
		return
	}
	s.names.askedEntry[entry] = now
	_ = s.conn.WritePacket(opcode.CMSGCreatureQuery, game.BuildCreatureQuery(entry, g))
}

func (s *Session) fillNames(e *events.Event) bool {
	ok := true
	if e.Source == "" && e.SourceGUID != 0 {
		n, known := s.nameOf(e.SourceGUID)
		e.Source, ok = n, ok && known
	}
	if e.Target == "" && e.TargetGUID != 0 {
		n, known := s.nameOf(e.TargetGUID)
		e.Target, ok = n, ok && known
	}
	return ok
}

// flushNames appends waiting events in arrival order, stopping at the first one whose
// names are unknown and whose wait has not run out.
func (s *Session) flushNames(now time.Time) {
	i := 0
	for ; i < len(s.names.waiting); i++ {
		p := &s.names.waiting[i]
		if !s.fillNames(&p.e) && now.Before(p.until) {
			break
		}
		s.appendEvent(p.e)
	}
	s.names.waiting = s.names.waiting[i:]
}

func (s *Session) onNameQueryResponse(body []byte) error {
	info, err := game.ParseNameQueryResponse(body)
	if err != nil {
		return err
	}
	s.names.setPlayer(info.GUID, info.Name)
	s.flushNames(time.Now())
	return nil
}

func (s *Session) onCreatureQueryResponse(body []byte) error {
	info, err := game.ParseCreatureQueryResponse(body)
	if err != nil {
		return err
	}
	name := info.Name
	if !info.Found {
		name = fmt.Sprintf("creature %d", info.Entry)
	}
	s.names.setCreature(info.Entry, name)
	s.flushNames(time.Now())
	return nil
}
```

- [ ] **Step 4: Implement chat handlers and actions**

`~/turtle-agent/internal/session/chat.go`:

```go
package session

import (
	"context"
	"fmt"
	"strings"

	"github.com/TortoiseBots/turtle-agent/internal/events"
	"github.com/TortoiseBots/turtle-agent/internal/game"
	"github.com/TortoiseBots/turtle-agent/internal/opcode"
)

func (s *Session) registerChat() {
	s.handlers[opcode.SMSGMessagechat] = s.onMessageChat
	s.handlers[opcode.SMSGNotification] = s.onNotification
	s.handlers[opcode.SMSGChatPlayerNotFound] = s.onPlayerNotFound
	s.handlers[opcode.SMSGNameQueryResponse] = s.onNameQueryResponse
	s.handlers[opcode.SMSGCreatureQueryResponse] = s.onCreatureQueryResponse
}

func chatKind(t game.ChatType) string {
	switch t {
	case game.ChatWhisper, game.ChatWhisperInform, game.ChatMonsterWhisper, game.ChatRaidBossWhisper:
		return events.KindWhisper
	case game.ChatSystem:
		return events.KindSystem
	}
	return events.KindChat
}

func (s *Session) onMessageChat(body []byte) error {
	m, err := game.ParseMessageChat(body)
	if err != nil {
		return err
	}
	data := map[string]any{"type": m.Type.String()}
	if m.Channel != "" {
		data["channel"] = m.Channel
	}
	if m.Tag != 0 {
		data["tag"] = m.Tag
	}
	s.emit(events.Event{Kind: chatKind(m.Type), Source: m.SenderName, SourceGUID: m.Sender,
		TargetGUID: m.Target, Text: m.Text, Data: data})
	return nil
}

func (s *Session) onNotification(body []byte) error {
	text, err := game.ParseCString(body)
	if err != nil {
		return err
	}
	s.emit(events.Event{Kind: events.KindSystem, Text: game.StripColors(text), Data: map[string]any{"type": "notification"}})
	return nil
}

func (s *Session) onPlayerNotFound(body []byte) error {
	name, err := game.ParseCString(body)
	if err != nil {
		return err
	}
	s.emit(events.Event{Kind: events.KindSystem, Text: fmt.Sprintf("No player named '%s' is currently playing.", name),
		Data: map[string]any{"type": "player_not_found", "name": name}})
	return nil
}

// Chat sends one chat line; typ is a sendable chat type name ("say", "party", "whisper", …).
func (s *Session) Chat(ctx context.Context, typ, text, to string) error {
	t, err := game.ChatTypeByName(typ)
	if err != nil {
		return err
	}
	body, err := game.BuildMessageChat(t, to, text)
	if err != nil {
		return err
	}
	return s.send(ctx, opcode.CMSGMessagechat, body)
}

// GM sends a GM or .bot command as say chat; the server runs "."-prefixed chat as a
// command and never broadcasts it (ChatHandler.cpp:83-96).
func (s *Session) GM(ctx context.Context, text string) error {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, ".") {
		text = "." + text
	}
	return s.Chat(ctx, "say", text, "")
}
```

- [ ] **Step 5: Wire it into `session.go`**

In `~/turtle-agent/internal/session/session.go`:

1. In the `Session` struct, under `// owner-loop only`, add the field `names *nameCache`.
2. In `New`, add `names: newNameCache(),` to the struct literal.
3. In `register`, add the line `s.registerChat()` after `s.registerCore()`.
4. Replace `func (s *Session) emit(e events.Event) { s.log.Append(e) }` and `func (s *Session) flushPending() {}` (with their comments) by:

```go
// emit queues e behind events still waiting for names and appends whatever is ready.
func (s *Session) emit(e events.Event) {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	s.names.waiting = append(s.names.waiting, pendingEvent{e: e, until: e.Time.Add(namesWait)})
	s.flushNames(time.Now())
}

// appendEvent stores a ready event in the log.
func (s *Session) appendEvent(e events.Event) { s.log.Append(e) }

// flushPending appends everything still waiting, with the names known so far.
func (s *Session) flushPending() { s.flushNames(time.Now().Add(time.Hour)) }
```

5. In `onTick`, add `s.flushNames(now)` as the first statement.
6. In `onVerifyWorld`, after `s.mu.Unlock()`, add `s.names.setPlayer(self.GUID, self.Name)`.

- [ ] **Step 6: Run the tests**

Run: `make -C ~/turtle-agent fmt test`
Expected: PASS for `internal/session` (the Task 9 tests still pass: their events carry no unknown GUIDs).

- [ ] **Step 7: Commit**

```bash
cd ~/turtle-agent && git status --short
git add internal/session
git commit -F - <<'EOF'
session: chat, whispers, GM/.bot commands; names resolved without reordering

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01MKCBbD9VJLeKqwGUyyzxyw
EOF
```

---

## Task 14: Loopback JSON-RPC server and the first methods

**Files:**
- Create: `internal/rpc/server.go`, `cmd/turtle-agent/methods.go`
- Modify: `cmd/turtle-agent/login.go` (`--rpc` flag, start and stop the server), `NOTICE` (append one row)
- Test: `internal/rpc/server_test.go`, `cmd/turtle-agent/methods_test.go`

**Interfaces:**
- Produces (package `rpc`): codes `CodeParse`, `CodeInvalidRequest`, `CodeMethodNotFound`, `CodeInvalidParams`, `CodeInternal`, `CodeNotInWorld`, `CodeDisconnected`, `CodeGame`, `CodeTimeout`; `type Error struct { Code int; Message string; Data any }`; `type Method func(ctx context.Context, params json.RawMessage) (any, error)`; `func Typed[P, R any](fn func(ctx context.Context, p P) (R, error)) Method`; `type Server`; `func NewServer() *Server`; `(*Server).Register(name string, m Method)`; `(*Server).Handler() http.Handler`; `func Listen(ctx context.Context, address string) (net.Listener, error)`; `func Serve(ctx context.Context, ln net.Listener, h http.Handler) error`.
- Produces (package `main`): `type empty struct{}`; `func registerMethods(srv *rpc.Server, s *session.Session)`; `func readEvents(ctx context.Context, log *events.Log, q events.Query, waitMS int) events.Page`; `func rpcErr(err error) error`. Methods: `status` {} → `session.StatusInfo`; `chat` {type, text, to?} → {}; `gm` {text} → {}; `events` {since, kinds?, party_only?, limit?, wait_ms?} → `events.Page`; `chat_log` {since, limit?, wait_ms?} → `events.Page`; `logout` {} → {}.
- Consumes: Task 7, Task 9 (`Status`, `Logout`, `ErrNotInWorld`, `ErrDisconnected`, `ErrClosed`, `LogoutError`), Task 13 (`Chat`, `GM`).

Params are JSON objects (positional arrays are rejected as invalid params). `wait_ms` long-polls up to 25 000 ms until at least one matching event exists. Error codes: -32001 not in world, -32002 disconnected or session closed, -32003 refused by the game (bad chat type, logout refused; `data.reason` carries the logout reason), -32004 timeout.

- [ ] **Step 1: Write the failing tests**

`~/turtle-agent/internal/rpc/server_test.go`:

```go
package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type addParams struct {
	A int `json:"a"`
	B int `json:"b"`
}

func newTestServer(t *testing.T) *httptest.Server {
	s := NewServer()
	s.Register("add", Typed(func(ctx context.Context, p addParams) (int, error) { return p.A + p.B, nil }))
	s.Register("nothing", Typed(func(ctx context.Context, _ struct{}) (struct{}, error) { return struct{}{}, nil }))
	s.Register("offline", Typed(func(ctx context.Context, _ struct{}) (int, error) {
		return 0, &Error{Code: CodeNotInWorld, Message: "not in world"}
	}))
	s.Register("broken", Typed(func(ctx context.Context, _ struct{}) (int, error) { return 0, errors.New("boom") }))
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func post(t *testing.T, ts *httptest.Server, body string, header map[string]string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/rpc", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func errCode(m map[string]any) float64 {
	e, _ := m["error"].(map[string]any)
	c, _ := e["code"].(float64)
	return c
}

func TestTypedRoundTripAndEmptyResult(t *testing.T) {
	ts := newTestServer(t)
	if _, out := post(t, ts, `{"jsonrpc":"2.0","id":1,"method":"add","params":{"a":2,"b":3}}`, nil); out["result"] != float64(5) {
		t.Fatalf("%v", out)
	}
	_, out := post(t, ts, `{"jsonrpc":"2.0","id":"x","method":"nothing"}`, nil)
	if r, ok := out["result"].(map[string]any); !ok || len(r) != 0 || out["id"] != "x" {
		t.Fatalf("%v", out)
	}
}

func TestErrorCodes(t *testing.T) {
	ts := newTestServer(t)
	for body, want := range map[string]float64{
		`{"jsonrpc":"2.0","id":1,"method":"nope"}`:                   CodeMethodNotFound,
		`{"jsonrpc":"2.0","id":1,"method":"add","params":{"c":1}}`:  CodeInvalidParams,
		`{"jsonrpc":"2.0","id":1,"method":"add","params":[1,2]}`:    CodeInvalidParams,
		`{"jsonrpc":"2.0","id":1,"method":"offline"}`:               CodeNotInWorld,
		`{"jsonrpc":"2.0","id":1,"method":"broken"}`:                CodeInternal,
		`{"jsonrpc":"1.0","id":1,"method":"add"}`:                   CodeInvalidRequest,
		`{not json`:                                                 CodeParse,
	} {
		if _, out := post(t, ts, body, nil); errCode(out) != want {
			t.Errorf("%s: %v, want %v", body, out, want)
		}
	}
}

func TestRejectsBrowserRequestsAndNotifications(t *testing.T) {
	ts := newTestServer(t)
	if code, _ := post(t, ts, `{"jsonrpc":"2.0","id":1,"method":"add"}`, map[string]string{"Origin": "http://evil.example"}); code != http.StatusForbidden {
		t.Fatalf("origin: %d", code)
	}
	if code, _ := post(t, ts, `{"jsonrpc":"2.0","method":"add","params":{"a":1,"b":1}}`, nil); code != http.StatusNoContent {
		t.Fatalf("notification: %d", code)
	}
}

func TestListenLoopbackOnly(t *testing.T) {
	if _, err := Listen(context.Background(), "0.0.0.0:0"); err == nil {
		t.Fatal("wildcard bind accepted")
	}
	ln, err := Listen(context.Background(), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
}
```

`~/turtle-agent/cmd/turtle-agent/methods_test.go`:

```go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/opcode"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
	"github.com/TortoiseBots/turtle-agent/internal/rpc"
	"github.com/TortoiseBots/turtle-agent/internal/session"
	"github.com/TortoiseBots/turtle-agent/internal/worldconn"
	"github.com/TortoiseBots/turtle-agent/internal/worldconn/worldtest"
)

type agentRig struct {
	t   *testing.T
	s   *session.Session
	srv *worldtest.Server
	ts  *httptest.Server
	run chan error
}

// newRig logs a session into a scripted world server and serves its methods over HTTP.
func newRig(t *testing.T) *agentRig {
	t.Helper()
	key := bytes.Repeat([]byte{7}, 40)
	cli, srvConn := net.Pipe()
	t.Cleanup(func() { cli.Close(); srvConn.Close() })
	ch := make(chan *worldtest.Server, 1)
	go func() { srv, _ := worldtest.Accept(srvConn, key, worldtest.Options{}); ch <- srv }()
	conn, err := worldconn.Client(cli, "TESTACC", key, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := &agentRig{t: t, srv: <-ch, run: make(chan error, 1)}
	r.s = session.New(conn, session.Options{Character: "Harnessb"})
	go func() { r.run <- r.s.Run(context.Background()) }()
	srv := rpc.NewServer()
	registerMethods(srv, r.s)
	r.ts = httptest.NewServer(srv.Handler())
	t.Cleanup(r.ts.Close)
	r.expect(opcode.CMSGCharEnum)
	enum := packet.NewWriter().U8(1).GUID(0x2A).CString("Harnessb").Append(make([]byte, 150)).Bytes()
	r.srv.Send(opcode.SMSGCharEnum, enum)
	r.expect(opcode.CMSGPlayerLogin)
	r.srv.Send(opcode.SMSGLoginVerifyWorld, packet.NewWriter().U32(0).F32(1).F32(2).F32(3).F32(0).Bytes())
	<-r.s.Ready()
	return r
}

func (r *agentRig) expect(op uint16) {
	r.t.Helper()
	if _, err := r.srv.Expect(op, 2*time.Second); err != nil {
		r.t.Fatal(err)
	}
}

func (r *agentRig) call(method, params string) map[string]any {
	r.t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"` + method + `","params":` + params + `}`
	resp, err := http.Post(r.ts.URL+"/rpc", "application/json", strings.NewReader(body))
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return out
}

func TestStatusChatAndChatLog(t *testing.T) {
	r := newRig(t)
	if st := r.call("status", "{}")["result"].(map[string]any); st["state"] != "in_world" || st["character"] != "Harnessb" {
		t.Fatalf("%v", st)
	}
	if out := r.call("chat", `{"type":"say","text":"hi"}`); out["error"] != nil {
		t.Fatalf("%v", out)
	}
	r.expect(opcode.CMSGMessagechat)
	if out := r.call("chat", `{"type":"system","text":"hi"}`); errCode(out) != rpc.CodeGame {
		t.Fatalf("%v", out)
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		r.srv.Send(opcode.SMSGNotification, []byte("You are now visible.\x00"))
	}()
	page := r.call("chat_log", `{"since":0,"wait_ms":3000}`)["result"].(map[string]any)
	evs := page["events"].([]any)
	if len(evs) != 1 || evs[0].(map[string]any)["text"] != "You are now visible." {
		t.Fatalf("%v", page)
	}
}

// Review Focus 4: after the server drops the connection, calls fail fast with -32002.
func TestRPCAfterDisconnect(t *testing.T) {
	r := newRig(t)
	r.srv.Close()
	<-r.run
	start := time.Now()
	if out := r.call("chat", `{"type":"say","text":"hi"}`); errCode(out) != rpc.CodeDisconnected {
		t.Fatalf("%v", out)
	}
	if time.Since(start) > time.Second {
		t.Fatal("call after disconnect was slow")
	}
	if st := r.call("status", "{}")["result"].(map[string]any); st["state"] != "disconnected" || st["error"] == "" {
		t.Fatalf("%v", st)
	}
}

func errCode(m map[string]any) int {
	e, _ := m["error"].(map[string]any)
	c, _ := e["code"].(float64)
	return int(c)
}
```

- [ ] **Step 2: Run them and see them fail**

Run: `cd ~/turtle-agent && ~/.local/go/bin/go test ./internal/rpc/ ./cmd/turtle-agent/`
Expected: FAIL, `undefined: NewServer` (rpc) and `undefined: registerMethods` (cmd).

- [ ] **Step 3: Implement the server**

`~/turtle-agent/internal/rpc/server.go`:

```go
// Copyright (c) 2026 agent-wow
// SPDX-License-Identifier: MIT
// Ported from github.com/agent-wow/agent-wow pkg/worldrpc/server.go and router.go at
// 5ef4916 (see NOTICE): loopback-only Listen, the local-process request checks and the
// JSON-RPC 2.0 envelope are kept; dispatch is a method registry instead of gRPC modules.

// Package rpc serves local JSON-RPC 2.0 over HTTP POST /rpc.
package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"
)

const (
	CodeParse          = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternal       = -32000
	CodeNotInWorld     = -32001
	CodeDisconnected   = -32002
	CodeGame           = -32003
	CodeTimeout        = -32004
)

// Error is a JSON-RPC error; methods return it to choose the code.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *Error) Error() string { return e.Message }

type Method func(ctx context.Context, params json.RawMessage) (any, error)

// Typed decodes object params into P (unknown fields rejected) and calls fn.
func Typed[P, R any](fn func(ctx context.Context, p P) (R, error)) Method {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p P
		if t := bytes.TrimSpace(raw); len(t) > 0 && string(t) != "null" {
			dec := json.NewDecoder(bytes.NewReader(t))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&p); err != nil {
				return nil, &Error{Code: CodeInvalidParams, Message: "invalid params: " + err.Error()}
			}
		}
		return fn(ctx, p)
	}
}

type Server struct {
	mu      sync.RWMutex
	methods map[string]Method
}

func NewServer() *Server { return &Server{methods: map[string]Method{}} }

func (s *Server) Register(name string, m Method) {
	s.mu.Lock()
	s.methods[name] = m
	s.mu.Unlock()
}

// Methods lists the registered names, sorted.
func (s *Server) Methods() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.methods))
	for n := range s.methods {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Listen accepts numeric loopback addresses (or localhost as 127.0.0.1) only.
func Listen(ctx context.Context, address string) (net.Listener, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, errors.New("RPC listen address must be a loopback host:port")
	}
	if host == "localhost" {
		host = "127.0.0.1"
	}
	ip := net.ParseIP(host)
	n, err := strconv.Atoi(port)
	if ip == nil || !ip.IsLoopback() || err != nil || n < 0 || n > 65535 {
		return nil, errors.New("RPC listen address must be a loopback IP and a port between 0 and 65535")
	}
	return (&net.ListenConfig{}).Listen(ctx, "tcp", net.JoinHostPort(ip.String(), strconv.Itoa(n)))
}

// Serve runs HTTP on ln until ctx ends, then shuts down, letting running calls finish (5 s).
func Serve(ctx context.Context, ln net.Listener, h http.Handler) error {
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
		if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

type request struct {
	Version string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	ID      json.RawMessage `json:"id"`
}

type response struct {
	Version string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Handler serves POST /rpc for local processes; browser-origin requests are refused.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rpc" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		_, origin := r.Header["Origin"]
		_, fetch := r.Header["Sec-Fetch-Site"]
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		ip := net.ParseIP(host)
		if origin || fetch || (host != "localhost" && (ip == nil || !ip.IsLoopback())) {
			http.Error(w, "local process requests only", http.StatusForbidden)
			return
		}
		if media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || media != "application/json" {
			http.Error(w, "application/json required", http.StatusUnsupportedMediaType)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
		if err != nil || !json.Valid(body) {
			writeJSON(w, response{Version: "2.0", ID: json.RawMessage("null"), Error: &Error{Code: CodeParse, Message: "Parse error"}})
			return
		}
		var req request
		trimmed := bytes.TrimSpace(body)
		if err := json.Unmarshal(body, &req); err != nil || trimmed[0] != '{' || req.Version != "2.0" || req.Method == "" || !validID(req.ID) {
			writeJSON(w, response{Version: "2.0", ID: json.RawMessage("null"), Error: &Error{Code: CodeInvalidRequest, Message: "Invalid Request"}})
			return
		}
		resp := response{Version: "2.0", ID: req.ID}
		s.mu.RLock()
		m, ok := s.methods[req.Method]
		s.mu.RUnlock()
		if !ok {
			resp.Error = &Error{Code: CodeMethodNotFound, Message: "Method not found: " + req.Method}
		} else if v, err := m(r.Context(), req.Params); err != nil {
			var re *Error
			if errors.As(err, &re) {
				resp.Error = re
			} else {
				resp.Error = &Error{Code: CodeInternal, Message: err.Error()}
			}
		} else if raw, err := json.Marshal(v); err != nil {
			resp.Error = &Error{Code: CodeInternal, Message: "encode result: " + err.Error()}
		} else {
			resp.Result = raw
		}
		if req.ID == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, resp)
	})
}

func writeJSON(w http.ResponseWriter, v any) { _ = json.NewEncoder(w).Encode(v) }

func validID(id json.RawMessage) bool {
	if id == nil {
		return true
	}
	var v any
	d := json.NewDecoder(bytes.NewReader(id))
	d.UseNumber()
	if d.Decode(&v) != nil {
		return false
	}
	switch v.(type) {
	case nil, string, json.Number:
		return true
	}
	return false
}
```

- [ ] **Step 4: Implement the methods and start the server from `login`**

`~/turtle-agent/cmd/turtle-agent/methods.go`:

```go
package main

import (
	"context"
	"errors"
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/events"
	"github.com/TortoiseBots/turtle-agent/internal/rpc"
	"github.com/TortoiseBots/turtle-agent/internal/session"
)

type empty struct{}

type chatParams struct {
	Type string `json:"type"`
	Text string `json:"text"`
	To   string `json:"to,omitempty"`
}

type gmParams struct {
	Text string `json:"text"`
}

type eventsParams struct {
	Since     uint64   `json:"since"`
	Kinds     []string `json:"kinds,omitempty"`
	PartyOnly bool     `json:"party_only,omitempty"`
	Limit     int      `json:"limit,omitempty"`
	WaitMS    int      `json:"wait_ms,omitempty"`
}

type chatLogParams struct {
	Since  uint64 `json:"since"`
	Limit  int    `json:"limit,omitempty"`
	WaitMS int    `json:"wait_ms,omitempty"`
}

// registerMethods wires the spec's JSON-RPC methods (v1) to the session; later
// milestones add their methods at the end of this function.
func registerMethods(srv *rpc.Server, s *session.Session) {
	log := s.Log()
	srv.Register("status", rpc.Typed(func(ctx context.Context, _ empty) (session.StatusInfo, error) {
		return s.Status(), nil
	}))
	srv.Register("chat", rpc.Typed(func(ctx context.Context, p chatParams) (empty, error) {
		return empty{}, rpcErr(s.Chat(ctx, p.Type, p.Text, p.To))
	}))
	srv.Register("gm", rpc.Typed(func(ctx context.Context, p gmParams) (empty, error) {
		return empty{}, rpcErr(s.GM(ctx, p.Text))
	}))
	srv.Register("events", rpc.Typed(func(ctx context.Context, p eventsParams) (events.Page, error) {
		q := events.Query{Since: p.Since, Kinds: p.Kinds, PartyOnly: p.PartyOnly, Limit: p.Limit}
		return readEvents(ctx, log, q, p.WaitMS), nil
	}))
	srv.Register("chat_log", rpc.Typed(func(ctx context.Context, p chatLogParams) (events.Page, error) {
		return readEvents(ctx, log, events.Query{Since: p.Since, Kinds: events.ChatKinds, Limit: p.Limit}, p.WaitMS), nil
	}))
	srv.Register("logout", rpc.Typed(func(ctx context.Context, _ empty) (empty, error) {
		return empty{}, rpcErr(s.Logout(ctx))
	}))
}

// readEvents long-polls up to waitMS (at most 25 s) until a matching event exists.
func readEvents(ctx context.Context, log *events.Log, q events.Query, waitMS int) events.Page {
	p := log.Read(q)
	if waitMS <= 0 || len(p.Events) > 0 {
		return p
	}
	if waitMS > 25000 {
		waitMS = 25000
	}
	wctx, cancel := context.WithTimeout(ctx, time.Duration(waitMS)*time.Millisecond)
	defer cancel()
	for len(p.Events) == 0 {
		if err := log.Wait(wctx, p.Next); err != nil {
			return p
		}
		q.Since = p.Next
		p = log.Read(q)
	}
	return p
}

// rpcErr maps session errors onto the JSON-RPC codes the Python wrapper branches on.
func rpcErr(err error) error {
	var le *session.LogoutError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, session.ErrNotInWorld):
		return &rpc.Error{Code: rpc.CodeNotInWorld, Message: err.Error()}
	case errors.Is(err, session.ErrDisconnected), errors.Is(err, session.ErrClosed):
		return &rpc.Error{Code: rpc.CodeDisconnected, Message: err.Error()}
	case errors.Is(err, context.DeadlineExceeded):
		return &rpc.Error{Code: rpc.CodeTimeout, Message: err.Error()}
	case errors.As(err, &le):
		return &rpc.Error{Code: rpc.CodeGame, Message: err.Error(), Data: map[string]any{"reason": le.Reason}}
	default:
		return &rpc.Error{Code: rpc.CodeGame, Message: err.Error()}
	}
}
```

In `~/turtle-agent/cmd/turtle-agent/login.go`:

1. Add `"github.com/TortoiseBots/turtle-agent/internal/rpc"` to the imports.
2. After the `stay := fs.Duration(...)` line add `rpcAddr := fs.String("rpc", "", "JSON-RPC listen address (default: the settings file's rpc)")`.
3. Between the `select` that waits for `s.Ready()` and the line `var stayC <-chan time.Time`, insert:

```go
	addrRPC := st.RPC
	if *rpcAddr != "" {
		addrRPC = *rpcAddr
	}
	ln, err := rpc.Listen(ctx, addrRPC)
	if err != nil {
		fmt.Fprintln(stderr, "rpc:", err)
		return logoutAndWait(s, runErr, stderr)
	}
	srv := rpc.NewServer()
	registerMethods(srv, s)
	rctx, stopRPC := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- rpc.Serve(rctx, ln, srv.Handler()) }()
	defer func() { stopRPC(); <-served }()
	fmt.Fprintf(stderr, "rpc: listening on http://%s/rpc\n", ln.Addr())
```

- [ ] **Step 5: Run the tests**

Run: `make -C ~/turtle-agent fmt test && make -C ~/turtle-agent build`
Expected: PASS for `internal/rpc` and `cmd/turtle-agent`; binary rebuilt.

- [ ] **Step 6: Record provenance and commit**

Append to `~/turtle-agent/NOTICE`:

```
internal/rpc/server.go | pkg/worldrpc/server.go, pkg/worldrpc/router.go | ported (loopback Listen, local-request checks, JSON-RPC 2.0 envelope kept; method registry and typed params new)
```

```bash
cd ~/turtle-agent && git status --short
git add internal/rpc cmd/turtle-agent NOTICE
git commit -F - <<'EOF'
rpc: loopback JSON-RPC server; status, chat, gm, events, chat_log, logout

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01MKCBbD9VJLeKqwGUyyzxyw
EOF
```

---

## Task 15: Milestone 2 live check (chat, GM, `.bot`) and the chat fixture

**Files:**
- Create: `testdata/captures/m2-chat.twcap` (recorded), `internal/game/capture_test.go`
- Test: `internal/game/capture_test.go`

**Interfaces:**
- Produces: test helper `func fixture(t *testing.T, name string) []capture.Record` in package `game`.
- Consumes: Tasks 12-14.

Throughout the live steps, `rpc` is this shell helper (it sends one JSON-RPC request to the agent):

```bash
rpc() { curl -s -H 'Content-Type: application/json' -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"$1\",\"params\":${2:-{\}}}" http://127.0.0.1:8765/rpc; echo; }
```

`$ACCOUNT`, `$BOT1`, `$BOT2`, `$SPOT` and `$SPELL` are the shell variables from Task 11 Step 1.

- [ ] **Step 1: Start the agent with a capture**

Run: `cd ~/turtle-agent && make build && ./bin/turtle-agent login --capture captures/m2-chat.twcap $ACCOUNT Harnessb 2> captures/m2.log & sleep 8; grep -E 'in world|rpc: listening' captures/m2.log`
Expected: `in world: Harnessb …` and `rpc: listening on http://127.0.0.1:8765/rpc`.

- [ ] **Step 2: Say, GM and `.bot` commands**

Run:

```bash
rpc status
rpc chat '{"type":"say","text":"turtle-agent milestone 2"}'
rpc gm '{"text":".gm off"}'
rpc gm '{"text":".gm visible on"}'
rpc gm '{"text":".bot version"}'
rpc gm "{\"text\":\".bot add $BOT1\"}"
sleep 5
rpc chat "{\"type\":\"whisper\",\"to\":\"$BOT1\",\"text\":\"hello\"}"
rpc chat_log '{"since":0,"wait_ms":3000}'
```

Expected: `status` has `"state":"in_world"` and `"character":"Harnessb"`; every `chat`/`gm` call answers `{"jsonrpc":"2.0","id":1,"result":{}}`; the `chat_log` page contains, in sequence order: a `chat` event with `"source":"Harnessb"`, `"text":"turtle-agent milestone 2"`, `data.type` `say`; `system` events for the two `.gm` commands (non-empty `text`); a `system` event whose text starts with `TBM:VERSION|`; the `.bot add` reply as `system` text naming BOT1; and a `whisper` event with `data.type` `whisper_inform` naming BOT1 (or, if BOT1 did not come online, a `system` event with `data.type` `player_not_found`). Bot whispers back to Harnessb, if any, appear as `whisper` events with `"source":"BOT1"`.

- [ ] **Step 3: Log out over RPC**

Run: `rpc logout; sleep 2; tail -3 captures/m2.log; jobs`
Expected: `{"jsonrpc":"2.0","id":1,"result":{}}`; the log ends with `logout: complete` and `capture: N records, R redacted, written to captures/m2-chat.twcap`; no job left running.

- [ ] **Step 4: Scrub, promote, and write the capture-driven test**

Run: `./bin/turtle-agent scrub-check captures/m2-chat.twcap && cp captures/m2-chat.twcap testdata/captures/m2-chat.twcap`
Expected: `captures/m2-chat.twcap: clean, N records`.

`~/turtle-agent/internal/game/capture_test.go`:

```go
package game

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/TortoiseBots/turtle-agent/internal/capture"
	"github.com/TortoiseBots/turtle-agent/internal/opcode"
)

func fixture(t *testing.T, name string) []capture.Record {
	t.Helper()
	recs, err := capture.ReadFile(filepath.Join("..", "..", "testdata", "captures", name))
	if err != nil {
		t.Fatalf("fixture %s: %v (record it with the milestone's live step)", name, err)
	}
	return recs
}

// Every chat packet the server sent during milestone 2 parses, and the .bot and say lines are there.
func TestM2ChatFromCapture(t *testing.T) {
	var sawVersion, sawSay bool
	for _, body := range capture.Bodies(fixture(t, "m2-chat.twcap"), opcode.SMSGMessagechat) {
		m, err := ParseMessageChat(body)
		if err != nil {
			t.Fatalf("% X: %v", body, err)
		}
		sawVersion = sawVersion || (m.Type == ChatSystem && strings.HasPrefix(m.Text, "TBM:VERSION|"))
		sawSay = sawSay || (m.Type == ChatSay && m.Text == "turtle-agent milestone 2")
	}
	if !sawVersion || !sawSay {
		t.Fatalf("version line %v, say line %v", sawVersion, sawSay)
	}
}
```

- [ ] **Step 5: Run the tests**

Run: `make -C ~/turtle-agent fmt test`
Expected: PASS, including `TestM2ChatFromCapture`.

- [ ] **Step 6: Commit**

```bash
cd ~/turtle-agent && git status --short
git add testdata/captures/m2-chat.twcap internal/game/capture_test.go
git commit -F - <<'EOF'
Milestone 2 on m910q: chat, GM and .bot commands over RPC; chat fixture

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01MKCBbD9VJLeKqwGUyyzxyw
EOF
```

---

# Milestone 3: party

## Task 16: Group codecs

**Files:**
- Create: `internal/game/group.go`
- Test: `internal/game/group_test.go`

**Interfaces:**
- Produces: `func BuildGroupInvite(name string) []byte`; `func BuildRequestPartyMemberStats(g packet.GUID) []byte`; group update flag constants `GroupUpdateStatus` … `GroupUpdatePetAurasNegative` and `GroupUpdateFull` (uint32); member status flags `MemberOnline`, `MemberDead`, `MemberGhost` (uint8); `type PartyResult struct { Op uint32; Member string; Result uint32 }` with `Text() string`; `func ParsePartyCommandResult(body []byte) (PartyResult, error)`; `type GroupMember struct { Name string; GUID packet.GUID; Status, Flags uint8 }`; `type GroupList struct { Type, OwnFlags uint8; Members []GroupMember; Leader packet.GUID; LootMethod uint8; Looter packet.GUID; LootThreshold uint8; Extra int }`; `func ParseGroupList(body []byte) (GroupList, error)`; `type MemberStats struct { GUID packet.GUID; Mask uint32; Status uint8; HP, MaxHP uint16; PowerType uint8; Power, MaxPower, Level, Zone, X, Y uint16; Auras []uint16; PetGUID packet.GUID; PetName string; PetHP, PetMaxHP uint16 }`; `func ParsePartyMemberStats(body []byte) (MemberStats, error)`.
- Consumes: Task 1, Task 12 (`ParseCString` for `SMSG_GROUP_INVITE` and `SMSG_GROUP_SET_LEADER`).

Layouts (`~/tortoise-wow/src/game`): `CMSG_GROUP_INVITE` is the member name (`Handlers/GroupHandler.cpp:56-59`); `SMSG_GROUP_INVITE` is the inviter's name (`:169-171`); `CMSG_GROUP_ACCEPT`, `CMSG_GROUP_DECLINE` and `CMSG_GROUP_DISBAND` (leave) are empty (`:176`, `:228`, `:354-370`); `SMSG_PARTY_COMMAND_RESULT` is u32 operation, member name, u32 result (`:46-54`) with operations and results at `WorldSession.h:86-102`; `SMSG_GROUP_LIST` is `Group::SendUpdate` (`Group/Group.cpp:1402-1432`: u8 group type, u8 own flags, u32 count, per other member name, u64 guid, u8 status, u8 flags, then u64 leader and, if count > 0, u8 loot method, u64 looter, u8 threshold and a u8 difficulty byte), and leaving a group sends 24 zero bytes (`Group.cpp:493-494`, `:595-596`); `SMSG_GROUP_UNINVITE` and `SMSG_GROUP_DESTROYED` are empty (`Group.cpp:463`, `:586`); `SMSG_GROUP_SET_LEADER` is the new leader's name (`Group.cpp:503-505`); member status flags are at `Group/Group.h:87-95`; `SMSG_PARTY_MEMBER_STATS` and `_FULL` are `BuildPartyMemberStatsPacket` (`GroupHandler.cpp:657-801`): packed guid, u32 mask, then per set flag in bit order the fields of `Group.h:108-132` (`GROUP_UPDATE_FULL = 0x001FFFFF`); an offline member gets packed guid, `GROUP_UPDATE_FLAG_STATUS`, status 0 (`GroupHandler.cpp:831-837`); `CMSG_REQUEST_PARTY_MEMBER_STATS` is the u64 guid (`:823-827`).

- [ ] **Step 1: Write the failing test**

`~/turtle-agent/internal/game/group_test.go`:

```go
package game

import (
	"bytes"
	"testing"

	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

// groupListBody transcribes Group::SendUpdate (Group.cpp:1402-1432) for the receiving player.
func groupListBody(leader packet.GUID, members ...GroupMember) []byte {
	w := packet.NewWriter().U8(0).U8(0).U32(uint32(len(members)))
	for _, m := range members {
		w.CString(m.Name).GUID(m.GUID).U8(m.Status).U8(m.Flags)
	}
	w.GUID(leader)
	if len(members) > 0 {
		w.U8(3).U64(0).U8(2).U8(0) // group loot, no looter, uncommon threshold, difficulty byte
	}
	return w.Bytes()
}

func TestGroupInviteAndResult(t *testing.T) {
	if !bytes.Equal(BuildGroupInvite("Gorm"), []byte("Gorm\x00")) {
		t.Fatal("invite body")
	}
	res, err := ParsePartyCommandResult(packet.NewWriter().U32(0).CString("Gorm").U32(4).Bytes())
	if err != nil || res.Result != 4 || res.Text() != "Gorm is already in a group." {
		t.Fatalf("%+v %q %v", res, res.Text(), err)
	}
}

func TestGroupListTwoMembers(t *testing.T) {
	body := groupListBody(0x2A, GroupMember{"Gorm", 0x30, MemberOnline, 0}, GroupMember{"Lia", 0x31, MemberOnline | MemberDead, 0})
	g, err := ParseGroupList(body)
	if err != nil || len(g.Members) != 2 || g.Members[1].Name != "Lia" || g.Leader != 0x2A || g.LootThreshold != 2 || g.Extra != 1 {
		t.Fatalf("%+v %v", g, err)
	}
}

// Review Focus 3: bytes past the fields we read (difficulty, or the 24-byte empty list) are tolerated.
func TestGroupListTrailingBytes(t *testing.T) {
	g, err := ParseGroupList(make([]byte, 24)) // Group.cpp:493-494
	if err != nil || len(g.Members) != 0 || g.Extra != 10 {
		t.Fatalf("%+v %v", g, err)
	}
	g, err = ParseGroupList(append(groupListBody(0x2A, GroupMember{"Gorm", 0x30, 1, 0}), 0xEE, 0xEE))
	if err != nil || g.Extra != 3 {
		t.Fatalf("%+v %v", g, err)
	}
	if _, err := ParseGroupList([]byte{0, 0, 5, 0, 0, 0}); err == nil {
		t.Fatal("truncated list accepted")
	}
}

// memberStatsBody transcribes BuildPartyMemberStatsPacket (GroupHandler.cpp:657-801), no pet.
func memberStatsBody(g packet.GUID, mask uint32) []byte {
	w := packet.NewWriter().PackedGUID(g).U32(mask)
	if mask&GroupUpdateStatus != 0 {
		w.U8(MemberOnline)
	}
	if mask&GroupUpdateCurHP != 0 {
		w.U16(500)
	}
	if mask&GroupUpdateMaxHP != 0 {
		w.U16(600)
	}
	if mask&GroupUpdatePowerType != 0 {
		w.U8(0)
	}
	if mask&GroupUpdateCurPower != 0 {
		w.U16(200)
	}
	if mask&GroupUpdateMaxPower != 0 {
		w.U16(300)
	}
	if mask&GroupUpdateLevel != 0 {
		w.U16(60)
	}
	if mask&GroupUpdateZone != 0 {
		w.U16(12)
	}
	if mask&GroupUpdatePosition != 0 {
		w.U16(100).U16(200)
	}
	if mask&GroupUpdateAuras != 0 {
		w.U32(0b101).U16(1243).U16(1459) // slots 0 and 2
	}
	if mask&GroupUpdateAurasNegative != 0 {
		w.U16(0b1).U16(589) // slot 32
	}
	if mask&GroupUpdatePetGUID != 0 {
		w.U64(0)
	}
	if mask&GroupUpdatePetName != 0 {
		w.U8(0)
	}
	for _, f := range []uint32{GroupUpdatePetModelID, GroupUpdatePetCurHP, GroupUpdatePetMaxHP} {
		if mask&f != 0 {
			w.U16(0)
		}
	}
	if mask&GroupUpdatePetPowerType != 0 {
		w.U8(0)
	}
	for _, f := range []uint32{GroupUpdatePetCurPower, GroupUpdatePetMaxPower} {
		if mask&f != 0 {
			w.U16(0)
		}
	}
	if mask&GroupUpdatePetAuras != 0 {
		w.U32(0)
	}
	if mask&GroupUpdatePetAurasNegative != 0 {
		w.U16(0)
	}
	return w.Bytes()
}

func TestPartyMemberStatsFullAndPartial(t *testing.T) {
	m, err := ParsePartyMemberStats(memberStatsBody(0x30, GroupUpdateFull))
	if err != nil || m.GUID != 0x30 || m.Status != MemberOnline || m.HP != 500 || m.MaxHP != 600 || m.Power != 200 ||
		m.Level != 60 || len(m.Auras) != 3 || m.Auras[2] != 589 {
		t.Fatalf("%+v %v", m, err)
	}
	m, err = ParsePartyMemberStats(memberStatsBody(0x30, GroupUpdateCurHP|GroupUpdateLevel))
	if err != nil || m.HP != 500 || m.Level != 60 || m.MaxHP != 0 {
		t.Fatalf("%+v %v", m, err)
	}
	offline := packet.NewWriter().PackedGUID(0x30).U32(GroupUpdateStatus).U8(0).Bytes() // GroupHandler.cpp:831-837
	if m, err := ParsePartyMemberStats(offline); err != nil || m.Status != 0 {
		t.Fatalf("%+v %v", m, err)
	}
}
```

- [ ] **Step 2: Run it and see it fail**

Run: `cd ~/turtle-agent && ~/.local/go/bin/go test ./internal/game/`
Expected: FAIL, `undefined: GroupMember` (and `BuildGroupInvite`, `MemberOnline`, `ParseGroupList`, `GroupUpdateFull`, …).

- [ ] **Step 3: Implement**

`~/turtle-agent/internal/game/group.go`:

```go
package game

import (
	"fmt"

	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

// BuildGroupInvite is CMSG_GROUP_INVITE: the name (GroupHandler.cpp:56-59).
func BuildGroupInvite(name string) []byte { return packet.NewWriter().CString(name).Bytes() }

// BuildRequestPartyMemberStats is CMSG_REQUEST_PARTY_MEMBER_STATS (GroupHandler.cpp:823-827).
func BuildRequestPartyMemberStats(g packet.GUID) []byte { return packet.NewWriter().GUID(g).Bytes() }

// Member status flags (Group.h:87-95).
const (
	MemberOnline uint8 = 0x01
	MemberDead   uint8 = 0x04
	MemberGhost  uint8 = 0x08
)

// Group update flags (Group.h:108-132).
const (
	GroupUpdateStatus           uint32 = 0x00000001
	GroupUpdateCurHP            uint32 = 0x00000002
	GroupUpdateMaxHP            uint32 = 0x00000004
	GroupUpdatePowerType        uint32 = 0x00000008
	GroupUpdateCurPower         uint32 = 0x00000010
	GroupUpdateMaxPower         uint32 = 0x00000020
	GroupUpdateLevel            uint32 = 0x00000040
	GroupUpdateZone             uint32 = 0x00000080
	GroupUpdatePosition         uint32 = 0x00000100
	GroupUpdateAuras            uint32 = 0x00000200
	GroupUpdateAurasNegative    uint32 = 0x00000400
	GroupUpdatePetGUID          uint32 = 0x00000800
	GroupUpdatePetName          uint32 = 0x00001000
	GroupUpdatePetModelID       uint32 = 0x00002000
	GroupUpdatePetCurHP         uint32 = 0x00004000
	GroupUpdatePetMaxHP         uint32 = 0x00008000
	GroupUpdatePetPowerType     uint32 = 0x00010000
	GroupUpdatePetCurPower      uint32 = 0x00020000
	GroupUpdatePetMaxPower      uint32 = 0x00040000
	GroupUpdatePetAuras         uint32 = 0x00080000
	GroupUpdatePetAurasNegative uint32 = 0x00100000
	GroupUpdateFull             uint32 = 0x001FFFFF
)

type PartyResult struct {
	Op     uint32 // PartyOperation: 0 invite, 2 leave (WorldSession.h:86-90)
	Member string
	Result uint32 // PartyResult (WorldSession.h:92-102)
}

// Text renders the result the way the client's strings do (WorldSession.h:94-102).
func (p PartyResult) Text() string {
	switch p.Result {
	case 0:
		return "OK"
	case 1:
		return fmt.Sprintf("Cannot find '%s'.", p.Member)
	case 2:
		return fmt.Sprintf("%s is not in your party.", p.Member)
	case 3:
		return "Your party is full."
	case 4:
		return fmt.Sprintf("%s is already in a group.", p.Member)
	case 5:
		return "You aren't in a party."
	case 6:
		return "You are not the party leader."
	case 7:
		return fmt.Sprintf("%s is of the wrong faction.", p.Member)
	case 8:
		return fmt.Sprintf("%s is ignoring you.", p.Member)
	}
	return fmt.Sprintf("party result %d for %s", p.Result, p.Member)
}

// ParsePartyCommandResult decodes SMSG_PARTY_COMMAND_RESULT (GroupHandler.cpp:46-54).
func ParsePartyCommandResult(body []byte) (PartyResult, error) {
	r := packet.NewReader(body)
	res := PartyResult{Op: r.U32(), Member: r.CString(), Result: r.U32()}
	if err := r.Err(); err != nil {
		return PartyResult{}, fmt.Errorf("SMSG_PARTY_COMMAND_RESULT: %w", err)
	}
	return res, nil
}

type GroupMember struct {
	Name   string
	GUID   packet.GUID
	Status uint8 // Member* flags
	Flags  uint8 // subgroup | 0x80 assistant
}

type GroupList struct {
	Type          uint8
	OwnFlags      uint8
	Members       []GroupMember // every member except the receiving player (Group.cpp:1411-1412)
	Leader        packet.GUID
	LootMethod    uint8
	Looter        packet.GUID
	LootThreshold uint8
	Extra         int // bytes after the fields read: the difficulty byte, or the empty list's zeros
}

// ParseGroupList decodes Group::SendUpdate (Group.cpp:1402-1432).
func ParseGroupList(body []byte) (GroupList, error) {
	r := packet.NewReader(body)
	var g GroupList
	g.Type, g.OwnFlags = r.U8(), r.U8()
	n := r.U32()
	if n > 40 {
		return GroupList{}, fmt.Errorf("SMSG_GROUP_LIST: %d members", n)
	}
	for i := uint32(0); i < n; i++ {
		var m GroupMember
		m.Name, m.GUID, m.Status, m.Flags = r.CString(), r.GUID(), r.U8(), r.U8()
		g.Members = append(g.Members, m)
	}
	g.Leader = r.GUID()
	if n > 0 {
		g.LootMethod, g.Looter, g.LootThreshold = r.U8(), r.GUID(), r.U8()
	}
	if err := r.Err(); err != nil {
		return GroupList{}, fmt.Errorf("SMSG_GROUP_LIST: %w", err)
	}
	g.Extra = r.Remaining()
	return g, nil
}

type MemberStats struct {
	GUID             packet.GUID
	Mask             uint32
	Status           uint8
	HP, MaxHP        uint16
	PowerType        uint8
	Power, MaxPower  uint16
	Level, Zone      uint16
	X, Y             uint16
	Auras            []uint16 // spell ids, positive slots first
	PetGUID          packet.GUID
	PetName          string
	PetHP, PetMaxHP  uint16
}

// ParsePartyMemberStats decodes BuildPartyMemberStatsPacket (GroupHandler.cpp:657-801),
// reading every flagged field in order so the pet fields stay aligned.
func ParsePartyMemberStats(body []byte) (MemberStats, error) {
	r := packet.NewReader(body)
	var m MemberStats
	m.GUID, m.Mask = r.PackedGUID(), r.U32()
	f := m.Mask
	if f&GroupUpdateStatus != 0 {
		m.Status = r.U8()
	}
	if f&GroupUpdateCurHP != 0 {
		m.HP = r.U16()
	}
	if f&GroupUpdateMaxHP != 0 {
		m.MaxHP = r.U16()
	}
	if f&GroupUpdatePowerType != 0 {
		m.PowerType = r.U8()
	}
	if f&GroupUpdateCurPower != 0 {
		m.Power = r.U16()
	}
	if f&GroupUpdateMaxPower != 0 {
		m.MaxPower = r.U16()
	}
	if f&GroupUpdateLevel != 0 {
		m.Level = r.U16()
	}
	if f&GroupUpdateZone != 0 {
		m.Zone = r.U16()
	}
	if f&GroupUpdatePosition != 0 {
		m.X, m.Y = r.U16(), r.U16()
	}
	if f&GroupUpdateAuras != 0 {
		mask := r.U32()
		for i := 0; i < 32; i++ {
			if mask&(1<<i) != 0 {
				m.Auras = append(m.Auras, r.U16())
			}
		}
	}
	if f&GroupUpdateAurasNegative != 0 {
		mask := r.U16() // aura mask >> 32: slots 32-47 (GroupHandler.cpp:700-708)
		for i := 0; i < 16; i++ {
			if mask&(1<<i) != 0 {
				m.Auras = append(m.Auras, r.U16())
			}
		}
	}
	if f&GroupUpdatePetGUID != 0 {
		m.PetGUID = r.GUID()
	}
	if f&GroupUpdatePetName != 0 {
		m.PetName = r.CString()
	}
	if f&GroupUpdatePetModelID != 0 {
		r.U16()
	}
	if f&GroupUpdatePetCurHP != 0 {
		m.PetHP = r.U16()
	}
	if f&GroupUpdatePetMaxHP != 0 {
		m.PetMaxHP = r.U16()
	}
	if f&GroupUpdatePetPowerType != 0 {
		r.U8()
	}
	if f&GroupUpdatePetCurPower != 0 {
		r.U16()
	}
	if f&GroupUpdatePetMaxPower != 0 {
		r.U16()
	}
	if f&GroupUpdatePetAuras != 0 {
		mask := r.U32()
		for i := 0; i < 32; i++ {
			if mask&(1<<i) != 0 {
				r.U16()
			}
		}
	}
	if f&GroupUpdatePetAurasNegative != 0 {
		mask := r.U16()
		for i := 0; i < 16; i++ {
			if mask&(1<<i) != 0 {
				r.U16()
			}
		}
	}
	if err := r.Err(); err != nil {
		return MemberStats{}, fmt.Errorf("party member stats: %w", err)
	}
	return m, nil
}
```

- [ ] **Step 4: Run the tests**

Run: `make -C ~/turtle-agent fmt test`
Expected: PASS for `internal/game`.

- [ ] **Step 5: Commit**

```bash
cd ~/turtle-agent && git status --short
git add internal/game/group.go internal/game/group_test.go
git commit -F - <<'EOF'
game: group invite, command result, group list and member stats codecs

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01MKCBbD9VJLeKqwGUyyzxyw
EOF
```

---

## Task 17: Session party state and party RPC methods

**Files:**
- Create: `internal/session/party.go`
- Modify: `internal/session/session.go` (`register`, struct field), `cmd/turtle-agent/methods.go` (four methods)
- Test: `internal/session/party_test.go`

**Interfaces:**
- Produces: `type PartyMember struct { Name string; GUID packet.GUID; Online, Dead, Leader bool; Health, MaxHealth uint16; PowerType uint8; Power, MaxPower, Level, Zone uint16 }`; `type PartyInfo struct { InGroup bool; Leader packet.GUID; Members []PartyMember; PendingInvite string }`; `(*Session).Invite(ctx context.Context, name string) error`; `(*Session).AcceptInvite(ctx context.Context) error`; `(*Session).LeaveGroup(ctx context.Context) error`; `(*Session).Party() PartyInfo`; internal `(*Session).inParty(g packet.GUID) bool` (true for the player and current members). RPC: `invite` {name} → {}; `accept_invite` {} → {}; `leave_group` {} → {}; `party` {} → `PartyInfo`.
- Consumes: Task 16 codecs, Task 13 (`emit`, names), Task 14 (`registerMethods`, `rpcErr`).

Events (`party` kind, `Data["event"]`): `invite` (`from`), `result` (`operation`, `member`, `result`; `Text` is `PartyResult.Text()`), `joined` / `left` (source = member), `leader` (`name`), `disbanded`, `uninvited`. `party_stats` events carry `Source` = member and `Data` `health`, `max_health`, `online`, `dead`. When a member joins, the session sends `CMSG_REQUEST_PARTY_MEMBER_STATS` for it so health and level arrive without waiting for a change.

- [ ] **Step 1: Write the failing test**

`~/turtle-agent/internal/session/party_test.go`:

```go
package session

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/events"
	"github.com/TortoiseBots/turtle-agent/internal/game"
	"github.com/TortoiseBots/turtle-agent/internal/opcode"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

// groupList transcribes Group::SendUpdate (Group.cpp:1402-1432).
func groupList(leader packet.GUID, members ...game.GroupMember) []byte {
	w := packet.NewWriter().U8(0).U8(0).U32(uint32(len(members)))
	for _, m := range members {
		w.CString(m.Name).GUID(m.GUID).U8(m.Status).U8(m.Flags)
	}
	w.GUID(leader)
	if len(members) > 0 {
		w.U8(3).U64(0).U8(2).U8(0)
	}
	return w.Bytes()
}

func fullStats(g packet.GUID, hp, maxHP uint16) []byte {
	mask := game.GroupUpdateStatus | game.GroupUpdateCurHP | game.GroupUpdateMaxHP | game.GroupUpdateLevel
	return packet.NewWriter().PackedGUID(g).U32(mask).U8(game.MemberOnline).U16(hp).U16(maxHP).U16(60).Bytes()
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestInviteGroupListAndStats(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	if err := h.s.Invite(context.Background(), "Gorm"); err != nil {
		t.Fatal(err)
	}
	if p := h.expect(opcode.CMSGGroupInvite); !bytes.Equal(p.Body, []byte("Gorm\x00")) {
		t.Fatalf("% X", p.Body)
	}
	h.send(opcode.SMSGPartyCommandResult, packet.NewWriter().U32(0).CString("Gorm").U32(0).Bytes())
	h.send(opcode.SMSGGroupList, groupList(me.GUID, game.GroupMember{Name: "Gorm", GUID: 0x30, Status: game.MemberOnline}))
	if p := h.expect(opcode.CMSGRequestPartyMemberStats); packet.NewReader(p.Body).GUID() != 0x30 {
		t.Fatalf("stats request % X", p.Body)
	}
	h.send(opcode.SMSGPartyMemberStatsFull, fullStats(0x30, 500, 600))
	waitFor(t, func() bool { p := h.s.Party(); return len(p.Members) == 1 && p.Members[0].Health == 500 })
	p := h.s.Party()
	if !p.InGroup || p.Leader != me.GUID || p.Members[0].Name != "Gorm" || !p.Members[0].Online || p.Members[0].MaxHealth != 600 || p.Members[0].Level != 60 {
		t.Fatalf("%+v", p)
	}
	if !h.s.inParty(0x30) || !h.s.inParty(me.GUID) || h.s.inParty(0x31) {
		t.Fatal("inParty")
	}
	h.logout()
	var got []string
	for _, e := range ofKind(h.s.Log(), events.KindParty) {
		got = append(got, e.Data["event"].(string)+":"+e.Source)
	}
	if strings.Join(got, ",") != "result:,joined:Gorm" {
		t.Fatalf("party events %v", got)
	}
	if st := ofKind(h.s.Log(), events.KindPartyStats); len(st) != 1 || st[0].Source != "Gorm" {
		t.Fatalf("stats events %+v", st)
	}
}

func TestAcceptInvite(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	h.send(opcode.SMSGGroupInvite, []byte("Lia\x00"))
	waitFor(t, func() bool { return h.s.Party().PendingInvite == "Lia" })
	if err := h.s.AcceptInvite(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.expect(opcode.CMSGGroupAccept)
	if err := h.s.AcceptInvite(context.Background()); err == nil || !strings.Contains(err.Error(), "no pending") {
		t.Fatalf("second accept: %v", err)
	}
	h.logout()
}

func TestLeaveGroupAndDisband(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	if err := h.s.LeaveGroup(context.Background()); err == nil {
		t.Fatal("left a group we are not in")
	}
	h.send(opcode.SMSGGroupList, groupList(0x30, game.GroupMember{Name: "Gorm", GUID: 0x30, Status: game.MemberOnline}))
	waitFor(t, func() bool { return h.s.Party().InGroup })
	if err := h.s.LeaveGroup(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.expect(opcode.CMSGGroupDisband)
	h.send(opcode.SMSGGroupDestroyed, nil)
	waitFor(t, func() bool { return !h.s.Party().InGroup })
	h.logout()
}
```

- [ ] **Step 2: Run it and see it fail**

Run: `cd ~/turtle-agent && ~/.local/go/bin/go test ./internal/session/`
Expected: FAIL, `h.s.Invite undefined` (and `Party`, `AcceptInvite`, `LeaveGroup`, `inParty`).

- [ ] **Step 3: Implement**

`~/turtle-agent/internal/session/party.go`:

```go
package session

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/TortoiseBots/turtle-agent/internal/events"
	"github.com/TortoiseBots/turtle-agent/internal/game"
	"github.com/TortoiseBots/turtle-agent/internal/opcode"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

type PartyMember struct {
	Name      string      `json:"name"`
	GUID      packet.GUID `json:"guid"`
	Online    bool        `json:"online"`
	Dead      bool        `json:"dead"`
	Leader    bool        `json:"leader"`
	Health    uint16      `json:"health"`
	MaxHealth uint16      `json:"max_health"`
	PowerType uint8       `json:"power_type"`
	Power     uint16      `json:"power"`
	MaxPower  uint16      `json:"max_power"`
	Level     uint16      `json:"level"`
	Zone      uint16      `json:"zone"`
}

// PartyInfo is the group seen from the player; Members never includes the player.
type PartyInfo struct {
	InGroup       bool          `json:"in_group"`
	Leader        packet.GUID   `json:"leader,omitempty"`
	Members       []PartyMember `json:"members"`
	PendingInvite string        `json:"pending_invite,omitempty"`
}

func (s *Session) registerParty() {
	s.handlers[opcode.SMSGGroupInvite] = s.onGroupInvite
	s.handlers[opcode.SMSGPartyCommandResult] = s.onPartyResult
	s.handlers[opcode.SMSGGroupList] = s.onGroupList
	s.handlers[opcode.SMSGGroupDestroyed] = func([]byte) error { s.clearParty("disbanded"); return nil }
	s.handlers[opcode.SMSGGroupUninvite] = func([]byte) error { s.clearParty("uninvited"); return nil }
	s.handlers[opcode.SMSGGroupSetLeader] = s.onGroupSetLeader
	s.handlers[opcode.SMSGPartyMemberStats] = s.onMemberStats
	s.handlers[opcode.SMSGPartyMemberStatsFull] = s.onMemberStats
}

func (s *Session) Party() PartyInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.party
	p.Members = append([]PartyMember{}, s.party.Members...)
	return p
}

func (s *Session) inParty(g packet.GUID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if g != 0 && g == s.self.GUID {
		return true
	}
	for _, m := range s.party.Members {
		if m.GUID == g {
			return true
		}
	}
	return false
}

func (s *Session) Invite(ctx context.Context, name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("invite needs a character name")
	}
	return s.send(ctx, opcode.CMSGGroupInvite, game.BuildGroupInvite(name))
}

func (s *Session) AcceptInvite(ctx context.Context) error {
	_, err := s.do(ctx, func() (any, error) {
		if st := s.State(); st != StatusInWorld {
			return nil, fmt.Errorf("%w (state %s)", ErrNotInWorld, st)
		}
		s.mu.Lock()
		pending := s.party.PendingInvite
		s.mu.Unlock()
		if pending == "" {
			return nil, errors.New("no pending group invite")
		}
		if err := s.conn.WritePacket(opcode.CMSGGroupAccept, nil); err != nil {
			return nil, err
		}
		s.mu.Lock()
		s.party.PendingInvite = ""
		s.mu.Unlock()
		return nil, nil
	})
	return err
}

// LeaveGroup sends CMSG_GROUP_DISBAND, which removes the player from the group (GroupHandler.cpp:354-370).
func (s *Session) LeaveGroup(ctx context.Context) error {
	_, err := s.do(ctx, func() (any, error) {
		if st := s.State(); st != StatusInWorld {
			return nil, fmt.Errorf("%w (state %s)", ErrNotInWorld, st)
		}
		if !s.Party().InGroup {
			return nil, errors.New("not in a group")
		}
		return nil, s.conn.WritePacket(opcode.CMSGGroupDisband, nil)
	})
	return err
}

func (s *Session) onGroupInvite(body []byte) error {
	from, err := game.ParseCString(body)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.party.PendingInvite = from
	s.mu.Unlock()
	s.emit(events.Event{Kind: events.KindParty, Source: from, Text: from + " invites you to a group.",
		Data: map[string]any{"event": "invite", "from": from}})
	return nil
}

func (s *Session) onPartyResult(body []byte) error {
	res, err := game.ParsePartyCommandResult(body)
	if err != nil {
		return err
	}
	s.emit(events.Event{Kind: events.KindParty, Text: res.Text(),
		Data: map[string]any{"event": "result", "operation": res.Op, "member": res.Member, "result": res.Result}})
	return nil
}

func (s *Session) onGroupList(body []byte) error {
	g, err := game.ParseGroupList(body)
	if err != nil {
		return err
	}
	s.mu.Lock()
	old := map[packet.GUID]PartyMember{}
	for _, m := range s.party.Members {
		old[m.GUID] = m
	}
	next := PartyInfo{InGroup: len(g.Members) > 0, Leader: g.Leader, PendingInvite: s.party.PendingInvite}
	var joined []PartyMember
	for _, gm := range g.Members {
		m, known := old[gm.GUID]
		m.Name, m.GUID, m.Leader = gm.Name, gm.GUID, gm.GUID == g.Leader
		m.Online, m.Dead = gm.Status&game.MemberOnline != 0, gm.Status&(game.MemberDead|game.MemberGhost) != 0
		next.Members = append(next.Members, m)
		delete(old, gm.GUID)
		if !known {
			joined = append(joined, m)
		}
	}
	s.party = next
	s.mu.Unlock()
	for _, m := range joined {
		s.names.setPlayer(m.GUID, m.Name)
		_ = s.conn.WritePacket(opcode.CMSGRequestPartyMemberStats, game.BuildRequestPartyMemberStats(m.GUID))
		s.emit(events.Event{Kind: events.KindParty, Source: m.Name, SourceGUID: m.GUID, Data: map[string]any{"event": "joined"}})
	}
	for _, m := range old {
		s.emit(events.Event{Kind: events.KindParty, Source: m.Name, SourceGUID: m.GUID, Data: map[string]any{"event": "left"}})
	}
	return nil
}

func (s *Session) clearParty(why string) {
	s.mu.Lock()
	s.party = PartyInfo{PendingInvite: s.party.PendingInvite}
	s.mu.Unlock()
	s.emit(events.Event{Kind: events.KindParty, Data: map[string]any{"event": why}})
}

func (s *Session) onGroupSetLeader(body []byte) error {
	name, err := game.ParseCString(body)
	if err != nil {
		return err
	}
	s.mu.Lock()
	for i := range s.party.Members {
		s.party.Members[i].Leader = strings.EqualFold(s.party.Members[i].Name, name)
		if s.party.Members[i].Leader {
			s.party.Leader = s.party.Members[i].GUID
		}
	}
	if strings.EqualFold(s.self.Name, name) {
		s.party.Leader = s.self.GUID
	}
	s.mu.Unlock()
	s.emit(events.Event{Kind: events.KindParty, Data: map[string]any{"event": "leader", "name": name}})
	return nil
}

func (s *Session) onMemberStats(body []byte) error {
	st, err := game.ParsePartyMemberStats(body)
	if err != nil {
		return err
	}
	s.mu.Lock()
	var name string
	for i := range s.party.Members {
		m := &s.party.Members[i]
		if m.GUID != st.GUID {
			continue
		}
		name = m.Name
		if st.Mask&game.GroupUpdateStatus != 0 {
			m.Online, m.Dead = st.Status&game.MemberOnline != 0, st.Status&(game.MemberDead|game.MemberGhost) != 0
		}
		if st.Mask&game.GroupUpdateCurHP != 0 {
			m.Health = st.HP
		}
		if st.Mask&game.GroupUpdateMaxHP != 0 {
			m.MaxHealth = st.MaxHP
		}
		if st.Mask&game.GroupUpdatePowerType != 0 {
			m.PowerType = st.PowerType
		}
		if st.Mask&game.GroupUpdateCurPower != 0 {
			m.Power = st.Power
		}
		if st.Mask&game.GroupUpdateMaxPower != 0 {
			m.MaxPower = st.MaxPower
		}
		if st.Mask&game.GroupUpdateLevel != 0 {
			m.Level = st.Level
		}
		if st.Mask&game.GroupUpdateZone != 0 {
			m.Zone = st.Zone
		}
	}
	s.mu.Unlock()
	s.emit(events.Event{Kind: events.KindPartyStats, Source: name, SourceGUID: st.GUID,
		Data: map[string]any{"mask": st.Mask, "health": st.HP, "max_health": st.MaxHP, "online": st.Status&game.MemberOnline != 0}})
	return nil
}
```

In `~/turtle-agent/internal/session/session.go`:

1. In the `Session` struct, among the `mu`-guarded fields, add `party PartyInfo`.
2. In `register`, add `s.registerParty()` after `s.registerChat()`.

In `~/turtle-agent/cmd/turtle-agent/methods.go`, add the type

```go
type nameParams struct {
	Name string `json:"name"`
}
```

and append to `registerMethods`:

```go
	srv.Register("invite", rpc.Typed(func(ctx context.Context, p nameParams) (empty, error) {
		return empty{}, rpcErr(s.Invite(ctx, p.Name))
	}))
	srv.Register("accept_invite", rpc.Typed(func(ctx context.Context, _ empty) (empty, error) {
		return empty{}, rpcErr(s.AcceptInvite(ctx))
	}))
	srv.Register("leave_group", rpc.Typed(func(ctx context.Context, _ empty) (empty, error) {
		return empty{}, rpcErr(s.LeaveGroup(ctx))
	}))
	srv.Register("party", rpc.Typed(func(ctx context.Context, _ empty) (session.PartyInfo, error) {
		return s.Party(), nil
	}))
```

- [ ] **Step 4: Run the tests**

Run: `make -C ~/turtle-agent fmt test && make -C ~/turtle-agent build`
Expected: PASS for `internal/session` and `cmd/turtle-agent`.

- [ ] **Step 5: Commit**

```bash
cd ~/turtle-agent && git status --short
git add internal/session cmd/turtle-agent/methods.go
git commit -F - <<'EOF'
session: party invite/accept/leave, group list and member stats; party RPCs

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01MKCBbD9VJLeKqwGUyyzxyw
EOF
```

---

## Task 18: Milestone 3 live check (two bots in the party) and the party fixture

**Files:**
- Create: `testdata/captures/m3-party.twcap` (recorded); add `TestM3PartyFromCapture` to `internal/game/capture_test.go`
- Test: `internal/game/capture_test.go`

**Interfaces:**
- Consumes: Tasks 16-17 and the `rpc` shell helper from Task 15.

- [ ] **Step 1: Start the agent, bring two bots online and invite them**

Run:

```bash
cd ~/turtle-agent && make build
./bin/turtle-agent login --capture captures/m3-party.twcap $ACCOUNT Harnessb 2> captures/m3.log & sleep 8
rpc gm "{\"text\":\".bot add $BOT1\"}"; rpc gm "{\"text\":\".bot add $BOT2\"}"; sleep 6
rpc invite "{\"name\":\"$BOT1\"}"; rpc invite "{\"name\":\"$BOT2\"}"; sleep 6
rpc party
rpc events '{"since":0,"kinds":["party"]}'
```

Expected: `party` returns `"in_group":true`, `"leader"` equal to Harnessb's GUID from `rpc status`, and `"members"` holding BOT1 and BOT2, each with `"online":true`, `"health"` > 0, `"max_health"` > 0 and `"level"` > 0. The `party` events include `result` events with `"result":0` for both names and `joined` events for BOT1 and BOT2. If a bot does not accept a direct invite, run `rpc gm "{\"text\":\".bot invite $BOT1\"}"` (player-controls.md, "Invite to Party") and expect the same `party` result.

- [ ] **Step 2: Leave the group and log out**

Run: `rpc leave_group; sleep 2; rpc party; rpc logout; sleep 2; tail -2 captures/m3.log`
Expected: `party` now shows `"in_group":false` and no members; the log ends with `logout: complete` and the capture line.

- [ ] **Step 3: Scrub and promote**

Run: `./bin/turtle-agent scrub-check captures/m3-party.twcap && cp captures/m3-party.twcap testdata/captures/m3-party.twcap`
Expected: `clean, N records`.

- [ ] **Step 4: Add the capture-driven test**

Append to `~/turtle-agent/internal/game/capture_test.go`:

```go
// Every group packet from milestone 3 parses; the populated list carries the bots.
func TestM3PartyFromCapture(t *testing.T) {
	recs := fixture(t, "m3-party.twcap")
	most := 0
	for _, body := range capture.Bodies(recs, opcode.SMSGGroupList) {
		g, err := ParseGroupList(body)
		if err != nil {
			t.Fatalf("% X: %v", body, err)
		}
		if len(g.Members) > most {
			most = len(g.Members)
		}
	}
	if most < 2 {
		t.Fatalf("largest group list had %d members, want 2", most)
	}
	stats := append(capture.Bodies(recs, opcode.SMSGPartyMemberStats), capture.Bodies(recs, opcode.SMSGPartyMemberStatsFull)...)
	if len(stats) == 0 {
		t.Fatal("no member stats captured")
	}
	for _, body := range stats {
		if _, err := ParsePartyMemberStats(body); err != nil {
			t.Fatalf("% X: %v", body, err)
		}
	}
}
```

- [ ] **Step 5: Run the tests**

Run: `make -C ~/turtle-agent fmt test`
Expected: PASS, including `TestM3PartyFromCapture`.

- [ ] **Step 6: Commit**

```bash
cd ~/turtle-agent && git status --short
git add testdata/captures/m3-party.twcap internal/game/capture_test.go
git commit -F - <<'EOF'
Milestone 3 on m910q: two bots invited, stats and leave; party fixture

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01MKCBbD9VJLeKqwGUyyzxyw
EOF
```

---

# Milestone 4: objects around the player

## Task 19: Update-object parser, movement info, compressed moves

**Files:**
- Create: `internal/game/movement.go`, `internal/objects/fields.go`, `internal/objects/update.go`, `internal/objects/moves.go`
- Test: `internal/game/movement_test.go`, `internal/objects/update_test.go`, `internal/objects/moves_test.go`, `internal/objects/capture_test.go` (capture-driven, milestone 1 fixture)

**Interfaces:**
- Produces (package `game`): movement flags `MoveForward`, `MoveBackward`, `MoveWalkMode`, `MoveRoot`, `MoveJumping`, `MoveFallingFar`, `MoveSwimming`, `MoveSplineEnabled`, `MoveOnTransport`, `MoveSplineElevation` (uint32); `type MoveInfo struct { Flags, Time uint32; X, Y, Z, O float32; Transport packet.GUID; TX, TY, TZ, TO, Pitch float32; FallTime uint32; JumpZSpeed, JumpCos, JumpSin, JumpXYSpeed, SplineElevation float32 }`; `func ReadMoveInfo(r *packet.Reader) MoveInfo`; `func WriteMoveInfo(w *packet.Writer, m MoveInfo)`.
- Produces (package `objects`): `type BlockType uint8` with `BlockValues`, `BlockMovement`, `BlockCreate`, `BlockCreate2`, `BlockOutOfRange`, `BlockNear`; `type Spline struct { Flags uint32; FinalPoint [3]float32; FinalTarget packet.GUID; FinalAngle float32; TimePassed, Duration, ID uint32; Points [][3]float32; Dest [3]float32 }`; `type Movement struct { UpdateFlags uint8; Living bool; Info game.MoveInfo; Speeds [6]float32; Spline *Spline; HasPosition bool; X, Y, Z, O float32; Victim packet.GUID }`; `type Block struct { Type BlockType; GUID packet.GUID; ObjType uint8; Move *Movement; Values map[uint16]uint32; GUIDs []packet.GUID }`; `func Inflate(body []byte) ([]byte, error)`; `func ParseUpdate(body []byte) ([]Block, error)`; `func ParseCompressedUpdate(body []byte) ([]Block, error)`; `type SubPacket struct { Op uint16; Body []byte }`; `func SplitCompressedMoves(body []byte) ([]SubPacket, error)`; `type MonsterMove struct { GUID packet.GUID; Start [3]float32; ID uint32; Stop bool; Dest [3]float32; Duration uint32 }`; `func ParseMonsterMove(body []byte) (MonsterMove, error)`; field constants `FieldGUID`, `FieldType`, `FieldEntry`, `UnitTarget`, `UnitHealth`, `UnitPower1`, `UnitMaxHealth`, `UnitMaxPower1`, `UnitLevel`, `UnitFaction`, `UnitBytes0`, `UnitFlags`, `UnitAura`, `UnitDynamicFlags`, `UnitNPCFlags` (uint16), `AuraSlots = 48`, `PositiveAuraSlots = 32`; unit flags `UnitFlagSpawning`, `UnitFlagPlayerControlled`, `UnitFlagImmuneToPlayer`, `UnitFlagInCombat`, `UnitFlagNotSelectable` (uint32); type ids `TypeUnit = 3`, `TypePlayer = 4`, `TypeGameObject = 5` (uint8).
- Consumes: Task 1.

Field indices (`~/tortoise-wow/src/game/Objects/UpdateFields.h`, `OBJECT_END = 0x06`): GUID :29, type :30, entry :31, `UNIT_FIELD_TARGET` (2 words) :77, health :80, power 1-5 :81-85, max health :86, max power 1-5 :87-91, level :92, faction template :93, bytes 0 (race, class, gender, power type; `Unit.h:455` reads the power type from byte 3) :94, flags :97, 48 aura slots :98-99, dynamic flags :128, NPC flags :132; the core's own cross-check `UpdateFields5875` gives flags 46, dynamic flags 143, NPC flags 147 (:338-348). Aura slots 0-31 are positive, 32-47 negative (`Spells/SpellAuraDefines.h:25-26`). Unit flags at `Objects/UnitDefines.h:402-441`. Type ids at `ObjectGuid.h:35-42`.

Layouts (`~/tortoise-wow/src/game`): `MovementInfo` read/write at `Objects/Object.cpp:64-106` / `:146-180` (u32 flags, u32 time, x, y, z, o; transport: u64 guid + 4 floats; swimming: pitch; u32 fall time; jumping: z speed, cos, sin, xy speed; spline elevation: float), flags at `Objects/Object.h:210-240`. The update packet header is u32 block count + u8 has-transport, then an out-of-range block if any (u8 type 4, u32 count, packed guids) (`Objects/UpdateData.cpp:97-117`); packets over 100 bytes become `SMSG_COMPRESSED_UPDATE_OBJECT`: u32 uncompressed size + zlib (`UpdateData.cpp:121-142`). Block types and update flags at `Objects/UpdateData.h:33-53`. A values block is u8 type, packed guid, values (`Object.cpp:378-390`); a movement block is u8 type, the full u64 guid, movement (`:264-272`); a create block is u8 type, packed guid, u8 type id, movement, values (`:274-308`). Movement is `Object::BuildMovementUpdate` (`:415-513`): u8 update flags; living: MovementInfo, six speeds, and if the flags carry spline-enabled the `PacketBuilder::WriteCreate` spline (`Movement/spline/packet_builder.cpp:154-180`: u32 spline flags; final angle float, or final target u64, or final point 3 floats; u32 time passed, u32 duration, u32 id, u32 node count, nodes, final destination; spline flags at `Movement/spline/MoveSplineFlag.h:36-70`); not living with has-position: 4 floats; then high-guid u32, all u32, melee-attacking packed victim guid, transport u32. Values are u8 mask block count, the u32 mask words, then one u32 per set bit in index order (`Object.cpp:553-556`). `SMSG_MONSTER_MOVE` is packed guid then either a stop (start x, y, z, u32 id, u8 1) (`Movement/spline/MoveSplineInit.cpp:134-153`) or `WriteMonsterMove` (`packet_builder.cpp:45-152`: start, u32 id, u8 facing type with 12/8/4 facing bytes for spot/target/angle, u32 flags, u32 duration, then for smooth (`Flying`) paths u32 count + count points, else u32 count + destination + packed offsets). `SMSG_COMPRESSED_MOVES` is u32 size + zlib of `u8 size, u16 opcode, body` entries where size counts the opcode bytes (`Objects/UpdateData.cpp:188-216`).

The one layout the source cannot settle is a living block whose movement flags carry spline-enabled while the spline is not initialized: `WriteCreate` then writes nothing (`packet_builder.cpp:158`). This parser reads the spline whenever the flag is set; `TestCapturedUpdatesParse` (this task, extended in Task 22) runs every captured update packet through it and pins the real behaviour.

- [ ] **Step 1: Write the failing tests**

`~/turtle-agent/internal/game/movement_test.go`:

```go
package game

import (
	"testing"

	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

func TestMoveInfoRoundTrip(t *testing.T) {
	plain := MoveInfo{Flags: MoveForward, Time: 1234, X: 1, Y: 2, Z: 3, O: 0.5, FallTime: 0}
	w := packet.NewWriter()
	WriteMoveInfo(w, plain)
	if w.Len() != 28 { // flags, time, 4 floats, fall time (Object.cpp:146-166)
		t.Fatalf("plain move info is %d bytes", w.Len())
	}
	if got := ReadMoveInfo(packet.NewReader(w.Bytes())); got != plain {
		t.Fatalf("%+v", got)
	}
	full := MoveInfo{Flags: MoveOnTransport | MoveSwimming | MoveJumping | MoveSplineElevation, X: 1, Transport: 0x1FC0000000000005,
		TX: 1, TY: 2, TZ: 3, TO: 4, Pitch: 0.1, FallTime: 9, JumpZSpeed: 1, JumpCos: 2, JumpSin: 3, JumpXYSpeed: 4, SplineElevation: 5}
	w = packet.NewWriter()
	WriteMoveInfo(w, full)
	r := packet.NewReader(w.Bytes())
	if got := ReadMoveInfo(r); got != full || r.Remaining() != 0 {
		t.Fatalf("%+v remaining %d", got, r.Remaining())
	}
}
```

`~/turtle-agent/internal/objects/update_test.go`:

```go
package objects

import (
	"bytes"
	"compress/zlib"
	"sort"
	"testing"

	"github.com/TortoiseBots/turtle-agent/internal/game"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

// values writes Object::BuildValuesUpdate's mask and values (Object.cpp:553-556), for an
// object of `count` fields.
func values(w *packet.Writer, count int, vals map[uint16]uint32) {
	blocks := (count + 31) / 32
	masks := make([]uint32, blocks)
	var idx []int
	for i := range vals {
		masks[i/32] |= 1 << (i % 32)
		idx = append(idx, int(i))
	}
	sort.Ints(idx)
	w.U8(uint8(blocks))
	for _, m := range masks {
		w.U32(m)
	}
	for _, i := range idx {
		w.U32(vals[uint16(i)])
	}
}

// living writes BuildMovementUpdate for a living unit (Object.cpp:415-449).
func living(w *packet.Writer, flags uint8, mi game.MoveInfo, spline func(*packet.Writer)) {
	w.U8(flags)
	game.WriteMoveInfo(w, mi)
	for i := 0; i < 6; i++ {
		w.F32(float32(i + 1))
	}
	if spline != nil {
		spline(w)
	}
	if flags&0x08 != 0 {
		w.U32(0)
	}
	if flags&0x10 != 0 {
		w.U32(1)
	}
}

func header(count uint32) *packet.Writer { return packet.NewWriter().U32(count).U8(0) }

func compress(plain []byte) []byte {
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	zw.Write(plain)
	zw.Close()
	return packet.NewWriter().U32(uint32(len(plain))).Append(z.Bytes()).Bytes()
}

const wolf = packet.GUID(0xF13000012B000001)

func TestCreateLivingUnitBlock(t *testing.T) {
	w := header(1).U8(uint8(BlockCreate2)).PackedGUID(wolf).U8(3)
	living(w, 0x20|0x10, game.MoveInfo{X: 10, Y: 20, Z: 30, O: 1}, nil)
	values(w, 188, map[uint16]uint32{UnitHealth: 100, UnitMaxHealth: 120, UnitLevel: 5, FieldEntry: 299})
	blocks, err := ParseUpdate(w.Bytes())
	if err != nil || len(blocks) != 1 {
		t.Fatalf("%v %v", blocks, err)
	}
	b := blocks[0]
	if b.Type != BlockCreate2 || b.GUID != wolf || b.ObjType != 3 || !b.Move.Living || b.Move.X != 10 ||
		b.Move.Speeds[1] != 2 || b.Values[UnitHealth] != 100 || b.Values[FieldEntry] != 299 {
		t.Fatalf("%+v %+v", b, b.Move)
	}
}

// Review Focus 2: several blocks inside one compressed packet.
func TestCompressedUpdateMultiBlock(t *testing.T) {
	w := header(3)
	w.U8(uint8(BlockOutOfRange)).U32(2).PackedGUID(0xF130000000000009).PackedGUID(0x31)
	w.U8(uint8(BlockCreate)).PackedGUID(0x2A).U8(4)
	living(w, 0x01|0x20|0x10, game.MoveInfo{X: -8913, Y: -136, Z: 82}, nil)
	values(w, 1300, map[uint16]uint32{UnitHealth: 1000, UnitMaxHealth: 1000})
	w.U8(uint8(BlockValues)).PackedGUID(wolf)
	values(w, 188, map[uint16]uint32{UnitHealth: 40})
	blocks, err := ParseCompressedUpdate(compress(w.Bytes()))
	if err != nil || len(blocks) != 3 {
		t.Fatalf("%v %v", blocks, err)
	}
	if len(blocks[0].GUIDs) != 2 || blocks[1].GUID != 0x2A || blocks[1].Move.UpdateFlags&0x01 == 0 || blocks[2].Values[UnitHealth] != 40 {
		t.Fatalf("%+v", blocks)
	}
}

// Review Focus 2: a moving creature's create block carries spline data before its values.
func TestSplineEnabledLivingBlock(t *testing.T) {
	w := header(1).U8(uint8(BlockCreate)).PackedGUID(wolf).U8(3)
	living(w, 0x20, game.MoveInfo{Flags: game.MoveForward | game.MoveSplineEnabled, X: 1}, func(w *packet.Writer) {
		w.U32(splineFinalTarget).U64(0x2A)                // final target (packet_builder.cpp:166-167)
		w.U32(500).U32(2000).U32(77)                      // time passed, duration, id
		w.U32(2).F32(1).F32(1).F32(1).F32(2).F32(2).F32(2) // two nodes
		w.F32(5).F32(6).F32(7)                            // final destination
	})
	values(w, 188, map[uint16]uint32{UnitHealth: 55})
	blocks, err := ParseUpdate(w.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	s := blocks[0].Move.Spline
	if s == nil || s.FinalTarget != 0x2A || s.Duration != 2000 || len(s.Points) != 2 || s.Dest != [3]float32{5, 6, 7} || blocks[0].Values[UnitHealth] != 55 {
		t.Fatalf("%+v %+v", s, blocks[0].Values)
	}
}

func TestTruncatedCompressedUpdate(t *testing.T) {
	good := compress(header(0).Bytes())
	if _, err := ParseCompressedUpdate(good[:len(good)-3]); err == nil {
		t.Fatal("truncated zlib accepted")
	}
	lying := packet.NewWriter().U32(500).Append(good[4:]).Bytes() // declares more than it holds
	if _, err := ParseCompressedUpdate(lying); err == nil {
		t.Fatal("short inflate accepted")
	}
	if _, err := ParseCompressedUpdate(packet.NewWriter().U32(1 << 30).Bytes()); err == nil {
		t.Fatal("huge declared size accepted")
	}
}

// Review Focus 3: mask bits past every known field are read and kept, not misparsed.
func TestValuesBeyondKnownFields(t *testing.T) {
	w := header(1).U8(uint8(BlockValues)).PackedGUID(0x2A)
	values(w, 1500, map[uint16]uint32{UnitHealth: 7, 1490: 0xABCD})
	blocks, err := ParseUpdate(w.Bytes())
	if err != nil || blocks[0].Values[1490] != 0xABCD || blocks[0].Values[UnitHealth] != 7 {
		t.Fatalf("%+v %v", blocks, err)
	}
}

func TestMovementBlockAndGameObjectPosition(t *testing.T) {
	w := header(2).U8(uint8(BlockMovement)).GUID(wolf) // full guid (Object.cpp:268-269)
	living(w, 0x20, game.MoveInfo{X: 3}, nil)
	w.U8(uint8(BlockCreate)).PackedGUID(0xF110000000000042).U8(5)
	w.U8(0x40 | 0x08 | 0x10).F32(1).F32(2).F32(3).F32(4).U32(0).U32(1) // has position, high guid, all
	values(w, 26, map[uint16]uint32{FieldEntry: 1731})
	blocks, err := ParseUpdate(w.Bytes())
	if err != nil || blocks[0].GUID != wolf || blocks[0].Move.X != 3 || !blocks[1].Move.HasPosition || blocks[1].Move.Z != 3 {
		t.Fatalf("%+v %v", blocks, err)
	}
}

func TestUnknownBlockTypeFails(t *testing.T) {
	if _, err := ParseUpdate(header(1).U8(9).Bytes()); err == nil {
		t.Fatal("block type 9 accepted")
	}
}
```

`~/turtle-agent/internal/objects/moves_test.go`:

```go
package objects

import (
	"testing"

	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

func linearMove(g packet.GUID) []byte {
	return packet.NewWriter().PackedGUID(g).F32(1).F32(2).F32(3).U32(9).U8(0). // start, id, normal facing
		U32(0x100).U32(1500).U32(2).F32(11).F32(12).F32(13).U32(0x01020304).Bytes() // flags, duration, count, dest, one packed offset
}

func TestMonsterMoveLinearSmoothAndStop(t *testing.T) {
	m, err := ParseMonsterMove(linearMove(wolf))
	if err != nil || m.GUID != wolf || m.Dest != [3]float32{11, 12, 13} || m.Duration != 1500 || m.Stop {
		t.Fatalf("%+v %v", m, err)
	}
	smooth := packet.NewWriter().PackedGUID(wolf).F32(1).F32(2).F32(3).U32(9).U8(4).F32(0.5).
		U32(splineFlying).U32(800).U32(2).F32(4).F32(4).F32(4).F32(8).F32(8).F32(8).Bytes()
	if m, err := ParseMonsterMove(smooth); err != nil || m.Dest != [3]float32{8, 8, 8} {
		t.Fatalf("%+v %v", m, err)
	}
	stop := packet.NewWriter().PackedGUID(wolf).F32(5).F32(6).F32(7).U32(9).U8(1).Bytes() // MoveSplineInit.cpp:148-153
	if m, err := ParseMonsterMove(stop); err != nil || !m.Stop || m.Dest != [3]float32{5, 6, 7} {
		t.Fatalf("%+v %v", m, err)
	}
}

// Review Focus 2: SMSG_COMPRESSED_MOVES bundles several length-prefixed packets.
func TestCompressedMovesSplit(t *testing.T) {
	a := linearMove(wolf)
	b := packet.NewWriter().PackedGUID(wolf).Bytes()
	plain := packet.NewWriter().U8(uint8(len(a) + 2)).U16(0x0DD).Append(a).U8(uint8(len(b) + 2)).U16(0x304).Append(b).Bytes()
	subs, err := SplitCompressedMoves(compress(plain))
	if err != nil || len(subs) != 2 || subs[0].Op != 0x0DD || len(subs[0].Body) != len(a) || subs[1].Op != 0x304 {
		t.Fatalf("%+v %v", subs, err)
	}
	if _, err := SplitCompressedMoves(compress([]byte{1, 0xDD})); err == nil {
		t.Fatal("size below 2 accepted")
	}
}
```

The capture-driven test runs first against the milestone 1 fixture (recorded in Task 11), which holds the login burst of update objects; it is what pins the spline case above:

`~/turtle-agent/internal/objects/capture_test.go`:

```go
package objects

import (
	"path/filepath"
	"testing"

	"github.com/TortoiseBots/turtle-agent/internal/capture"
	"github.com/TortoiseBots/turtle-agent/internal/opcode"
)

func fixture(t *testing.T, name string) []capture.Record {
	t.Helper()
	recs, err := capture.ReadFile(filepath.Join("..", "..", "testdata", "captures", name))
	if err != nil {
		t.Fatalf("fixture %s: %v (record it with the milestone's live step)", name, err)
	}
	return recs
}

// Review Focus 2 and 3, pinned by captures: every update packet the server sent parses
// completely, compressed ones included, and so do the compressed moves. Task 22 adds the
// milestone 4 fixture to the list.
func TestCapturedUpdatesParse(t *testing.T) {
	compressed, blocks := 0, 0
	for _, name := range []string{"m1-login.twcap"} {
		recs := fixture(t, name)
		for _, body := range capture.Bodies(recs, opcode.SMSGUpdateObject) {
			b, err := ParseUpdate(body)
			if err != nil {
				t.Fatalf("%s SMSG_UPDATE_OBJECT: %v", name, err)
			}
			blocks += len(b)
		}
		for _, body := range capture.Bodies(recs, opcode.SMSGCompressedUpdateObject) {
			b, err := ParseCompressedUpdate(body)
			if err != nil {
				t.Fatalf("%s SMSG_COMPRESSED_UPDATE_OBJECT: %v", name, err)
			}
			compressed++
			blocks += len(b)
		}
		for _, body := range capture.Bodies(recs, opcode.SMSGCompressedMoves) {
			subs, err := SplitCompressedMoves(body)
			if err != nil {
				t.Fatalf("%s SMSG_COMPRESSED_MOVES: %v", name, err)
			}
			for _, sp := range subs {
				if sp.Op == opcode.SMSGMonsterMove {
					if _, err := ParseMonsterMove(sp.Body); err != nil {
						t.Fatalf("%s compressed monster move: %v", name, err)
					}
				}
			}
		}
		for _, body := range capture.Bodies(recs, opcode.SMSGMonsterMove) {
			if _, err := ParseMonsterMove(body); err != nil {
				t.Fatalf("%s SMSG_MONSTER_MOVE: %v", name, err)
			}
		}
	}
	if compressed == 0 || blocks < 10 {
		t.Fatalf("only %d compressed updates and %d blocks in the fixtures", compressed, blocks)
	}
}
```

- [ ] **Step 2: Run them and see them fail**

Run: `cd ~/turtle-agent && ~/.local/go/bin/go test ./internal/game/ ./internal/objects/`
Expected: FAIL, `undefined: MoveInfo` (game) and `undefined: ParseUpdate`, `undefined: UnitHealth` (objects).

- [ ] **Step 3: Implement movement info**

`~/turtle-agent/internal/game/movement.go`:

```go
package game

import "github.com/TortoiseBots/turtle-agent/internal/packet"

// Movement flags (Objects/Object.h:210-240).
const (
	MoveForward         uint32 = 0x00000001
	MoveBackward        uint32 = 0x00000002
	MoveWalkMode        uint32 = 0x00000100
	MoveRoot            uint32 = 0x00001000
	MoveJumping         uint32 = 0x00002000
	MoveFallingFar      uint32 = 0x00004000
	MoveSwimming        uint32 = 0x00200000
	MoveSplineEnabled   uint32 = 0x00400000
	MoveOnTransport     uint32 = 0x02000000
	MoveSplineElevation uint32 = 0x04000000
)

// MoveInfo is MovementInfo (Object.cpp:64-106 read, :146-180 write).
type MoveInfo struct {
	Flags                                     uint32
	Time                                      uint32
	X, Y, Z, O                                float32
	Transport                                 packet.GUID
	TX, TY, TZ, TO                            float32
	Pitch                                     float32
	FallTime                                  uint32
	JumpZSpeed, JumpCos, JumpSin, JumpXYSpeed float32
	SplineElevation                           float32
}

func ReadMoveInfo(r *packet.Reader) MoveInfo {
	var m MoveInfo
	m.Flags, m.Time = r.U32(), r.U32()
	m.X, m.Y, m.Z, m.O = r.F32(), r.F32(), r.F32(), r.F32()
	if m.Flags&MoveOnTransport != 0 {
		m.Transport = r.GUID()
		m.TX, m.TY, m.TZ, m.TO = r.F32(), r.F32(), r.F32(), r.F32()
	}
	if m.Flags&MoveSwimming != 0 {
		m.Pitch = r.F32()
	}
	m.FallTime = r.U32()
	if m.Flags&MoveJumping != 0 {
		m.JumpZSpeed, m.JumpCos, m.JumpSin, m.JumpXYSpeed = r.F32(), r.F32(), r.F32(), r.F32()
	}
	if m.Flags&MoveSplineElevation != 0 {
		m.SplineElevation = r.F32()
	}
	return m
}

func WriteMoveInfo(w *packet.Writer, m MoveInfo) {
	w.U32(m.Flags).U32(m.Time).F32(m.X).F32(m.Y).F32(m.Z).F32(m.O)
	if m.Flags&MoveOnTransport != 0 {
		w.GUID(m.Transport).F32(m.TX).F32(m.TY).F32(m.TZ).F32(m.TO)
	}
	if m.Flags&MoveSwimming != 0 {
		w.F32(m.Pitch)
	}
	w.U32(m.FallTime)
	if m.Flags&MoveJumping != 0 {
		w.F32(m.JumpZSpeed).F32(m.JumpCos).F32(m.JumpSin).F32(m.JumpXYSpeed)
	}
	if m.Flags&MoveSplineElevation != 0 {
		w.F32(m.SplineElevation)
	}
}
```

- [ ] **Step 4: Implement the update parser and the move parsers**

`~/turtle-agent/internal/objects/fields.go`:

```go
package objects

// Update-field indices (Objects/UpdateFields.h; OBJECT_END = 0x06).
const (
	FieldGUID        uint16 = 0x00 // OBJECT_FIELD_GUID, 2 words (:29)
	FieldType        uint16 = 0x02 // (:30)
	FieldEntry       uint16 = 0x03 // (:31)
	UnitTarget       uint16 = 0x10 // UNIT_FIELD_TARGET, 2 words (:77)
	UnitHealth       uint16 = 0x16 // (:80)
	UnitPower1       uint16 = 0x17 // POWER1..5 (:81-85)
	UnitMaxHealth    uint16 = 0x1C // (:86)
	UnitMaxPower1    uint16 = 0x1D // MAXPOWER1..5 (:87-91)
	UnitLevel        uint16 = 0x22 // (:92)
	UnitFaction      uint16 = 0x23 // UNIT_FIELD_FACTIONTEMPLATE (:93)
	UnitBytes0       uint16 = 0x24 // race, class, gender, power type (:94; Unit.h:455)
	UnitFlags        uint16 = 0x2E // (:97; FIELD_UNIT_FIELD_FLAGS = 46 at :345)
	UnitAura         uint16 = 0x2F // 48 slots of spell ids (:98-99)
	UnitDynamicFlags uint16 = 0x8F // (:128; FIELD_UNIT_DYNAMIC_FLAGS = 143 at :346)
	UnitNPCFlags     uint16 = 0x93 // (:132; FIELD_UNIT_NPC_FLAGS = 147 at :347)
)

// Aura slots (Spells/SpellAuraDefines.h:25-26).
const (
	AuraSlots         = 48
	PositiveAuraSlots = 32
)

// Unit flags (Objects/UnitDefines.h:402-441).
const (
	UnitFlagSpawning         uint32 = 0x00000002
	UnitFlagPlayerControlled uint32 = 0x00000008
	UnitFlagImmuneToPlayer   uint32 = 0x00000100
	UnitFlagInCombat         uint32 = 0x00080000
	UnitFlagNotSelectable    uint32 = 0x02000000
)

// Type ids (ObjectGuid.h:35-42).
const (
	TypeUnit       uint8 = 3
	TypePlayer     uint8 = 4
	TypeGameObject uint8 = 5
)
```

`~/turtle-agent/internal/objects/update.go`:

```go
// Package objects parses update-object packets and keeps the units around the player.
package objects

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"io"

	"github.com/TortoiseBots/turtle-agent/internal/game"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

// BlockType is ObjectUpdateType (UpdateData.h:33-41).
type BlockType uint8

const (
	BlockValues      BlockType = 0
	BlockMovement    BlockType = 1
	BlockCreate      BlockType = 2
	BlockCreate2     BlockType = 3
	BlockOutOfRange  BlockType = 4
	BlockNear        BlockType = 5
)

// Update flags (UpdateData.h:44-53).
const (
	updateFlagSelf           = 0x01
	updateFlagTransport      = 0x02
	updateFlagMeleeAttacking = 0x04
	updateFlagHighGUID       = 0x08
	updateFlagAll            = 0x10
	updateFlagLiving         = 0x20
	updateFlagHasPosition    = 0x40
)

// Spline flags (Movement/spline/MoveSplineFlag.h:36-70).
const (
	splineFlying      = 0x00000200 // Catmull-Rom path
	splineFinalPoint  = 0x00010000
	splineFinalTarget = 0x00020000
	splineFinalAngle  = 0x00040000
)

const (
	maxInflated = 8 << 20
	maxBlocks   = 10000
	maxNodes    = 1000
)

type Spline struct {
	Flags                    uint32
	FinalPoint               [3]float32
	FinalTarget              packet.GUID
	FinalAngle               float32
	TimePassed, Duration, ID uint32
	Points                   [][3]float32
	Dest                     [3]float32
}

type Movement struct {
	UpdateFlags uint8
	Living      bool
	Info        game.MoveInfo // living units only
	Speeds      [6]float32    // walk, run, run back, swim, swim back, turn rate
	Spline      *Spline
	HasPosition bool
	X, Y, Z, O  float32
	Victim      packet.GUID
}

type Block struct {
	Type    BlockType
	GUID    packet.GUID
	ObjType uint8 // TypeID (ObjectGuid.h:35-42), create blocks only
	Move    *Movement
	Values  map[uint16]uint32
	GUIDs   []packet.GUID // out-of-range and near-objects blocks
}

// Inflate reads a u32 uncompressed size and a zlib stream (UpdateData.cpp:121-135).
func Inflate(body []byte) ([]byte, error) {
	r := packet.NewReader(body)
	size := r.U32()
	if err := r.Err(); err != nil {
		return nil, fmt.Errorf("inflate: %w", err)
	}
	if size > maxInflated {
		return nil, fmt.Errorf("inflate: declared size %d is too large", size)
	}
	zr, err := zlib.NewReader(bytes.NewReader(body[4:]))
	if err != nil {
		return nil, fmt.Errorf("inflate: %w", err)
	}
	defer zr.Close()
	out := make([]byte, size)
	if _, err := io.ReadFull(zr, out); err != nil {
		return nil, fmt.Errorf("inflate: %w", err)
	}
	return out, nil
}

func ParseCompressedUpdate(body []byte) ([]Block, error) {
	plain, err := Inflate(body)
	if err != nil {
		return nil, fmt.Errorf("SMSG_COMPRESSED_UPDATE_OBJECT: %w", err)
	}
	return ParseUpdate(plain)
}

// ParseUpdate decodes SMSG_UPDATE_OBJECT. On error it returns the blocks parsed so far.
func ParseUpdate(body []byte) ([]Block, error) {
	r := packet.NewReader(body)
	count := r.U32()
	r.U8() // has transport
	if err := r.Err(); err != nil {
		return nil, fmt.Errorf("update header: %w", err)
	}
	if count > maxBlocks {
		return nil, fmt.Errorf("update header: %d blocks", count)
	}
	blocks := make([]Block, 0, count)
	for i := uint32(0); i < count; i++ {
		b, err := readBlock(r)
		if err != nil {
			return blocks, fmt.Errorf("update block %d of %d (type %d): %w", i+1, count, b.Type, err)
		}
		blocks = append(blocks, b)
	}
	return blocks, nil
}

func readBlock(r *packet.Reader) (Block, error) {
	b := Block{Type: BlockType(r.U8())}
	var err error
	switch b.Type {
	case BlockValues:
		b.GUID = r.PackedGUID() // Object.cpp:382-383
		b.Values, err = readValues(r)
	case BlockMovement:
		b.GUID = r.GUID() // Object.cpp:268-269
		b.Move, err = readMovement(r)
	case BlockCreate, BlockCreate2:
		b.GUID, b.ObjType = r.PackedGUID(), r.U8() // Object.cpp:298-300
		if b.Move, err = readMovement(r); err == nil {
			b.Values, err = readValues(r)
		}
	case BlockOutOfRange, BlockNear:
		n := r.U32() // UpdateData.cpp:108-114
		if n > maxBlocks {
			return b, fmt.Errorf("%d guids", n)
		}
		for j := uint32(0); j < n; j++ {
			b.GUIDs = append(b.GUIDs, r.PackedGUID())
		}
	default:
		return b, fmt.Errorf("unknown block type %d", b.Type)
	}
	if err != nil {
		return b, err
	}
	return b, r.Err()
}

// readMovement is Object::BuildMovementUpdate read back (Object.cpp:415-513).
func readMovement(r *packet.Reader) (*Movement, error) {
	m := &Movement{UpdateFlags: r.U8()}
	if m.UpdateFlags&updateFlagLiving != 0 {
		m.Living, m.HasPosition = true, true
		m.Info = game.ReadMoveInfo(r)
		m.X, m.Y, m.Z, m.O = m.Info.X, m.Info.Y, m.Info.Z, m.Info.O
		for i := range m.Speeds {
			m.Speeds[i] = r.F32()
		}
		if m.Info.Flags&game.MoveSplineEnabled != 0 {
			s, err := readSpline(r)
			if err != nil {
				return m, err
			}
			m.Spline = s
		}
	} else if m.UpdateFlags&updateFlagHasPosition != 0 {
		m.HasPosition = true
		m.X, m.Y, m.Z, m.O = r.F32(), r.F32(), r.F32(), r.F32()
	}
	if m.UpdateFlags&updateFlagHighGUID != 0 {
		r.U32()
	}
	if m.UpdateFlags&updateFlagAll != 0 {
		r.U32()
	}
	if m.UpdateFlags&updateFlagMeleeAttacking != 0 {
		m.Victim = r.PackedGUID()
	}
	if m.UpdateFlags&updateFlagTransport != 0 {
		r.U32()
	}
	return m, r.Err()
}

// readSpline is PacketBuilder::WriteCreate read back (packet_builder.cpp:154-180).
func readSpline(r *packet.Reader) (*Spline, error) {
	s := &Spline{Flags: r.U32()}
	switch {
	case s.Flags&splineFinalAngle != 0:
		s.FinalAngle = r.F32()
	case s.Flags&splineFinalTarget != 0:
		s.FinalTarget = r.GUID()
	case s.Flags&splineFinalPoint != 0:
		s.FinalPoint = [3]float32{r.F32(), r.F32(), r.F32()}
	}
	s.TimePassed, s.Duration, s.ID = r.U32(), r.U32(), r.U32()
	n := r.U32()
	if n > maxNodes {
		return s, fmt.Errorf("spline with %d nodes", n)
	}
	for i := uint32(0); i < n; i++ {
		s.Points = append(s.Points, [3]float32{r.F32(), r.F32(), r.F32()})
	}
	s.Dest = [3]float32{r.F32(), r.F32(), r.F32()}
	return s, r.Err()
}

// readValues reads the mask and the set values in index order (Object.cpp:553-556).
// Indices past the fields this client knows are kept; the store ignores them.
func readValues(r *packet.Reader) (map[uint16]uint32, error) {
	blocks := int(r.U8())
	masks := make([]uint32, blocks)
	for i := range masks {
		masks[i] = r.U32()
	}
	vals := make(map[uint16]uint32)
	for i, m := range masks {
		for bit := 0; bit < 32; bit++ {
			if m&(1<<bit) != 0 {
				vals[uint16(i*32+bit)] = r.U32()
			}
		}
	}
	return vals, r.Err()
}
```

`~/turtle-agent/internal/objects/moves.go`:

```go
package objects

import (
	"fmt"

	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

type SubPacket struct {
	Op   uint16
	Body []byte
}

// SplitCompressedMoves decodes SMSG_COMPRESSED_MOVES: inflate, then entries of u8 size
// (opcode + body), u16 opcode, body (UpdateData.cpp:188-216).
func SplitCompressedMoves(body []byte) ([]SubPacket, error) {
	plain, err := Inflate(body)
	if err != nil {
		return nil, fmt.Errorf("SMSG_COMPRESSED_MOVES: %w", err)
	}
	r := packet.NewReader(plain)
	var out []SubPacket
	for r.Remaining() > 0 {
		size := int(r.U8())
		if size < 2 {
			return out, fmt.Errorf("SMSG_COMPRESSED_MOVES: entry size %d", size)
		}
		op := r.U16()
		b := r.Bytes(size - 2)
		if err := r.Err(); err != nil {
			return out, fmt.Errorf("SMSG_COMPRESSED_MOVES: %w", err)
		}
		out = append(out, SubPacket{Op: op, Body: b})
	}
	return out, nil
}

type MonsterMove struct {
	GUID     packet.GUID
	Start    [3]float32
	ID       uint32
	Stop     bool
	Dest     [3]float32
	Duration uint32
}

func vec(r *packet.Reader) [3]float32 { return [3]float32{r.F32(), r.F32(), r.F32()} }

// ParseMonsterMove decodes SMSG_MONSTER_MOVE (MoveSplineInit.cpp:134-156, packet_builder.cpp:45-152).
func ParseMonsterMove(body []byte) (MonsterMove, error) {
	r := packet.NewReader(body)
	var m MonsterMove
	m.GUID = r.PackedGUID()
	m.Start = vec(r)
	m.ID = r.U32()
	switch r.U8() { // MonsterMoveType (packet_builder.cpp:36-43)
	case 1: // stop
		m.Stop, m.Dest = true, m.Start
		return m, r.Err()
	case 2: // facing spot
		r.Skip(12)
	case 3: // facing target
		r.Skip(8)
	case 4: // facing angle
		r.Skip(4)
	}
	flags := r.U32()
	m.Duration = r.U32()
	count := r.U32()
	if count > maxNodes {
		return m, fmt.Errorf("SMSG_MONSTER_MOVE: %d points", count)
	}
	if flags&splineFlying != 0 { // smooth path: count points, the last is the destination
		if count == 0 {
			return m, fmt.Errorf("SMSG_MONSTER_MOVE: smooth path without points")
		}
		r.Skip(int(count-1) * 12)
	}
	m.Dest = vec(r) // linear path: destination first, packed offsets follow
	if err := r.Err(); err != nil {
		return m, fmt.Errorf("SMSG_MONSTER_MOVE: %w", err)
	}
	return m, nil
}
```

- [ ] **Step 5: Run the tests**

Run: `make -C ~/turtle-agent fmt test`
Expected: PASS for `internal/game` and `internal/objects`, including `TestCapturedUpdatesParse` on the milestone 1 fixture.

- [ ] **Step 6: If the capture test fails, pin the layout from the bytes**

If `TestCapturedUpdatesParse` fails in a living block whose movement flags carry `MoveSplineEnabled`, the server sent no spline data for it (`packet_builder.cpp:158`): change `readMovement` to read the spline only when the bytes that follow form one (the failing hex shows which), add that packet's bytes as a unit test in `update_test.go`, rerun, and say so in the commit. Any other failure is a layout drift: fix the parser against the bytes and add the case to the unit tests.

- [ ] **Step 7: Commit**

```bash
cd ~/turtle-agent && git status --short
git add internal/game/movement.go internal/game/movement_test.go internal/objects
git commit -F - <<'EOF'
objects: 1.12 update-object, compressed update, compressed moves, monster move

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01MKCBbD9VJLeKqwGUyyzxyw
EOF
```

---

## Task 20: Object store and the units snapshot

**Files:**
- Create: `internal/objects/store.go`
- Test: `internal/objects/store_test.go`

**Interfaces:**
- Produces: `type ChangeKind uint8` with `ChangeCreated`, `ChangeRemoved`, `ChangeHealth`, `ChangeAura`, `ChangeCombat`; `type Change struct { GUID packet.GUID; Kind ChangeKind; Slot int; Old, New uint32 }`; `type Unit struct { GUID packet.GUID; Type string; Entry uint32; Name string; X, Y, Z, O, Distance float32; Health, MaxHealth uint32; PowerType uint8; Power, MaxPower, Level, Faction uint32; Target packet.GUID; Flags, NPCFlags uint32; InCombat, Dead, Hostile, InParty bool; Auras []uint32 }`; `type Filter struct { Near float32; Entry uint32; Hostile *bool; Name string; Limit int }`; `type Store`; `func NewStore() *Store`; `(*Store).Apply(blocks []Block) []Change`; `(*Store).Destroy(g packet.GUID) bool`; `(*Store).MoveTo(g packet.GUID, x, y, z, o float32)`; `(*Store).Clear()`; `(*Store).Get(g packet.GUID, ox, oy, oz float32) (Unit, bool)`; `(*Store).Units(f Filter, ox, oy, oz float32) []Unit`.
- Consumes: Task 19 (blocks, field constants).

`Hostile` is a heuristic because faction reactions live in DBCs the client does not load: a living creature with no NPC flags and none of spawning, player-controlled, immune-to-player or not-selectable. `Dead` is health 0 with a known max health. `Name` and `InParty` are filled by the session (Task 21); `Filter.Name` is applied there too.

- [ ] **Step 1: Write the failing test**

`~/turtle-agent/internal/objects/store_test.go`:

```go
package objects

import (
	"testing"

	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

func createBlock(g packet.GUID, typ uint8, x, y float32, vals map[uint16]uint32) Block {
	return Block{Type: BlockCreate2, GUID: g, ObjType: typ, Move: &Movement{Living: true, HasPosition: true, X: x, Y: y}, Values: vals}
}

func TestApplyCreateValuesAndUnits(t *testing.T) {
	s := NewStore()
	ch := s.Apply([]Block{
		createBlock(wolf, TypeUnit, 10, 0, map[uint16]uint32{FieldEntry: 299, UnitHealth: 100, UnitMaxHealth: 100, UnitLevel: 5, UnitAura + 33: 589}),
		createBlock(0x30, TypePlayer, 3, 4, map[uint16]uint32{UnitHealth: 500, UnitMaxHealth: 600, UnitBytes0: 1 << 24, UnitPower1 + 1: 40, UnitMaxPower1 + 1: 100}),
	})
	if len(ch) != 2 || ch[0].Kind != ChangeCreated || ch[1].Kind != ChangeCreated {
		t.Fatalf("created objects report only creation: %+v", ch)
	}
	us := s.Units(Filter{}, 0, 0, 0)
	if len(us) != 2 || us[0].GUID != 0x30 || us[0].Distance != 5 || us[0].Type != "player" || us[0].Power != 40 || us[0].PowerType != 1 {
		t.Fatalf("%+v", us)
	}
	w := us[1]
	if w.Entry != 299 || w.Type != "creature" || !w.Hostile || w.Dead || len(w.Auras) != 1 || w.Auras[0] != 589 {
		t.Fatalf("%+v", w)
	}
	ch = s.Apply([]Block{{Type: BlockValues, GUID: wolf, Values: map[uint16]uint32{UnitHealth: 0, UnitAura + 33: 0, UnitAura + 2: 1243, UnitFlags: UnitFlagInCombat}}})
	want := map[ChangeKind]Change{
		ChangeHealth: {GUID: wolf, Kind: ChangeHealth, Old: 100, New: 0},
		ChangeCombat: {GUID: wolf, Kind: ChangeCombat, Old: 0, New: UnitFlagInCombat},
	}
	auras := 0
	for _, c := range ch {
		if c.Kind == ChangeAura {
			auras++
			if (c.Slot == 33 && (c.Old != 589 || c.New != 0)) || (c.Slot == 2 && c.New != 1243) {
				t.Fatalf("aura change %+v", c)
			}
			continue
		}
		if want[c.Kind] != c {
			t.Fatalf("change %+v", c)
		}
	}
	if auras != 2 || len(ch) != 4 {
		t.Fatalf("%+v", ch)
	}
	if u, ok := s.Get(wolf, 0, 0, 0); !ok || !u.Dead || u.Hostile || !u.InCombat {
		t.Fatalf("%+v", u)
	}
}

func TestOutOfRangeDestroyAndClear(t *testing.T) {
	s := NewStore()
	s.Apply([]Block{createBlock(wolf, TypeUnit, 1, 1, nil), createBlock(0x30, TypePlayer, 2, 2, nil)})
	ch := s.Apply([]Block{{Type: BlockOutOfRange, GUIDs: []packet.GUID{wolf, 0x99}}})
	if len(ch) != 1 || ch[0].Kind != ChangeRemoved || ch[0].GUID != wolf {
		t.Fatalf("%+v", ch)
	}
	if !s.Destroy(0x30) || s.Destroy(0x30) {
		t.Fatal("destroy")
	}
	s.Apply([]Block{createBlock(wolf, TypeUnit, 1, 1, nil)})
	s.Clear()
	if len(s.Units(Filter{}, 0, 0, 0)) != 0 {
		t.Fatal("clear")
	}
}

func TestFiltersAndMoveTo(t *testing.T) {
	s := NewStore()
	s.Apply([]Block{
		createBlock(wolf, TypeUnit, 10, 0, map[uint16]uint32{FieldEntry: 299, UnitHealth: 1, UnitMaxHealth: 1}),
		createBlock(0xF130000064000002, TypeUnit, 50, 0, map[uint16]uint32{FieldEntry: 100, UnitHealth: 1, UnitMaxHealth: 1, UnitNPCFlags: 2}),
		createBlock(0xF110000000000003, TypeGameObject, 1, 0, nil),
	})
	no, yes := false, true
	if us := s.Units(Filter{Near: 20}, 0, 0, 0); len(us) != 1 || us[0].GUID != wolf {
		t.Fatalf("near: %+v", us)
	}
	if us := s.Units(Filter{Entry: 100}, 0, 0, 0); len(us) != 1 || us[0].NPCFlags != 2 {
		t.Fatalf("entry: %+v", us)
	}
	if us := s.Units(Filter{Hostile: &yes}, 0, 0, 0); len(us) != 1 || us[0].GUID != wolf {
		t.Fatalf("hostile: %+v", us)
	}
	if us := s.Units(Filter{Hostile: &no, Limit: 1}, 0, 0, 0); len(us) != 1 || us[0].Entry != 100 {
		t.Fatalf("friendly: %+v", us)
	}
	s.MoveTo(wolf, 100, 0, 0, 0)
	if u, _ := s.Get(wolf, 0, 0, 0); u.Distance != 100 {
		t.Fatalf("moved %+v", u)
	}
}

func TestValuesBeforeCreateAreKept(t *testing.T) {
	s := NewStore()
	s.Apply([]Block{{Type: BlockValues, GUID: wolf, Values: map[uint16]uint32{UnitHealth: 9, UnitMaxHealth: 10}}})
	if u, ok := s.Get(wolf, 0, 0, 0); !ok || u.Health != 9 || u.Type != "creature" {
		t.Fatalf("%+v", u)
	}
}
```

- [ ] **Step 2: Run it and see it fail**

Run: `cd ~/turtle-agent && ~/.local/go/bin/go test ./internal/objects/`
Expected: FAIL, `undefined: NewStore` (and `Filter`, `ChangeCreated`, `ChangeKind`).

- [ ] **Step 3: Implement the store**



`~/turtle-agent/internal/objects/store.go`:

```go
package objects

import (
	"math"
	"sort"
	"sync"

	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

type ChangeKind uint8

const (
	ChangeCreated ChangeKind = iota + 1
	ChangeRemoved
	ChangeHealth
	ChangeAura
	ChangeCombat
)

// Change is a state change the session turns into events. Objects seen for the first
// time report only ChangeCreated, so a login burst does not flood the log with auras.
type Change struct {
	GUID     packet.GUID
	Kind     ChangeKind
	Slot     int // aura slot, ChangeAura only
	Old, New uint32
}

type Unit struct {
	GUID      packet.GUID `json:"guid"`
	Type      string      `json:"type"` // "player" or "creature"
	Entry     uint32      `json:"entry,omitempty"`
	Name      string      `json:"name"`
	X         float32     `json:"x"`
	Y         float32     `json:"y"`
	Z         float32     `json:"z"`
	O         float32     `json:"o"`
	Distance  float32     `json:"distance"`
	Health    uint32      `json:"health"`
	MaxHealth uint32      `json:"max_health"`
	PowerType uint8       `json:"power_type"`
	Power     uint32      `json:"power"`
	MaxPower  uint32      `json:"max_power"`
	Level     uint32      `json:"level"`
	Faction   uint32      `json:"faction"`
	Target    packet.GUID `json:"target,omitempty"`
	Flags     uint32      `json:"flags"`
	NPCFlags  uint32      `json:"npc_flags"`
	InCombat  bool        `json:"in_combat"`
	Dead      bool        `json:"dead"`
	Hostile   bool        `json:"hostile"`
	InParty   bool        `json:"in_party"`
	Auras     []uint32    `json:"auras,omitempty"`
}

type Filter struct {
	Near    float32 // 0: any distance
	Entry   uint32
	Hostile *bool
	Name    string // applied by the session after names are filled in
	Limit   int    // 0: 100
}

type object struct {
	guid       packet.GUID
	typ        uint8
	values     map[uint16]uint32
	x, y, z, o float32
}

type Store struct {
	mu   sync.RWMutex
	objs map[packet.GUID]*object
}

func NewStore() *Store { return &Store{objs: map[packet.GUID]*object{}} }

func (s *Store) get(g packet.GUID) (*object, bool) {
	o, ok := s.objs[g]
	if !ok {
		o = &object{guid: g, values: map[uint16]uint32{}}
		s.objs[g] = o
	}
	return o, ok
}

// Apply applies blocks in order and returns the resulting changes.
func (s *Store) Apply(blocks []Block) []Change {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ch []Change
	for _, b := range blocks {
		switch b.Type {
		case BlockCreate, BlockCreate2:
			o, existed := s.get(b.GUID)
			if !existed {
				ch = append(ch, Change{GUID: b.GUID, Kind: ChangeCreated})
			}
			o.typ = b.ObjType
			moveObject(o, b.Move)
			ch = setValues(o, b.Values, ch, existed)
		case BlockValues:
			o, existed := s.get(b.GUID) // values before a create we missed are kept
			ch = setValues(o, b.Values, ch, existed)
		case BlockMovement:
			if o, ok := s.objs[b.GUID]; ok {
				moveObject(o, b.Move)
			}
		case BlockOutOfRange:
			for _, g := range b.GUIDs {
				if _, ok := s.objs[g]; ok {
					delete(s.objs, g)
					ch = append(ch, Change{GUID: g, Kind: ChangeRemoved})
				}
			}
		}
	}
	return ch
}

func moveObject(o *object, m *Movement) {
	if m != nil && m.HasPosition {
		o.x, o.y, o.z, o.o = m.X, m.Y, m.Z, m.O
	}
}

func setValues(o *object, vals map[uint16]uint32, ch []Change, report bool) []Change {
	idx := make([]int, 0, len(vals))
	for i := range vals {
		idx = append(idx, int(i))
	}
	sort.Ints(idx)
	for _, i := range idx {
		k, v := uint16(i), vals[uint16(i)]
		old := o.values[k]
		o.values[k] = v
		if !report || old == v {
			continue
		}
		switch {
		case k == UnitHealth:
			ch = append(ch, Change{GUID: o.guid, Kind: ChangeHealth, Old: old, New: v})
		case k >= UnitAura && k < UnitAura+AuraSlots:
			ch = append(ch, Change{GUID: o.guid, Kind: ChangeAura, Slot: int(k - UnitAura), Old: old, New: v})
		case k == UnitFlags && (old^v)&UnitFlagInCombat != 0:
			ch = append(ch, Change{GUID: o.guid, Kind: ChangeCombat, Old: old & UnitFlagInCombat, New: v & UnitFlagInCombat})
		}
	}
	return ch
}

func (s *Store) Destroy(g packet.GUID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.objs[g]
	delete(s.objs, g)
	return ok
}

func (s *Store) MoveTo(g packet.GUID, x, y, z, o float32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ob, ok := s.objs[g]; ok {
		ob.x, ob.y, ob.z, ob.o = x, y, z, o
	}
}

// Clear forgets every object (SMSG_NEW_WORLD).
func (s *Store) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objs = map[packet.GUID]*object{}
}

func (s *Store) Get(g packet.GUID, ox, oy, oz float32) (Unit, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	o, ok := s.objs[g]
	if !ok {
		return Unit{}, false
	}
	return toUnit(o, ox, oy, oz), true
}

// Units returns players and creatures matching f, nearest first.
func (s *Store) Units(f Filter, ox, oy, oz float32) []Unit {
	s.mu.RLock()
	var out []Unit
	for _, o := range s.objs {
		if !isUnit(o) {
			continue
		}
		u := toUnit(o, ox, oy, oz)
		if (f.Near > 0 && u.Distance > f.Near) || (f.Entry != 0 && u.Entry != f.Entry) || (f.Hostile != nil && u.Hostile != *f.Hostile) {
			continue
		}
		out = append(out, u)
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Distance < out[j].Distance })
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func isUnit(o *object) bool {
	switch o.typ {
	case TypeUnit, TypePlayer:
		return true
	case 0: // values seen before a create block
		return o.guid.IsPlayer() || o.guid.IsCreature()
	}
	return false
}

func toUnit(o *object, ox, oy, oz float32) Unit {
	v := o.values
	u := Unit{
		GUID: o.guid, Type: "creature", Entry: v[FieldEntry], X: o.x, Y: o.y, Z: o.z, O: o.o,
		Health: v[UnitHealth], MaxHealth: v[UnitMaxHealth], PowerType: uint8(v[UnitBytes0] >> 24),
		Level: v[UnitLevel], Faction: v[UnitFaction], Flags: v[UnitFlags], NPCFlags: v[UnitNPCFlags],
		Target: packet.GUID(uint64(v[UnitTarget]) | uint64(v[UnitTarget+1])<<32),
	}
	if o.typ == TypePlayer || o.guid.IsPlayer() {
		u.Type = "player"
	}
	if u.Entry == 0 && o.guid.IsCreature() {
		u.Entry = o.guid.Entry()
	}
	if u.PowerType < 5 {
		u.Power, u.MaxPower = v[UnitPower1+uint16(u.PowerType)], v[UnitMaxPower1+uint16(u.PowerType)]
	}
	u.InCombat = u.Flags&UnitFlagInCombat != 0
	u.Dead = u.MaxHealth > 0 && u.Health == 0
	u.Hostile = u.Type == "creature" && !u.Dead && u.NPCFlags == 0 &&
		u.Flags&(UnitFlagSpawning|UnitFlagPlayerControlled|UnitFlagImmuneToPlayer|UnitFlagNotSelectable) == 0
	for i := uint16(0); i < AuraSlots; i++ {
		if id := v[UnitAura+i]; id != 0 {
			u.Auras = append(u.Auras, id)
		}
	}
	dx, dy, dz := float64(o.x-ox), float64(o.y-oy), float64(o.z-oz)
	u.Distance = float32(math.Sqrt(dx*dx + dy*dy + dz*dz))
	return u
}
```

- [ ] **Step 4: Run the tests**

Run: `make -C ~/turtle-agent fmt test`
Expected: PASS for `internal/objects`.

- [ ] **Step 5: Commit**

```bash
cd ~/turtle-agent && git status --short
git add internal/objects
git commit -F - <<'EOF'
objects: store with health/aura/combat changes and filtered unit snapshots

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01MKCBbD9VJLeKqwGUyyzxyw
EOF
```

---

## Task 21: Session world handlers, death and aura events, `units` RPC

**Files:**
- Create: `internal/session/world.go`
- Modify: `internal/session/session.go` (`Options.Store`, struct field, `New`, `register`), `cmd/turtle-agent/methods.go` (`units`)
- Test: `internal/session/world_test.go`

**Interfaces:**
- Produces: `Options.Store *objects.Store` (nil: a new store); `StatusInfo` fields `Health`, `MaxHealth`, `Power`, `MaxPower` (uint32, the player's own values from the store); `(*Session).Units(f objects.Filter) []objects.Unit`; `(*Session).Store() *objects.Store`; internal `(*Session).applyBlocks(blocks []objects.Block)`, `(*Session).selfPos() (x, y, z, o float32)`. RPC `units` {near?, entry?, hostile?, name?, limit?} → `[]objects.Unit`.
- Consumes: Tasks 19-20, Task 13 (names), Task 17 (`inParty`).

Handlers: `SMSG_UPDATE_OBJECT`, `SMSG_COMPRESSED_UPDATE_OBJECT` (blocks parsed before an error are still applied, then the error is reported as a parse error), `SMSG_DESTROY_OBJECT` (u64 guid, `Objects/Object.cpp:410-411`), `SMSG_MONSTER_MOVE` (position := destination), `SMSG_COMPRESSED_MOVES` (each sub-packet goes through the same handler table), and the movement relays the server sends for other players: packed guid + MovementInfo (`Handlers/MovementHandler.cpp:414-418`; `MSG_MOVE_TELEPORT` to observers has the same layout, `Movement/MovementPacketSender.cpp:166-172`). New players and creatures get a name or creature query right away. Changes become events: health reaching 0 → `death` (source = the unit); an aura slot losing a spell → `aura_removed`, gaining one → `aura_applied` (target = the unit, `spell_id`, `Data["slot"]`, `Data["harmful"]` = slot ≥ 32).

- [ ] **Step 1: Write the failing test**

`~/turtle-agent/internal/session/world_test.go`:

```go
package session

import (
	"bytes"
	"compress/zlib"
	"testing"

	"github.com/TortoiseBots/turtle-agent/internal/events"
	"github.com/TortoiseBots/turtle-agent/internal/game"
	"github.com/TortoiseBots/turtle-agent/internal/objects"
	"github.com/TortoiseBots/turtle-agent/internal/opcode"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

const youngWolf = packet.GUID(0xF13000012B000001) // entry 299

// unitCreate writes one SMSG_UPDATE_OBJECT with a living create block (Object.cpp:274-308, :415-449).
func unitCreate(g packet.GUID, typ uint8, x, y, z float32, vals map[uint16]uint32) []byte {
	w := packet.NewWriter().U32(1).U8(0).U8(uint8(objects.BlockCreate2)).PackedGUID(g).U8(typ).U8(0x20)
	game.WriteMoveInfo(w, game.MoveInfo{X: x, Y: y, Z: z})
	for i := 0; i < 6; i++ {
		w.F32(1)
	}
	return appendValues(w, vals).Bytes()
}

func valuesUpdate(g packet.GUID, vals map[uint16]uint32) []byte {
	w := packet.NewWriter().U32(1).U8(0).U8(uint8(objects.BlockValues)).PackedGUID(g)
	return appendValues(w, vals).Bytes()
}

// appendValues writes a 6-block mask (192 fields) and the values in index order (Object.cpp:553-556).
func appendValues(w *packet.Writer, vals map[uint16]uint32) *packet.Writer {
	masks := make([]uint32, 6)
	for i := range vals {
		masks[i/32] |= 1 << (i % 32)
	}
	w.U8(6)
	for _, m := range masks {
		w.U32(m)
	}
	for i := uint16(0); i < 192; i++ {
		if v, ok := vals[i]; ok {
			w.U32(v)
		}
	}
	return w
}

func zlibPacked(plain []byte) []byte {
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	zw.Write(plain)
	zw.Close()
	return packet.NewWriter().U32(uint32(len(plain))).Append(z.Bytes()).Bytes()
}

func TestUnitsWithNamesAndParty(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	h.send(opcode.SMSGGroupList, groupList(me.GUID, game.GroupMember{Name: "Gorm", GUID: 0x30, Status: game.MemberOnline}))
	h.send(opcode.SMSGUpdateObject, unitCreate(youngWolf, objects.TypeUnit, me.X+10, me.Y, me.Z, map[uint16]uint32{objects.FieldEntry: 299, objects.UnitHealth: 50, objects.UnitMaxHealth: 50}))
	if p := h.expect(opcode.CMSGCreatureQuery); packet.NewReader(p.Body).U32() != 299 {
		t.Fatalf("creature query % X", p.Body)
	}
	h.send(opcode.SMSGCreatureQueryResponse, packet.NewWriter().U32(299).CString("Young Wolf").U8(0).U8(0).U8(0).CString("").
		U32(0).U32(1).U32(1).U32(0).U32(0).U32(0).U32(161).U8(0).U8(0).Bytes())
	h.send(opcode.SMSGCompressedUpdateObject, zlibPacked(unitCreate(0x30, objects.TypePlayer, me.X+3, me.Y+4, me.Z, map[uint16]uint32{objects.UnitHealth: 500, objects.UnitMaxHealth: 600})))
	waitFor(t, func() bool {
		return len(h.s.Units(objects.Filter{})) == 2 && len(h.s.Units(objects.Filter{Name: "young wolf"})) == 1
	})
	us := h.s.Units(objects.Filter{})
	if us[0].Name != "Gorm" || !us[0].InParty || us[0].Distance != 5 || us[1].Name != "Young Wolf" || !us[1].Hostile {
		t.Fatalf("%+v", us)
	}
	h.logout()
}

func TestDeathAuraAndMoves(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	h.send(opcode.SMSGCreatureQueryResponse, packet.NewWriter().U32(299).CString("Young Wolf").U8(0).U8(0).U8(0).CString("").
		U32(0).U32(1).U32(1).U32(0).U32(0).U32(0).U32(161).U8(0).U8(0).Bytes())
	h.send(opcode.SMSGUpdateObject, unitCreate(youngWolf, objects.TypeUnit, 0, 0, 0, map[uint16]uint32{objects.UnitHealth: 50, objects.UnitMaxHealth: 50}))
	h.send(opcode.SMSGUpdateObject, valuesUpdate(youngWolf, map[uint16]uint32{objects.UnitAura + 32: 589}))
	h.send(opcode.SMSGUpdateObject, valuesUpdate(youngWolf, map[uint16]uint32{objects.UnitHealth: 0, objects.UnitAura + 32: 0}))
	move := packet.NewWriter().PackedGUID(youngWolf).F32(0).F32(0).F32(0).U32(1).U8(0).U32(0x100).U32(1000).U32(1).F32(7).F32(8).F32(9).Bytes()
	h.send(opcode.SMSGCompressedMoves, zlibPacked(packet.NewWriter().U8(uint8(len(move)+2)).U16(opcode.SMSGMonsterMove).Append(move).Bytes()))
	h.send(opcode.SMSGUpdateObject, []byte{1, 0, 0, 0, 0, 9}) // unknown block type: dropped, logged
	h.logout()
	if d := ofKind(h.s.Log(), events.KindDeath); len(d) != 1 || d[0].Source != "Young Wolf" {
		t.Fatalf("death %+v", d)
	}
	a := ofKind(h.s.Log(), events.KindAuraApplied)
	r := ofKind(h.s.Log(), events.KindAuraRemoved)
	if len(a) != 1 || a[0].SpellID != 589 || a[0].Target != "Young Wolf" || a[0].Data["harmful"] != true || len(r) != 1 || r[0].Seq < a[0].Seq {
		t.Fatalf("auras %+v %+v", a, r)
	}
	if u, ok := h.s.Store().Get(youngWolf, 0, 0, 0); !ok || u.X != 7 || u.Z != 9 {
		t.Fatalf("monster move not applied: %+v", u)
	}
	if h.s.Status().ParseErrors != 1 {
		t.Fatalf("parse errors %d", h.s.Status().ParseErrors)
	}
}

func TestUnitsBeforeWorldIsEmpty(t *testing.T) {
	h := start(t, Options{})
	if us := h.s.Units(objects.Filter{}); len(us) != 0 {
		t.Fatalf("%+v", us)
	}
	h.expect(opcode.CMSGCharEnum)
}
```

- [ ] **Step 2: Run it and see it fail**

Run: `cd ~/turtle-agent && ~/.local/go/bin/go test ./internal/session/`
Expected: FAIL, `h.s.Units undefined` and `h.s.Store undefined`.

- [ ] **Step 3: Implement**

`~/turtle-agent/internal/session/world.go`:

```go
package session

import (
	"errors"
	"strings"

	"github.com/TortoiseBots/turtle-agent/internal/events"
	"github.com/TortoiseBots/turtle-agent/internal/game"
	"github.com/TortoiseBots/turtle-agent/internal/objects"
	"github.com/TortoiseBots/turtle-agent/internal/opcode"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

// moveRelays are the movement packets the server relays for other units: packed guid +
// MovementInfo (MovementHandler.cpp:414-418, MovementPacketSender.cpp:166-172).
var moveRelays = []uint16{
	opcode.MSGMoveStartForward, opcode.MSGMoveStartBackward, opcode.MSGMoveStop, opcode.MSGMoveStartStrafeLeft,
	opcode.MSGMoveStartStrafeRight, opcode.MSGMoveStopStrafe, opcode.MSGMoveJump, opcode.MSGMoveStartTurnLeft,
	opcode.MSGMoveStartTurnRight, opcode.MSGMoveStopTurn, opcode.MSGMoveSetRunMode, opcode.MSGMoveSetWalkMode,
	opcode.MSGMoveFallLand, opcode.MSGMoveStartSwim, opcode.MSGMoveStopSwim, opcode.MSGMoveSetFacing,
	opcode.MSGMoveHeartbeat, opcode.MSGMoveTeleport,
}

func (s *Session) registerWorld() {
	s.handlers[opcode.SMSGUpdateObject] = func(b []byte) error { return s.onUpdate(objects.ParseUpdate(b)) }
	s.handlers[opcode.SMSGCompressedUpdateObject] = func(b []byte) error { return s.onUpdate(objects.ParseCompressedUpdate(b)) }
	s.handlers[opcode.SMSGDestroyObject] = s.onDestroy
	s.handlers[opcode.SMSGMonsterMove] = s.onMonsterMove
	s.handlers[opcode.SMSGCompressedMoves] = s.onCompressedMoves
	for _, op := range moveRelays {
		s.handlers[op] = s.onMoveRelay
	}
}

func (s *Session) Store() *objects.Store { return s.store }

func (s *Session) selfPos() (x, y, z, o float32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pos[0], s.pos[1], s.pos[2], s.pos[3]
}

// Units snapshots the units around the player (not the player itself) with names and
// party membership filled in.
func (s *Session) Units(f objects.Filter) []objects.Unit {
	x, y, z, _ := s.selfPos()
	s.mu.Lock()
	self := s.self.GUID
	s.mu.Unlock()
	name := strings.ToLower(f.Name)
	limit := f.Limit
	f.Name, f.Limit = "", 0
	var out []objects.Unit
	for _, u := range s.store.Units(f, x, y, z) {
		if u.GUID == self {
			continue
		}
		u.Name, _ = s.names.lookup(u.GUID)
		u.InParty = s.inParty(u.GUID)
		if name != "" && !strings.Contains(strings.ToLower(u.Name), name) {
			continue
		}
		out = append(out, u)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out
}

func (s *Session) onUpdate(blocks []objects.Block, err error) error {
	s.applyBlocks(blocks)
	return err
}

func (s *Session) applyBlocks(blocks []objects.Block) {
	for _, c := range s.store.Apply(blocks) {
		switch c.Kind {
		case objects.ChangeCreated:
			if c.GUID.IsPlayer() || c.GUID.IsCreature() {
				s.nameOf(c.GUID) // warm the name cache
			}
		case objects.ChangeHealth:
			if c.Old > 0 && c.New == 0 {
				s.emit(events.Event{Kind: events.KindDeath, SourceGUID: c.GUID})
			}
		case objects.ChangeAura:
			harmful := c.Slot >= objects.PositiveAuraSlots
			if c.Old != 0 {
				s.emit(events.Event{Kind: events.KindAuraRemoved, TargetGUID: c.GUID, SpellID: c.Old,
					Data: map[string]any{"slot": c.Slot, "harmful": harmful}})
			}
			if c.New != 0 {
				s.emit(events.Event{Kind: events.KindAuraApplied, TargetGUID: c.GUID, SpellID: c.New,
					Data: map[string]any{"slot": c.Slot, "harmful": harmful}})
			}
		}
	}
}

func (s *Session) onDestroy(body []byte) error {
	r := packet.NewReader(body)
	g := r.GUID() // Object.cpp:410-411
	if err := r.Err(); err != nil {
		return err
	}
	s.store.Destroy(g)
	return nil
}

func (s *Session) onMonsterMove(body []byte) error {
	m, err := objects.ParseMonsterMove(body)
	if err != nil {
		return err
	}
	s.store.MoveTo(m.GUID, m.Dest[0], m.Dest[1], m.Dest[2], 0)
	return nil
}

func (s *Session) onCompressedMoves(body []byte) error {
	subs, err := objects.SplitCompressedMoves(body)
	var errs []error
	if err != nil {
		errs = append(errs, err)
	}
	for _, sp := range subs {
		h, ok := s.handlers[sp.Op]
		if !ok {
			s.mu.Lock()
			s.unknown[sp.Op]++
			s.mu.Unlock()
			continue
		}
		if err := h(sp.Body); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *Session) onMoveRelay(body []byte) error {
	r := packet.NewReader(body)
	g := r.PackedGUID()
	mi := game.ReadMoveInfo(r)
	if err := r.Err(); err != nil {
		return err
	}
	s.store.MoveTo(g, mi.X, mi.Y, mi.Z, mi.O)
	return nil
}
```

In `~/turtle-agent/internal/session/session.go`:

1. Add `"github.com/TortoiseBots/turtle-agent/internal/objects"` to the imports.
2. In `Options`, add `Store *objects.Store // nil: a new store`.
3. In the `Session` struct, next to `log`, add `store *objects.Store`.
4. In `New`, before building `s`, add `if opts.Store == nil { opts.Store = objects.NewStore() }`, and add `store: opts.Store,` to the struct literal.
5. In `register`, add `s.registerWorld()` after `s.registerParty()`.
6. In `StatusInfo`, after `O`, add

```go
	Health    uint32 `json:"health"`
	MaxHealth uint32 `json:"max_health"`
	Power     uint32 `json:"power"`
	MaxPower  uint32 `json:"max_power"`
```

and in `Status()` replace `return StatusInfo{` … `}` by `info := StatusInfo{` … `}` followed by

```go
	if u, ok := s.store.Get(s.self.GUID, s.pos[0], s.pos[1], s.pos[2]); ok {
		info.Health, info.MaxHealth, info.Power, info.MaxPower = u.Health, u.MaxHealth, u.Power, u.MaxPower
	}
	return info
```

In `~/turtle-agent/cmd/turtle-agent/methods.go`, add `"github.com/TortoiseBots/turtle-agent/internal/objects"` to the imports, the type

```go
type unitsParams struct {
	Near    float32 `json:"near,omitempty"`
	Entry   uint32  `json:"entry,omitempty"`
	Hostile *bool   `json:"hostile,omitempty"`
	Name    string  `json:"name,omitempty"`
	Limit   int     `json:"limit,omitempty"`
}
```

and append to `registerMethods`:

```go
	srv.Register("units", rpc.Typed(func(ctx context.Context, p unitsParams) ([]objects.Unit, error) {
		us := s.Units(objects.Filter{Near: p.Near, Entry: p.Entry, Hostile: p.Hostile, Name: p.Name, Limit: p.Limit})
		if us == nil {
			us = []objects.Unit{}
		}
		return us, nil
	}))
```

- [ ] **Step 4: Run the tests**

Run: `make -C ~/turtle-agent fmt test && make -C ~/turtle-agent build`
Expected: PASS for every package.

- [ ] **Step 5: Commit**

```bash
cd ~/turtle-agent && git status --short
git add internal/session cmd/turtle-agent/methods.go
git commit -F - <<'EOF'
session: object store wiring, monster moves, death/aura events, units RPC

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01MKCBbD9VJLeKqwGUyyzxyw
EOF
```

---

## Task 22: Milestone 4 live check (units around the player) and the world fixture

**Files:**
- Create: `testdata/captures/m4-world.twcap` (recorded)
- Modify: `internal/objects/capture_test.go` (fixture list)
- Test: `internal/objects/capture_test.go`

**Interfaces:**
- Consumes: Tasks 19-21, the `rpc` helper (Task 15).

- [ ] **Step 1: Log in near mobs with the two bots**

Run:

```bash
cd ~/turtle-agent && make build
./bin/turtle-agent login --capture captures/m4-world.twcap $ACCOUNT Harnessb 2> captures/m4.log & sleep 8
rpc gm "{\"text\":\".bot add $BOT1\"}"; rpc gm "{\"text\":\".bot add $BOT2\"}"; sleep 6
rpc invite "{\"name\":\"$BOT1\"}"; rpc invite "{\"name\":\"$BOT2\"}"; sleep 8
rpc units '{"near":60}'
rpc units '{"near":60,"hostile":true,"limit":5}'
rpc party
rpc status
```

Expected: the first `units` result lists several entries sorted by `distance` (all ≤ 60) with non-empty `name`, `health` > 0 and `max_health` > 0; BOT1 and BOT2 appear with `"type":"player"`, `"in_party":true`, and the same `health`/`max_health` that `party` shows for them (within one stats update). If Harnessb stands where hostile mobs are, the second call lists them with `"hostile":true` and a `level`; if it returns `[]`, the hostile check is covered by Task 25, which teleports to the settings `spot` first. `status` shows `"parse_errors":0`.

- [ ] **Step 2: Log out, scrub, promote**

Run: `rpc logout; sleep 2; tail -2 captures/m4.log; ./bin/turtle-agent scrub-check captures/m4-world.twcap && cp captures/m4-world.twcap testdata/captures/m4-world.twcap`
Expected: `logout: complete`, then `clean, N records`.

- [ ] **Step 3: Extend the capture-driven test to the world fixture**

In `~/turtle-agent/internal/objects/capture_test.go` (created in Task 19), change the fixture list in `TestCapturedUpdatesParse` from `[]string{"m1-login.twcap"}` to `[]string{"m1-login.twcap", "m4-world.twcap"}`.

- [ ] **Step 4: Run the tests**

Run: `make -C ~/turtle-agent fmt test`
Expected: PASS. A failure here is handled exactly as in Task 19 Step 6: fix the parser against the failing bytes, add them as a unit test in `update_test.go`, and say so in the commit.

- [ ] **Step 5: Commit**

```bash
cd ~/turtle-agent && git status --short
git add testdata/captures/m4-world.twcap internal/objects/capture_test.go
git commit -F - <<'EOF'
Milestone 4 on m910q: units with names, health and party flags; world fixture

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01MKCBbD9VJLeKqwGUyyzxyw
EOF
```

---

# Milestone 5: target, attack, cast, face, walk, teleport

## Task 23: Action codecs (selection, attack, cast, movement, server-forced moves)

**Files:**
- Create: `internal/game/actions.go`
- Test: `internal/game/actions_test.go`

**Interfaces:**
- Produces: `func BuildSetSelection(g packet.GUID) []byte`; `func BuildAttackSwing(g packet.GUID) []byte`; target flags `TargetFlagSelf`, `TargetFlagUnit`, `TargetFlagItem`, `TargetFlagSourceLocation`, `TargetFlagDestLocation`, `TargetFlagObjectUnk`, `TargetFlagPvPCorpse`, `TargetFlagObject`, `TargetFlagTradeItem`, `TargetFlagString`, `TargetFlagCorpse` (uint16); `func BuildCastSpell(spell uint32, target packet.GUID) []byte`; `func BuildMovement(m MoveInfo) []byte`; `func Facing(x0, y0, x1, y1 float32) float32`; `type TeleportAck struct { GUID packet.GUID; Counter uint32; Move MoveInfo }`; `func ParseTeleportAck(body []byte) (TeleportAck, error)`; `func BuildTeleportAck(g packet.GUID, counter, clientTime uint32) []byte`; `type NewWorld struct { Map uint32; X, Y, Z, O float32 }`; `func ParseNewWorld(body []byte) (NewWorld, error)`; `func ParseTransferPending(body []byte) (uint32, error)`; `type MovementChange struct { GUID packet.GUID; Counter uint32; Speed float32 }`; `func ParseMovementChange(body []byte, hasSpeed bool) (MovementChange, error)`; `func BuildMovementChangeAck(g packet.GUID, counter uint32, m MoveInfo, speed *float32) []byte`; `type AttackPair struct { Attacker, Victim packet.GUID }`; `func ParseAttackStart(body []byte) (AttackPair, error)`; `func ParseAttackStop(body []byte) (AttackPair, error)`.
- Consumes: Task 1, Task 19 (`MoveInfo`, `WriteMoveInfo`, `ReadMoveInfo`).

Layouts (`~/tortoise-wow/src/game`): `CMSG_SET_SELECTION` u64 guid (`Handlers/MiscHandler.cpp:488-493`); `CMSG_ATTACKSWING` u64 guid (`Handlers/CombatHandler.cpp:30-33`), `CMSG_ATTACKSTOP` empty (`:67-69`); `CMSG_CAST_SPELL` u32 spell then `SpellCastTargets::read` (`Handlers/SpellHandler.cpp:325-328`, `Spells/Spell.cpp:180-226`): u16 target mask (`Spell.h:179`), mask 0 targets the caster, the unit flag adds a packed guid; target flags at `Database/DBCEnums.h:131-148`. Client movement packets are a bare MovementInfo (`Handlers/MovementHandler.cpp:295-321`). The server's `MSG_MOVE_TELEPORT_ACK` is packed guid, u32 counter, MovementInfo (`Movement/MovementPacketSender.cpp:135-160`); the client answers u64 guid, u32 counter, u32 time (`MovementHandler.cpp:224-253`). `SMSG_TRANSFER_PENDING` is u32 map (+ two u32 on a transport) (`Objects/Player.cpp:2784-2791`); `SMSG_NEW_WORLD` is u32 map, x, y, z, o (`Player.cpp:2827-2842`); the client answers an empty `MSG_MOVE_WORLDPORT_ACK` (`MovementHandler.cpp:43-49`). Forced speed changes are packed guid, u32 counter, float (`MovementPacketSender.cpp:57-66`), root/unroot packed guid + u32 counter (`:259-276`); their acks are u64 guid, u32 counter, MovementInfo, plus the float for speeds (`MovementHandler.cpp:430-443`, `:657-668`). `SMSG_ATTACKSTART` is u64 attacker, u64 victim (`Objects/Unit.cpp:2704-2709`); `SMSG_ATTACKSTOP` packed attacker, packed victim, u32 (`Unit.cpp:2715-2723`).

- [ ] **Step 1: Write the failing test**

`~/turtle-agent/internal/game/actions_test.go`:

```go
package game

import (
	"bytes"
	"math"
	"testing"

	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

const wolfGUID = packet.GUID(0xF13000012B000001)

func TestSelectionAttackAndCastLayouts(t *testing.T) {
	if !bytes.Equal(BuildSetSelection(wolfGUID), packet.NewWriter().GUID(wolfGUID).Bytes()) ||
		!bytes.Equal(BuildAttackSwing(wolfGUID), packet.NewWriter().GUID(wolfGUID).Bytes()) {
		t.Fatal("selection or swing")
	}
	if b := BuildCastSpell(133, wolfGUID); !bytes.Equal(b, packet.NewWriter().U32(133).U16(TargetFlagUnit).PackedGUID(wolfGUID).Bytes()) {
		t.Fatalf("unit cast % X", b)
	}
	if b := BuildCastSpell(2061, 0); !bytes.Equal(b, packet.NewWriter().U32(2061).U16(TargetFlagSelf).Bytes()) {
		t.Fatalf("self cast % X", b)
	}
}

func TestFacing(t *testing.T) {
	for _, c := range []struct{ x, y, want float32 }{{1, 0, 0}, {0, 1, math.Pi / 2}, {-1, 0, math.Pi}, {0, -1, 3 * math.Pi / 2}} {
		if got := Facing(0, 0, c.x, c.y); math.Abs(float64(got-c.want)) > 1e-5 {
			t.Errorf("to (%v,%v): %v want %v", c.x, c.y, got, c.want)
		}
	}
}

func TestTeleportAndNewWorld(t *testing.T) {
	w := packet.NewWriter().PackedGUID(0x2A).U32(7)
	WriteMoveInfo(w, MoveInfo{X: 1, Y: 2, Z: 3, O: 4})
	ack, err := ParseTeleportAck(w.Bytes())
	if err != nil || ack.GUID != 0x2A || ack.Counter != 7 || ack.Move.Z != 3 {
		t.Fatalf("%+v %v", ack, err)
	}
	if b := BuildTeleportAck(0x2A, 7, 99); !bytes.Equal(b, packet.NewWriter().GUID(0x2A).U32(7).U32(99).Bytes()) {
		t.Fatalf("% X", b)
	}
	nw, err := ParseNewWorld(packet.NewWriter().U32(1).F32(10).F32(20).F32(30).F32(0.5).Bytes())
	if err != nil || nw != (NewWorld{Map: 1, X: 10, Y: 20, Z: 30, O: 0.5}) {
		t.Fatalf("%+v %v", nw, err)
	}
	if m, err := ParseTransferPending(packet.NewWriter().U32(1).Bytes()); err != nil || m != 1 {
		t.Fatalf("%v %v", m, err)
	}
}

func TestMovementChangeAndAck(t *testing.T) {
	c, err := ParseMovementChange(packet.NewWriter().PackedGUID(0x2A).U32(3).F32(14).Bytes(), true)
	if err != nil || c.Counter != 3 || c.Speed != 14 {
		t.Fatalf("%+v %v", c, err)
	}
	speed := float32(14)
	b := BuildMovementChangeAck(0x2A, 3, MoveInfo{X: 1}, &speed)
	want := packet.NewWriter().GUID(0x2A).U32(3)
	WriteMoveInfo(want, MoveInfo{X: 1})
	if !bytes.Equal(b, want.F32(14).Bytes()) {
		t.Fatalf("% X", b)
	}
	if c, err := ParseMovementChange(packet.NewWriter().PackedGUID(0x2A).U32(4).Bytes(), false); err != nil || c.Counter != 4 {
		t.Fatalf("root %+v %v", c, err)
	}
}

func TestAttackStartStop(t *testing.T) {
	a, err := ParseAttackStart(packet.NewWriter().GUID(0x2A).GUID(wolfGUID).Bytes())
	if err != nil || a != (AttackPair{0x2A, wolfGUID}) {
		t.Fatalf("%+v %v", a, err)
	}
	a, err = ParseAttackStop(packet.NewWriter().PackedGUID(wolfGUID).PackedGUID(0x2A).U32(0).Bytes())
	if err != nil || a != (AttackPair{wolfGUID, 0x2A}) {
		t.Fatalf("%+v %v", a, err)
	}
}
```

- [ ] **Step 2: Run it and see it fail**

Run: `cd ~/turtle-agent && ~/.local/go/bin/go test ./internal/game/`
Expected: FAIL, `undefined: BuildSetSelection` (and the other action codecs).

- [ ] **Step 3: Implement**

`~/turtle-agent/internal/game/actions.go`:

```go
package game

import (
	"fmt"
	"math"

	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

// BuildSetSelection is CMSG_SET_SELECTION (MiscHandler.cpp:488-493).
func BuildSetSelection(g packet.GUID) []byte { return packet.NewWriter().GUID(g).Bytes() }

// BuildAttackSwing is CMSG_ATTACKSWING (CombatHandler.cpp:30-33); CMSG_ATTACKSTOP is empty.
func BuildAttackSwing(g packet.GUID) []byte { return packet.NewWriter().GUID(g).Bytes() }

// Spell target flags (DBCEnums.h:131-148).
const (
	TargetFlagSelf           uint16 = 0x0000
	TargetFlagUnit           uint16 = 0x0002
	TargetFlagItem           uint16 = 0x0010
	TargetFlagSourceLocation uint16 = 0x0020
	TargetFlagDestLocation   uint16 = 0x0040
	TargetFlagObjectUnk      uint16 = 0x0080
	TargetFlagPvPCorpse      uint16 = 0x0200
	TargetFlagObject         uint16 = 0x0800
	TargetFlagTradeItem      uint16 = 0x1000
	TargetFlagString         uint16 = 0x2000
	TargetFlagCorpse         uint16 = 0x8000
)

// BuildCastSpell is CMSG_CAST_SPELL (SpellHandler.cpp:325-328, Spell.cpp:180-226): a zero
// target casts on the caster.
func BuildCastSpell(spell uint32, target packet.GUID) []byte {
	w := packet.NewWriter().U32(spell)
	if target == 0 {
		return w.U16(TargetFlagSelf).Bytes()
	}
	return w.U16(TargetFlagUnit).PackedGUID(target).Bytes()
}

// BuildMovement is a client MSG_MOVE_*: MovementInfo, no guid (MovementHandler.cpp:295-321).
func BuildMovement(m MoveInfo) []byte {
	w := packet.NewWriter()
	WriteMoveInfo(w, m)
	return w.Bytes()
}

// Facing is the orientation from (x0, y0) towards (x1, y1), in [0, 2π).
func Facing(x0, y0, x1, y1 float32) float32 {
	a := math.Atan2(float64(y1-y0), float64(x1-x0))
	if a < 0 {
		a += 2 * math.Pi
	}
	return float32(a)
}

type TeleportAck struct {
	GUID    packet.GUID
	Counter uint32
	Move    MoveInfo
}

// ParseTeleportAck decodes the server's MSG_MOVE_TELEPORT_ACK (MovementPacketSender.cpp:155-158).
func ParseTeleportAck(body []byte) (TeleportAck, error) {
	r := packet.NewReader(body)
	a := TeleportAck{GUID: r.PackedGUID(), Counter: r.U32()}
	a.Move = ReadMoveInfo(r)
	if err := r.Err(); err != nil {
		return TeleportAck{}, fmt.Errorf("MSG_MOVE_TELEPORT_ACK: %w", err)
	}
	return a, nil
}

// BuildTeleportAck is the client's MSG_MOVE_TELEPORT_ACK (MovementHandler.cpp:228-233).
func BuildTeleportAck(g packet.GUID, counter, clientTime uint32) []byte {
	return packet.NewWriter().GUID(g).U32(counter).U32(clientTime).Bytes()
}

type NewWorld struct {
	Map        uint32
	X, Y, Z, O float32
}

// ParseNewWorld decodes SMSG_NEW_WORLD (Player.cpp:2827-2842).
func ParseNewWorld(body []byte) (NewWorld, error) {
	r := packet.NewReader(body)
	n := NewWorld{Map: r.U32(), X: r.F32(), Y: r.F32(), Z: r.F32(), O: r.F32()}
	if err := r.Err(); err != nil {
		return NewWorld{}, fmt.Errorf("SMSG_NEW_WORLD: %w", err)
	}
	return n, nil
}

// ParseTransferPending returns the destination map (Player.cpp:2784-2791).
func ParseTransferPending(body []byte) (uint32, error) {
	r := packet.NewReader(body)
	m := r.U32()
	return m, r.Err()
}

type MovementChange struct {
	GUID    packet.GUID
	Counter uint32
	Speed   float32 // speed changes only
}

// ParseMovementChange decodes SMSG_FORCE_*_SPEED_CHANGE (hasSpeed) and SMSG_FORCE_MOVE_(UN)ROOT
// (MovementPacketSender.cpp:57-66, :259-276).
func ParseMovementChange(body []byte, hasSpeed bool) (MovementChange, error) {
	r := packet.NewReader(body)
	c := MovementChange{GUID: r.PackedGUID(), Counter: r.U32()}
	if hasSpeed {
		c.Speed = r.F32()
	}
	if err := r.Err(); err != nil {
		return MovementChange{}, fmt.Errorf("movement change: %w", err)
	}
	return c, nil
}

// BuildMovementChangeAck acknowledges a forced change: u64 guid, u32 counter, MovementInfo,
// and the new speed for speed changes (MovementHandler.cpp:430-443, :657-668).
func BuildMovementChangeAck(g packet.GUID, counter uint32, m MoveInfo, speed *float32) []byte {
	w := packet.NewWriter().GUID(g).U32(counter)
	WriteMoveInfo(w, m)
	if speed != nil {
		w.F32(*speed)
	}
	return w.Bytes()
}

type AttackPair struct {
	Attacker, Victim packet.GUID
}

// ParseAttackStart decodes SMSG_ATTACKSTART (Unit.cpp:2704-2709).
func ParseAttackStart(body []byte) (AttackPair, error) {
	r := packet.NewReader(body)
	a := AttackPair{Attacker: r.GUID(), Victim: r.GUID()}
	return a, r.Err()
}

// ParseAttackStop decodes SMSG_ATTACKSTOP (Unit.cpp:2715-2723).
func ParseAttackStop(body []byte) (AttackPair, error) {
	r := packet.NewReader(body)
	a := AttackPair{Attacker: r.PackedGUID(), Victim: r.PackedGUID()}
	r.U32()
	return a, r.Err()
}
```

- [ ] **Step 4: Run the tests**

Run: `make -C ~/turtle-agent fmt test`
Expected: PASS for `internal/game`.

- [ ] **Step 5: Commit**

```bash
cd ~/turtle-agent && git status --short
git add internal/game/actions.go internal/game/actions_test.go
git commit -F - <<'EOF'
game: selection, attack, cast, movement, teleport and forced-move codecs

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01MKCBbD9VJLeKqwGUyyzxyw
EOF
```

---

## Task 24: Session actions and their RPC methods

**Files:**
- Create: `internal/session/actions.go`
- Modify: `internal/session/session.go` (fields, `New`, `register`, `onTick`, `send`, `StatusInfo`, `Status`), `cmd/turtle-agent/methods.go` (seven methods)
- Test: `internal/session/actions_test.go`

**Interfaces:**
- Produces: `(*Session).Target(ctx context.Context, g packet.GUID, name string) (objects.Unit, error)`; `Attack(ctx) error`; `StopAttack(ctx) error`; `Cast(ctx context.Context, spell uint32, target packet.GUID) error`; `Face(ctx context.Context, g packet.GUID) error`; `WalkTo(ctx context.Context, x, y, z float32) error`; `Teleport(ctx context.Context, x, y, z float32, mapID int) error` (`mapID < 0`: current map); `StatusInfo.Target packet.GUID`; internal `(*Session).inWorld() error`, `(*Session).clock() uint32`, `(*Session).moveInfo(flags uint32) game.MoveInfo` (caller holds `s.mu`), `(*Session).tickWalk(now time.Time) error`, `(*Session).stopWalk(reason error, sendStop bool)`; const `maxWalk = 40`, `defaultRunSpeed = 7.0`. RPC: `target` {guid?, name?} → `objects.Unit`; `attack` {}; `stop_attack` {}; `cast` {spell_id, target?}; `face` {guid}; `walk_to` {x, y, z}; `teleport` {x, y, z, map?}.
- Consumes: Task 23 codecs, Tasks 13, 17, 21.

Handlers: `MSG_MOVE_TELEPORT_ACK` (answer, move, finish waiting teleports), `SMSG_TRANSFER_PENDING` (event), `SMSG_NEW_WORLD` (move, clear the store, `MSG_MOVE_WORLDPORT_ACK`, `CMSG_SET_ACTIVE_MOVER`, finish waiting teleports), the six `SMSG_FORCE_*_SPEED_CHANGE` and the two root packets (acknowledge for the player; remember the run speed), `SMSG_ATTACKSTART`/`SMSG_ATTACKSTOP` (`attack_start`/`attack_stop` events), and the five `SMSG_ATTACKSWING_*` errors (`attack_error`, `Data["reason"]` `not_in_range`, `bad_facing`, `not_standing`, `dead_target`, `cant_attack`). Walking sends `MSG_MOVE_START_FORWARD`, a `MSG_MOVE_HEARTBEAT` every 500 ms with the interpolated position, and `MSG_MOVE_STOP` at the destination; the run speed is 7 yards/s until the server sets another. Unacknowledged server changes are only resent and then resolved by the server (`Unit.cpp:7235-7300`), so the acks keep the server's pending queue short rather than avoid a kick.

- [ ] **Step 1: Write the failing test**

`~/turtle-agent/internal/session/actions_test.go`:

```go
package session

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/events"
	"github.com/TortoiseBots/turtle-agent/internal/game"
	"github.com/TortoiseBots/turtle-agent/internal/objects"
	"github.com/TortoiseBots/turtle-agent/internal/opcode"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

func wolfNearby(h *harness) {
	h.t.Helper()
	h.send(opcode.SMSGCreatureQueryResponse, packet.NewWriter().U32(299).CString("Young Wolf").U8(0).U8(0).U8(0).CString("").
		U32(0).U32(1).U32(1).U32(0).U32(0).U32(0).U32(161).U8(0).U8(0).Bytes())
	h.send(opcode.SMSGUpdateObject, unitCreate(youngWolf, objects.TypeUnit, me.X+10, me.Y, me.Z, map[uint16]uint32{objects.UnitHealth: 50, objects.UnitMaxHealth: 50}))
	waitFor(h.t, func() bool { return len(h.s.Units(objects.Filter{Name: "young wolf"})) == 1 })
}

func TestTargetAttackStopCast(t *testing.T) {
	ctx := context.Background()
	h := start(t, Options{})
	h.enterWorld()
	if err := h.s.Attack(ctx); err == nil || !strings.Contains(err.Error(), "no target") {
		t.Fatalf("attack without target: %v", err)
	}
	wolfNearby(h)
	u, err := h.s.Target(ctx, 0, "young wolf")
	if err != nil || u.GUID != youngWolf || u.Name != "Young Wolf" {
		t.Fatalf("%+v %v", u, err)
	}
	if p := h.expect(opcode.CMSGSetSelection); packet.NewReader(p.Body).GUID() != youngWolf {
		t.Fatalf("% X", p.Body)
	}
	if err := h.s.Attack(ctx); err != nil {
		t.Fatal(err)
	}
	if p := h.expect(opcode.CMSGAttackswing); packet.NewReader(p.Body).GUID() != youngWolf {
		t.Fatalf("% X", p.Body)
	}
	if err := h.s.Cast(ctx, 133, 0); err != nil {
		t.Fatal(err)
	}
	if p := h.expect(opcode.CMSGCastSpell); string(p.Body) != string(game.BuildCastSpell(133, youngWolf)) {
		t.Fatalf("% X", p.Body)
	}
	if err := h.s.StopAttack(ctx); err != nil {
		t.Fatal(err)
	}
	h.expect(opcode.CMSGAttackstop)
	if h.s.Status().Target != youngWolf {
		t.Fatal("status target")
	}
	h.send(opcode.SMSGAttackstart, packet.NewWriter().GUID(me.GUID).GUID(youngWolf).Bytes())
	h.send(opcode.SMSGAttackswingNotinrange, nil)
	h.logout()
	if a := ofKind(h.s.Log(), events.KindAttackStart); len(a) != 1 || a[0].Source != "Harnessb" || a[0].Target != "Young Wolf" {
		t.Fatalf("%+v", a)
	}
	if e := ofKind(h.s.Log(), events.KindAttackError); len(e) != 1 || e[0].Data["reason"] != "not_in_range" {
		t.Fatalf("%+v", e)
	}
}

func TestFaceAndWalk(t *testing.T) {
	ctx := context.Background()
	h := start(t, Options{})
	h.enterWorld()
	wolfNearby(h)
	if err := h.s.Face(ctx, youngWolf); err != nil {
		t.Fatal(err)
	}
	if mi := game.ReadMoveInfo(packet.NewReader(h.expect(opcode.MSGMoveSetFacing).Body)); math.Abs(float64(mi.O)) > 1e-4 || mi.X != me.X {
		t.Fatalf("%+v", mi)
	}
	if err := h.s.WalkTo(ctx, me.X+100, me.Y, me.Z); err == nil || !strings.Contains(err.Error(), "teleport") {
		t.Fatalf("long walk: %v", err)
	}
	if err := h.s.WalkTo(ctx, me.X+3.5, me.Y, me.Z); err != nil {
		t.Fatal(err)
	}
	if mi := game.ReadMoveInfo(packet.NewReader(h.expect(opcode.MSGMoveStartForward).Body)); mi.Flags != game.MoveForward {
		t.Fatalf("start %+v", mi)
	}
	if mi := game.ReadMoveInfo(packet.NewReader(h.expect(opcode.MSGMoveStop).Body)); mi.Flags != 0 || mi.X != me.X+3.5 {
		t.Fatalf("stop %+v", mi)
	}
	if st := h.s.Status(); st.X != me.X+3.5 {
		t.Fatalf("%+v", st)
	}
	h.logout()
}

func TestTeleportNearFarAndSpeed(t *testing.T) {
	ctx := context.Background()
	h := start(t, Options{})
	h.enterWorld()
	done := make(chan error, 1)
	go func() { done <- h.s.Teleport(ctx, 1, 2, 3, -1) }()
	if p := h.expect(opcode.CMSGMessagechat); !strings.Contains(string(p.Body), ".go xyz 1.00 2.00 3.00") {
		t.Fatalf("% X", p.Body)
	}
	w := packet.NewWriter().PackedGUID(me.GUID).U32(7)
	game.WriteMoveInfo(w, game.MoveInfo{X: 1, Y: 2, Z: 3})
	h.send(opcode.MSGMoveTeleportAck, w.Bytes())
	r := packet.NewReader(h.expect(opcode.MSGMoveTeleportAck).Body)
	if r.GUID() != me.GUID || r.U32() != 7 {
		t.Fatal("teleport ack")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	h.send(opcode.SMSGTransferPending, packet.NewWriter().U32(1).Bytes())
	h.send(opcode.SMSGNewWorld, packet.NewWriter().U32(1).F32(10).F32(20).F32(30).F32(0).Bytes())
	h.expect(opcode.MSGMoveWorldportAck)
	h.expect(opcode.CMSGSetActiveMover)
	h.send(opcode.SMSGForceRunSpeedChange, packet.NewWriter().PackedGUID(me.GUID).U32(3).F32(14).Bytes())
	ack := packet.NewReader(h.expect(opcode.CMSGForceRunSpeedChangeAck).Body)
	if ack.GUID() != me.GUID || ack.U32() != 3 {
		t.Fatal("speed ack header")
	}
	game.ReadMoveInfo(ack)
	if ack.F32() != 14 || ack.Remaining() != 0 {
		t.Fatal("speed ack tail")
	}
	time.Sleep(50 * time.Millisecond)
	if st := h.s.Status(); st.Map != 1 || st.X != 10 {
		t.Fatalf("%+v", st)
	}
	h.logout()
}
```

- [ ] **Step 2: Run it and see it fail**

Run: `cd ~/turtle-agent && ~/.local/go/bin/go test ./internal/session/`
Expected: FAIL, `h.s.Attack undefined` (and `Target`, `Cast`, `StopAttack`, `Face`, `WalkTo`, `Teleport`, `StatusInfo.Target`).

- [ ] **Step 3: Implement**

`~/turtle-agent/internal/session/actions.go`:

```go
package session

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/events"
	"github.com/TortoiseBots/turtle-agent/internal/game"
	"github.com/TortoiseBots/turtle-agent/internal/objects"
	"github.com/TortoiseBots/turtle-agent/internal/opcode"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

const (
	maxWalk         = 40  // yards; longer moves use Teleport
	defaultRunSpeed = 7.0 // yards per second until the server sets another
)

type walkState struct {
	from, to [3]float32
	o        float32
	start    time.Time
	dur      time.Duration
	nextBeat time.Time
	done     chan error
}

// speedAcks maps each forced speed change to its ack (Opcodes_1_12_1.h:229-238, :733-738).
var speedAcks = map[uint16]uint16{
	opcode.SMSGForceRunSpeedChange:      opcode.CMSGForceRunSpeedChangeAck,
	opcode.SMSGForceRunBackSpeedChange:  opcode.CMSGForceRunBackSpeedChangeAck,
	opcode.SMSGForceSwimSpeedChange:     opcode.CMSGForceSwimSpeedChangeAck,
	opcode.SMSGForceWalkSpeedChange:     opcode.CMSGForceWalkSpeedChangeAck,
	opcode.SMSGForceSwimBackSpeedChange: opcode.CMSGForceSwimBackSpeedChangeAck,
	opcode.SMSGForceTurnRateChange:      opcode.CMSGForceTurnRateChangeAck,
}

var swingErrors = map[uint16]string{
	opcode.SMSGAttackswingNotinrange:  "not_in_range",
	opcode.SMSGAttackswingBadfacing:   "bad_facing",
	opcode.SMSGAttackswingNotstanding: "not_standing",
	opcode.SMSGAttackswingDeadtarget:  "dead_target",
	opcode.SMSGAttackswingCantAttack:  "cant_attack",
}

func (s *Session) registerActions() {
	s.handlers[opcode.MSGMoveTeleportAck] = s.onTeleportAck
	s.handlers[opcode.SMSGTransferPending] = s.onTransferPending
	s.handlers[opcode.SMSGNewWorld] = s.onNewWorld
	for op, ack := range speedAcks {
		s.handlers[op] = s.speedHandler(op, ack)
	}
	s.handlers[opcode.SMSGForceMoveRoot] = s.rootHandler(opcode.CMSGForceMoveRootAck, game.MoveRoot)
	s.handlers[opcode.SMSGForceMoveUnroot] = s.rootHandler(opcode.CMSGForceMoveUnrootAck, 0)
	s.handlers[opcode.SMSGAttackstart] = s.onAttackStart
	s.handlers[opcode.SMSGAttackstop] = s.onAttackStop
	for op, reason := range swingErrors {
		reason := reason
		s.handlers[op] = func([]byte) error {
			s.emit(events.Event{Kind: events.KindAttackError, Text: "attack swing failed: " + reason, Data: map[string]any{"reason": reason}})
			return nil
		}
	}
}

func (s *Session) inWorld() error {
	if st := s.State(); st != StatusInWorld {
		return fmt.Errorf("%w (state %s)", ErrNotInWorld, st)
	}
	return nil
}

func (s *Session) clock() uint32 { return uint32(time.Since(s.start).Milliseconds()) }

// moveInfo is the player's current MovementInfo; the caller holds s.mu.
func (s *Session) moveInfo(flags uint32) game.MoveInfo {
	return game.MoveInfo{Flags: flags, Time: s.clock(), X: s.pos[0], Y: s.pos[1], Z: s.pos[2], O: s.pos[3]}
}

func (s *Session) currentFlags() uint32 {
	if s.walk != nil {
		return game.MoveForward
	}
	return 0
}

// Target selects a unit by guid, or by name (nearest match), and returns it.
func (s *Session) Target(ctx context.Context, g packet.GUID, name string) (objects.Unit, error) {
	if g == 0 {
		if name == "" {
			return objects.Unit{}, errors.New("target needs a guid or a name")
		}
		us := s.Units(objects.Filter{Name: name, Limit: 1})
		if len(us) == 0 {
			return objects.Unit{}, fmt.Errorf("no unit named %q nearby", name)
		}
		g = us[0].GUID
	}
	_, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		if err := s.conn.WritePacket(opcode.CMSGSetSelection, game.BuildSetSelection(g)); err != nil {
			return nil, err
		}
		s.mu.Lock()
		s.target = g
		s.mu.Unlock()
		return nil, nil
	})
	if err != nil {
		return objects.Unit{}, err
	}
	x, y, z, _ := s.selfPos()
	u, _ := s.store.Get(g, x, y, z)
	u.GUID = g
	u.Name, _ = s.names.lookup(g)
	u.InParty = s.inParty(g)
	return u, nil
}

func (s *Session) Attack(ctx context.Context) error {
	_, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		s.mu.Lock()
		t := s.target
		s.mu.Unlock()
		if t == 0 {
			return nil, errors.New("no target selected; call target first")
		}
		return nil, s.conn.WritePacket(opcode.CMSGAttackswing, game.BuildAttackSwing(t))
	})
	return err
}

func (s *Session) StopAttack(ctx context.Context) error { return s.send(ctx, opcode.CMSGAttackstop, nil) }

// Cast casts spell on target, or on the current target when target is 0, or on the
// player when nothing is selected.
func (s *Session) Cast(ctx context.Context, spell uint32, target packet.GUID) error {
	if spell == 0 {
		return errors.New("cast needs a spell id")
	}
	_, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		t := target
		if t == 0 {
			s.mu.Lock()
			t = s.target
			s.mu.Unlock()
		}
		return nil, s.conn.WritePacket(opcode.CMSGCastSpell, game.BuildCastSpell(spell, t))
	})
	return err
}

// Face turns the player towards a known unit (MSG_MOVE_SET_FACING).
func (s *Session) Face(ctx context.Context, g packet.GUID) error {
	x, y, z, _ := s.selfPos()
	u, ok := s.store.Get(g, x, y, z)
	if !ok {
		return fmt.Errorf("unit %s is not in view", g)
	}
	o := game.Facing(x, y, u.X, u.Y)
	_, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		if s.walk != nil {
			return nil, errors.New("walking; wait for walk_to to finish")
		}
		s.mu.Lock()
		s.pos[3] = o
		mi := s.moveInfo(0)
		s.mu.Unlock()
		return nil, s.conn.WritePacket(opcode.MSGMoveSetFacing, game.BuildMovement(mi))
	})
	return err
}

// WalkTo walks in a straight line to a point at most maxWalk yards away and returns on arrival.
func (s *Session) WalkTo(ctx context.Context, x, y, z float32) error {
	v, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		if s.walk != nil {
			return nil, errors.New("already walking")
		}
		s.mu.Lock()
		from := [3]float32{s.pos[0], s.pos[1], s.pos[2]}
		s.mu.Unlock()
		dx, dy, dz := float64(x-from[0]), float64(y-from[1]), float64(z-from[2])
		dist := math.Sqrt(dx*dx + dy*dy + dz*dz)
		if dist > maxWalk {
			return nil, fmt.Errorf("walk_to is for short moves: %.1f yards is over %d, use teleport", dist, maxWalk)
		}
		done := make(chan error, 1)
		if dist < 0.1 {
			done <- nil
			return done, nil
		}
		now := time.Now()
		w := &walkState{from: from, to: [3]float32{x, y, z}, o: game.Facing(from[0], from[1], x, y), start: now,
			dur: time.Duration(dist / float64(s.runSpeed) * float64(time.Second)), nextBeat: now.Add(500 * time.Millisecond), done: done}
		s.mu.Lock()
		s.pos[3] = w.o
		mi := s.moveInfo(game.MoveForward)
		s.mu.Unlock()
		if err := s.conn.WritePacket(opcode.MSGMoveStartForward, game.BuildMovement(mi)); err != nil {
			return nil, err
		}
		s.walk = w
		return done, nil
	})
	if err != nil {
		return err
	}
	select {
	case err := <-v.(chan error):
		return err
	case <-ctx.Done():
		_, _ = s.do(context.Background(), func() (any, error) { s.stopWalk(errors.New("walk cancelled"), true); return nil, nil })
		return ctx.Err()
	case <-s.done:
		return ErrClosed
	}
}

// tickWalk advances a walk: heartbeat every 500 ms, stop at the destination.
func (s *Session) tickWalk(now time.Time) error {
	w := s.walk
	if w == nil {
		return nil
	}
	frac := float32(1)
	if w.dur > 0 {
		frac = float32(now.Sub(w.start)) / float32(w.dur)
	}
	if frac >= 1 {
		s.mu.Lock()
		s.pos = [4]float32{w.to[0], w.to[1], w.to[2], w.o}
		mi := s.moveInfo(0)
		s.mu.Unlock()
		s.walk = nil
		err := s.conn.WritePacket(opcode.MSGMoveStop, game.BuildMovement(mi))
		w.done <- err
		return err
	}
	if now.Before(w.nextBeat) {
		return nil
	}
	w.nextBeat = now.Add(500 * time.Millisecond)
	s.mu.Lock()
	for i := 0; i < 3; i++ {
		s.pos[i] = w.from[i] + (w.to[i]-w.from[i])*frac
	}
	mi := s.moveInfo(game.MoveForward)
	s.mu.Unlock()
	return s.conn.WritePacket(opcode.MSGMoveHeartbeat, game.BuildMovement(mi))
}

// stopWalk ends a walk early; sendStop is false when the server moved the player.
func (s *Session) stopWalk(reason error, sendStop bool) {
	w := s.walk
	if w == nil {
		return
	}
	s.walk = nil
	if sendStop {
		s.mu.Lock()
		mi := s.moveInfo(0)
		s.mu.Unlock()
		_ = s.conn.WritePacket(opcode.MSGMoveStop, game.BuildMovement(mi))
	}
	w.done <- reason
}

// Teleport sends ".go xyz x y z [map]" (Commands.cpp:8347-8367) and waits up to 10 s for
// the server's teleport (MSG_MOVE_TELEPORT_ACK or SMSG_NEW_WORLD).
func (s *Session) Teleport(ctx context.Context, x, y, z float32, mapID int) error {
	cmd := fmt.Sprintf(".go xyz %.2f %.2f %.2f", x, y, z)
	if mapID >= 0 {
		cmd += fmt.Sprintf(" %d", mapID)
	}
	body, err := game.BuildMessageChat(game.ChatSay, "", cmd)
	if err != nil {
		return err
	}
	v, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		ch := make(chan struct{})
		s.teleportWaiters = append(s.teleportWaiters, ch)
		return ch, s.conn.WritePacket(opcode.CMSGMessagechat, body)
	})
	if err != nil {
		return err
	}
	tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	select {
	case <-v.(chan struct{}):
		return nil
	case <-tctx.Done():
		return fmt.Errorf("teleport: no MSG_MOVE_TELEPORT_ACK or SMSG_NEW_WORLD within 10 s (does the account have GM rights?): %w", tctx.Err())
	case <-s.done:
		return ErrClosed
	}
}

func (s *Session) teleported() {
	for _, ch := range s.teleportWaiters {
		close(ch)
	}
	s.teleportWaiters = nil
}

func (s *Session) selfGUID() packet.GUID {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.self.GUID
}

func (s *Session) onTeleportAck(body []byte) error {
	a, err := game.ParseTeleportAck(body)
	if err != nil {
		return err
	}
	self := s.selfGUID()
	if a.GUID != self {
		return nil
	}
	s.stopWalk(errors.New("teleported by the server"), false)
	s.mu.Lock()
	s.pos = [4]float32{a.Move.X, a.Move.Y, a.Move.Z, a.Move.O}
	s.mu.Unlock()
	s.store.MoveTo(self, a.Move.X, a.Move.Y, a.Move.Z, a.Move.O)
	if err := s.conn.WritePacket(opcode.MSGMoveTeleportAck, game.BuildTeleportAck(self, a.Counter, s.clock())); err != nil {
		return &fatalError{err}
	}
	s.emit(events.Event{Kind: events.KindSession, Data: map[string]any{"state": "teleported", "x": a.Move.X, "y": a.Move.Y, "z": a.Move.Z}})
	s.teleported()
	return nil
}

func (s *Session) onTransferPending(body []byte) error {
	m, err := game.ParseTransferPending(body)
	if err != nil {
		return err
	}
	s.emit(events.Event{Kind: events.KindSession, Data: map[string]any{"state": "transfer_pending", "map": m}})
	return nil
}

func (s *Session) onNewWorld(body []byte) error {
	nw, err := game.ParseNewWorld(body)
	if err != nil {
		return err
	}
	s.stopWalk(errors.New("teleported to another map"), false)
	s.mu.Lock()
	s.mapID, s.pos = nw.Map, [4]float32{nw.X, nw.Y, nw.Z, nw.O}
	self := s.self.GUID
	s.mu.Unlock()
	s.store.Clear()
	if err := s.conn.WritePacket(opcode.MSGMoveWorldportAck, nil); err != nil { // MovementHandler.cpp:43-49
		return &fatalError{err}
	}
	if err := s.conn.WritePacket(opcode.CMSGSetActiveMover, packet.NewWriter().GUID(self).Bytes()); err != nil {
		return &fatalError{err}
	}
	s.emit(events.Event{Kind: events.KindSession, Data: map[string]any{"state": "new_world", "map": nw.Map, "x": nw.X, "y": nw.Y, "z": nw.Z}})
	s.teleported()
	return nil
}

func (s *Session) speedHandler(op, ack uint16) handler {
	return func(body []byte) error {
		c, err := game.ParseMovementChange(body, true)
		if err != nil {
			return err
		}
		self := s.selfGUID()
		if c.GUID != self {
			return nil
		}
		if op == opcode.SMSGForceRunSpeedChange && c.Speed > 0 {
			s.runSpeed = c.Speed
		}
		s.mu.Lock()
		mi := s.moveInfo(s.currentFlags())
		s.mu.Unlock()
		speed := c.Speed
		return s.conn.WritePacket(ack, game.BuildMovementChangeAck(self, c.Counter, mi, &speed))
	}
}

func (s *Session) rootHandler(ack uint16, flags uint32) handler {
	return func(body []byte) error {
		c, err := game.ParseMovementChange(body, false)
		if err != nil {
			return err
		}
		self := s.selfGUID()
		if c.GUID != self {
			return nil
		}
		s.mu.Lock()
		mi := s.moveInfo(flags)
		s.mu.Unlock()
		return s.conn.WritePacket(ack, game.BuildMovementChangeAck(self, c.Counter, mi, nil))
	}
}

func (s *Session) onAttackStart(body []byte) error {
	a, err := game.ParseAttackStart(body)
	if err != nil {
		return err
	}
	s.emit(events.Event{Kind: events.KindAttackStart, SourceGUID: a.Attacker, TargetGUID: a.Victim})
	return nil
}

func (s *Session) onAttackStop(body []byte) error {
	a, err := game.ParseAttackStop(body)
	if err != nil {
		return err
	}
	s.emit(events.Event{Kind: events.KindAttackStop, SourceGUID: a.Attacker, TargetGUID: a.Victim})
	return nil
}
```

In `~/turtle-agent/internal/session/session.go`:

1. In the `Session` struct, among the `mu`-guarded fields add `target packet.GUID`; under `// owner-loop only` add `walk *walkState`, `runSpeed float32` and `teleportWaiters []chan struct{}`.
2. In `New`, add `runSpeed: defaultRunSpeed,` to the struct literal.
3. In `register`, add `s.registerActions()` after `s.registerWorld()`.
4. In `onTick`, after `s.flushNames(now)`, add:

```go
	if err := s.tickWalk(now); err != nil {
		return err
	}
```

5. In `StatusInfo`, after `O`, add the field

```go
	Target packet.GUID `json:"target,omitempty"`
```

and in `Status()` add `Target: s.target,` to the `StatusInfo{…}` literal.
6. Replace the body of `send` so it uses the new helper:

```go
func (s *Session) send(ctx context.Context, op uint16, body []byte) error {
	_, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		return nil, s.conn.WritePacket(op, body)
	})
	return err
}
```

In `~/turtle-agent/cmd/turtle-agent/methods.go`, add `"github.com/TortoiseBots/turtle-agent/internal/packet"` to the imports, the types

```go
type targetParams struct {
	GUID packet.GUID `json:"guid,omitempty"`
	Name string      `json:"name,omitempty"`
}

type castParams struct {
	SpellID uint32      `json:"spell_id"`
	Target  packet.GUID `json:"target,omitempty"`
}

type guidParams struct {
	GUID packet.GUID `json:"guid"`
}

type pointParams struct {
	X   float32 `json:"x"`
	Y   float32 `json:"y"`
	Z   float32 `json:"z"`
	Map *int    `json:"map,omitempty"`
}
```

and append to `registerMethods`:

```go
	srv.Register("target", rpc.Typed(func(ctx context.Context, p targetParams) (objects.Unit, error) {
		u, err := s.Target(ctx, p.GUID, p.Name)
		return u, rpcErr(err)
	}))
	srv.Register("attack", rpc.Typed(func(ctx context.Context, _ empty) (empty, error) { return empty{}, rpcErr(s.Attack(ctx)) }))
	srv.Register("stop_attack", rpc.Typed(func(ctx context.Context, _ empty) (empty, error) { return empty{}, rpcErr(s.StopAttack(ctx)) }))
	srv.Register("cast", rpc.Typed(func(ctx context.Context, p castParams) (empty, error) {
		return empty{}, rpcErr(s.Cast(ctx, p.SpellID, p.Target))
	}))
	srv.Register("face", rpc.Typed(func(ctx context.Context, p guidParams) (empty, error) { return empty{}, rpcErr(s.Face(ctx, p.GUID)) }))
	srv.Register("walk_to", rpc.Typed(func(ctx context.Context, p pointParams) (empty, error) {
		return empty{}, rpcErr(s.WalkTo(ctx, p.X, p.Y, p.Z))
	}))
	srv.Register("teleport", rpc.Typed(func(ctx context.Context, p pointParams) (empty, error) {
		m := -1
		if p.Map != nil {
			m = *p.Map
		}
		return empty{}, rpcErr(s.Teleport(ctx, p.X, p.Y, p.Z, m))
	}))
```

- [ ] **Step 4: Run the tests**

Run: `make -C ~/turtle-agent fmt test && make -C ~/turtle-agent build`
Expected: PASS for every package.

- [ ] **Step 5: Commit**

```bash
cd ~/turtle-agent && git status --short
git add internal/session cmd/turtle-agent/methods.go
git commit -F - <<'EOF'
session: target, attack, cast, face, short walks, GM teleport with acks

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01MKCBbD9VJLeKqwGUyyzxyw
EOF
```

---

## Task 25: Milestone 5 live check (teleport, target, attack, cast, walk) and the attack fixture

**Files:**
- Create: `testdata/captures/m5-attack.twcap` (recorded); add `TestM5ActionsFromCapture` to `internal/game/capture_test.go`
- Test: `internal/game/capture_test.go`

**Interfaces:**
- Consumes: Tasks 23-24, the `rpc` helper and the settings variables (Task 15).

- [ ] **Step 1: Start, bring the bots, teleport to the spot**

Run:

```bash
cd ~/turtle-agent && make build
./bin/turtle-agent login --capture captures/m5-attack.twcap $ACCOUNT Harnessb 2> captures/m5.log & sleep 8
rpc gm '{"text":".gm off"}'
rpc gm "{\"text\":\".bot add $BOT1\"}"; rpc gm "{\"text\":\".bot add $BOT2\"}"; sleep 6
rpc invite "{\"name\":\"$BOT1\"}"; rpc invite "{\"name\":\"$BOT2\"}"; sleep 6
rpc teleport "$SPOT"
rpc status
rpc units '{"near":40,"hostile":true,"limit":3}'
```

Expected: `teleport` answers `{"result":{}}` within 10 s; `status` shows `map`, `x`, `y`, `z` within 2 yards of the settings `spot`; `units` lists at least one hostile creature with a `name`, `health` > 0 and its `guid`. Write that name and guid down as `MOB` and `MOBGUID`.

- [ ] **Step 2: Target, face, walk closer, attack, cast**

Run (replace `MOB`/`MOBGUID` with the values from Step 1; `MX`, `MY`, `MZ` are that unit's `x`, `y`, `z`):

```bash
rpc target '{"name":"MOB"}'
rpc face '{"guid":"MOBGUID"}'
rpc walk_to '{"x":MX,"y":MY,"z":MZ}'
rpc status
rpc attack
sleep 4
rpc events '{"since":0,"kinds":["attack_start","attack_error","session"]}'
rpc units '{"name":"MOB","limit":1}'
rpc cast "{\"spell_id\":$SPELL}"
sleep 3
rpc stop_attack
```

Expected: `target` returns the unit with `"name":"MOB"`; `walk_to` answers `{}` after about distance/7 seconds and the following `status` has `x`/`y` within 1 yard of `MX`/`MY` with no `teleported` session event after the walk (the server did not snap the player back); the events page holds an `attack_start` with `"source":"Harnessb"` and `"target":"MOB"` (an `attack_error` with `not_in_range` means the walk did not get close enough: walk again and repeat `attack`); the second `units` call shows MOB's `health` below its value in Step 1 (or `"dead":true`). The `cast` call answers `{}`; whether the spell landed shows up in milestone 6's events (Task 30) and in the capture as `SMSG_SPELL_START`/`SMSG_SPELL_GO` or `SMSG_CAST_RESULT`.

- [ ] **Step 3: Log out, scrub, promote**

Run: `rpc logout; sleep 2; ./bin/turtle-agent capture-stats captures/m5-attack.twcap | grep -E 'ATTACK|SPELL|CAST|TELEPORT|NEW_WORLD'; ./bin/turtle-agent scrub-check captures/m5-attack.twcap && cp captures/m5-attack.twcap testdata/captures/m5-attack.twcap`
Expected: the stats list `SMSG_ATTACKSTART`, `SMSG_ATTACKERSTATEUPDATE`, `CMSG_ATTACKSWING`, `CMSG_CAST_SPELL` and `MSG_MOVE_TELEPORT_ACK` or `SMSG_NEW_WORLD`; scrub-check prints `clean, N records`.

- [ ] **Step 4: Add the capture-driven test**

Append to `~/turtle-agent/internal/game/capture_test.go`:

```go
// Milestone 5: the attack and teleport packets the server sent parse with the source layouts.
func TestM5ActionsFromCapture(t *testing.T) {
	recs := fixture(t, "m5-attack.twcap")
	starts := capture.Bodies(recs, opcode.SMSGAttackstart)
	if len(starts) == 0 {
		t.Fatal("no SMSG_ATTACKSTART captured")
	}
	for _, b := range starts {
		if a, err := ParseAttackStart(b); err != nil || a.Attacker == 0 || a.Victim == 0 {
			t.Fatalf("% X: %+v %v", b, a, err)
		}
	}
	for _, b := range capture.Bodies(recs, opcode.SMSGAttackstop) {
		if _, err := ParseAttackStop(b); err != nil {
			t.Fatalf("% X: %v", b, err)
		}
	}
	teleports := 0
	for _, b := range capture.Bodies(recs, opcode.MSGMoveTeleportAck) {
		if _, err := ParseTeleportAck(b); err != nil {
			t.Fatalf("% X: %v", b, err)
		}
		teleports++
	}
	for _, b := range capture.Bodies(recs, opcode.SMSGNewWorld) {
		if _, err := ParseNewWorld(b); err != nil {
			t.Fatalf("% X: %v", b, err)
		}
		teleports++
	}
	if teleports == 0 {
		t.Fatal("no teleport captured")
	}
}
```

- [ ] **Step 5: Run the tests**

Run: `make -C ~/turtle-agent fmt test`
Expected: PASS, including `TestM5ActionsFromCapture`.

- [ ] **Step 6: Commit**

```bash
cd ~/turtle-agent && git status --short
git add testdata/captures/m5-attack.twcap internal/game/capture_test.go
git commit -F - <<'EOF'
Milestone 5 on m910q: teleport, target, walk, attack and cast; attack fixture

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01MKCBbD9VJLeKqwGUyyzxyw
EOF
```

---

# Milestone 6: combat events for the party and `fight_report` through Python

## Task 26: Combat-log codecs

**Files:**
- Create: `internal/game/combat.go`, `internal/game/gametest/server.go`
- Test: `internal/game/combat_test.go` (package `game_test`)

**Interfaces:**
- Produces (package `game`): `HitInfoMiss`, `HitInfoAbsorb`, `HitInfoResist`, `HitInfoCritical`, `HitInfoGlancing`, `HitInfoCrushing` (uint32); `func VictimState(v uint32) string`; `type SubDamage struct { School uint32; Coef float32; Damage, Absorb uint32; Resist int32 }`; `type MeleeHit struct { HitInfo uint32; Attacker, Victim packet.GUID; Total uint32; Subs []SubDamage; VictimState, SpellID, Blocked uint32 }` with `Crit() bool`, `Miss() bool`, `Outcome() string`; `func ParseAttackerStateUpdate(body []byte) (MeleeHit, error)`; `const SpellHitCrit uint32 = 0x02`; `type SpellDamage struct { Target, Attacker packet.GUID; SpellID, Damage uint32; School uint8; Absorb uint32; Resist int32; Periodic bool; Blocked, HitInfo uint32 }` with `Crit() bool`; `func ParseSpellNonMeleeDamageLog(body []byte) (SpellDamage, error)`; aura types `AuraPeriodicDamage`, `AuraPeriodicHeal`, `AuraObsModHealth`, `AuraObsModMana`, `AuraPeriodicEnergize`, `AuraPeriodicManaLeech`, `AuraPeriodicDamagePercent` (uint32); `type PeriodicTick struct { AuraType, Amount, School, Absorb uint32; Resist int32; PowerType uint32; Multiplier float32 }` with `Kind() string` (`damage`, `heal`, `energize`, `leech`); `type PeriodicLog struct { Target, Caster packet.GUID; SpellID uint32; Ticks []PeriodicTick }`; `func ParsePeriodicAuraLog(body []byte) (PeriodicLog, error)`; `type Heal struct { Target, Caster packet.GUID; SpellID, Amount uint32; Crit bool }`; `func ParseSpellHealLog(body []byte) (Heal, error)`; `type Energize struct { Target, Caster packet.GUID; SpellID, Power, Amount uint32 }`; `func ParseSpellEnergizeLog(body []byte) (Energize, error)`; `type SpellTargets struct { Mask uint16; Unit, Item packet.GUID; Src, Dst [3]float32; Str string }`; `const CastFlagAmmo uint16 = 0x0020`; `type SpellStart struct { Item, Caster packet.GUID; SpellID uint32; Flags uint16; CastTime uint32; Targets SpellTargets }`; `func ParseSpellStart(body []byte) (SpellStart, error)`; `type SpellMiss struct { Target packet.GUID; Reason uint8 }`; `func SpellMissName(code uint8) string`; `type SpellGo struct { Item, Caster packet.GUID; SpellID uint32; Flags uint16; Hits []packet.GUID; Misses []SpellMiss; Targets SpellTargets }`; `func ParseSpellGo(body []byte) (SpellGo, error)`; `type CastResult struct { SpellID uint32; Failed bool; Reason uint8 }`; `func ParseCastResult(body []byte) (CastResult, error)`; `type CastFailedOther struct { Caster packet.GUID; SpellID uint32 }`; `func ParseSpellFailedOther(body []byte) (CastFailedOther, error)`; `type SpellLogMiss struct { SpellID uint32; Caster packet.GUID; Misses []SpellMiss }`; `func ParseSpellLogMiss(body []byte) (SpellLogMiss, error)`; `type Kill struct { Killer, Victim packet.GUID }`; `func ParsePartyKillLog(body []byte) (Kill, error)`.
- Produces (package `gametest`, tests only): `AttackerStateUpdate`, `SpellNonMeleeDamage`, `PeriodicDamage`, `PeriodicHeal`, `PeriodicEnergize`, `SpellHeal`, `SpellEnergize`, `SpellStart`, `SpellGo`, `CastResult`, `SpellFailedOther`, `SpellLogMiss`, `PartyKill` (signatures in Step 3).
- Consumes: Task 1, Task 23 (target flags).

Layouts (`~/tortoise-wow/src/game`): `SMSG_ATTACKERSTATEUPDATE` (`Objects/Unit.cpp:4888-4921`: u32 hit info, packed attacker, packed victim, u32 total, u8 sub count, per sub u32 school, float coefficient, u32 damage, u32 absorb, i32 resist; then u32 victim state, u32 0, u32 spell id, u32 blocked), hit info and victim states at `Objects/UnitDefines.h:175-205`; `SMSG_SPELLNONMELEEDAMAGELOG` (`Objects/Object.cpp:4319-4335`: packed target, packed attacker, u32 spell, u32 damage, u8 school, u32 absorb, i32 resist, u8 periodic, u8 0, u32 blocked, u32 hit info, u8 0), crit flag `SPELL_HIT_TYPE_CRIT = 0x02` (`Spells/SpellDefines.h:112`); `SMSG_PERIODICAURALOG` (`Objects/Unit.cpp:4703-4741`: packed target, packed caster, u32 spell, u32 count = 1, then per tick u32 aura type and its fields; no other aura types are sent), aura type values at `Spells/SpellAuraDefines.h:54-227`; `SMSG_SPELLHEALLOG` (`Object.cpp:4260-4270`: packed victim, packed caster, u32 spell, u32 amount, u8 crit); `SMSG_SPELLENERGIZELOG` (`Object.cpp:4307-4316`: packed victim, packed caster, u32 spell, u32 power, u32 amount); `SMSG_SPELL_START` (`Spells/Spell.cpp:4612-4647`: packed item-or-caster, packed caster, u32 spell, u16 cast flags, u32 cast time, targets, ammo u32 display + u32 inventory type when `CAST_FLAG_AMMO`); `SMSG_SPELL_GO` (`Spell.cpp:4649-4688`, `:4758-4809`: packed item-or-caster, packed caster, u32 spell, u16 flags, u8 hit count + u64 guids, u8 miss count + (u64 guid, u8 reason, u8 reflect result when reason is `SPELL_MISS_REFLECT` 11), targets, ammo); targets are `SpellCastTargets::write` (`Spell.cpp:228-273`: u16 mask, packed unit/object/corpse guid, packed item guid, source and destination 3 floats each, string); cast flags at `Spells/Spell.h:60-71`; miss reasons at `Spells/SpellDefines.h:93-107`; `SMSG_CAST_RESULT` (`Spell.cpp:4581-4611`: u32 spell, u8 status 0 or 2, u8 reason when failed, then reason-specific extras); `SMSG_SPELL_FAILED_OTHER` (`Spell.cpp:4926-4934`: u64 caster, u32 spell); `SMSG_SPELLLOGMISS` (`Object.cpp:4127-4140`, `Spell.cpp:4952-4964`: u32 spell, u64 caster, u8 0, u32 count, count × (u64 target, u8 reason)); `SMSG_PARTYKILLLOG` (`Objects/Unit.cpp:1127-1129`: u64 killer, u64 victim).

- [ ] **Step 1: Write the failing test**

`~/turtle-agent/internal/game/combat_test.go`:

```go
package game_test

import (
	"testing"

	"github.com/TortoiseBots/turtle-agent/internal/game"
	"github.com/TortoiseBots/turtle-agent/internal/game/gametest"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

const (
	gorm = packet.GUID(0x30)
	wolf = packet.GUID(0xF13000012B000001)
)

func TestMelee(t *testing.T) {
	m, err := game.ParseAttackerStateUpdate(gametest.AttackerStateUpdate(game.HitInfoCritical|0x02, gorm, wolf, 24, 0, 3, 0, 1, 0))
	if err != nil || m.Attacker != gorm || m.Victim != wolf || m.Total != 24 || !m.Crit() || m.Miss() || len(m.Subs) != 1 || m.Subs[0].Absorb != 3 || m.Outcome() != "normal" {
		t.Fatalf("%+v %v", m, err)
	}
	m, _ = game.ParseAttackerStateUpdate(gametest.AttackerStateUpdate(game.HitInfoMiss, gorm, wolf, 0, 0, 0, 0, 0, 0))
	if !m.Miss() || m.Outcome() != "miss" {
		t.Fatalf("%+v", m)
	}
	m, _ = game.ParseAttackerStateUpdate(gametest.AttackerStateUpdate(0, wolf, gorm, 0, 0, 0, 0, 2, 0))
	if m.Outcome() != "dodge" {
		t.Fatalf("%+v", m)
	}
}

func TestSpellDamageHealEnergize(t *testing.T) {
	d, err := game.ParseSpellNonMeleeDamageLog(gametest.SpellNonMeleeDamage(wolf, gorm, 133, 36, 2, 0, 0, false, 0, game.SpellHitCrit))
	if err != nil || d.SpellID != 133 || d.Damage != 36 || d.School != 2 || !d.Crit() || d.Target != wolf {
		t.Fatalf("%+v %v", d, err)
	}
	h, err := game.ParseSpellHealLog(gametest.SpellHeal(gorm, gorm, 2061, 48, true))
	if err != nil || h.Amount != 48 || !h.Crit || h.Caster != gorm {
		t.Fatalf("%+v %v", h, err)
	}
	e, err := game.ParseSpellEnergizeLog(gametest.SpellEnergize(gorm, gorm, 29166, 0, 400))
	if err != nil || e.Amount != 400 || e.Power != 0 {
		t.Fatalf("%+v %v", e, err)
	}
}

func TestPeriodicKinds(t *testing.T) {
	for _, c := range []struct {
		body []byte
		kind string
		amt  uint32
	}{
		{gametest.PeriodicDamage(wolf, gorm, 589, game.AuraPeriodicDamage, 5, 5, 0, 0), "damage", 5},
		{gametest.PeriodicDamage(wolf, gorm, 589, game.AuraPeriodicDamagePercent, 7, 0, 0, 0), "damage", 7},
		{gametest.PeriodicHeal(gorm, gorm, 139, game.AuraPeriodicHeal, 20), "heal", 20},
		{gametest.PeriodicEnergize(gorm, gorm, 25, game.AuraPeriodicEnergize, 0, 30), "energize", 30},
	} {
		p, err := game.ParsePeriodicAuraLog(c.body)
		if err != nil || len(p.Ticks) != 1 || p.Ticks[0].Kind() != c.kind || p.Ticks[0].Amount != c.amt {
			t.Fatalf("%s: %+v %v", c.kind, p, err)
		}
	}
	bad := packet.NewWriter().PackedGUID(wolf).PackedGUID(gorm).U32(1).U32(1).U32(99).Bytes()
	if _, err := game.ParsePeriodicAuraLog(bad); err == nil {
		t.Fatal("unknown aura type accepted")
	}
}

func TestSpellStartGoAndMisses(t *testing.T) {
	s, err := game.ParseSpellStart(gametest.SpellStart(gorm, 133, 3000, wolf))
	if err != nil || s.Caster != gorm || s.SpellID != 133 || s.CastTime != 3000 || s.Targets.Unit != wolf {
		t.Fatalf("%+v %v", s, err)
	}
	g, err := game.ParseSpellGo(gametest.SpellGo(gorm, 133, []packet.GUID{wolf}, []packet.GUID{0xF130000000000099}, []uint8{game.SpellMissReflect}, wolf))
	if err != nil || len(g.Hits) != 1 || g.Hits[0] != wolf || len(g.Misses) != 1 || g.Misses[0].Reason != 11 || g.Targets.Unit != wolf {
		t.Fatalf("%+v %v", g, err)
	}
	m, err := game.ParseSpellLogMiss(gametest.SpellLogMiss(133, gorm, wolf, 2))
	if err != nil || m.Caster != gorm || len(m.Misses) != 1 || game.SpellMissName(m.Misses[0].Reason) != "resist" {
		t.Fatalf("%+v %v", m, err)
	}
}

func TestCastResultFailedOtherAndKill(t *testing.T) {
	r, err := game.ParseCastResult(gametest.CastResult(133, -1))
	if err != nil || r.Failed {
		t.Fatalf("%+v %v", r, err)
	}
	r, err = game.ParseCastResult(gametest.CastResult(133, 0x0B))
	if err != nil || !r.Failed || r.Reason != 0x0B {
		t.Fatalf("%+v %v", r, err)
	}
	o, err := game.ParseSpellFailedOther(gametest.SpellFailedOther(gorm, 133))
	if err != nil || o.Caster != gorm || o.SpellID != 133 {
		t.Fatalf("%+v %v", o, err)
	}
	k, err := game.ParsePartyKillLog(gametest.PartyKill(gorm, wolf))
	if err != nil || k != (game.Kill{Killer: gorm, Victim: wolf}) {
		t.Fatalf("%+v %v", k, err)
	}
}
```

- [ ] **Step 2: Run it and see it fail**

Run: `cd ~/turtle-agent && ~/.local/go/bin/go test ./internal/game/...`
Expected: FAIL, `package .../internal/game/gametest is not in std` / `no non-test Go files`, and `undefined: game.ParseAttackerStateUpdate`.

- [ ] **Step 3: Implement the test builders**

`~/turtle-agent/internal/game/gametest/server.go`:

```go
// Package gametest writes combat packets the way the core does, for tests in game and
// session. It imports only packet, so any test package can use it.
package gametest

import "github.com/TortoiseBots/turtle-agent/internal/packet"

// AttackerStateUpdate is Unit::SendAttackStateUpdate with one sub-damage (Unit.cpp:4892-4920).
func AttackerStateUpdate(hitInfo uint32, attacker, victim packet.GUID, total, school, absorb uint32, resist int32, victimState, blocked uint32) []byte {
	w := packet.NewWriter().U32(hitInfo).PackedGUID(attacker).PackedGUID(victim).U32(total).U8(1)
	coef := float32(0)
	if total != 0 {
		coef = 1
	}
	w.U32(school).F32(coef).U32(total).U32(absorb).I32(resist)
	return w.U32(victimState).U32(0).U32(0).U32(blocked).Bytes()
}

// SpellNonMeleeDamage is WorldObject::SendSpellNonMeleeDamageLog (Object.cpp:4319-4335).
func SpellNonMeleeDamage(target, attacker packet.GUID, spell, damage uint32, school uint8, absorb uint32, resist int32, periodic bool, blocked, hitInfo uint32) []byte {
	p := uint8(0)
	if periodic {
		p = 1
	}
	return packet.NewWriter().PackedGUID(target).PackedGUID(attacker).U32(spell).U32(damage).U8(school).U32(absorb).
		I32(resist).U8(p).U8(0).U32(blocked).U32(hitInfo).U8(0).Bytes()
}

func periodicHead(target, caster packet.GUID, spell, auraType uint32) *packet.Writer {
	return packet.NewWriter().PackedGUID(target).PackedGUID(caster).U32(spell).U32(1).U32(auraType) // Unit.cpp:4709-4714
}

// PeriodicDamage is the damage branch of SendPeriodicAuraLog (Unit.cpp:4716-4722).
func PeriodicDamage(target, caster packet.GUID, spell, auraType, amount, school, absorb uint32, resist int32) []byte {
	return periodicHead(target, caster, spell, auraType).U32(amount).U32(school).U32(absorb).I32(resist).Bytes()
}

// PeriodicHeal is the heal branch (Unit.cpp:4723-4726).
func PeriodicHeal(target, caster packet.GUID, spell, auraType, amount uint32) []byte {
	return periodicHead(target, caster, spell, auraType).U32(amount).Bytes()
}

// PeriodicEnergize is the energize branch (Unit.cpp:4727-4731).
func PeriodicEnergize(target, caster packet.GUID, spell, auraType, power, amount uint32) []byte {
	return periodicHead(target, caster, spell, auraType).U32(power).U32(amount).Bytes()
}

// SpellHeal is WorldObject::SendHealSpellLog (Object.cpp:4260-4270).
func SpellHeal(target, caster packet.GUID, spell, amount uint32, crit bool) []byte {
	c := uint8(0)
	if crit {
		c = 1
	}
	return packet.NewWriter().PackedGUID(target).PackedGUID(caster).U32(spell).U32(amount).U8(c).Bytes()
}

// SpellEnergize is WorldObject::SendEnergizeSpellLog (Object.cpp:4307-4316).
func SpellEnergize(target, caster packet.GUID, spell, power, amount uint32) []byte {
	return packet.NewWriter().PackedGUID(target).PackedGUID(caster).U32(spell).U32(power).U32(amount).Bytes()
}

// unitTargets is SpellCastTargets::write for a unit target (Spell.cpp:228-273).
func unitTargets(w *packet.Writer, target packet.GUID) {
	if target == 0 {
		w.U16(0)
		return
	}
	w.U16(0x0002).PackedGUID(target)
}

// SpellStart is Spell::SendSpellStart (Spell.cpp:4612-4647), cast flags CAST_FLAG_UNKNOWN2.
func SpellStart(caster packet.GUID, spell, castTime uint32, target packet.GUID) []byte {
	w := packet.NewWriter().PackedGUID(caster).PackedGUID(caster).U32(spell).U16(0x0002).U32(castTime)
	unitTargets(w, target)
	return w.Bytes()
}

// SpellGo is Spell::SendSpellGo (Spell.cpp:4649-4688, :4758-4809), cast flags CAST_FLAG_UNKNOWN9.
func SpellGo(caster packet.GUID, spell uint32, hits, missGUIDs []packet.GUID, missCodes []uint8, target packet.GUID) []byte {
	w := packet.NewWriter().PackedGUID(caster).PackedGUID(caster).U32(spell).U16(0x0100).U8(uint8(len(hits)))
	for _, h := range hits {
		w.GUID(h)
	}
	w.U8(uint8(len(missGUIDs)))
	for i, m := range missGUIDs {
		w.GUID(m).U8(missCodes[i])
		if missCodes[i] == 11 { // SPELL_MISS_REFLECT carries the reflect result
			w.U8(0)
		}
	}
	unitTargets(w, target)
	return w.Bytes()
}

// CastResult is Spell::SendCastResult (Spell.cpp:4581-4611); failReason < 0 means success.
func CastResult(spell uint32, failReason int) []byte {
	w := packet.NewWriter().U32(spell)
	if failReason < 0 {
		return w.U8(0).Bytes()
	}
	return w.U8(2).U8(uint8(failReason)).Bytes()
}

// SpellFailedOther is Spell::SendInterrupted's broadcast (Spell.cpp:4926-4934).
func SpellFailedOther(caster packet.GUID, spell uint32) []byte {
	return packet.NewWriter().GUID(caster).U32(spell).Bytes()
}

// SpellLogMiss is WorldObject::SendSpellMiss (Object.cpp:4127-4140).
func SpellLogMiss(spell uint32, caster, target packet.GUID, miss uint8) []byte {
	return packet.NewWriter().U32(spell).GUID(caster).U8(0).U32(1).GUID(target).U8(miss).Bytes()
}

// PartyKill is SMSG_PARTYKILLLOG (Unit.cpp:1127-1129).
func PartyKill(killer, victim packet.GUID) []byte {
	return packet.NewWriter().GUID(killer).GUID(victim).Bytes()
}
```

- [ ] **Step 4: Implement the parsers**

`~/turtle-agent/internal/game/combat.go`:

```go
package game

import (
	"fmt"

	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

// Hit info flags (UnitDefines.h:188-205).
const (
	HitInfoMiss     uint32 = 0x00000010
	HitInfoAbsorb   uint32 = 0x00000020
	HitInfoResist   uint32 = 0x00000040
	HitInfoCritical uint32 = 0x00000080
	HitInfoGlancing uint32 = 0x00004000
	HitInfoCrushing uint32 = 0x00008000
)

var victimStates = []string{"unaffected", "normal", "dodge", "parry", "interrupt", "block", "evade", "immune", "deflect"} // UnitDefines.h:175-186

func VictimState(v uint32) string {
	if int(v) < len(victimStates) {
		return victimStates[v]
	}
	return fmt.Sprintf("state_%d", v)
}

type SubDamage struct {
	School         uint32
	Coef           float32
	Damage, Absorb uint32
	Resist         int32
}

type MeleeHit struct {
	HitInfo          uint32
	Attacker, Victim packet.GUID
	Total            uint32
	Subs             []SubDamage
	VictimState      uint32
	SpellID          uint32
	Blocked          uint32
}

func (m MeleeHit) Crit() bool { return m.HitInfo&HitInfoCritical != 0 }
func (m MeleeHit) Miss() bool { return m.HitInfo&HitInfoMiss != 0 }

// Outcome is "miss", or the victim state ("normal", "dodge", "parry", "block", …).
func (m MeleeHit) Outcome() string {
	if m.Miss() {
		return "miss"
	}
	return VictimState(m.VictimState)
}

// ParseAttackerStateUpdate decodes SMSG_ATTACKERSTATEUPDATE (Unit.cpp:4888-4921).
func ParseAttackerStateUpdate(body []byte) (MeleeHit, error) {
	r := packet.NewReader(body)
	m := MeleeHit{HitInfo: r.U32(), Attacker: r.PackedGUID(), Victim: r.PackedGUID(), Total: r.U32()}
	n := int(r.U8())
	for i := 0; i < n; i++ {
		m.Subs = append(m.Subs, SubDamage{School: r.U32(), Coef: r.F32(), Damage: r.U32(), Absorb: r.U32(), Resist: r.I32()})
	}
	m.VictimState = r.U32()
	r.U32()
	m.SpellID, m.Blocked = r.U32(), r.U32()
	if err := r.Err(); err != nil {
		return MeleeHit{}, fmt.Errorf("SMSG_ATTACKERSTATEUPDATE: %w", err)
	}
	return m, nil
}

// SpellHitCrit is SPELL_HIT_TYPE_CRIT (SpellDefines.h:112).
const SpellHitCrit uint32 = 0x02

type SpellDamage struct {
	Target, Attacker packet.GUID
	SpellID, Damage  uint32
	School           uint8
	Absorb           uint32
	Resist           int32
	Periodic         bool
	Blocked, HitInfo uint32
}

func (d SpellDamage) Crit() bool { return d.HitInfo&SpellHitCrit != 0 }

// ParseSpellNonMeleeDamageLog decodes SMSG_SPELLNONMELEEDAMAGELOG (Object.cpp:4319-4335).
func ParseSpellNonMeleeDamageLog(body []byte) (SpellDamage, error) {
	r := packet.NewReader(body)
	d := SpellDamage{Target: r.PackedGUID(), Attacker: r.PackedGUID(), SpellID: r.U32(), Damage: r.U32(), School: r.U8(),
		Absorb: r.U32(), Resist: r.I32(), Periodic: r.U8() != 0}
	r.U8()
	d.Blocked, d.HitInfo = r.U32(), r.U32()
	r.U8()
	if err := r.Err(); err != nil {
		return SpellDamage{}, fmt.Errorf("SMSG_SPELLNONMELEEDAMAGELOG: %w", err)
	}
	return d, nil
}

// Aura types sent in SMSG_PERIODICAURALOG (SpellAuraDefines.h:54-227).
const (
	AuraPeriodicDamage        uint32 = 3
	AuraPeriodicHeal          uint32 = 8
	AuraObsModHealth          uint32 = 20
	AuraObsModMana            uint32 = 21
	AuraPeriodicEnergize      uint32 = 24
	AuraPeriodicManaLeech     uint32 = 64
	AuraPeriodicDamagePercent uint32 = 89
)

type PeriodicTick struct {
	AuraType, Amount, School, Absorb uint32
	Resist                           int32
	PowerType                        uint32
	Multiplier                       float32
}

func (t PeriodicTick) Kind() string {
	switch t.AuraType {
	case AuraPeriodicDamage, AuraPeriodicDamagePercent:
		return "damage"
	case AuraPeriodicHeal, AuraObsModHealth:
		return "heal"
	case AuraObsModMana, AuraPeriodicEnergize:
		return "energize"
	}
	return "leech"
}

type PeriodicLog struct {
	Target, Caster packet.GUID
	SpellID        uint32
	Ticks          []PeriodicTick
}

// ParsePeriodicAuraLog decodes SMSG_PERIODICAURALOG (Unit.cpp:4703-4741).
func ParsePeriodicAuraLog(body []byte) (PeriodicLog, error) {
	r := packet.NewReader(body)
	p := PeriodicLog{Target: r.PackedGUID(), Caster: r.PackedGUID(), SpellID: r.U32()}
	n := r.U32()
	if n > 16 {
		return PeriodicLog{}, fmt.Errorf("SMSG_PERIODICAURALOG: %d ticks", n)
	}
	for i := uint32(0); i < n; i++ {
		t := PeriodicTick{AuraType: r.U32()}
		switch t.AuraType {
		case AuraPeriodicDamage, AuraPeriodicDamagePercent:
			t.Amount, t.School, t.Absorb, t.Resist = r.U32(), r.U32(), r.U32(), r.I32()
		case AuraPeriodicHeal, AuraObsModHealth:
			t.Amount = r.U32()
		case AuraObsModMana, AuraPeriodicEnergize:
			t.PowerType, t.Amount = r.U32(), r.U32()
		case AuraPeriodicManaLeech:
			t.PowerType, t.Amount, t.Multiplier = r.U32(), r.U32(), r.F32()
		default:
			return PeriodicLog{}, fmt.Errorf("SMSG_PERIODICAURALOG: aura type %d is never sent (Unit.cpp:4737-4739)", t.AuraType)
		}
		p.Ticks = append(p.Ticks, t)
	}
	if err := r.Err(); err != nil {
		return PeriodicLog{}, fmt.Errorf("SMSG_PERIODICAURALOG: %w", err)
	}
	return p, nil
}

type Heal struct {
	Target, Caster  packet.GUID
	SpellID, Amount uint32
	Crit            bool
}

// ParseSpellHealLog decodes SMSG_SPELLHEALLOG (Object.cpp:4260-4270).
func ParseSpellHealLog(body []byte) (Heal, error) {
	r := packet.NewReader(body)
	h := Heal{Target: r.PackedGUID(), Caster: r.PackedGUID(), SpellID: r.U32(), Amount: r.U32(), Crit: r.U8() != 0}
	if err := r.Err(); err != nil {
		return Heal{}, fmt.Errorf("SMSG_SPELLHEALLOG: %w", err)
	}
	return h, nil
}

type Energize struct {
	Target, Caster         packet.GUID
	SpellID, Power, Amount uint32
}

// ParseSpellEnergizeLog decodes SMSG_SPELLENERGIZELOG (Object.cpp:4307-4316).
func ParseSpellEnergizeLog(body []byte) (Energize, error) {
	r := packet.NewReader(body)
	e := Energize{Target: r.PackedGUID(), Caster: r.PackedGUID(), SpellID: r.U32(), Power: r.U32(), Amount: r.U32()}
	if err := r.Err(); err != nil {
		return Energize{}, fmt.Errorf("SMSG_SPELLENERGIZELOG: %w", err)
	}
	return e, nil
}

type SpellTargets struct {
	Mask       uint16
	Unit, Item packet.GUID
	Src, Dst   [3]float32
	Str        string
}

// readSpellTargets is SpellCastTargets::write read back (Spell.cpp:228-273).
func readSpellTargets(r *packet.Reader) SpellTargets {
	t := SpellTargets{Mask: r.U16()}
	if t.Mask&(TargetFlagUnit|TargetFlagPvPCorpse|TargetFlagObject|TargetFlagCorpse) != 0 {
		t.Unit = r.PackedGUID()
	}
	if t.Mask&(TargetFlagItem|TargetFlagTradeItem) != 0 {
		t.Item = r.PackedGUID()
	}
	if t.Mask&TargetFlagSourceLocation != 0 {
		t.Src = [3]float32{r.F32(), r.F32(), r.F32()}
	}
	if t.Mask&TargetFlagDestLocation != 0 {
		t.Dst = [3]float32{r.F32(), r.F32(), r.F32()}
	}
	if t.Mask&TargetFlagString != 0 {
		t.Str = r.CString()
	}
	return t
}

// CastFlagAmmo adds two u32 (ammo display, inventory type) after the targets (Spell.h:60-71).
const CastFlagAmmo uint16 = 0x0020

type SpellStart struct {
	Item, Caster packet.GUID
	SpellID      uint32
	Flags        uint16
	CastTime     uint32
	Targets      SpellTargets
}

// ParseSpellStart decodes SMSG_SPELL_START (Spell.cpp:4612-4647).
func ParseSpellStart(body []byte) (SpellStart, error) {
	r := packet.NewReader(body)
	s := SpellStart{Item: r.PackedGUID(), Caster: r.PackedGUID(), SpellID: r.U32(), Flags: r.U16(), CastTime: r.U32()}
	s.Targets = readSpellTargets(r)
	if s.Flags&CastFlagAmmo != 0 {
		r.U32()
		r.U32()
	}
	if err := r.Err(); err != nil {
		return SpellStart{}, fmt.Errorf("SMSG_SPELL_START: %w", err)
	}
	return s, nil
}

// Spell miss reasons (SpellDefines.h:93-107).
const SpellMissReflect uint8 = 11

var spellMissNames = []string{"none", "miss", "resist", "dodge", "parry", "block", "evade", "immune", "immune", "deflect", "absorb", "reflect"}

func SpellMissName(code uint8) string {
	if int(code) < len(spellMissNames) {
		return spellMissNames[code]
	}
	return fmt.Sprintf("miss_%d", code)
}

type SpellMiss struct {
	Target packet.GUID
	Reason uint8
}

type SpellGo struct {
	Item, Caster packet.GUID
	SpellID      uint32
	Flags        uint16
	Hits         []packet.GUID
	Misses       []SpellMiss
	Targets      SpellTargets
}

// ParseSpellGo decodes SMSG_SPELL_GO (Spell.cpp:4649-4688, :4758-4809).
func ParseSpellGo(body []byte) (SpellGo, error) {
	r := packet.NewReader(body)
	g := SpellGo{Item: r.PackedGUID(), Caster: r.PackedGUID(), SpellID: r.U32(), Flags: r.U16()}
	for n := int(r.U8()); n > 0; n-- {
		g.Hits = append(g.Hits, r.GUID())
	}
	for n := int(r.U8()); n > 0; n-- {
		m := SpellMiss{Target: r.GUID(), Reason: r.U8()}
		if m.Reason == SpellMissReflect {
			r.U8()
		}
		g.Misses = append(g.Misses, m)
	}
	g.Targets = readSpellTargets(r)
	if g.Flags&CastFlagAmmo != 0 {
		r.U32()
		r.U32()
	}
	if err := r.Err(); err != nil {
		return SpellGo{}, fmt.Errorf("SMSG_SPELL_GO: %w", err)
	}
	return g, nil
}

type CastResult struct {
	SpellID uint32
	Failed  bool
	Reason  uint8 // SpellCastResult
}

// ParseCastResult decodes SMSG_CAST_RESULT (Spell.cpp:4581-4611); reason-specific extras are skipped.
func ParseCastResult(body []byte) (CastResult, error) {
	r := packet.NewReader(body)
	c := CastResult{SpellID: r.U32()}
	if r.U8() != 0 {
		c.Failed, c.Reason = true, r.U8()
	}
	if err := r.Err(); err != nil {
		return CastResult{}, fmt.Errorf("SMSG_CAST_RESULT: %w", err)
	}
	return c, nil
}

type CastFailedOther struct {
	Caster  packet.GUID
	SpellID uint32
}

// ParseSpellFailedOther decodes SMSG_SPELL_FAILED_OTHER (Spell.cpp:4926-4934).
func ParseSpellFailedOther(body []byte) (CastFailedOther, error) {
	r := packet.NewReader(body)
	c := CastFailedOther{Caster: r.GUID(), SpellID: r.U32()}
	return c, r.Err()
}

type SpellLogMiss struct {
	SpellID uint32
	Caster  packet.GUID
	Misses  []SpellMiss
}

// ParseSpellLogMiss decodes SMSG_SPELLLOGMISS (Object.cpp:4127-4140, Spell.cpp:4952-4964).
func ParseSpellLogMiss(body []byte) (SpellLogMiss, error) {
	r := packet.NewReader(body)
	m := SpellLogMiss{SpellID: r.U32(), Caster: r.GUID()}
	extra := r.U8() != 0 // two floats per target when set; the core never sets it
	n := r.U32()
	if n > 64 {
		return SpellLogMiss{}, fmt.Errorf("SMSG_SPELLLOGMISS: %d targets", n)
	}
	for i := uint32(0); i < n; i++ {
		m.Misses = append(m.Misses, SpellMiss{Target: r.GUID(), Reason: r.U8()})
		if extra {
			r.Skip(8)
		}
	}
	if err := r.Err(); err != nil {
		return SpellLogMiss{}, fmt.Errorf("SMSG_SPELLLOGMISS: %w", err)
	}
	return m, nil
}

type Kill struct {
	Killer, Victim packet.GUID
}

// ParsePartyKillLog decodes SMSG_PARTYKILLLOG (Unit.cpp:1127-1129).
func ParsePartyKillLog(body []byte) (Kill, error) {
	r := packet.NewReader(body)
	k := Kill{Killer: r.GUID(), Victim: r.GUID()}
	return k, r.Err()
}
```

- [ ] **Step 5: Run the tests**

Run: `make -C ~/turtle-agent fmt test`
Expected: PASS for `internal/game` and `internal/game/gametest`.

- [ ] **Step 6: Commit**

```bash
cd ~/turtle-agent && git status --short
git add internal/game/combat.go internal/game/combat_test.go internal/game/gametest
git commit -F - <<'EOF'
game: combat-log parsers for every 1.12 combat event kind

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01MKCBbD9VJLeKqwGUyyzxyw
EOF
```

---

## Task 27: Combat events with names and the party flag

**Files:**
- Create: `internal/session/combat.go`
- Modify: `internal/session/session.go` (`register`, `appendEvent` sets `Party`)
- Test: `internal/session/combat_test.go`

**Interfaces:**
- Produces: events of kinds `melee` (source attacker, target victim, `amount` total, `crit`, `Data` `outcome`, `hit_info`, `absorb`, `resist`, `blocked`), `spell_damage` (`spell_id`, `amount`, `crit`, `Data` `school`, `absorb`, `resist`, `blocked`, `periodic`), `periodic` (`Data` `aura_type`, `tick` = damage/heal/energize/leech, `school`), `heal` (`crit`), `energize` (`Data` `power`), `spell_start` (`Data` `cast_ms`), `spell_go` (`Data` `hits`, `misses`), `spell_fail` (own failures: `Data` `reason` code; others' interrupts: `Data` `reason` `interrupted`), `spell_miss` (`Data` `miss` name), `kill` (source killer, target victim). Every appended event gets `Party = true` when its source or target is the player or a party member.
- Consumes: Task 26, Tasks 13, 17.

- [ ] **Step 1: Write the failing test**

`~/turtle-agent/internal/session/combat_test.go`:

```go
package session

import (
	"testing"
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/events"
	"github.com/TortoiseBots/turtle-agent/internal/game"
	"github.com/TortoiseBots/turtle-agent/internal/game/gametest"
	"github.com/TortoiseBots/turtle-agent/internal/opcode"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

func TestCombatEventsNamesAndPartyFilter(t *testing.T) {
	old := namesWait
	namesWait = 100 * time.Millisecond
	defer func() { namesWait = old }()
	h := start(t, Options{})
	h.enterWorld()
	gorm := packet.GUID(0x30)
	stranger := packet.GUID(0x40)
	h.send(opcode.SMSGGroupList, groupList(me.GUID, game.GroupMember{Name: "Gorm", GUID: gorm, Status: game.MemberOnline}))
	h.send(opcode.SMSGCreatureQueryResponse, packet.NewWriter().U32(299).CString("Young Wolf").U8(0).U8(0).U8(0).CString("").
		U32(0).U32(1).U32(1).U32(0).U32(0).U32(0).U32(161).U8(0).U8(0).Bytes())
	h.send(opcode.SMSGAttackerstateupdate, gametest.AttackerStateUpdate(game.HitInfoCritical, gorm, youngWolf, 24, 0, 0, 0, 1, 0))
	h.send(opcode.SMSGSpellnonmeleedamagelog, gametest.SpellNonMeleeDamage(gorm, youngWolf, 0, 5, 0, 0, 0, false, 0, 0))
	h.send(opcode.SMSGSpellnonmeleedamagelog, gametest.SpellNonMeleeDamage(youngWolf, stranger, 133, 30, 2, 0, 0, false, 0, 0))
	h.send(opcode.SMSGPeriodicauralog, gametest.PeriodicDamage(youngWolf, gorm, 589, game.AuraPeriodicDamage, 6, 5, 0, 0))
	h.send(opcode.SMSGSpellheallog, gametest.SpellHeal(gorm, me.GUID, 2061, 48, false))
	h.send(opcode.SMSGSpellenergizelog, gametest.SpellEnergize(gorm, gorm, 29166, 0, 400))
	h.send(opcode.SMSGSpellStart, gametest.SpellStart(me.GUID, 133, 3000, youngWolf))
	h.send(opcode.SMSGSpellGo, gametest.SpellGo(me.GUID, 133, []packet.GUID{youngWolf}, nil, nil, youngWolf))
	h.send(opcode.SMSGCastResult, gametest.CastResult(133, 0x0B))
	h.send(opcode.SMSGSpellFailedOther, gametest.SpellFailedOther(gorm, 2061))
	h.send(opcode.SMSGSpelllogmiss, gametest.SpellLogMiss(133, me.GUID, youngWolf, 2))
	h.send(opcode.SMSGPartykilllog, gametest.PartyKill(gorm, youngWolf))
	time.Sleep(300 * time.Millisecond) // the stranger's name query is never answered
	h.logout()
	all := h.s.Log().Read(events.Query{Limit: events.MaxLimit}).Events
	byKind := map[string][]events.Event{}
	for _, e := range all {
		byKind[e.Kind] = append(byKind[e.Kind], e)
	}
	m := byKind[events.KindMelee]
	if len(m) != 1 || m[0].Source != "Gorm" || m[0].Target != "Young Wolf" || m[0].Amount != 24 || !m[0].Crit || !m[0].Party || m[0].Data["outcome"] != "normal" {
		t.Fatalf("melee %+v", m)
	}
	sd := byKind[events.KindSpellDamage]
	if len(sd) != 2 || sd[0].Target != "Gorm" || !sd[0].Party || sd[1].SpellID != 133 || sd[1].Party || sd[1].Source != "" {
		t.Fatalf("spell damage %+v", sd)
	}
	if p := byKind[events.KindPeriodic]; len(p) != 1 || p[0].SpellID != 589 || p[0].Data["tick"] != "damage" || p[0].Source != "Gorm" {
		t.Fatalf("periodic %+v", p)
	}
	if hl := byKind[events.KindHeal]; len(hl) != 1 || hl[0].Source != "Harnessb" || hl[0].Target != "Gorm" || hl[0].Amount != 48 {
		t.Fatalf("heal %+v", hl)
	}
	if en := byKind[events.KindEnergize]; len(en) != 1 || en[0].Amount != 400 {
		t.Fatalf("energize %+v", en)
	}
	if ss := byKind[events.KindSpellStart]; len(ss) != 1 || ss[0].Target != "Young Wolf" || ss[0].Data["cast_ms"] != uint32(3000) {
		t.Fatalf("spell start %+v", ss)
	}
	if sg := byKind[events.KindSpellGo]; len(sg) != 1 || sg[0].Target != "Young Wolf" || sg[0].Data["hits"] != 1 {
		t.Fatalf("spell go %+v", sg)
	}
	if sf := byKind[events.KindSpellFail]; len(sf) != 2 || sf[0].Source != "Harnessb" || sf[0].Data["reason"] != uint8(0x0B) || sf[1].Data["reason"] != "interrupted" {
		t.Fatalf("spell fail %+v", sf)
	}
	if sm := byKind[events.KindSpellMiss]; len(sm) != 1 || sm[0].Data["miss"] != "resist" {
		t.Fatalf("spell miss %+v", sm)
	}
	if k := byKind[events.KindKill]; len(k) != 1 || k[0].Source != "Gorm" || k[0].Target != "Young Wolf" {
		t.Fatalf("kill %+v", k)
	}
	party := h.s.Log().Read(events.Query{Kinds: []string{events.KindSpellDamage}, PartyOnly: true}).Events
	if len(party) != 1 || party[0].Target != "Gorm" {
		t.Fatalf("party filter %+v", party)
	}
}
```

- [ ] **Step 2: Run it and see it fail**

Run: `cd ~/turtle-agent && ~/.local/go/bin/go test ./internal/session/ -run Combat`
Expected: FAIL: `melee []` — no combat handler is registered yet, so the packets are only counted as unknown.

- [ ] **Step 3: Implement**

`~/turtle-agent/internal/session/combat.go`:

```go
package session

import (
	"github.com/TortoiseBots/turtle-agent/internal/events"
	"github.com/TortoiseBots/turtle-agent/internal/game"
	"github.com/TortoiseBots/turtle-agent/internal/opcode"
)

func (s *Session) registerCombat() {
	s.handlers[opcode.SMSGAttackerstateupdate] = s.onMelee
	s.handlers[opcode.SMSGSpellnonmeleedamagelog] = s.onSpellDamage
	s.handlers[opcode.SMSGPeriodicauralog] = s.onPeriodic
	s.handlers[opcode.SMSGSpellheallog] = s.onHeal
	s.handlers[opcode.SMSGSpellenergizelog] = s.onEnergize
	s.handlers[opcode.SMSGSpellStart] = s.onSpellStart
	s.handlers[opcode.SMSGSpellGo] = s.onSpellGo
	s.handlers[opcode.SMSGCastResult] = s.onCastResult
	s.handlers[opcode.SMSGSpellFailedOther] = s.onSpellFailedOther
	s.handlers[opcode.SMSGSpelllogmiss] = s.onSpellLogMiss
	s.handlers[opcode.SMSGPartykilllog] = s.onKill
}

func (s *Session) onMelee(body []byte) error {
	m, err := game.ParseAttackerStateUpdate(body)
	if err != nil {
		return err
	}
	var absorb uint32
	var resist int32
	for _, sub := range m.Subs {
		absorb += sub.Absorb
		resist += sub.Resist
	}
	s.emit(events.Event{Kind: events.KindMelee, SourceGUID: m.Attacker, TargetGUID: m.Victim, Amount: int64(m.Total), Crit: m.Crit(),
		Data: map[string]any{"outcome": m.Outcome(), "hit_info": m.HitInfo, "absorb": absorb, "resist": resist, "blocked": m.Blocked}})
	return nil
}

func (s *Session) onSpellDamage(body []byte) error {
	d, err := game.ParseSpellNonMeleeDamageLog(body)
	if err != nil {
		return err
	}
	s.emit(events.Event{Kind: events.KindSpellDamage, SourceGUID: d.Attacker, TargetGUID: d.Target, SpellID: d.SpellID,
		Amount: int64(d.Damage), Crit: d.Crit(),
		Data: map[string]any{"school": d.School, "absorb": d.Absorb, "resist": d.Resist, "blocked": d.Blocked, "periodic": d.Periodic}})
	return nil
}

func (s *Session) onPeriodic(body []byte) error {
	p, err := game.ParsePeriodicAuraLog(body)
	if err != nil {
		return err
	}
	for _, t := range p.Ticks {
		s.emit(events.Event{Kind: events.KindPeriodic, SourceGUID: p.Caster, TargetGUID: p.Target, SpellID: p.SpellID,
			Amount: int64(t.Amount), Data: map[string]any{"aura_type": t.AuraType, "tick": t.Kind(), "school": t.School}})
	}
	return nil
}

func (s *Session) onHeal(body []byte) error {
	h, err := game.ParseSpellHealLog(body)
	if err != nil {
		return err
	}
	s.emit(events.Event{Kind: events.KindHeal, SourceGUID: h.Caster, TargetGUID: h.Target, SpellID: h.SpellID, Amount: int64(h.Amount), Crit: h.Crit})
	return nil
}

func (s *Session) onEnergize(body []byte) error {
	e, err := game.ParseSpellEnergizeLog(body)
	if err != nil {
		return err
	}
	s.emit(events.Event{Kind: events.KindEnergize, SourceGUID: e.Caster, TargetGUID: e.Target, SpellID: e.SpellID,
		Amount: int64(e.Amount), Data: map[string]any{"power": e.Power}})
	return nil
}

func (s *Session) onSpellStart(body []byte) error {
	st, err := game.ParseSpellStart(body)
	if err != nil {
		return err
	}
	s.emit(events.Event{Kind: events.KindSpellStart, SourceGUID: st.Caster, TargetGUID: st.Targets.Unit, SpellID: st.SpellID,
		Data: map[string]any{"cast_ms": st.CastTime}})
	return nil
}

func (s *Session) onSpellGo(body []byte) error {
	g, err := game.ParseSpellGo(body)
	if err != nil {
		return err
	}
	target := g.Targets.Unit
	if target == 0 && len(g.Hits) > 0 {
		target = g.Hits[0]
	}
	s.emit(events.Event{Kind: events.KindSpellGo, SourceGUID: g.Caster, TargetGUID: target, SpellID: g.SpellID,
		Data: map[string]any{"hits": len(g.Hits), "misses": len(g.Misses)}})
	return nil
}

func (s *Session) onCastResult(body []byte) error {
	c, err := game.ParseCastResult(body)
	if err != nil || !c.Failed {
		return err
	}
	s.emit(events.Event{Kind: events.KindSpellFail, SourceGUID: s.selfGUID(), SpellID: c.SpellID, Data: map[string]any{"reason": c.Reason}})
	return nil
}

func (s *Session) onSpellFailedOther(body []byte) error {
	c, err := game.ParseSpellFailedOther(body)
	if err != nil {
		return err
	}
	s.emit(events.Event{Kind: events.KindSpellFail, SourceGUID: c.Caster, SpellID: c.SpellID, Data: map[string]any{"reason": "interrupted"}})
	return nil
}

func (s *Session) onSpellLogMiss(body []byte) error {
	m, err := game.ParseSpellLogMiss(body)
	if err != nil {
		return err
	}
	for _, miss := range m.Misses {
		s.emit(events.Event{Kind: events.KindSpellMiss, SourceGUID: m.Caster, TargetGUID: miss.Target, SpellID: m.SpellID,
			Data: map[string]any{"miss": game.SpellMissName(miss.Reason)}})
	}
	return nil
}

func (s *Session) onKill(body []byte) error {
	k, err := game.ParsePartyKillLog(body)
	if err != nil {
		return err
	}
	s.emit(events.Event{Kind: events.KindKill, SourceGUID: k.Killer, TargetGUID: k.Victim})
	return nil
}
```

In `~/turtle-agent/internal/session/session.go`, add `s.registerCombat()` in `register` after `s.registerActions()`.

In `~/turtle-agent/internal/session/session.go`, replace the one-line `appendEvent` added in Task 13 with:

```go
// appendEvent stores a ready event, flagging it when the player or a party member is involved.
func (s *Session) appendEvent(e events.Event) {
	e.Party = s.inParty(e.SourceGUID) || s.inParty(e.TargetGUID)
	s.log.Append(e)
}
```


- [ ] **Step 4: Run the tests**

Run: `make -C ~/turtle-agent fmt test && make -C ~/turtle-agent build`
Expected: PASS for every package.

- [ ] **Step 5: Commit**

```bash
cd ~/turtle-agent && git status --short
git add internal/session
git commit -F - <<'EOF'
session: combat events with names, kills, spell failures and the party flag

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01MKCBbD9VJLeKqwGUyyzxyw
EOF
```

---

## Task 28: Python `HeadlessClient` in wow-play-harness

**Files (in `~/wow-play-harness`):**
- Create: `wowharness/headless.py`
- Test: `tests/test_headless.py`

**Interfaces:**
- Produces: `DEFAULT_RPC = "127.0.0.1:8765"`; `CHAT_KINDS`, `COMBAT_KINDS`; `class HeadlessError(Exception)` with `.code`; `class HeadlessClient(rpc: str = DEFAULT_RPC, timeout_s: float = 35.0, spell_names: dict[int, str] | None = None)` with `call(method: str, **params)`, `chat(text: str) -> None`, `gm(cmd: str, wait_s: float = 3.0) -> dict` (`{"reply": str}`, like `mcp.gm`), `bot(cmd: str, wait_s: float = 5.0) -> dict` (`{"lines": [...]}`, like `mcp.bot`), `state() -> dict` (`{"line": "HX|p=…", "via": "headless", "status": {...}}`, like `mcp.state`), `tail_combat_log(since: int = -1) -> dict` and `tail_chat(since: int = -1) -> dict` (`{"offset", "lines", "restarted"}`, like `mcp.tail_combat_log`/`tail_chat`), `fight_report(combat_since: int, chat_since: int, me: str, party: list[str]) -> dict` (`{"report", "summary"}`, like `mcp.fight_report`); module functions `clock(iso: str) -> str`, `combat_line(e: dict, spell_name) -> str | None`, `chat_line(e: dict) -> str`, `to_report_events(evs: list[dict], spell_name) -> list[combat.Event]`.
- Consumes: turtle-agent JSON-RPC (`status`, `chat`, `gm`, `events`, `party`, `units`, and the action methods through `call`); `wowharness.combat.Event`, `wowharness.report.build`/`summary` (unchanged).

Offsets are event sequence numbers (`status.last_seq`), not line counts; `restarted` is the page's `dropped` flag. Combat lines use the 1.12 GlobalStrings shapes that `combat.parse_lines` already matches, prefixed with `M/D HH:MM:SS.mmm  ` in local time. Names stay names (the real log says "You"/"Your" for the player; `parse_lines` maps both to `me`). Spell names come from `spell_names` (spell id → name), else `spell <id>`, because the client has no DBC. `fight_report` builds `combat.Event` objects straight from the structured events instead of re-parsing text, and counts a `spell_go` as a cast only when that caster's spell produced no damage or heal event in the window (the real log prints "casts … on …" only for spells without a damage or heal line). `state()`'s `HX|p=` line has world `x`/`y` and `map<id>` where the addon has map-relative coordinates and a zone name.

- [ ] **Step 1: Check the repository state**

Run: `git -C ~/wow-play-harness status --short`
Expected: the owner's own uncommitted changes may be listed (for example `wowharness/mcp.py`, `wowharness/client.py`); this task touches none of them and stages only its two new files.

- [ ] **Step 2: Write the failing test**

`~/wow-play-harness/tests/test_headless.py`:

```python
import json
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import pytest

from wowharness.combat import parse_lines
from wowharness.headless import HeadlessClient, HeadlessError

T = "2026-10-12T21:03:40.120000000+00:00"
NAMES = {2050: "Lesser Heal", 133: "Fireball", 1130: "Hunter's Mark"}


def ev(seq, kind, **kw):
    e = {"seq": seq, "time": T, "kind": kind}
    e.update(kw)
    return e


# The same fight as tests/test_report.py, as turtle-agent events.
EVENTS = [
    ev(1, "melee", source="Gorm", target="Kobold Vermin", amount=12, data={"outcome": "normal"}),
    ev(2, "melee", source="Kobold Vermin", target="Gorm", amount=4, data={"outcome": "normal"}),
    ev(3, "spell_start", source="Lia", spell_id=2050),
    ev(4, "heal", source="Lia", target="Gorm", spell_id=2050, amount=48),
    ev(5, "spell_go", source="Lia", target="Gorm", spell_id=2050, data={"hits": 1}),
    ev(6, "spell_damage", source="Harnessb", target="Kobold Vermin", spell_id=133, amount=33, data={"school": 2}),
    ev(7, "aura_applied", target="Kobold Vermin", spell_id=1130, data={"harmful": True, "slot": 32}),
    ev(8, "death", source="Kobold Vermin"),
    ev(9, "whisper", source="Gorm", text="Attacking Kobold Vermin", data={"type": "whisper"}),
    ev(10, "system", text="TBM:ACTION_ACK|attack|bot:Gorm|1", data={"type": "system"}),
]


class RpcError(Exception):
    def __init__(self, code, msg):
        super().__init__(msg)
        self.code, self.msg = code, msg


class Agent:
    """A scripted turtle-agent: answers the JSON-RPC methods the wrapper uses."""

    def __init__(self):
        self.calls, self.events = [], list(EVENTS)

    def add(self, kind, **kw):
        self.events.append(ev(len(self.events) + 1, kind, **kw))

    def handle(self, method, params):
        self.calls.append((method, params))
        if method == "status":
            return {"state": "in_world", "character": "Harnessb", "last_seq": len(self.events), "map": 0,
                    "x": 1.0, "y": 2.0, "health": 50, "max_health": 100, "power": 30, "max_power": 60}
        if method == "events":
            kinds = params.get("kinds") or []
            evs = [e for e in self.events if e["seq"] > params.get("since", 0) and (not kinds or e["kind"] in kinds)]
            return {"events": evs, "next": len(self.events), "oldest": 1, "dropped": False, "more": False}
        if method == "party":
            return {"in_group": True, "members": [{"name": "Gorm", "health": 90, "max_health": 100},
                                                  {"name": "Lia", "health": 100, "max_health": 100}]}
        if method == "units":
            return [{"guid": "0xF130000000000001", "name": "Kobold Vermin", "health": 0, "max_health": 50, "dead": True}]
        if method == "target":
            return {"guid": "0xF130000000000001", "name": "Kobold Vermin", "health": 50, "max_health": 50}
        if method == "gm":
            if params["text"] == ".fail":
                raise RpcError(-32003, "boom")
            if params["text"].startswith(".bot version"):
                self.add("system", text="TBM:VERSION|2026-10-10-v3", data={"type": "system"})
            elif params["text"].startswith(".gm"):
                self.add("system", text="You are now visible.", data={"type": "system"})
            return {}
        if method == "attack":  # the fight happens: replay the scripted combat and chat events
            for e in EVENTS:
                self.add(e["kind"], **{k: v for k, v in e.items() if k not in ("seq", "time", "kind")})
            return {}
        if method in ("chat", "stop_attack", "invite", "teleport"):
            return {}
        raise RpcError(-32601, "Method not found: " + method)


@pytest.fixture
def agent():
    a = Agent()

    class Handler(BaseHTTPRequestHandler):
        def do_POST(self):
            req = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
            try:
                out = {"jsonrpc": "2.0", "id": req["id"], "result": a.handle(req["method"], req.get("params") or {})}
            except RpcError as e:
                out = {"jsonrpc": "2.0", "id": req["id"], "error": {"code": e.code, "message": e.msg}}
            body = json.dumps(out).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *args):
            pass

    srv = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    a.rpc = f"127.0.0.1:{srv.server_address[1]}"
    yield a
    srv.shutdown()


def test_chat_routes_like_the_chat_box(agent):
    c = HeadlessClient(agent.rpc)
    c.chat("/p hi")
    c.chat(".bot version")
    c.chat("/w Gorm come here")
    c.chat("hello")
    sent = [x for x in agent.calls if x[0] in ("chat", "gm")]
    assert sent == [("chat", {"type": "party", "text": "hi"}), ("gm", {"text": ".bot version"}),
                    ("chat", {"type": "whisper", "to": "Gorm", "text": "come here"}), ("chat", {"type": "say", "text": "hello"})]
    with pytest.raises(HeadlessError):
        c.chat("/script print(1)")
    with pytest.raises(ValueError):
        c.chat("x" * 256)


def test_errors(agent):
    with pytest.raises(HeadlessError) as err:
        HeadlessClient(agent.rpc).chat(".fail")
    assert err.value.code == -32003
    with pytest.raises(HeadlessError, match="not reachable"):
        HeadlessClient("127.0.0.1:1", timeout_s=1).call("status")


def test_tail_combat_log_lines_parse_back(agent):
    c = HeadlessClient(agent.rpc, spell_names=NAMES)
    assert c.tail_combat_log() == {"offset": 10, "lines": []}
    out = c.tail_combat_log(0)
    kinds = [e.kind for e in parse_lines(out["lines"], "Harnessb")]
    assert kinds == ["melee", "melee", "cast_start", "heal", "cast", "spell", "aura", "dies"]
    assert out["lines"][0].endswith("  Gorm hits Kobold Vermin for 12.") and out["restarted"] is False


def test_tail_chat_lines(agent):
    lines = HeadlessClient(agent.rpc).tail_chat(0)["lines"]
    assert lines[0].endswith("  [Gorm] whispers: Attacking Kobold Vermin")
    assert lines[1].endswith("  TBM:ACTION_ACK|attack|bot:Gorm|1")


def test_fight_report_matches_the_real_report(agent):
    out = HeadlessClient(agent.rpc, spell_names=NAMES).fight_report(0, 0, "Harnessb", ["Gorm", "Lia"])
    a = out["report"]["actors"]
    assert a["Gorm"]["damage_done"] == 12 and a["Gorm"]["damage_taken"] == 4
    assert a["Lia"]["healing_done"] == 48 and a["Lia"]["casts"] == {"Lesser Heal": 1}
    assert a["Harnessb"]["damage_done"] == 33 and a["Harnessb"]["casts"] == {"Fireball": 1}
    assert out["report"]["killed"] == ["Kobold Vermin"] and out["report"]["auras"] == ["Hunter's Mark on Kobold Vermin"]
    assert len(out["report"]["bot_lines"]) == 2
    assert "Gorm: 12 damage" in out["summary"] and "Lia: healed 48" in out["summary"]


def test_state_gm_and_bot(agent):
    c = HeadlessClient(agent.rpc)
    st = c.state()
    assert st["line"] == "HX|p=Harnessb,50,50,1.0,2.0,map0|party=Gorm:90,Lia:100" and st["via"] == "headless"
    assert c.gm(".gm visible on", wait_s=0.3) == {"reply": "You are now visible."}
    assert any("TBM:VERSION|" in l for l in c.bot("version", wait_s=0.3)["lines"])
```

- [ ] **Step 3: Run it and see it fail**

Run: `cd ~/wow-play-harness && .venv/bin/python -m pytest tests/test_headless.py -q`
Expected: FAIL, `ModuleNotFoundError: No module named 'wowharness.headless'`.

- [ ] **Step 4: Implement**

`~/wow-play-harness/wowharness/headless.py`:

```python
"""Headless backend: the tester surface of client.Client / mcp.py over turtle-agent's JSON-RPC.

turtle-agent (github.com/TortoiseBots/turtle-agent) logs one character in without a game window and
serves JSON-RPC 2.0 on a loopback port. Offsets here are event sequence numbers (status.last_seq).
"""
import itertools
import json
import time
import urllib.request
from datetime import datetime

from .combat import Event
from .report import build, summary

DEFAULT_RPC = "127.0.0.1:8765"
CHAT_KINDS = ["chat", "whisper", "system"]
COMBAT_KINDS = ["melee", "spell_damage", "periodic", "heal", "energize", "spell_start", "spell_go",
                "aura_applied", "aura_removed", "death", "kill"]
NEEDS_REAL_CLIENT = ("/script", "/run", "/hx", "/reload", "/console")
SLASH = {"/s": "say", "/say": "say", "/p": "party", "/party": "party", "/y": "yell", "/yell": "yell",
         "/e": "emote", "/emote": "emote", "/me": "emote", "/g": "guild", "/guild": "guild",
         "/ra": "raid", "/raid": "raid"}
SCHOOLS = ["Physical", "Holy", "Fire", "Nature", "Frost", "Shadow", "Arcane"]
POWERS = ["Mana", "Rage", "Focus", "Energy", "Happiness"]
OUTCOMES = {"dodge": "dodges", "parry": "parries", "block": "blocks", "evade": "evades",
            "immune": "is immune", "deflect": "deflects"}


class HeadlessError(Exception):
    """A turtle-agent RPC error, or something only the real client can do."""

    def __init__(self, message: str, code: int | None = None):
        super().__init__(message)
        self.code = code


def clock(iso: str) -> str:
    """turtle-agent's RFC 3339 time as the chat/combat log prefix M/D HH:MM:SS.mmm (local time)."""
    t = datetime.fromisoformat(iso.replace("Z", "+00:00")).astimezone()
    return f"{t.month}/{t.day} {t:%H:%M:%S}.{t.microsecond // 1000:03d}"


def _school(n) -> str:
    return SCHOOLS[n] if isinstance(n, int) and 0 <= n < len(SCHOOLS) else "Physical"


def combat_line(e: dict, spell_name) -> str | None:
    """One combat event as a 1.12 combat-log line that combat.parse_line understands."""
    k, d = e["kind"], e.get("data") or {}
    src, tgt = e.get("source") or "Unknown", e.get("target") or "Unknown"
    sp = spell_name(e["spell_id"]) if e.get("spell_id") else ""
    amt, hit = e.get("amount", 0), "crits" if e.get("crit") else "hits"
    if k == "melee":
        o = d.get("outcome")
        body = (f"{src} {hit} {tgt} for {amt}." if o == "normal" else
                f"{src} misses {tgt}." if o == "miss" else f"{src} attacks. {tgt} {OUTCOMES.get(o, o)}.")
    elif k == "spell_damage":
        school = d.get("school", 0)
        body = f"{src}'s {sp} {hit} {tgt} for {amt}" + (f" {_school(school)} damage." if school else ".")
    elif k == "periodic":
        tick = d.get("tick")
        if tick == "damage":
            body = f"{tgt} suffers {amt} {_school(d.get('school', 0))} damage from {src}'s {sp}."
        elif tick == "heal":
            body = f"{tgt} gains {amt} health from {src}'s {sp}."
        else:
            body = f"{tgt} gains {amt} Mana from {src}'s {sp}."
    elif k == "heal":
        body = f"{src}'s {sp} {'critically ' if e.get('crit') else ''}heals {tgt} for {amt}."
    elif k == "energize":
        p = d.get("power", 0)
        body = f"{tgt} gains {amt} {POWERS[p] if 0 <= p < len(POWERS) else 'Mana'} from {src}'s {sp}."
    elif k == "spell_start":
        body = f"{src} begins to cast {sp}."
    elif k == "spell_go":
        body = f"{src} casts {sp} on {tgt}." if e.get("target") else f"{src} casts {sp}."
    elif k == "aura_applied":
        body = f"{tgt} is afflicted by {sp}." if d.get("harmful") else f"{tgt} gains {sp}."
    elif k == "aura_removed":
        body = f"{sp} fades from {tgt}."
    elif k == "death":
        body = f"{src} dies."
    elif k == "kill":
        body = f"{tgt} is slain by {src}!"
    else:
        return None
    return f"{clock(e['time'])}  {body}"


def chat_line(e: dict) -> str:
    """One chat/system event as a chat-log line."""
    d = e.get("data") or {}
    typ, src, text = d.get("type", e["kind"]), e.get("source") or "", e.get("text", "")
    body = {
        "say": f"[{src}] says: {text}", "yell": f"[{src}] yells: {text}", "party": f"[Party] [{src}]: {text}",
        "whisper": f"[{src}] whispers: {text}", "whisper_inform": f"To [{src}]: {text}", "emote": f"{src} {text}",
        "guild": f"[Guild] [{src}]: {text}", "raid": f"[Raid] [{src}]: {text}", "monster_say": f"{src} says: {text}",
        "monster_yell": f"{src} yells: {text}", "monster_whisper": f"{src} whispers: {text}",
        "channel": f"[{d.get('channel', '')}] [{src}]: {text}", "hardcore": f"[Hardcore] [{src}]: {text}",
    }.get(typ, text)
    return f"{clock(e['time'])}  {body}"


def to_report_events(evs: list[dict], spell_name) -> list[Event]:
    """Structured events as combat.Event, the input report.build expects."""
    effects = {(e.get("source"), e.get("spell_id")) for e in evs if e["kind"] in ("spell_damage", "heal")}
    out = []
    for e in evs:
        k, d = e["kind"], e.get("data") or {}
        t, src, tgt = clock(e["time"]), e.get("source") or "", e.get("target")
        sp = spell_name(e["spell_id"]) if e.get("spell_id") else None
        amt, crit, raw = e.get("amount", 0), bool(e.get("crit")), combat_line(e, spell_name) or ""
        if k == "melee":
            out.append(Event(t, "melee", src, None, tgt, amt, crit, raw) if d.get("outcome") == "normal"
                       else Event(t, "other", "", None, None, None, False, raw))
        elif k == "spell_damage":
            out.append(Event(t, "spell", src, sp, tgt, amt, crit, raw))
        elif k == "periodic" and d.get("tick") in ("damage", "heal"):
            out.append(Event(t, "periodic" if d["tick"] == "damage" else "heal", src, sp, tgt, amt, False, raw))
        elif k == "heal":
            out.append(Event(t, "heal", src, sp, tgt, amt, crit, raw))
        elif k == "spell_start":
            out.append(Event(t, "cast_start", src, sp, None, None, False, raw))
        elif k == "spell_go" and tgt and (src, e.get("spell_id")) not in effects:
            out.append(Event(t, "cast", src, sp, tgt, None, False, raw))
        elif k == "aura_applied":
            out.append(Event(t, "aura", "", sp, tgt, None, False, raw) if d.get("harmful")
                       else Event(t, "gain", tgt or "", sp, None, None, False, raw))
        elif k == "death":
            out.append(Event(t, "dies", src, None, None, None, False, raw))
        elif k == "kill":
            out.append(Event(t, "slain", src, None, tgt, None, False, raw))
    return out


def _pct(cur, mx) -> int:
    return int(cur * 100 // mx) if mx else 0


class HeadlessClient:
    def __init__(self, rpc: str = DEFAULT_RPC, timeout_s: float = 35.0, spell_names: dict[int, str] | None = None):
        self.url = f"http://{rpc}/rpc"
        self.timeout_s = timeout_s
        self.spell_names = dict(spell_names or {})
        self._ids = itertools.count(1)

    def _spell(self, spell_id: int) -> str:
        return self.spell_names.get(spell_id, f"spell {spell_id}")

    def call(self, method: str, **params):
        body = json.dumps({"jsonrpc": "2.0", "id": next(self._ids), "method": method, "params": params}).encode()
        req = urllib.request.Request(self.url, data=body, headers={"Content-Type": "application/json"}, method="POST")
        try:
            with urllib.request.urlopen(req, timeout=self.timeout_s) as resp:
                out = json.loads(resp.read().decode())
        except OSError as err:
            raise HeadlessError(f"turtle-agent not reachable at {self.url}: {err}") from None
        if out.get("error"):
            e = out["error"]
            raise HeadlessError(f"{method}: {e.get('message')} ({e.get('code')})", e.get("code"))
        return out.get("result")

    def _read_all(self, since: int, kinds: list[str]) -> tuple[list[dict], int, bool]:
        evs, dropped = [], False
        while True:
            page = self.call("events", since=since, kinds=kinds, limit=5000)
            evs += page["events"]
            dropped = dropped or page["dropped"]
            since = page["next"]
            if not page["more"]:
                return evs, since, dropped

    def _collect(self, since: int, kinds: list[str], wait_s: float) -> list[dict]:
        out, deadline = [], time.monotonic() + wait_s
        while (left := deadline - time.monotonic()) > 0:
            page = self.call("events", since=since, kinds=kinds, wait_ms=int(left * 1000))
            out += page["events"]
            since = page["next"]
            if not page["events"]:
                time.sleep(min(0.05, max(left, 0)))
        return out

    def chat(self, text: str) -> None:
        """A chat-box line, as Client.chat takes it: "." lines are commands, /s /p /y /e /g /ra /w pick the
        channel, plain text is say. Lua (/script, /run) and /hx need the real client."""
        if len(text) > 255:
            raise ValueError(f"chat line is {len(text)} chars; WoW cuts at 255 - use an HX_* addon helper or split it")
        t = text.strip()
        if t.lower().startswith(NEEDS_REAL_CLIENT):
            raise HeadlessError(f"{t.split()[0]} needs the real client (no Lua in headless mode)")
        if t.startswith("."):
            self.call("gm", text=t)
            return
        if t.startswith("/"):
            cmd, _, rest = t.partition(" ")
            cmd = cmd.lower()
            if cmd in ("/w", "/whisper", "/t", "/tell"):
                to, _, msg = rest.partition(" ")
                self.call("chat", type="whisper", to=to, text=msg)
                return
            if cmd not in SLASH:
                raise HeadlessError(f"unsupported slash command {cmd} in headless mode")
            self.call("chat", type=SLASH[cmd], text=rest)
            return
        self.call("chat", type="say", text=t)

    def gm(self, cmd: str, wait_s: float = 3.0) -> dict:
        """A GM command run by the character; the reply is the system lines that followed within wait_s."""
        since = self.call("status")["last_seq"]
        self.call("gm", text=cmd)
        return {"reply": "\n".join(e.get("text", "") for e in self._collect(since, ["system"], wait_s))}

    def bot(self, cmd: str, wait_s: float = 5.0) -> dict:
        """.bot <cmd>; returns the chat lines seen within wait_s (TBM:/bot replies)."""
        since = self.call("status")["last_seq"]
        self.call("gm", text=".bot " + cmd)
        return {"lines": [chat_line(e) for e in self._collect(since, CHAT_KINDS, wait_s)]}

    def state(self) -> dict:
        """The observer addon's HX| state line, built from status, target and party."""
        st = self.call("status")
        line = (f"HX|p={st.get('character', '?')},{_pct(st.get('health', 0), st.get('max_health', 0))},"
                f"{_pct(st.get('power', 0), st.get('max_power', 0))},{st.get('x', 0.0):.1f},{st.get('y', 0.0):.1f},"
                f"map{st.get('map', 0)}")
        if st.get("target"):
            for u in self.call("units", limit=200):
                if u["guid"] == st["target"]:
                    line += f"|t={u['name']},{_pct(u['health'], u['max_health'])},{'dead' if u.get('dead') else 'alive'}"
        members = self.call("party").get("members") or []
        if members:
            line += "|party=" + ",".join(f"{m['name']}:{_pct(m['health'], m['max_health'])}" for m in members)
        return {"line": line, "via": "headless", "status": st}

    def tail_combat_log(self, since: int = -1) -> dict:
        if since < 0:
            return {"offset": self.call("status")["last_seq"], "lines": []}
        evs, nxt, dropped = self._read_all(since, COMBAT_KINDS)
        return {"offset": nxt, "lines": [l for l in (combat_line(e, self._spell) for e in evs) if l], "restarted": dropped}

    def tail_chat(self, since: int = -1) -> dict:
        if since < 0:
            return {"offset": self.call("status")["last_seq"], "lines": []}
        evs, nxt, dropped = self._read_all(since, CHAT_KINDS)
        return {"offset": nxt, "lines": [chat_line(e) for e in evs], "restarted": dropped}

    def fight_report(self, combat_since: int, chat_since: int, me: str, party: list[str]) -> dict:
        evs, _, _ = self._read_all(combat_since, COMBAT_KINDS)
        chat, _, _ = self._read_all(chat_since, CHAT_KINDS)
        rep = build(to_report_events(evs, self._spell), [chat_line(e) for e in chat], me, party)
        return {"report": rep, "summary": summary(rep, party)}
```

- [ ] **Step 5: Run the tests**

Run: `cd ~/wow-play-harness && .venv/bin/python -m pytest tests/test_headless.py tests/test_report.py tests/test_combat.py -q`
Expected: PASS (all tests in the three files).

- [ ] **Step 6: Commit (only the new files)**

```bash
cd ~/wow-play-harness && git status --short
git add wowharness/headless.py tests/test_headless.py
git commit -F - <<'EOF'
Headless backend: HeadlessClient over turtle-agent's JSON-RPC

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01MKCBbD9VJLeKqwGUyyzxyw
EOF
```

---

## Task 29: Headless MCP server and the MVP scenario script

**Files (in `~/wow-play-harness`):**
- Create: `wowharness/mcp_headless.py`, `tools/headless_mvp.py`
- Test: `tests/test_mcp_headless.py`

**Interfaces:**
- Produces: MCP server `wowharness-headless` (`python -m wowharness.mcp_headless [--rpc HOST:PORT] [--spell-names FILE]`); script options `--rpc`, `--bots BOT1 BOT2`, `--mob NAME`, `--at X Y Z MAP`, `--fight-s`, `--spell-names`, `--wait-s` with the harness tool names `chat`, `gm`, `bot`, `state`, `tail_combat_log`, `tail_chat`, `fight_report`, plus `status`, `units`, `party`, `invite`, `accept_invite`, `leave_group`, `target`, `attack`, `stop_attack`, `cast`, `face`, `walk_to`, `teleport`, `logout`; `def option_from_argv(argv: list[str], name: str) -> str | None`; `def guarded(fn)`; `tools/headless_mvp.py` with `def main(argv: list[str] | None = None) -> int`.
- Consumes: Task 28.

Test briefs keep their tool names; a session points its MCP config at `wowharness.mcp_headless` instead of `wowharness.mcp` to run without a game window. `wowharness/mcp.py` (which has uncommitted owner changes) is not touched.

- [ ] **Step 1: Write the failing test**

`~/wow-play-harness/tests/test_mcp_headless.py`:

```python
import json

from tests.test_headless import agent  # noqa: F401  (the scripted turtle-agent fixture)
from wowharness import mcp_headless
from wowharness.headless import HeadlessClient, HeadlessError


def test_option_from_argv():
    assert mcp_headless.option_from_argv(["--rpc", "127.0.0.1:9000"], "--rpc") == "127.0.0.1:9000"
    assert mcp_headless.option_from_argv(["--rpc=127.0.0.1:9001"], "--rpc") == "127.0.0.1:9001"
    assert mcp_headless.option_from_argv([], "--rpc") is None


def test_guarded_turns_headless_errors_into_ok_false():
    @mcp_headless.guarded
    def tool():
        raise HeadlessError("chat: not in world (-32001)", -32001)
    assert tool() == {"ok": False, "error": "chat: not in world (-32001)"}


def test_tools_delegate(agent):  # noqa: F811
    mcp_headless._state["client"] = HeadlessClient(agent.rpc)
    assert mcp_headless.chat("/p hi") == {"ok": True}
    assert mcp_headless.party()["party"]["in_group"] is True
    assert mcp_headless.tail_combat_log()["offset"] == 10
    assert mcp_headless.attack() == {"ok": True}
    assert mcp_headless.fight_report(0, 0, "Harnessb", ["Gorm", "Lia"])["ok"] is True


def test_mvp_script_against_scripted_agent(agent, capsys):  # noqa: F811
    from tools.headless_mvp import main
    assert main(["--rpc", agent.rpc, "--bots", "Gorm", "Lia", "--mob", "Kobold Vermin", "--fight-s", "1", "--wait-s", "0.1"]) == 0
    out = capsys.readouterr().out
    assert "Gorm: 12 damage" in out and "Killed: Kobold Vermin" in out
    assert ("target", {"name": "Kobold Vermin"}) in agent.calls and ("attack", {}) in agent.calls
```

`tests` and `tools` import as namespace packages because `pyproject.toml` puts the repository root on `pythonpath`; no `__init__.py` is added (it would change how pytest names the existing test modules).

- [ ] **Step 2: Run it and see it fail**

Run: `cd ~/wow-play-harness && .venv/bin/python -m pytest tests/test_mcp_headless.py -q`
Expected: FAIL, `ImportError: cannot import name 'mcp_headless' from 'wowharness'`.

- [ ] **Step 3: Implement the MCP server**

`~/wow-play-harness/wowharness/mcp_headless.py`:

```python
"""MCP tools with the harness's names, backed by turtle-agent (no game window).

Run: python -m wowharness.mcp_headless [--rpc 127.0.0.1:8765] [--spell-names names.json]
turtle-agent must already be logged in (turtle-agent login <account> <character>)."""
import functools
import json
import sys

from mcp.server.fastmcp import FastMCP

from .headless import DEFAULT_RPC, HeadlessClient, HeadlessError

app = FastMCP("wowharness-headless")
_state: dict = {"rpc": DEFAULT_RPC, "spell_names": {}}


def option_from_argv(argv: list[str], name: str) -> str | None:
    """The value of `name VALUE` or `name=VALUE`, else None."""
    for i, arg in enumerate(argv):
        if arg == name and i + 1 < len(argv):
            return argv[i + 1]
        if arg.startswith(name + "="):
            return arg[len(name) + 1:]
    return None


def guarded(fn):
    @functools.wraps(fn)
    def run(*a, **k):
        try:
            return {"ok": True, **fn(*a, **k)}
        except HeadlessError as err:
            return {"ok": False, "error": str(err)}
    return run


def _c() -> HeadlessClient:
    if "client" not in _state:
        _state["client"] = HeadlessClient(_state["rpc"], spell_names=_state["spell_names"])
    return _state["client"]


@app.tool()
@guarded
def chat(text: str) -> dict:
    """A chat-box line: '.' commands, /p /s /y /w Name text, or plain say. No Lua in headless mode."""
    _c().chat(text)
    return {}


@app.tool()
@guarded
def gm(cmd: str) -> dict:
    """A GM command run by the character in game; returns the system reply lines."""
    return _c().gm(cmd)


@app.tool()
@guarded
def bot(cmd: str) -> dict:
    """A TortoiseBots player command: .bot <cmd>. Returns the chat lines seen within 5 s."""
    return _c().bot(cmd)


@app.tool()
@guarded
def state(live: bool = True) -> dict:
    """One HX| state line (player, target, party). `live` is accepted for parity and ignored."""
    return _c().state()


@app.tool()
@guarded
def tail_combat_log(since: int = -1) -> dict:
    """Combat-log lines since an event offset (-1 = just return the current offset)."""
    return _c().tail_combat_log(since)


@app.tool()
@guarded
def tail_chat(since: int = -1) -> dict:
    """Chat lines since an event offset (-1 = just return the current offset)."""
    return _c().tail_chat(since)


@app.tool()
@guarded
def fight_report(combat_since: int, chat_since: int, me: str, party: list[str]) -> dict:
    """Per-actor totals, bot lines and a text summary for the fight since the offsets."""
    return _c().fight_report(combat_since, chat_since, me, party)


@app.tool()
@guarded
def status() -> dict:
    """turtle-agent's session status (state, position, health, latency, parse errors)."""
    return {"status": _c().call("status")}


@app.tool()
@guarded
def units(near: float = 40, hostile: bool | None = None, name: str = "", limit: int = 50) -> dict:
    """Units around the player, nearest first."""
    params = {"near": near, "limit": limit}
    if hostile is not None:
        params["hostile"] = hostile
    if name:
        params["name"] = name
    return {"units": _c().call("units", **params)}


@app.tool()
@guarded
def party() -> dict:
    """Party members with health, power, level and online state."""
    return {"party": _c().call("party")}


@app.tool()
@guarded
def invite(name: str) -> dict:
    """Invite a character to the party."""
    return {"result": _c().call("invite", name=name)}


@app.tool()
@guarded
def accept_invite() -> dict:
    """Accept the pending party invite."""
    return {"result": _c().call("accept_invite")}


@app.tool()
@guarded
def leave_group() -> dict:
    """Leave the party."""
    return {"result": _c().call("leave_group")}


@app.tool()
@guarded
def target(name: str = "", guid: str = "") -> dict:
    """Select a unit by name (nearest match) or guid."""
    params = {"guid": guid} if guid else {"name": name}
    return {"unit": _c().call("target", **params)}


@app.tool()
@guarded
def attack() -> dict:
    """Start auto-attack on the current target."""
    _c().call("attack")
    return {}


@app.tool()
@guarded
def stop_attack() -> dict:
    """Stop auto-attack."""
    _c().call("stop_attack")
    return {}


@app.tool()
@guarded
def cast(spell_id: int, target: str = "") -> dict:
    """Cast a spell by id on a unit guid, else on the current target, else on the player."""
    params = {"spell_id": spell_id}
    if target:
        params["target"] = target
    _c().call("cast", **params)
    return {}


@app.tool()
@guarded
def face(guid: str) -> dict:
    """Turn towards a unit."""
    _c().call("face", guid=guid)
    return {}


@app.tool()
@guarded
def walk_to(x: float, y: float, z: float) -> dict:
    """Walk in a straight line to a point at most 40 yards away."""
    _c().call("walk_to", x=x, y=y, z=z)
    return {}


@app.tool()
@guarded
def teleport(x: float, y: float, z: float, map: int = -1) -> dict:
    """GM teleport (.go xyz); map -1 keeps the current map."""
    params = {"x": x, "y": y, "z": z}
    if map >= 0:
        params["map"] = map
    _c().call("teleport", **params)
    return {}


@app.tool()
@guarded
def logout() -> dict:
    """Log the character out; turtle-agent exits afterwards."""
    _c().call("logout")
    return {}


if __name__ == "__main__":
    _state["rpc"] = option_from_argv(sys.argv[1:], "--rpc") or DEFAULT_RPC
    names_file = option_from_argv(sys.argv[1:], "--spell-names")
    if names_file:
        with open(names_file, encoding="utf-8") as f:
            _state["spell_names"] = {int(k): v for k, v in json.load(f).items()}
    app.run()
```

- [ ] **Step 4: Implement the MVP script**

`~/wow-play-harness/tools/headless_mvp.py`:

```python
"""The harness MVP scenario without a game window: GM off and visible, two bots in the party,
target and attack a mob, then the same fight report the real-client harness prints.

turtle-agent must already be logged in with its RPC on --rpc."""
import argparse
import json
import time

from wowharness.headless import DEFAULT_RPC, HeadlessClient


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--rpc", default=DEFAULT_RPC)
    ap.add_argument("--bots", nargs=2, required=True, metavar=("BOT1", "BOT2"))
    ap.add_argument("--mob", required=True, help="creature name to attack (nearest match)")
    ap.add_argument("--at", nargs=4, type=float, metavar=("X", "Y", "Z", "MAP"), help="GM-teleport here first")
    ap.add_argument("--fight-s", type=float, default=60, help="stop waiting for the kill after this long")
    ap.add_argument("--spell-names", help="JSON file mapping spell id to name for the report")
    ap.add_argument("--wait-s", type=float, default=3.0, help="how long to collect replies to each command")
    a = ap.parse_args(argv)
    names = {}
    if a.spell_names:
        with open(a.spell_names, encoding="utf-8") as f:
            names = {int(k): v for k, v in json.load(f).items()}
    c = HeadlessClient(a.rpc, spell_names=names)
    me = c.call("status")["character"]
    for cmd in (".gm off", ".gm visible on"):
        print(cmd, "->", c.gm(cmd, wait_s=a.wait_s)["reply"] or "(no reply)")
    for b in a.bots:
        c.bot(f"add {b}", wait_s=a.wait_s)
        c.call("invite", name=b)
    deadline = time.monotonic() + 30
    while len(c.call("party").get("members") or []) < 2:
        if time.monotonic() > deadline:
            print("the bots did not join the party within 30 s")
            return 2
        time.sleep(1)
    if a.at:
        c.call("teleport", x=a.at[0], y=a.at[1], z=a.at[2], map=int(a.at[3]))
    combat0, chat0 = c.tail_combat_log()["offset"], c.tail_chat()["offset"]
    mob = c.call("target", name=a.mob)
    print("target:", mob.get("name"), mob.get("guid"), f"{mob.get('health')}/{mob.get('max_health')}")
    c.call("attack")
    c.bot("action attack", wait_s=a.wait_s)
    deadline = time.monotonic() + a.fight_s
    while time.monotonic() < deadline:
        seen = c.call("units", name=a.mob, limit=1)
        if not seen or seen[0].get("dead"):
            break
        time.sleep(1)
    c.call("stop_attack")
    rep = c.fight_report(combat0, chat0, me, list(a.bots))
    print(rep["summary"])
    print(json.dumps(rep["report"], indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
```

- [ ] **Step 5: Run the tests**

Run: `cd ~/wow-play-harness && .venv/bin/python -m pytest tests/test_mcp_headless.py tests/test_headless.py tests/test_mcp.py -q`
Expected: PASS.

- [ ] **Step 6: Commit (only the new files)**

```bash
cd ~/wow-play-harness && git status --short
git add wowharness/mcp_headless.py tools/headless_mvp.py tests/test_mcp_headless.py
git commit -F - <<'EOF'
Headless MCP server with the harness tool names, and the MVP scenario script

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01MKCBbD9VJLeKqwGUyyzxyw
EOF
```


---

## Task 30: Milestone 6 live end-to-end run on m910q and the fight fixture

**Files:**
- Create: `~/turtle-agent/testdata/captures/m6-fight.twcap` (recorded); add `TestCombatFromCaptures` to `~/turtle-agent/internal/game/capture_test.go`
- Test: `internal/game/capture_test.go`

**Interfaces:**
- Consumes: everything above; the shell variables (Task 11) and `rpc` helper (Task 15); `MOB` is a hostile creature name seen at the settings `spot` in Task 25.

This is the spec's "done means": Harnessb logged in headless on m910q, `.gm off` / `.gm visible on`, two bots invited, a mob targeted and attacked, and the parsed combat log plus what each bot did, in the real-client harness's report format.

- [ ] **Step 1: Start the agent with a capture**

Run: `cd ~/turtle-agent && make build && ./bin/turtle-agent login --capture captures/m6-fight.twcap $ACCOUNT Harnessb 2> captures/m6.log & sleep 8; grep 'rpc: listening' captures/m6.log`
Expected: `rpc: listening on http://127.0.0.1:8765/rpc`.

- [ ] **Step 2: Run the MVP scenario headless**

Run:

```bash
AT=$(python3 -I -c 'import json,sys; s=json.load(open(sys.argv[1]))["spot"]; print(s["x"], s["y"], s["z"], s["map"])' $CFG)
cd ~/wow-play-harness && .venv/bin/python tools/headless_mvp.py --bots $BOT1 $BOT2 --mob "MOB" --at $AT --fight-s 90
```

Expected: `.gm off -> …` and `.gm visible on -> …` with the server's replies; a `target: MOB 0xF130… N/N` line; then the summary in the real harness's format: one line per bot, `BOT1: <n> damage; …` / `BOT2: …` where at least one bot shows damage, healing or casts (a bot that did nothing prints `did nothing in the combat log`), an `Auras:` line if debuffs landed, and `Killed: MOB. Unparsed lines: <k>.` with MOB in the kill list. The JSON report's `bot_lines` holds the bots' `TBM:` acknowledgements for `.bot action attack`.

- [ ] **Step 3: Cross-check against the raw events and the real-client harness**

Run: `rpc events '{"since":0,"party_only":true,"kinds":["melee","spell_damage","periodic","heal","kill"],"limit":50}'; rpc status`
Expected: party-flagged `melee`/`spell_damage` events whose `source` is BOT1, BOT2 or Harnessb and whose `target` is MOB, a `kill` or `death` for MOB, and `"parse_errors":0` in `status`. Then the owner runs the same scenario with the real-client harness (`fight_report` through `wowharness.mcp` on the Windows laptop) at the same spot and compares: the same two bot names appear, the same mob is killed, and each bot's damage and cast list are of the same kind (exact numbers differ between fights). Record both summaries in the commit message body.

- [ ] **Step 4: Log out, scrub, promote**

Run: `rpc logout; sleep 2; cd ~/turtle-agent && ./bin/turtle-agent scrub-check captures/m6-fight.twcap && cp captures/m6-fight.twcap testdata/captures/m6-fight.twcap`
Expected: `clean, N records`.

- [ ] **Step 5: Add the combat capture test**

Append to `~/turtle-agent/internal/game/capture_test.go`:

```go
// Every combat packet the server sent in milestones 5 and 6 parses with the source layouts.
func TestCombatFromCaptures(t *testing.T) {
	parsers := map[uint16]func([]byte) error{
		opcode.SMSGAttackerstateupdate:    func(b []byte) error { _, err := ParseAttackerStateUpdate(b); return err },
		opcode.SMSGSpellnonmeleedamagelog: func(b []byte) error { _, err := ParseSpellNonMeleeDamageLog(b); return err },
		opcode.SMSGPeriodicauralog:        func(b []byte) error { _, err := ParsePeriodicAuraLog(b); return err },
		opcode.SMSGSpellheallog:           func(b []byte) error { _, err := ParseSpellHealLog(b); return err },
		opcode.SMSGSpellenergizelog:       func(b []byte) error { _, err := ParseSpellEnergizeLog(b); return err },
		opcode.SMSGSpellStart:             func(b []byte) error { _, err := ParseSpellStart(b); return err },
		opcode.SMSGSpellGo:                func(b []byte) error { _, err := ParseSpellGo(b); return err },
		opcode.SMSGCastResult:             func(b []byte) error { _, err := ParseCastResult(b); return err },
		opcode.SMSGSpellFailedOther:       func(b []byte) error { _, err := ParseSpellFailedOther(b); return err },
		opcode.SMSGSpelllogmiss:           func(b []byte) error { _, err := ParseSpellLogMiss(b); return err },
		opcode.SMSGPartykilllog:           func(b []byte) error { _, err := ParsePartyKillLog(b); return err },
	}
	seen := map[uint16]int{}
	for _, name := range []string{"m5-attack.twcap", "m6-fight.twcap"} {
		recs := fixture(t, name)
		for op, parse := range parsers {
			for _, body := range capture.Bodies(recs, op) {
				if err := parse(body); err != nil {
					t.Fatalf("%s %s % X: %v", name, opcode.Name(op), body, err)
				}
				seen[op]++
			}
		}
	}
	if seen[opcode.SMSGAttackerstateupdate] == 0 || seen[opcode.SMSGSpellGo] == 0 {
		t.Fatalf("fixtures lack melee or spell packets: %v", seen)
	}
}
```

- [ ] **Step 6: Run the tests**

Run: `make -C ~/turtle-agent fmt test`
Expected: PASS, including `TestCombatFromCaptures`. A failure names the opcode and the bytes: fix that parser against the bytes and add the bytes as a case in `combat_test.go` before committing.

- [ ] **Step 7: Commit**

```bash
cd ~/turtle-agent && git status --short
git add testdata/captures/m6-fight.twcap internal/game/capture_test.go
git commit -F - <<'EOF'
Milestone 6 on m910q: headless MVP fight report; fight fixture

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01MKCBbD9VJLeKqwGUyyzxyw
EOF
```

---

## Spec coverage

| Spec section | Tasks |
| :--- | :--- |
| Goal, "Done means" | 30 (headless MVP on m910q, report compared with the real-client run) |
| Non-goals | Respected throughout: no rendering, pathfinding, Warden emulation, character creation or module system; one process per character (Task 10 `login`) |
| Architecture: `auth` | 4 (SRP6, unpadded forms, short-M1 redraw), 5 (build 7272, 75-byte proof, 26-byte reply, vanilla realm list, one retry) |
| Architecture: `worldconn` | 6 (u16 BE + u16/u32 headers, XOR cipher, 4-byte seed, build 5875, zlib addon block, 1.12 auth response lengths) |
| Architecture: `opcode` | 2 (generated from `Opcodes_1_12_1.h`) |
| Architecture: `session` | 9 (char enum, player login, verify world, pings, Warden ignored, logout), 13, 17, 21, 24, 27 |
| Architecture: `objects` | 19 (update, compressed update, moves), 20 (store), 21 (names, party flag, `units`) |
| Architecture: `game` | 12 (chat incl. `CHAT_MSG_HARDCORE`), 16 (group), 23 (actions, movement, GM teleport acks), 26 (combat log) |
| Architecture: `events` | 7 (ordered log), 13 (names without reordering), 27 (party flag) |
| Architecture: `rpc` | 14 (loopback JSON-RPC, `events(since)`, `chat_log`), 17, 21, 24 (remaining methods) |
| Architecture: `cmd/turtle-agent` | 0, 10 (`login <account> <character>`, `--config`, capture), 14 (`--rpc`) |
| Credentials outside the repo | 3 (settings file, mode 0600, redacted printing), 8 and 10 (capture redaction, `scrub-check`) |
| JSON-RPC methods (v1) | `status`, `chat`, `gm`, `events`, `chat_log`, `logout` (14); `invite`, `accept_invite`, `leave_group`, `party` (17); `units` (21); `target`, `attack`, `stop_attack`, `cast`, `face`, `walk_to`, `teleport` (24) |
| Python wrapper | 28 (`HeadlessClient`: chat, gm, bot, state, tail_combat_log, tail_chat, fight_report), 29 (MCP server with the same tool names, MVP script) |
| Data flow | 9 (reader goroutine → owner loop → handlers), 21 (handlers → objects), 13 (handlers → events), 14 (RPC reads snapshots and event ranges) |
| Error handling | 5 and 6 (clear auth codes and texts), 9 (disconnect event and status, no reconnect, unknown opcodes counted, parse errors logged with hex dump), 10 (exit codes), 14 (RPC error codes); throttles in Global Constraints and Task 5 |
| Testing: unit tests on recorded packets | 4, 6, 9, 12, 16, 19, 20, 23, 26 (source-transcribed packets); 11, 15, 18, 22, 25, 30 (capture regressions) |
| Testing: live tests per milestone | 11, 15, 18, 22, 25, 30 |
| Testing: end-to-end | 30 |
| Milestones 1-6 | M1: 0-11; M2: 12-15; M3: 16-18; M4: 19-22; M5: 23-25; M6: 26-30 |
| Risks | Turtle changes: Review Focus 3 and the capture tests; Warden: Task 9; PIN: Task 5 `ErrPINRequired`; SRP edge case: Review Focus 1; toolchain: Task 0 |
| Licence and provenance | 0 (`NOTICE`), rows added in 2, 4, 6, 9, 14 |
