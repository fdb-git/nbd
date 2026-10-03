# launch-nbd (client Go) — pipeline di build.
# Documentazione: docs/build-toolchain.md (tool per host) e docs/assets.md
# (inventario del contenuto embeddato). Contratto: build-<GOOS> fallisce se
# l'albero internal/assets/<GOOS> è vuoto (guardia anti-sorpresa).

GO      ?= go
BIN     := bin
ASSETS  := internal/assets

.PHONY: all build build-linux build-windows assets test vet check-assets clean

all: test build

# --- piattaforma corrente ---------------------------------------------------
build:
	$(GO) build -o $(BIN)/launch-nbd ./cmd/launch-nbd

# --- cross: richiede l'albero asset del GOOS target --------------------------
# (solo i marker .keep sono versionati: il contenuto si popola con `make
# assets` sull'host giusto — vedi docs/assets.md e docs/build-toolchain.md §5)
build-linux:
	@sh -c 'test -n "$$(find $(ASSETS)/linux -type f ! -name '"'"'.keep'"'"' 2>/dev/null | head -1)" || { echo "error: $(ASSETS)/linux è vuoto: popolalo su un host Linux (make assets) — vedi docs/build-toolchain.md"; exit 1; }'
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -o $(BIN)/launch-nbd-linux-amd64 ./cmd/launch-nbd

build-windows:
	@sh -c 'test -n "$$(find $(ASSETS)/windows -type f ! -name '"'"'.keep'"'"' 2>/dev/null | head -1)" || { echo "error: $(ASSETS)/windows è vuoto: popolalo su un host Windows (make assets) — vedi docs/build-toolchain.md"; exit 1; }'
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 $(GO) build -o $(BIN)/launch-nbd-windows-amd64.exe ./cmd/launch-nbd

# --- assets: marker (M1). Il copia automatico dai pacchetti di sistema arriva
# con i target per-piattaforma; il marker tiene l'embed compilabile e rende
# onesta la guardia dei build-<GOOS>. ------------------------------------------
assets:
	mkdir -p $(ASSETS)/linux $(ASSETS)/windows
	touch $(ASSETS)/linux/.keep $(ASSETS)/windows/.keep
	@echo "assets: marker presenti. Popolamento: copia binari/firmware in $(ASSETS)/<GOOS>/{qemu,ovmf,tools} — vedi docs/assets.md"

# --- test / verifica ----------------------------------------------------------
vet:
	$(GO) vet ./...

test: vet
	$(GO) test ./...

# verifica della risoluzione asset (dev-mode/fallback) senza QEMU reale
check-assets:
	$(GO) test ./internal/assets/ -v

clean:
	rm -rf $(BIN)