# Assets inclusi nel binario `launch-nbd`

**Scopo:** inventario dei file embeddati nel binario Go (via `//go:embed`,
Fase B M1) e dei tool esterni usati per popolarli. Aggiornato **in itinere**
con la Fase B: ogni asset aggiunto/rimosso va registrato qui.

**Meccanismo:** l'albero `assets/<GOOS>/` (NON versionato: popolato da
`make assets` sull'host giusto) viene incorporato da `internal/assets`
(embed.go con build-tag, M1) ed estratto a runtime in una dir temp
(extract.go, PathResolver, cleanup su exit).

**Dev-mode:** `LAUNCH_NBD_ASSETS_DIR=<dir>` fa risolvere i binari da una dir
esterna invece che dall'extract → sviluppo senza popolare `assets/`; l'embed
resta solo nelle build release (`make build-linux|build-windows`).

---

## Inventario pianificato (da plan-go.md §4 — M1)

### Linux (`assets/linux/`)

| Risorsa | Sorgente (pkg host) | Destinazione in assets/ | Note |
|---|---|---|---|
| QEMU system | `qemu-system-x86_64` (pkg `qemu-system-x86`) | `linux/qemu/qemu-system-x86_64` | ~100-300 MB |
| qemu-img | `qemu-img` (pkg `qemu-utils`) | `linux/qemu/qemu-img` | |
| firmware OVMF | `/usr/share/edk2/ovmf/OVMF_CODE.fd`, `OVMF_VARS.fd` (pkg `ovmf`) | `linux/ovmf/` | VARS copiata scrivibile a runtime |
| firmware SeaBIOS | `/usr/share/seabios/` (fallback) | `linux/ovmf/` | solo se OVMF assente |
| client SPICE | `remote-viewer` / `virt-viewer` (pkg `virt-viewer`) | `linux/tools/` | fallback flatpak |
| reset bridge | `qemu-bridge-helper` (pkg `qemu-system-common`) | `linux/qemu/` | rete bridged (root o helper) |

### Windows (`assets/windows/`)

| Risorsa | Sorgente (installazione Win) | Destinazione in assets/ | Note |
|---|---|---|---|
| QEMU | `qemu-system-x86_64.exe` | `windows/qemu/` | |
| qemu-img | `qemu-img.exe` | `windows/qemu/` | |
| client SPICE | `virt-viewer.exe` | `windows/tools/` | al posto di remote-viewer |
| firmware | pkg OVMF per Windows | `windows/ovmf/` | |
| driver TAP | TAP-Windows6: `tapinstall.exe`/`tapctl.exe` + `.inf`/`.sys` (OpenVPN) | `windows/tap-ovpn/` | rete tap/dual L2, richiede admin |
| driver Wintun | `wintun.dll` + `wintun.sys` (WireGuard) | `windows/wintun/` | alternativa L3-only, no bridged |

---

## Log in itinere

| Data | Fase | Asset | Stato |
|---|---|---|---|
| — | B0 | nessun embed (binario senza albero assets) | M1: `make assets` + `internal/assets` |
| — | M1 | primo albero `assets/<GOOS>` + embed/extract/PathResolver | da implementare |

## Regole

- Il binario buildato senza `assets/<GOOS>/` **non è utilizzabile** per quel
  GOOS (manca QEMU): `make build-<GOOS>` fallisce se l'albero è vuoto
  (build-toolchain.md §5).
- Strumenti per popolare gli asset per host: build-toolchain.md §3.