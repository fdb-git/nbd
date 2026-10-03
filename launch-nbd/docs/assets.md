# Assets inclusi nel binario `launch-nbd`

**Scopo:** inventario dei file embeddati nel binario Go (via `//go:embed`,
Fase B M1) e dei tool esterni usati per popolarli. Aggiornato **in itinere**
con la Fase B: ogni asset aggiunto/rimosso va registrato qui.

**Meccanismo (M1, implementato):** l'albero `internal/assets/<GOOS>/` viene
incorporato da `internal/assets` (`embed_linux.go` / `embed_windows.go`,
build-tag) ed estratto a runtime in una dir temp da `extract()`; `Set.Resolve`
mappa i nomi logici (`qemu`, `qemu-img`, `viewer`, `bridge-helper`) sul file
estratto e `Set.Cleanup()` (retry su Windows) rimuove la temp all'uscita.
`Staged()` rileva se l'albero contiene asset reali o solo marker.

**Perché l'albero sta in `internal/assets/` e non in `assets/`:** `//go:embed`
non può risalire a `../`. Inoltre i file che iniziano con `.` sono esclusi dal
default: si usa `//go:embed all:<os>` e si versionano solo i marker `.keep`
(il resto è gitignored), così l'embed compila anche su un clone pulito.

**Dev-mode:** `LAUNCH_NBD_ASSETS_DIR=<dir>` fa risolvere i binari da una dir
esterna (layout `<GOOS>/{qemu,ovmf,tools}`) invece che dall'extract → sviluppo
e smoke test senza popolare l'albero; l'embed resta per le build release.

**Fallback:** senza asset reali (`Staged()==false`) `Prepare` non estrae nulla
e `Resolve` restituisce `""`; il chiamante ripiega sui binari di sistema
(`cfg.QEMU`/`PATH`). Non è un errore fatale per il dev host.

---

## Inventario pianificato (da plan-go.md §4 — M1)

### Linux (`internal/assets/linux/`)

| Risorsa | Sorgente (pkg host) | Destinazione | Note |
|---|---|---|---|
| QEMU system | `qemu-system-x86_64` (pkg `qemu-system-x86`) | `internal/assets/linux/qemu/` | ~100-300 MB |
| qemu-img | `qemu-img` (pkg `qemu-utils`) | `internal/assets/linux/qemu/` | |
| firmware OVMF | `/usr/share/edk2/ovmf/OVMF_CODE.fd`, `OVMF_VARS.fd` (pkg `ovmf`) | `internal/assets/linux/ovmf/` | VARS copiata scrivibile a runtime |
| firmware SeaBIOS | `/usr/share/seabios/` (fallback) | `internal/assets/linux/ovmf/` | solo se OVMF assente |
| client SPICE | `remote-viewer` / `virt-viewer` (pkg `virt-viewer`) | `internal/assets/linux/tools/` | fallback flatpak |
| reset bridge | `qemu-bridge-helper` (pkg `qemu-system-common`) | `internal/assets/linux/qemu/` | rete bridged (root o helper) |

### Windows (`internal/assets/windows/`)

| Risorsa | Sorgente (installazione Win) | Destinazione | Note |
|---|---|---|---|
| QEMU | `qemu-system-x86_64.exe` | `internal/assets/windows/qemu/` | |
| qemu-img | `qemu-img.exe` | `internal/assets/windows/qemu/` | |
| client SPICE | `virt-viewer.exe` | `internal/assets/windows/tools/` | al posto di remote-viewer |
| firmware | pkg OVMF per Windows | `internal/assets/windows/ovmf/` | |
| driver TAP | TAP-Windows6: `tapinstall.exe`/`tapctl.exe` + `.inf`/`.sys` (OpenVPN) | `internal/assets/windows/tap-ovpn/` | rete tap/dual L2, richiede admin |
| driver Wintun | `wintun.dll` + `wintun.sys` (WireGuard) | `internal/assets/windows/wintun/` | alternativa L3-only, no bridged |

---

## Log in itinere

| Data | Fase | Asset | Stato |
|---|---|---|---|
| 2026-09-25 | B0 | nessun embed (binario senza albero assets) | solo fallback `cfg.QEMU` |
| 2026-09-25 | **M1** | albero `internal/assets/<GOOS>` + marker `.keep`; `embed_{linux,windows}.go`, `extract`, `Resolve`, `Cleanup`, `Staged`; dev-mode `LAUNCH_NBD_ASSETS_DIR`; guardia Makefile | **fatto** — verificato end-to-end con asset finto (embed→estrazione→temp path→cleanup) e dev-mode; nessun asset reale ancora popolato (binari QEMU/firmware) |

## Regole

- Il binario buildato senza `internal/assets/<GOOS>/` **non è utilizzabile** per
  quel GOOS (manca QEMU): `make build-<GOOS>` fallisce se l'albero è vuoto
  (build-toolchain.md §5); `make build` nativo resta possibile in dev (fallback).
- Strumenti per popolare gli asset per host: build-toolchain.md §3.