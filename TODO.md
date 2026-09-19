# TODO — continuazione lavoro

Riprendere da: pianificazione completata, da passare a implementazione.
Sorgenti di riferimento: `launch-nbd/docs/plan-go.md` e `export-nbd/docs/plan-state-export.md`.

## 1. Server nbdkit (`export-nbd`) — implementare `docs/plan-state-export.md`

- [ ] Nuovo subcommand `state` in `nbd-export.sh`: `init|export|set|show|remove` (niente collisione col `status` esistente)
- [ ] Unit `nbd-export-state.service`: `nbdkit file dir=$STATE_DIR`, porta fissa 10819, **senza** `limit`/`multi-conn` (riservati all'unit dati)
- [ ] Variabili `.env`: `STATE_DIR` (default `/var/lib/launch-nbd/state`), `STATE_PORT` (10819), `STATE_ADDR`
- [ ] Verifiche nbdkit 1.36.3 (checkbox §5 del doc): `file dir=` selezione per nome, supporto `flush`/`fua`, `cmd_list`/`cmd_status` senza regressioni
- [ ] Test §7 del doc: persistenza del marker dopo restart dell'unit, `nbdinfo` sull'export di stato (no consumo slot `limit=1`), round-trip del campo stato

## 2. Client launcher (`launch-nbd`, in Go)

- [ ] **§5.10 stato cross-host**: estendere `internal/nbd/client.go` con `readState`/`writeState` + `NBD_CMD_FLUSH`; guard in `run`/`direct`/`install`/`commit` (committing → fail-stop, resume solo owner con overlay); chiave `NBD_STATE_PORT` nei default di piattaforma
- [ ] **M0**: `go.mod`, skeleton package, sottocomandi, wizard `configure` + TOML + preflight accel Linux
- [ ] **M1**: `internal/assets` embed/extract/path-resolver + target make
- [ ] **M2**: `qemu/args.go` per `run`/`install` con golden test
- [ ] **M3**: `iso` + `install` (download resume + SHA256 discovery, test httptest)
- [ ] **M4**: `qemu/run.go` + `commit` + monitor + snapshot cleanup + **stato §5.10** (mock NBD `WRITE`+`FLUSH`)
- [ ] **M5**: `net/*` Linux (tap/bridged/dual) con revert testata
- [ ] **M6**: `display`+`audio`, preflight accel Windows, Windows platform layer (TAP-Windows6/Wintun)

## 3. Open / da decidere (dal doc server)

- [ ] `--with-state` in `cmd_export` (creare il file `.status` insieme all'export dati)
- [ ] Sicurezza del marker (es. `--filter=ip` sull'unit di stato) — fuori scope v1, da rivalutare
- [ ] Advertising multi-export (nbdkit ≥1.38) per il discovery, se l'host dati verrà aggiornato