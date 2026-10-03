# TODO — continuazione lavoro

Riprendere da: **Fase A completata** (server state export, implementata e testata
dall'Odroid, commit `960470b` su `master`/`feature/export-nbd-state`). Prossimo
lavoro: **Fase B** (client launcher in Go, branch `feature/launch-nbd-go`).

Sorgenti: doc canonico **`docs/status-format.md`** (root — unica fonte del formato
`.status`), `launch-nbd/docs/plan-go.md`, operazioni `state` in `AGENTS.md` (root).

## 1. ✅ Server nbdkit (`export-nbd`) — export di stato (FATTO)

Implementato in `960470b` (12 commit `b2ab2e9..960470b`, master e
`feature/export-nbd-state`); tutti i punti del piano
`export-nbd/docs/plan-state-export.md` chiusi:

- [x] Subcommand `state` (`init|export|set|show|remove`) in `nbd-export.sh`
      (niente collisione col `status` esistente; nome `state` riservato)
- [x] Unit `nbd-export-state.service`: `nbdkit file dir=$STATE_DIR`, porta fissa
      **10819**, **senza** `limit`/`multi-conn`/`readonly` (riservati all'unit dati);
      `--user/--group` = owner della dir servita, stesso hardening
- [x] Variabili `.env`: `STATE_DIR` (default `/var/lib/launch-nbd/state`),
      `STATE_PORT` (10819), `STATE_ADDR` (0.0.0.0)
- [x] Verifiche nbdkit 1.36.3 (§5): evidenza in `tests/test-state-nbdkit-verify.sh`
      — selezione per nome `demo.status` (size 4096), `can_flush=True`, size esposta
      = stat del file, due client concorrenti accettati. Verificate prima su build
      user-space 1.36.3, poi su deb di sistema (1.24.1 di apt non bastava)
- [x] Test §7: 9 nuovi `tests/test-state-*.sh` (file/format round-trip, export,
      cmd, init, unit, list/status, auto-export, auto-reuse, nbdkit-verify);
      i no-root girano anche su macchine senza systemd
- [x] Contratto canonico: tabella spostata in **`docs/status-format.md`** con
      **correzione del magic** (`NBDST\0\0\0` 8 byte a offset 0–7 + version a 8,
      non `NBDST\0\0\0\1` fuso); plan doc resi storici

### Deviazioni dal piano (decise in implementazione)

- **Auto-create su `export <path>`**: crea `<name>.status` in `clean` e, se il
  server di stato è fermo, lo riavvia senza riscriverlo (la unit esistente è la
  fonte di verità, modello A: permanente, `enable` al boot). Opt-out con
  `--no-state` / `--no-status`. L'opzione `--with-state` della §8 è quindi nata
  come **default**, non come flag
- `remove <name>` elimina anche il `.status` (silenzioso; la unit state resta);
  `state remove <name>` elimina il solo file (export dati intatto)
- **ctime** è scritto una volta all'`init` del record e non viene aggiornato da
  `state set`/marker: indica la creazione del file, NON l'ultimo cambio di stato

## 2. Client launcher (`launch-nbd`, in Go) — IN CORSO (M0–M2 fatte)

Branch `feature/launch-nbd-go` (su `960470b`, base aggiornata). Contratto del marker:
**`docs/status-format.md`** (non le tabelle storiche dei plan).

- [x] **M0 (FATTO, B0)**: `go.mod` (go 1.22, `x/sys v0.30.0`, `toml v1.6.0`),
      skeleton `cmd/launch-nbd` + `internal/{cli,config,accel}`, sottocomandi
      `configure|install|run|commit|help` con exit-code 0/1/2 (unknown → 1,
      install senza --iso → 2), wizard `configure` + TOML save/load
      (round-trip, no-clobber) + `.env` legacy + `LAUNCH_NBD_*` env, preflight
      accel Linux (kvm: /proc/cpuinfo + /dev/kvm) e Windows (WHPX/HAXM);
      default di piattaforma build-tag; suite test verde (cli, config, wizard);
      Makefile con guardia anti-sorpresa su `assets/<GOOS>`; cross-check
      linux/amd64+arm64; toolchain/assets documentati in
      `launch-nbd/docs/{build-toolchain,assets}.md`. run/commit: solo stub
      (M2/M4)

- [ ] **§5.10 stato cross-host**: estendere `internal/nbd/client.go` con
      `readState`/`writeState` + `NBD_CMD_FLUSH`; guard in
      `run`/`direct`/`install`/`commit` (committing → fail-stop, resume solo owner
      con overlay); chiave `NBD_STATE_PORT` nei default di piattaforma. Nota:
      writeState scrive il blocco 4 KiB intero (owner+hash+stato) e la semantica
      di ctime va decisa (aggiornarlo o lasciarlo all'init server?)
- [x] **M1 (FATTO)**: `internal/assets` — `embed_{linux,windows}.go`
      (`//go:embed all:internal/assets/<GOOS>`, marker `.keep` versionati),
      `Prepare`/`Resolve`/`Cleanup`/`Staged`, `extract` testabile con
      `fstest.MapFS`, fallback a `cfg.QEMU` senza asset, dev-mode
      `LAUNCH_NBD_ASSETS_DIR`; Makefile con guardia su `internal/assets/<GOOS>`
      e target `check-assets`; wiring in `run` (summary mostra il qemu risolto);
      verificato end-to-end con asset finto (embed→temp→cleanup). Manca solo il
      popolamento reale dell'albero (binari QEMU/firmware) sul build host

- [ ] **M2**: `qemu/args.go` per `run`/`install` con golden test
- [x] **M2 (FATTO)**: `internal/qemu/args.go` — builder puro `Build(cfg, Facts)`
      1:1 con la sezione ARGS dello script (ordine e valori): accel a due token,
      cache_mode→write-cache/direct/no-flush, FW pflash/SeaBIOS, blockdev
      NBD/qcow2, virtio-blk+bootindex, CD solo in install (boot 0) o secondario
      (boot 1) in run, nic per modalità + hostfwd, virtio-serial/chardev per
      display, vga+gl, display/spice, audio, rtc, monitor stdio/socket;
      validazione enum con messaggi come lo script. 9 **golden file**
      (`internal/qemu/testdata/*.golden`, rigenerabili con `-update`) +
      test errori + fallback accel + boot order. Build è puro (nessun
      FS/ambiente): i fatti runtime arrivano in `Facts`

- [ ] **M3**: `iso` + `install` (download resume + SHA256 discovery, test httptest)
- [ ] **M4**: `qemu/run.go` + `commit` + monitor + snapshot cleanup + **stato §5.10**
      (mock NBD `WRITE`+`FLUSH`)
- [ ] **M5**: `net/*` Linux (tap/bridged/dual) con revert testata
- [ ] **M6**: `display`+`audio`, preflight accel Windows, Windows platform layer
      (TAP-Windows6/Wintun)

## 3. Open / da decidere

- [ ] Sicurezza del marker lato server (es. `--filter=ip` sull'unit di stato) —
      fuori scope v1, da rivalutare
- [ ] Advertising multi-export (nbdkit ≥1.38) per il discovery, se l'host dati
      verrà aggiornato
- [ ] Semantica ctime lato client: `writeState` aggiorna il campo o resta
      "creazione record" (lato server non lo tocca al marker)
- [ ] Decisione coesistenza: `launch-nbd.sh` resta accanto al binario Go durante
      la transizione (consigliato) — da chiudere in Fase B, non toccare prima
- [ ] Gate nbdkit-verify lato cliente: l'ordinamento "leggi stato → fingerprint →
      avvio" in run/direct/install (plan §5.8.7+§5.10) da verificare in M4