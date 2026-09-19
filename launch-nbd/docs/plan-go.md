# Piano di reimplementazione `launch-nbd.sh` in Go

**Deliverable:** solo piano dettagliato (nessun codice in questa sessione). Constraint raccolti: embed completo dei binari (incluso `remote-viewer`), rete TAP via comandi di sistema esterni, target **Windows + Linux**, architettura di orchestrazione QEMU in pure Go.

---

## 1. Panoramica

Un unico binario `launch-nbd` prodotto con `go build`, che:

- esegue la **stessa logica** dello script (parsing config, download ISO con resume + SHA256, firmware UEFI, overlay/snapshot qcow2, rete, display, audio, monitor), organizzata in **sottocomandi**;
- **ingloba i binari dipendenti** (`qemu-system-x86_64`, `qemu-img`, `remote-viewer`/`virt-viewer`, firmware OVMF/SeaBIOS, `curl`/`sha256sum` per parità, `bridge-helper`) via `//go:embed`, estraendoli a runtime in una dir temporanea e invocandoli da lì;
- sostituisce il più possibile tool esterni con **stdlib Go**: `net/http`+`Range` per curl `-C -`, `crypto/sha256`, parsing argomenti custom.
- offre i sottocomandi `configure` (wizard interattivo che genera un file **TOML** documentato), `install` (provisioning una-tantum di un NBD nuovo tramite ISO), `run` (default), `commit` e `help`.

**Decisioni di superficie adottate:**
1. **ISO = solo provisioning**: nessuna sezione TOML, nessun ISO nei run normali; l'ISO si usa solo con `launch-nbd install` per installare un export NBD nuovo (poi `run` avvia direttamente dal disco, senza CD-ROM).
2. **`disk_mode = direct|overlay` solo nel TOML**; `--snapshot` resta flag CLI per il comando run (throwaway, prevale su `disk_mode`).
3. **`--daemon` rimosso**: sempre foreground, revert rete sempre eseguito, dir temp esterna sempre ripulita.
4. **`help` accettato anche come comando** (oltre a `--help`).

Nessuna dipendenza npm/go esterna se non `golang.org/x/sys` e `github.com/BurntSushi/toml` (parser/scrittore TOML, dettaglio sotto).

---

## 2. Struttura del progetto

```
launch-nbd/
├── go.mod
├── Makefile                    # build Linux/Windows + asset bootstrap
├── cmd/launch-nbd/main.go      # entrypoint: configure|install|run|commit|help
├── internal/
│   ├── config/                 # struct Cfg + merge TOML / env
│   │   ├── toml.go             # load/save Cfg in TOML (BurntSushi)
│   │   ├── interactive.go      # wizard "configure": prompt con default + help inline
│   │   ├── defaults_linux.go   # default piattaforma Linux (build-tag, = preset unificato)
│   │   └── defaults_windows.go # default piattaforma Windows (build-tag, = preset unificato)
│   ├── accel/                  # preflight accelerazione per configure (build-tag)
│   │   ├── detect_linux.go     # /dev/kvm, modprobe, gruppo kvm
│   │   └── detect_windows.go   # WHPX / HypervisorPlatform / HAXM legacy
│   ├── cli/                    # dispatch sottocomandi + flag residui (--snapshot, --set)
│   │   └── args.go
│   ├── iso/                    # download resume + checksum discovery (solo per install)
│   │   ├── download.go
│   │   └── checksum.go
│   ├── nbd/                    # mini client NBD (handshake+READ/WRITE/FLUSH):
│   │   │                       # fingerprint GPT e stato di commit cross-host (§5.10)
│   │   └── client.go
│   ├── install/                # provisioning NBD nuovo: scarica ISO, boot CD-first
│   │   └── install.go
│   ├── qemu/                   # costruzione ARGS + lancio + monitor + commit
│   │   ├── args.go             # assemble ARGS[] (spec: 1:1 con lo script)
│   │   ├── run.go              # foreground, segnali, exit code
│   │   ├── monitor.go          # stdio / unix socket HMP
│   │   └── qemuimg.go          # create (backing NBD), commit, snapshot cleanup, overlay hash-named
│   ├── net/                    # setup TAP/bridge IPTABLES via exec esterni
│   │   ├── tap.go              # /dev/net/tun, ip tuntap, addr, MASQUERADE, revert (Linux)
│   │   ├── tap_windows.go      # TAP-Windows6/Wintun: tapinstall, netsh, revert (Windows)
│   │   └── bridge.go           # qemu-bridge-helper / root path
│   ├── display/                # spice/gtk/sdl/vnc/none + client remote-viewer
│   │   ├── mode.go
│   │   └── spice.go
│   ├── audio/                  # rilevamento pipewire/pulse
│   │   └── detect.go
│   └── assets/                 # gestione binari embed + estrazione temp
│       ├── embed.go            //go:embed assets/...
│       ├── extract.go          # Estratto in dir temp; PATH; cleanup
│       └── platform.go         # mappa Windows/Linux (estensioni .exe)
├── assets/                     # NON versionato: riempito da make assets
│   ├── linux/qemu/…
│   ├── linux/ovmf/…
│   ├── linux/tools/…           # remote-viewer, curl, sha256sum
│   └── windows/qemu/…          # .exe
│       └── tap-ovpn/…          # TAP-Windows6: tapinstall.exe/tapctl.exe, .inf/.sys
│       └── wintun/…            # wintun.dll + wintun.sys (alternativa L3-only)
└── README.md                   # aggiornato
```

---

## 3. Decodifica delle funzionalità (mappa script → Go)

| Funzione script | Implementazione Go |
|---|---|
| config sorgente | `config/toml.go` (+ `envfile.go` legacy `.env`) — merge con precedenza CLI (> solo `--set`/`--snapshot`) > TOML > `.env` legacy > ambiente; file TOML mancante → warning e default |
| `configure` | `config/interactive.go` — wizard interattivo su terminale: per ogni parametro stampa nome, descrizione, valori ammessi e default tra `[]`; Enter = accetta default, `?` = help esteso; in output serializza un **file TOML** (default `launch-nbd.toml`) con commenti di documentazione, così come `.env.example` era il template documentato. Per `ACCEL` esegue un **preflight** piattaforma-specifico (`accel/detect_*.go`): rileva la disponibilità e propone l'**abilitazione** se non attiva |
| `install` (ISO) | comando dedicato `launch-nbd install [--iso <file\|URL>]` — `iso/download.go` GET con `Range: bytes=…` per resume (equivalente `curl -C -`), riprove `--retry 3`; `iso/checksum.go` — discovery in ordine dello script (`SHA256SUMS`, `.sha256`, locale), verifica `crypto/sha256`; poi boot VM con CD-ROM che **boota per primo** (bootindex 0), disco NBD dietro, per installare l'OS sull'export; una-tantum, non riguarda il run normale; nessuna sezione TOML |
| `commit` | comando dedicato `launch-nbd commit` — `qemu/qemuimg.go` esegue `qemu-img commit <overlay>`, guard-rail "no VM in uso", exit 0 |
| overlay/direct/snapshot | `disk_mode = direct\|overlay` chiave **solo TOML**; `--snapshot` flag CLI del run (overlay throwaway in temp con cleanup su exit via `defer`, prevale su `disk_mode` come nello script). Overlay persistenti **renamed con l'hash del disco**: `<HASH_Base64URL>.qcow2` (vedi §5.8) |
| cache/aio | `config` valida i valori (enumerazione identica), `qemu/args.go` traduce in `write-cache`/`cache.direct`/`cache.no-flush` |
| firmware UEFI | `assets` — OVMF_CODE/VARS embed; VARS copiata scrivibile in dir lavorativa se assente; fallback SeaBIOS se assenti |
| NBD blockdev graph | `qemu/args.go` — array `-blockdev` identico (nbd `reconnect-delay`, `cache.direct`, `cache.no-flush`; qcow2 `cache-size`/`l2-cache-size`; `iothread` dedicate) |
| boot order | `run`: solo disco NBD (**nessun CD-ROM** montato — output args più snello); `install`: CD-ROM boot 0 + `virtio-blk` (stessa semantica script) |
| display | `display/mode.go` — `auto`/`gtk`/`sdl`/`vnc`/`spice`/`none`; alias `--vnc`; banner warning identici |
| SPICE client | `display/spice.go` — lancia `remote-viewer` (binary embed) o `flatpak run org.virt_manager.virt-viewer` come fallback, `sleep 2` + `exec` |
| virtio-serial/vdagent | in args.go: `virtio-serial-pci` + `virtserialport` `com.redhat.spice.0`, chardev `spicevmc` (spice) / `qemu-vdagent` (gtk/vnc) con `clipboard=on` |
| audio | `audio/detect.go` — check socket pipewire/pulse Auto (e `.env` override) |
| rete dual/nat/hostonly | args build (`-nic user` + hostfwds), no root richiesto |
| rete tap/dual | `net/tap.go` — via `os/exec` esterni: `sudo ip tuntap add`, `ip addr`, `sysctl`, `iptables MASQUERADE`; salvataggio stato originario + **revert deterministico** (lista comandi, eseguiti al revoke in ordine inverso; sempre eseguito: nessun daemon) |
| rete tap/dual (Windows) | `net/tap_windows.go` — installa/riusa l'adapter **TAP-Windows6** (driver OpenVPN embed) via `tapinstall.exe`/`tapctl.exe`, `netsh interface ip set address` + NAT Windows (`netsh routing`); rete solo L3 con `wintun.sys`/`wintun.dll` (no bridged); revert + warn; fallback `nat`/`hostonly`/`none` |
| rete bridged | `net/bridge.go` — root (`ip tuntap`, `master` bridge) oppure `qemu-bridge-helper` embed se aggiunto in `assets` |
| monitor | `qemu/monitor.go` — `-monitor stdio` o `unix:<sock>,server,wait=off`; HMP è gestito da QEMU, la Go solo costruisce l'arg |
| accelerazione | `auto` → check `/dev/kvm` (Unix) → `kvm`/`tcg`; a Windows: `whpx`/`haxm`/`tcg` (vedi §7); arg a due token `-accel <mode>` |
| audit/exit-code | `run.go` propaga `rc` di QEMU; snapshot cleanup; `exit $rc` |

---

## 4. Strategia di embedding dei binari

**Principio:** `//go:embed` può includere solo file *nel modulo*. Quindi QEMU (Linux ~100-300 MB, Windows simile) va **copiato in `assets/` prima del build** tramite `make assets`, non può stare in git.

**Flusso:**

1. `make assets` invoca uno script/platform-detector che copia da installazione sistema (o da percorso `QEMU_DIR`):
   - Linux: `/usr/bin/qemu-system-x86_64`, `/usr/bin/qemu-img`, `/usr/bin/remote-viewer`, `/usr/share/edk2/ovmf/*.fd`, `qemu-bridge-helper`, firmware;
   - Windows: `qemu-system-x86_64.exe`, `qemu-img.exe`, `virt-viewer.exe` (client SPICE Win) da installazione locale; **driver rete**: `tapinstall.exe`/`tapctl.exe` + `tap-windows6` (`.inf`/`.sys`) da installazione OpenVPN, e `wintun.dll`/`wintun.sys` (progetto WireGuard) per la variante L3-only.
2. `internal/assets/embed.go` espone `//go:embed assets/*` su un `embed.FS` **per-piattaforma** (build-tag: `assets_linux.go` / `assets_windows.go` montano l'albero giusto).
3. A runtime, `extract.go`:
   - crea `os.MkdirTemp("", "launch-nbd-*")`;
   - copia i file con permessi esecutivi (`0755`);
   - restituisce un `PathResolver` che risolve `qemu-system-x86_64` e `qemu-img` → percorso completo estratto (indipendente da PATH);
   - registra `defer os.RemoveAll(dir)` come "trap EXIT".
4. **Caveat (solo Windows):** i binari in-use bloccano l'eliminazione (`DeleteFile` occupato) → bisogno di retry/attesa dopo l'exit di QEMU. Su Linux il cleanup è immediato. Nessun vincolo daemon (feature rimossa): il processo QEMU finisce sempre prima del padre.

**Alternativa scartata:** embed di un *installer* che installa in `~/.cache/launch-nbd/` con lock — più robusto per i binari grandi; documentare nel piano come miglioria opzionale. Default scelto: estrazione in temp + cleanup.

---

## 5. Dettaglio moduli chiave

### 5.1 `internal/config`
Struct `Cfg` con tutti i campi (NBD_HOST, NBD_PORT, NBD_EXPORT, NBD_URI derivata, **NBD_STATE_PORT** — porta dell'export di stato cross-host, §5.10, MEM_MB, CPUS, ACCEL, NET_MODE, TAP_*, BRIDGE_IF, DISPLAY_MODE, SPICE_PORT, VGA_MODE, GL, AUDIO_*, CACHE_MODE, AIO, RECONNECT_DELAY, QCACHE_MB, L2_CACHE_MB, QEMU, QEMU_IMG, DISK_MODE, OVERLAY_DIR, MONITOR/MON_SOCK, PF[]). **Nessun campo ISO**: l'ISO vive solo come argomento di `install`.
- Merge: default → TOML (opzionale `.env` legacy) → CLI (`--set`/`--snapshot`). Identico ordine prioritario dello script.
- `toml.go`: `Load` legge `launch-nbd.toml` (o `--config <file>`), `Save`/`Generate` serializza con commenti; `envfile.go` resta solo per compatibilità col vecchio `.env`.
- `interactive.go`: wizard `configure` che itera una tabella delle opzioni (nome, descrizione, valori ammessi/enum, default), legge da stdin con prompt `nome [default]: `, valida l'input, e al termine scrive il TOML documentato (ogni chiave con commento di spiegazione e **equivalente env** `LAUNCH_NBD_*`, come l'`.env.example`). Per `ACCEL` richiama il **preflight §5.9** (`accel/detect_*.go`). Flag `--config <path>` e `--yes`/`--force` (usa tutti i default senza chiedere, per l'uso non interattivo/CI).
- **Default di piattaforma unificati** (`defaults_linux.go`/`defaults_windows.go`, build-tag): un solo strato — niente più "default base" + "preset" separati. Ogni piattaforma ha la sua tabella completa di default (che include i valori dell'`.env.example` adattati a quella piattaforma secondo §7). Es.: Linux → `ACCEL=kvm`, `NET_MODE=dual`, `AUDIO_DRV=pipewire`; Windows → `ACCEL=whpx` (o `tcg` se il preflight non è attivo), `NET_MODE=hostonly` (TAP/Wintun), `AUDIO_DRV=sdl`.
- **Fonte dei default proposti dal wizard**, in ordine: **1) TOML esistente** — se `launch-nbd.toml` (o `--config`) esiste già, i suoi valori correnti diventano i default dei prompt (rigenerando `configure` la config è modificata in-place, non resettata); **2) default di piattaforma unificati** di cui sopra. `--force`/`--yes` usa la stessa catena senza interazione.
- Validazione: check d'integrità di tutte le variabili (idem `for v in …; do [ -z ]`), quindi il binario risolve `QEMU`/`QEMU_IMG` **dai path embed**, non dal PATH di sistema.

### 5.2 `internal/cli` — sottocomandi e flag
Dispatch del primo argomento (o default `run`):
- **`configure`** `[--config <file>] [--force]` → wizard `config/interactive.go`, exit 0; il TOML generato è la sorgente per i run successivi.
- **`install`** `[--iso <file|URL>] [--config <file>]` → provisioning una-tantum: risolve ISO (URL → download resume+sha256; file → diretto), avvia la VM con CD-ROM boot 0 + disco NBD dietro per installare l'OS. Se `--iso` manca, stampa l'uso e l'help (exit 2); nessuna persistenza in TOML.
- **`run`** (default) `[--snapshot] [--set k=v ...] [--config <file>]` → legge TOML+env, avvia in foreground dal disco NBD (nessun CD-ROM); `--snapshot` prevale su `disk_mode`; `--set` fa override one-shot di qualunque chiave (es. `--set net=nat --set AIO=native`).
- **`commit`** → committa **solo** l'overlay il cui hash-nome corrisponde al disco attuale (§5.8); se c'è un `<hash>-committing.qcow2` lo **riprende** (comando idempotente/riavviabile); mismatched/stale → rifiuto senza `--force`; `-committed` → "nulla da committare" e proposta di delete; a fine ok rename del marker; exit 0. In parallelo aggiorna l'**export di stato server-side** (§5.10: scrive `committing`+FLUSH prima di qemu-img, `clean`+FLUSH a fine), così l'interruzione resta visibile anche agli host remoti.
- **`help`** / **`--help`** / **`-h`** → stampa l'help (uso di tutti i sottocomandi), exit 0; unknown → help su stderr + exit 1.
- Flag globali: `--config <file>` (tutti), `--quiet`. `--vnc` e gli altri flag dello script **non** vengono riproposti: sostituiti da chiavi TOML/env o da `--set`.

### 5.3 `internal/qemu/args.go`
Traduzione **1:1** delle sezioni dello script in `[]string`:
- FW args, VARS copy, check UEFI embed;
- `-accel` a due token;
- `-machine q35`, `-cpu max`, `-smp`, `-m`, `-object iothread,id=io0`;
- blockdev NBD qcow2/direct; `-device virtio-blk,…write-cache=bootindex`;
- CD_DRIVE presente **solo in `install`** (boot 0); in `run` nessun CD;
- audio; net; virtio-serial; VGA+GL; SPICE+display; monitor.

### 5.4 `internal/qemu/run.go`
- Sempre foreground: `cmd := exec.Command(resolvedQEMU, args...)` con `Stdin/out/err` ereditati; attende, cattura `ExitCode`, esegue cleanup snapshot, exit rc.
- `signal.Notify` per `SIGINT/SIGTERM` (e `os.Interrupt`/Ctrl-C): inoltra a QEMU e al client SPICE, poi prosegue cleanup.

### 5.5 `internal/net/tap.go`
- Rileva root (`uid != 0` → require `sudo`, con cache credenziali `sudo -v`);
- concede rw `/dev/net/tun` salvando mode, crea TAP `ip tuntap add dev … mode tap user …`, `ip addr`, `ip link set up`, sysctl ip_forward, `iptables -t nat -A POSTROUTING -s … ! -o … -j MASQUERADE`;
- build della lista di **revert** in struct (MASQ removed, ip_forward, tap delete, /dev/net/tun mode); esecuzione su `defer`, ordine inverso, **sempre eseguita** (nessun daemon);
- errori non fatali → warn (stesso comportamento bash).

### 5.6 `internal/display`
- `mode` risolve `auto`: presenza `DISPLAY`/`WAYLAND_DISPLAY` → gtk, altrimenti vnc;
- spice: costruisce `remote-viewer spice://127.0.0.1:PORT`, verifica presenza embed o flatpak, altrimenti warn di installazione (come script).

### 5.7 Layer Windows
`internal/assets/platform.go`:
- mappa estensioni: no `.exe` su Linux, `.exe` su Windows.
- In Windows non esistono `/dev/net/tun`, `iptables`, pipewire, `/dev/kvm`:
  - **TAP**: modalità `tap`/`dual` → installare/riusare l'adapter **TAP-Windows6** estratto dagli asset (con `tapinstall.exe`/`tapctl.exe`, richiede admin), configurarlo con `netsh interface ip set address`/`set dns` e attivare NAT via `netsh routing ip nat`; il bridge L2 resta `warn` e fallimento controllato.
  - **Wintun** (opzionale): device L3-only (`wintun.sys` + API `wintun.dll`) → adatto a `nat`/routing, **non** a bridged; usato come alternativa/try-before-TAP.
  - revert deterministico su `defer` (disinstallazione adapter TAP / liberazione handle Wintun), sempre eseguito;
  - accel → whpx/haxm/tcg; audio backend → `sdl`/`none`; niente `remote-viewer` → usare `virt-viewer.exe` embed.

### 5.8 Provenienza e stato dell'overlay (stale-guard)

**Problema:** un overlay qcow2 è valido solo rispetto allo **stato esatto del backing** visto alla creazione. Committare un overlay di un'installazione precedente sull'export corrente "spinge" contenuti stantii sopra il disco nuovo → corruzione. Inoltre un **commit interrotto** lascia il disco NBD in stato ibrido (parte delle modifiche espulse, il resto ancora nell'overlay): avviare `direct`/`run` in quel punto = boot su disco corrotto.

**Pattern scelto — identità E stato nel filename (niente sidecar):**
1. **`internal/nbd/client.go`** — mini client NBD (handshake + `NBD_CMD_READ`) in pura stdlib Go: legge **primi 64 KiB + ultimi 64 KiB** dell'export (GPT/protective MBR head + partition table, disk GUID all'offset 96, e backup GPT in testa). Queste regioni cambiano con ogni installazione/resize.
2. **`Fingerprint(export)`** → `SHA-256` delle regioni lette → `Base64URL` **senza padding e senza `/`, `+`, `=`** → id filesystem-safe, es. `AqJUa4Xw0n...`.
3. **Filename con stato**: `OVERLAY_DIR/<hash>(-<state>).qcow2`, dove `<state>` è uno di:
   - *(assente)* = `live`: overlay con deltas non ancora espulsi, usabile da `run`;
   - `-committing` = commit **in corso o interrotto** (marker persistente);
   - `-committed` = commit completato (overlay vuoto, candidato a delete).
4. **Lifecycle `commit`** (guardie: nessuna VM in uso + `Fingerprint(NBD) == <hash>` del file):
   a. rename atomico `<hash>.qcow2` → `<hash>-committing.qcow2` **prima** di avviare qemu-img: se il processo è ucciso (SIGKILL incluso) il marker resta e chi viene dopo lo vede. Il rename lo fa la Go tra spawn ed exit di qemu-img → nessun lock concorrente sul file;
   b. `qemu-img commit <hash>-committing.qcow2`;
   c. exit 0 → rename → `<hash>-committed.qcow2` (oppure delete); exit ≠ 0 o segnale → resta `-committing`, comando idempotente/riavviabile da capo (interruzione innocua: l'overlay conserva tutti i dati e la riscrittura dei cluster converge).
5. **Guard su ogni invocazione** (`run`, `direct`, altro `commit`):
   - esiste `<hash>-committing.qcow2` → **blocca tutto tranne la ripresa**: "commit interrotto: ri-esegui `launch-nbd commit` per completarlo". Vale anche per `direct` e `--snapshot`: disco parzialmente aggiornato = corruzione, nessuna modalità ammessa finché il commit non è finito;
   - esiste `<hash>-committed.qcow2` → nulla da committare: `commit` lo riporta e propone la delete; `run` può continuarci sopra (overlay vuoto);
   - qualsiasi `<hash2>.qcow2` con hash diverso → overlay di un'altra installazione, rifiutato (stale per definizione, mai usato/committato).
6. **In `run` (`disk_mode=overlay`)**: calcola `h = Fingerprint(NBD)`; usa `OVERLAY_DIR/<h>.qcow2` se esiste e non c'è un `<h>-committing` attivo; se manca → warning "nessun overlay valido per il disco attuale" e propone ricreo o `--snapshot`. Gli overlay stale restano in sede ma non matchano mai → inerti, rotazione gratuita.
7. **Nota sul limite del fingerprint**: il GPT in testa di solito non è toccato dalle scritture dell'OS → il fingerprint **non** rileva da solo un commit interrotto; è il marker `-committing` che lo rende rilevabile a livello applicativo. Il caso "host remoto che non vede il marker locale" è coperto dall'export di stato server-side **§5.10**.
8. Dischi troppo piccoli (size < 128 KiB o export vuoto) → niente overlay persistenti, warn.
9. **Snapshot** (`--snapshot`) non ha bisogno di fingerprint/stato: throwaway, file in temp, cleanup `defer` all'exit; non committabile.

### 5.9 Preflight accelerazione (in `configure`)
Il wizard, alla voce `ACCEL`, esegue un rilevamento **diverso per piattaforma** (`internal/accel`, build-tag) e, se l'accelerazione è disponibile ma **non attiva**, propone i comandi di abilitazione e li esegue previa conferma (elevando con `sudo` su Linux, elevation PowerShell su Windows). Esito: default del prompt (`kvm`/`whpx`/`tcg`) e, se l'abilitazione è andata a buon fine, `ACCEL` già valorizzata nel TOML generato. **`ACCEL` sovrascrive il default di piattaforma §5.1**: il preflight dinamico prevale, perché l'accelerazione effettiva si scopre solo a runtime/macchina.

**Linux (`detect_linux.go`):**
1. vendor CPU da `/proc/cpuinfo`: `vmx` → Intel VT-x, `svm` → AMD SVM; nessuno dei due → warn "virtualizzazione disattivata nel BIOS/sistema" → default `tcg`.
2. se manca `/dev/kvm` → propone `sudo modprobe kvm_intel` (o `kvm_amd`) e ricontrolla.
3. se `/dev/kvm` esiste ma non scrivibile dall'utente (non nel gruppo `kvm`) → propone `sudo usermod -aG kvm $USER` (richiede ri-logon) oppure registra la necessità di `sudo` nei run.
4. ok → default `kvm`.

**Windows (`detect_windows.go`):**
1. rilevamento **WHPX**: presenza `WinHvPlatform.dll`/`WinHvEmulation.dll` in `System32` e/o feature `HypervisorPlatform` (`Get-WindowsOptionalFeature -Online -FeatureName HypervisorPlatform`) o `VirtualMachinePlatform`.
2. se manca → propone comando elevato `Enable-WindowsOptionalFeature -Online -FeatureName HypervisorPlatform -All` (+ eventuale reboot) e ricontrolla; nota se la virtualizzazione risulta disabilitata nel firmware via `systeminfo` ("Virtualization Enabled In Firmware: No").
3. fallback legacy: driver **HAXM** (`IntelHaxm.sys`, deprecato, solo Intel) se presente.
4. ok → default `whpx`, altrimenti `tcg`.
Resta invariato il comportamento a `run` (ACCEL=auto ricontrolla a runtime, vedi §3/§7).

### 5.10 Stato di commit cross-host (status export server-side)

**Problema.** §5.8 rende un commit interrotto rilevabile e obbligatorio per gli **host locali** (marker `-committing` nel filename). Il marker però vive nel filesystem dell'host: un client/VM avviato da un **altro host** (stesso export NBD) non lo vede e può partire su `direct`/`--snapshot` mentre il disco è ibrido. Il fingerprint (§5.8.7) non aiuta: le scritture dell'OS raramente toccano l'header/protective-MBR in testa.

**Decisione (opzione 1 — plugin `file`).** Il launcher, in ogni invocazione, legge **prima** di toccare il disco lo stato di commit da un **export di stato dedicato** servito lato server da nbdkit col plugin `file` su un **file persistente su disco** (sopravvive a riavvii e crash; diverso da `memory`/`tmpdisk` = volatili). Il cliente launcher scrive il marker con **`NBD_CMD_FLUSH`** → durabilità, non solo shutdown pulito. Il contratto completo lato server è pianificato in `export-nbd/docs/plan-state-export.md` (repository `export-nbd`).

**Contratto lato server (riassunto):**
- ogni host NBD espone `<export>.status` su una **porta dedicata fissa `NBD_STATE_PORT` (default 10819)**, servita da un'unica unit `nbd-export-state.service` con `nbdkit --foreground file dir=<STATE_DIR>` (mapping export-name→file del plugin `file`; richiede solo la **selezione per nome**, non l'advertising multi-export di nbdkit ≥1.38);
- l'unit di stato **non** porta i filtri `limit=1`/`multi-conn` (riservati all'unit dati): più client la leggono in concorrenza con l'unit dati senza consumarne lo slot;
- file di stato **4 KiB fissi**, creato con `truncate` all'init, servito con FLUSH/FUA supportati da nbdkit (regular file → `can_fua`/`flush` attivi, da verificare in 1.36.3).

**Formato del file di stato (4 KiB binari, scrittura mono-blocco zero-padded):**

| offset | size | campo |
|---|---|---|
| 0 | 8 | magic `NBDST\0\0\0\1` |
| 8 | 1 | version (1) |
| 9 | 1 | stato `enum {0=clean, 1=committing, 2=committed}` |
| 10 | 6 | riservato (zero) |
| 16 | 64 | owner: hostname (zero-padded) |
| 80 | 36 | owner: UUID sessione (zero-padded) |
| 116 | 256 | overlay hash Base64URL (§5.8) |
| 372 | 8 | ctime unix (little-endian) |
| 380 | 3716 | padding zero |

**Client `internal/nbd/client.go` esteso:**
- `readState(export)` / `writeState(export, state, owner, hash)` riusano l'handshake e i `READ`/`WRITE` del mini-client, sempre sul singolo blocco da 4 KiB, inviando `NBD_CMD_FLUSH` dopo ogni scrittura;
- URI derivata da `NBD_STATE_HOST` (= `NBD_HOST`) + `NBD_STATE_PORT`; export name = `<export>.status`.

**Guardie:**
- `run` (overlay/direct) e `--snapshot` e `install` — prima di qualunque avvio leggono lo stato: `state=committing` → **fail-stop** ("commit interrotto lato server: completa prima `launch-nbd commit`"); l'owner stesso (hostname+UUID) con overlay presente viene lasciato riprendere il flusso;
- `commit` — sequenza: scrivi `committing`+FLUSH → `qemu-img commit` → scrivi `clean`+FLUSH. Qualunque interruzione (SIGKILL, crash host, rete) lascia il marker **persistente** lato server, visibile a ogni host successivo;
- fallback: se l'export di stato è irraggiungibile → `commit` non parte (safe), i soli `run` desktop leggono ma avvisano se anche il disco è in stato di commit secondo §5.8 (fallback host-locale, warning esplicito).

**Relazione con §5.8.** La stale-guard filename resta intatta (difesa host-locale e idempotenza); lo status export aggiunge la copertura **cross-host** senza sidecar locali aggiuntivi. Config: chiave `NBD_STATE_PORT` nei default di piattaforma (§5.1/§7); niente ISO/TOML extra.

---

## 6. Pipeline build (Makefile)

```
make assets         # copia i binari della piattaforma corrente in assets/
make build-linux    # GOOS=linux GOARCH=amd64
make build-windows  # GOOS=windows GOARCH=amd64  (da BUILD host Linux)
make build          # piattaforma corrente
make test
```

**Importante:** cross-build richiede che `assets/<GOOS>/*` siano già presenti; regola nel makefile: fallisce se l'albero della piattaforma target è vuoto. Il makefile può includere un target `assets` con `find`/`cp` dei binari dalla macchina di sviluppo.

---

## 7. Differenze piattaforma (tabella requirement Windows vs Linux)

| Area | Linux (Fedora) | Windows |
|---|---|---|
| QEMU bin | `qemu-system-x86_64` | `qemu-system-x86_64.exe` |
| qemu-img | `qemu-img` | `qemu-img.exe` |
| SPICE client | `remote-viewer` | `virt-viewer.exe` |
| firmware | OVMF `/usr/share/edk2/ovmf` | OVMF pkg (dir MSYS / installazione locale) |
| accel | kvm/tcg | whpx/tcg (haxm deprecato) |
| TAP/net | `/dev/net/tun` + `ip tuntap`/`iptables` | TAP-Windows6 (OpenVPN) embed: `tapinstall.exe`/`tapctl.exe` + `.inf`/`.sys` → adapter `tap` (L2, supporta nat/dual); alternativa L3-only `wintun.sys`/`wintun.dll` (no bridged); fallback `nat`/`hostonly`/`none` |
| audio | pipewire/pulse | `sdl`/`none` |
| segnali | SIGINT/SIGTERM | os.Interrupt / handler Ctrl-C |
| path temp file lock | delete immediato | retry su `DeleteFile` occupato |

---

## 8. Testing

- **Unit:** parser TOML e CLI (dispatch sottocomandi, unknown → help/exit 1, `--set k=v`, `--snapshot`); round-trip TOML (Save → Load → uguale Cfg); wizard `configure` con stdin simulato (typing via `io.Pipe`): default, input valido, input invalido + re-prompt, `--force`; `install` senza `--iso` → uso+exit 2; checksum discovery con server test HTTP temporaneo (httptest); conversione cache→blockdev; filtro ISO URL vs file (solo `install`); **fingerprint NBD**: mock di una export (ring buffer NBD in-test), stabilità hash, cambio GPT → hash diverso, filename `<hash>(-stato).qcow2` atteso; `commit` su overlay mismatched → rifiuto; lifecycle stato: rename `live`→`-committing` pre-spawn, simulazione interruzione (KILL) → marker persistente e guard che blocca `direct`/`run`, ripresa del commit → `-committed`/delete; **stato export §5.10**: mock NBD con `WRITE`+`FLUSH` — verifica che un `committing` visto da un "secondo host" blocchi `run`/`direct`/`--snapshot`, che `commit` scriva la sequenza committing→FLUSH→clean e che il formato 4 KiB (magic/offset/padding, round-trip read/write) sia stabile; **preflight accel** Linux con `/dev/kvm`/`/proc/cpuinfo` simulati (manca→modprobe proposto; presente ma no gruppo→usermod; tutto ok→kvm).
- **Integration (Linux):** fake-QEMU (script/binario Go) per validare gli ARGS generati (dump args) sia per `run` (senza CD) sia per `install` (CD boot 0), senza avviare VM vera; test tap setup usando un TAP di test con revert.
- **Windows:** smoke test su build Windows (solo `--set net=none` con TCG) eseguito manualmente.

---

## 9. Fasi di implementazione (proposta milestones)

1. **M0** — scaffold Go: `go.mod`, package skeleton, sottocomandi (`configure`/`install`/`run`/`commit`/`help`), `--help`, exit-code, **wizard `configure` + TOML save/load + preflight accel Linux**; unit test dispatch, parser e wizard (compilazione ok).
2. **M1** — `internal/assets`: embed/extract/path-resolver + makefile target; smoke test binario embed e run `--version` di qemu estratto.
3. **M2** — `qemu/args.go`: generatore ARGS per `run` (senza CD) e `install` (CD boot 0) per tutte le modalità (senza lanci), con snapshot test "golden file" confrontati all'output atteso dello script.
4. **M3** — `iso` + `install`: download resume + SHA256 + discovery; test con httptest.
5. **M4** — `qemu/run.go` + `commit` + monitor + snapshot cleanup (`--snapshot`) + **stato commit cross-host §5.10** (read guard in `run`/`direct`/`install`, write `committing`→`clean` + `FLUSH` in `commit`; test con mock NBD `WRITE`+`FLUSH`).
6. **M5** — `net/*` (tap/bridged/dual) Linux; revert testata.
7. **M6** — `display` + `audio`, **preflight accel Windows** (`accel/detect_windows.go`: WHPX/HAXM), Windows platform layer (`assets/platform.go`, `net/tap_windows.go` con TAP-Windows6/Wintun), polish banner/log/messaggi.

---

## 10. Rischi / note

- **Dimensione binario:** embed di QEMU (circa 200-500 MB) → build lunga e binario enorme; documentare e valutare `go:embed` + gzip vs lay estratto (scelta: no compressione per QEMU, eventualmente gzip per firmware piccoli).
- **Cross-build:** impossibile compilare asset Windows su Linux senza installazione Win di QEMU → makefile con check.
- **Drivers Windows:** l'installazione di TAP-Windows6/Wintun richiede **privilegi admin** e firma driver valida (reboot o RPC di servizio) → errore/fallback controllato a `nat`/`hostonly`; Wintun è L3-only (niente bridged) e l'uso diretto da QEMU è limitato, documentare il caso.
- **TOML:** unica dipendenza aggiunta `github.com/BurntSushi/toml`; validare la catena load/merge con i tipi (int/string/bool) e documentare nel TOML generato la stessa superficie dell'`.env.example`.
- **Overlay stale / commit:** il filename `<hash>(-stato).qcow2` (§5.8) rende il commit su disco cambiato un errore strutturale (overlay vecchio ≠ hash del disco nuovo) e il marker `-committing` rende rilevabile e obbligatoria la ripresa di un commit interrotto (blocco di `direct`/`run` fino a completamento). Se l'utente vuole forzare un vecchio overlay su un disco nuovo (rebase), è fuori scope: `qemu-img rebase` manuale e cosciente, mai automatizzato.
- **HMP/block-commit:** resta delegato a QEMU (stringa) — nessuna implementazione.
- **Export di stato §5.10:** con nbdkit 1.36.3 il plugin `file` con `dir=` mappa gli export-name su file — serve solo la **selezione per nome**, non l'advertising multi-export di ≥1.38; verificare supporto `flush`/`fua` lato regular-file. L'unit di stato **non** deve portare i filtri `limit=1`/`multi-conn` (dedicati all'unit dati); dettagli e verifiche nel doc server `export-nbd/docs/plan-state-export.md`.
- **Obiettivo:** preservare la **stessa semantica** dello script, con la superficie CLI ridotta a 5 sottocomandi + `--snapshot`/`--set`/`--config`/`--help`; tutto il resto configurabile solo via TOML/env. Test golden garantiscono la semantica degli ARGS, non il testo esatto del banner.