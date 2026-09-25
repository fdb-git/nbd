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

## 2. Client launcher (`launch-nbd`, in Go) — DA FARE

Branch `feature/launch-nbd-go` (su `960470b`, base aggiornata; nessun codice Go).
Contratto del marker: **`docs/status-format.md`** (non le tabelle storiche dei plan).

- [ ] **§5.10 stato cross-host**: estendere `internal/nbd/client.go` con
      `readState`/`writeState` + `NBD_CMD_FLUSH`; guard in
      `run`/`direct`/`install`/`commit` (committing → fail-stop, resume solo owner
      con overlay); chiave `NBD_STATE_PORT` nei default di piattaforma. Nota:
      writeState scrive il blocco 4 KiB intero (owner+hash+stato) e la semantica
      di ctime va decisa (aggiornarlo o lasciarlo all'init server?)
- [ ] **M0**: `go.mod`, skeleton package, sottocomandi, wizard `configure` + TOML
      + preflight accel Linux
- [ ] **M1**: `internal/assets` embed/extract/path-resolver + target make
      (`LAUNCH_NBD_ASSETS_DIR` per dev-mode, embed solo in release)
- [ ] **M2**: `qemu/args.go` per `run`/`install` con golden test
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