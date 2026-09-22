# Formato dei file di stato NBD (`<name>.status`)

**Contratto cross-repo (canonico).** Definizione unica del formato dei file di stato
serviti dall'export di stato NBD. Le vecchie copie nei plan doc
(`export-nbd/docs/plan-state-export.md` §3 e `launch-nbd/docs/plan-go.md` §5.10) sono
**storiche e puntano a questo documento** — non duplicare la tabella altrove.

Stato: **implementato lato server** (`export-nbd/nbd-export.sh`), **pianificato lato
client** (Go, `launch-nbd/docs/plan-go.md` §5.10 — non ancora implementato). Le garanzie
reali sugli offset sono i test round-trip in `export-nbd/tests/test-state-*.sh`.

## Scopo

Marker di commit per export NBD: il client launcher scrive il byte di stato prima e dopo
un `qemu-img commit`; qualunque host legge il marker **prima di toccare il disco** e fa
fail-stop se lo trova `committing` (commit interrotto = overlay ibrido sul disco).

## Servizio

- **File:** `$STATE_DIR/<name>.status` (default `/var/lib/launch-nbd/state`), regolare,
  4 KiB fissi (creato con `truncate -s 4096` all'init; la size esposta da nbdkit = stat del file).
- **Export name:** `<name>.status` (nome dati + suffisso `.status`), es. `demo.status`.
- **URI:** `nbd://<host>:10819/<name>.status` — porta fissa `STATE_PORT` (default 10819).
- **Server:** unit `nbd-export-state.service` = `nbdkit --foreground file dir=$STATE_DIR`
  (plugin `file` con `dir=`: mapping export name → file; richiede solo la **selezione per
  nome**, l'advertising multi-export è nbdkit ≥1.38, best-effort).
- L'unit di stato **non** porta `--filter=limit`/`--filter=multi-conn`: lo stato si legge
  senza consumare lo slot `limit=1` dell'export dati.
- **Durabilità:** il client scrive il marker con un singolo `WRITE` (blocco 4 KiB
  zero-padded) seguito da **`NBD_CMD_FLUSH`**.

## Formato (4 KiB binari, mono-blocco zero-padded)

| offset | size | campo |
|---|---|---|
| 0 | 8 | magic `NBDST\0\0\0` |
| 8 | 1 | version (1) |
| 9 | 1 | stato `enum {0=clean, 1=committing, 2=committed}` |
| 10 | 6 | riservato (zero) |
| 16 | 64 | owner: hostname (zero-padded) |
| 80 | 36 | owner: UUID sessione (zero-padded) |
| 116 | 256 | overlay hash (Base64URL) |
| 372 | 8 | ctime unix (little-endian) |
| 380 | 3716 | padding zero |

Nota: la spec originale scriveva il magic come `NBDST\0\0\0\1` fondendo nell'escape il
version byte — qui è corretto: `NBDST\0\0\0` a offset 0–7, version a offset 8.
Il file nasce tutto zero → i campi scritti sono zero-padded.

## Semantica e regole client

- `committed` = commit completato ma marker non ancora azzerato (`clean`), per diagnosi.
- Lo stato **non** è un lock: è un marker. Il fail-stop è responsabilità dei client che lo leggono.
- Cambi di formato: aggiornare **solo** questo file e i test round-trip
  (`export-nbd/tests/test-state-*.sh`); i plan doc restano storici e non si toccano.

## Implementazione di riferimento

- `export-nbd/nbd-export.sh` righe 139–210: `state_init_file`, `state_set_marker`,
  `state_write_str`, `write_u64_le`/`read_u64_le`, `state_dump`.
- CLI: `nbd-export.sh state init|export|set|show|remove` (`usage_state`, riga ~1006) —
  gestione server, dettagli in `export-nbd/AGENT.md`.
- Test: `export-nbd/tests/test-state-*.sh` (unit, file, export, set, show, list-status,
  nbdkit-verify).

## Storico

- `export-nbd/docs/plan-state-export.md` §3 e `launch-nbd/docs/plan-go.md` §5.10:
  spec originali da cui nasce questo contratto.