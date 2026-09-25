# launch-nbd (client Go) — pipeline di build.
# Documentazione: docs/build-toolchain.md (tool per host) e docs/assets.md
# (inventario del contenuto embeddato). Contratto: build-<GOOS> fallisce se
# l'albero assets/<GOOS> è vuoto (firma anti-sorpresa, vedi build-toolchain.md §5).

GO      ?= go
BIN     := bin
ASSETS  := assets

.PHONY: all build build-linux build-windows assets test vet clean

all: test build

# --- piattaforma corrente ---------------------------------------------------
build:
	$(GO) build -o $(BIN)/launch-nbd ./cmd/launch-nbd

# --- cross: richiede l'albero degli asset del GOOS target --------------------
# (assets/ NON è versionato: popolato da `make assets` sull'host giusto)
build-linux:
	@sh -c 'test -n "$$(find $(ASSETS)/linux -type f ! -name '"'"'.keep'"'"' 2>/dev/null | head -1)" || { echo "error: $(ASSETS)/linux è vuoto: popolalo su un host Linux (make assets) — vedi docs/build-toolchain.md"; exit 1; }'
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -o $(BIN)/launch-nbd-linux-amd64 ./cmd/launch-nbd

build-windows:
	@sh -c 'test -n "$$(find $(ASSETS)/windows -type f ! -name '"'"'.keep'"'"' 2>/dev/null | head -1)" || { echo "error: $(ASSETS)/windows è vuoto: popolalo su un host Windows (make assets) — vedi docs/build-toolchain.md"; exit 1; }'
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 $(GO) build -o $(BIN)/launch-nbd-windows-amd64.exe ./cmd/launch-nbd

# --- assets: placeholder fino alla Fase B M1 (markers per non far fallire
# le find sopra come "dir assente") -------------------------------------------
assets:
	mkdir -p $(ASSETS)/linux $(ASSETS)/windows
	touch $(ASSETS)/linux/.keep $(ASSETS)/windows/.keep
	@echo "assets: placeholder creati (popolamento reale in Fase B M1 — vedi docs/assets.md)"

# --- test / verifica ----------------------------------------------------------
vet:
	$(GO) vet ./...

test: vet
	$(GO) test ./...

clean:
	rm -rf $(BIN)