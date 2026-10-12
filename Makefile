GOROOT_LOCAL := $(HOME)/.local/go
GO := $(GOROOT_LOCAL)/bin/go
GOFMT := $(GOROOT_LOCAL)/bin/gofmt
CORE ?= $(HOME)/tortoise-wow
export GOTOOLCHAIN := local

.PHONY: test fmt fmt-check vet nocgo build opcodes

test: fmt-check vet nocgo
	$(GO) test -race -count=1 ./...

fmt:
	$(GOFMT) -w .

fmt-check:
	@out=$$($(GOFMT) -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

vet:
	$(GO) vet ./...

# A build without cgo still works, without path_to and walk_path (navmesh.ErrNoEngine).
nocgo:
	CGO_ENABLED=0 $(GO) build ./...

build:
	$(GO) build -o bin/turtle-agent ./cmd/turtle-agent

opcodes:
	$(GO) run ./tools/opcodegen -in $(CORE)/src/game/Protocol/Opcodes_1_12_1.h -out internal/opcode/opcodes_gen.go
