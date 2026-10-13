GOROOT_LOCAL := $(HOME)/.local/go
GO := $(GOROOT_LOCAL)/bin/go
GOFMT := $(GOROOT_LOCAL)/bin/gofmt
CORE ?= $(HOME)/tortoise-wow
# The server's DBC folder; make spellnames reads SpellCastTimes, SpellRange and SpellDuration from it.
DBC ?= $(HOME)/tortoise-docker-penqle/data/dbc
export GOTOOLCHAIN := local

.PHONY: test fuzz fmt fmt-check vet nocgo build opcodes spellnames tbm-contract addon-host

test: fmt-check vet nocgo
	$(GO) test -race -count=1 ./...

fmt:
	$(GOFMT) -w .

fmt-check:
	@out=$$($(GOFMT) -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

vet:
	$(GO) vet ./...

# Fuzz every Fuzz* target of the SMSG parsers, FUZZTIME each (go test -fuzz takes one target at a time).
# `make test` runs only their seeds. A crash is saved under <package>/testdata/fuzz/<target>/: commit it
# with the fix as a regression seed.
FUZZTIME ?= 5s
FUZZPKGS := ./internal/game ./internal/objects
fuzz:
	@set -e; for pkg in $(FUZZPKGS); do \
		for t in $$($(GO) test -list '^Fuzz' $$pkg | grep '^Fuzz'); do \
			echo "== $$pkg $$t"; \
			$(GO) test -run '^$$' -fuzz "^$$t$$" -fuzztime $(FUZZTIME) $$pkg; \
		done; \
	done

# A build without cgo still works, without path_to and walk_path (navmesh.ErrNoEngine).
nocgo:
	CGO_ENABLED=0 $(GO) build ./...

build:
	$(GO) build -o bin/turtle-agent ./cmd/turtle-agent

opcodes:
	$(GO) run ./tools/opcodegen -in $(CORE)/src/game/Protocol/Opcodes_1_12_1.h -out internal/opcode/opcodes_gen.go

spellnames:
	$(GO) run ./tools/spellnamegen -in $(CORE)/sql/base/tw_world_spell_template.sql -updates $(CORE)/sql/database_updates/world -out internal/spells/names.tsv.gz -mounts internal/spells/mounts.txt.gz -meta internal/spells/meta.tsv.gz -dbc $(DBC)

# Replay TBM: lines through the real TortoiseBotsManager addon (Lua 5.1, fetched at a pinned commit).
# Needs lua5.1 and, on first run, network; it skips with a message without them. Not part of `make test`.
# EVENTS="a.json b.json" replays the TBM: lines of those saved events replies instead of the fixtures.
tbm-contract:
	GO=$(GO) tools/tbmcontract/run.sh $(foreach e,$(EVENTS),--events $(e))

# The addon host's end-to-end tests against the real addon (README "Addon host"). `make test` runs them when the addon
# is cached and lua5.1 is installed; this target fetches the addon first and fails instead of skipping.
addon-host:
	TBM_ADDON_DIR="$$(tools/tbmcontract/fetch.sh)" TBM_ADDON_HOST_STRICT=1 $(GO) test -race -count=1 -run 'AddonHost|TestHost' ./cmd/turtle-agent ./internal/addonhost
