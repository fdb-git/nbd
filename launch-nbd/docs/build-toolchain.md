# Toolchain di build — client Go (`launch-nbd`) per host

**Scope:** documenta gli strumenti necessari per produrre il binario `launch-nbd`
(riscrittura Go, piano `plan-go.md`) sui diversi host. La Fase A (server bash,
`export-nbd/`) **non** richiede nulla di questo: è solo il client Go.

**Stato:** Fase B non iniziata — questo doc è il contratto per i futuri rebuild.

---

## 1. Vincolo fondamentale (perché "qui non si compila per Linux")

`//go:embed` incorpora **solo file presenti nel modulo sorgente**: il binario
completo contiene l'albero `assets/<GOOS>/` (QEMU, OVMF/SeaBIOS, remote-viewer,
bridge-helper, driver Windows). Conseguenze:

1. Il **codice Go** si cross-compila ovunque (`GOOS=linux GOARCH=amd64`, verificato
   su dev Windows con Go 1.27.1): questo host produce un binario linux **senza
   asset** (inutile a runtime, utile solo per compile-check dei test).
2. Il **binario completo per Linux si produce solo su un host Linux** con
   l'installazione dei pacchetti che popolano `assets/linux/` (QEMU, OVMF, tools).
3. Idem Windows: per `build-windows` serve un host (o una copia) con gli **asset
   Windows** (`qemu-system-x86_64.exe`, `qemu-img.exe`, `virt-viewer.exe`,
   TAP-Windows6/Wintun) — il piano vieta di "compilare" asset Windows da Linux
   (§10 Rischi di `plan-go.md`).

## 2. Matrice dei ruoli

| Host | Prodotto completo | Solo cross (codice, no asset) | Note |
|---|---|---|---|
| Dev **Windows** (questa macchina) | `windows/amd64` | `linux/amd64`, `linux/arm64` | Compile-check + test unit; i test Linux-only non girano (niente WSL) |
| Build **Linux x86_64** (CI o VM) | `linux/amd64` | `windows/amd64` (se `assets/windows/` presente) | Host di riferimento per la build linux |
| **Odroid** aarch64 (Ubuntu) | `linux/arm64` | — | Architettura del server; usabile anche come build host del client arm64 |
| CI (GitHub Actions) | `linux/amd64` + `windows/amd64` | — | `ubuntu-latest` per assets linux, `windows-latest` per assets windows; job separati |

## 3. Strumenti per host

### 3.1 Comuni a tutti gli host

- **Go** ≥ 1.22 (verificato: 1.27.1); rete verso `proxy.golang.org` (GOPROXY)
- **GNU make** (target del Makefile: `assets`, `build-linux`, `build-windows`,
  `build`, `test`)
- **`CGO_ENABLED=0`** per ogni cross-build: le dipendenze previste
  (`golang.org/x/sys`, `github.com/BurntSushi/toml`) sono pure-Go → **nessun gcc
  richiesto** per il cross

### 3.2 Host Linux (Debian/Ubuntu — build `linux/*` completa)

```bash
# strumenti di build
sudo apt install golang-go make

# pacchetti sorgente degli ASSET (quelli embeddati in assets/linux/):
sudo apt install qemu-system-x86 qemu-utils ovmf virt-viewer \
                 qemu-bridge-helper
```

- `qemu-system-x86_64` → `assets/linux/qemu/`
- `qemu-img` → `assets/linux/qemu/`
- `/usr/share/edk2/ovmf/*.fd` → `assets/linux/ovmf/`
- `remote-viewer` (o `virt-viewer`) → `assets/linux/tools/`
- `qemu-bridge-helper` → `assets/linux/qemu/` (opzionale, rete bridged)

Fedora/RHEL (host di riferimento nel piano): `dnf install golang make qemu-system-x86
qemu-img edk2-ovmf virt-viewer` (percorsi firmware `/usr/share/edk2/ovmf/`).

### 3.3 Host Windows (questa macchina — build `windows/amd64` completa)

- Go 1.27.1 via scoop (`scoop install go`) + git-bash (make se serve: `scoop install make`)
- Per `make assets` (Windows): **installazione QEMU per Windows** (direttore QEMU,
  `qemu-system-x86_64.exe`, `qemu-img.exe`), `virt-viewer.exe` (client SPICE),
  driver **TAP-Windows6** (OpenVPN: `tapinstall.exe`/`tapctl.exe` + `.inf/.sys`),
  **Wintun** (`wintun.dll`/`wintun.sys`, WireGuard) — copiati in `assets/windows/`
- Dev-mode: la chiave **`LAUNCH_NBD_ASSETS_DIR`** sostituisce l'extract del binario
  embed con una dir esterna → permette di sviluppare/testare (M0–M6) **senza**
  popolare `assets/` (embed solo nelle build release)

### 3.4 Odroid aarch64 (Ubuntu jammy — build `linux/arm64`)

```bash
sudo apt install golang-go make qemu-system-aarch64 qemu-utils edk2-ovmf virt-viewer
GOOS=linux GOARCH=arm64 make build
```

Se serve il client **amd64** dall'Odroid (macchine host x86_64 in rete):
`GOOS=linux GOARCH=amd64 CGO_ENABLED=0 make build` — nessun cross-gcc necessario.

## 4. Verifica toolchain (check per host)

```bash
# ovunque
go version && go env GOOS GOARCH GOPROXY CGO_ENABLED

# build host linux: presenza sorgenti degli asset
qemu-system-x86_64 --version && qemu-img --version
test -d /usr/share/edk2/ovmf && ls /usr/share/edk2/ovmf | head
remote-viewer --version || virt-viewer --version

# smoke cross (dovunque), fa fallire l'embed se manca assets/<GOOS>:
#   (build-linux dal Makefile: deve FALLIRE con messaggio se assets/linux/ è vuoto)
```

## 5. Contratto Makefile (da B0)

```make
make assets          # popola assets/<GOOS> dalla piattaforma corrente (o QEMU_DIR)
make build-linux     # GOOS=linux  GOARCH=amd64; FAIL se assets/linux/ è vuoto
make build-windows   # GOOS=windows GOARCH=amd64; FAIL se assets/windows/ è vuoto
make build           # piattaforma corrente
make test            # go vet + go test ./...
```

Regola anti-sorpresa: ogni target `build-<GOOS>` verifica la presenza di almeno un
file non-stub in `assets/<GOOS>/` prima di lanciare `go build`, così un host
sbagliato fallisce subito con un messaggio chiaro invece di produrre un binario
senza asset.

## 6. Dove si builda cosa (raccomandazione per il ciclo Fase B)

| Milestone | Host di sviluppo | Build completa | Esecuzione test |
|---|---|---|---|
| B0–B3 (config, assets extract, args, iso) | Windows dev | `windows/amd64` | Windows + unit |
| B4 (run/commit + stato ¥5.10) | Windows dev | — | mock NBD (unit, qui) |
| B5 (net/* Linux) | Windows dev | **Odroid o CI linux** | Odroid/CI (tap di test) |
| B6 (layer Windows) | Windows dev (admin) | `windows/amd64` | Windows smoke (+ Odroid per linux) |

## 7. Storia / decisioni

- 2026-09-24: verifica toolchain su dev Windows (Go 1.27.1): build nativa,
  cross `linux/amd64` + `linux/arm64` con `CGO_ENABLED=0`, dipendenze
  `x/sys v0.48.0` + `toml v1.6.0` fetch+compile ok. WSL non installato → i test
  Linux-only non girano su questo host.
- 2026-09-24: **requisito go abbassato a 1.22** — `golang.org/x/sys v0.48.0`
  richiede go 1.26 (troppo nuovo per host build datati): pin a
  **`golang.org/x/sys v0.30.0`** (go 1.18) + `github.com/BurntSushi/toml v1.6.0`
  (go 1.18) nel `go.mod`.

## 8. Strumenti usati (log in itinere, Fase B)

Registro dei tool realmente impiegati milestone per milestone, per i futuri
rebuild su altri host. Asset embeddati: `docs/assets.md`.

| Data | Fase | Strumenti | Note |
|---|---|---|---|
| 2026-09-24 | B0 (M0) | `go 1.27.1` (build nativa + cross `GOOS=linux GOARCH=amd64/arm64`), `gofmt`, `go vet`, `go test`, `go build`, deps `x/sys v0.30.0` + `toml v1.6.0` (go 1.22) | suite verde; cross-check amd64+arm64; nessun embed asset (M1); **WSL assente** → test Linux-only solo cross-compile |