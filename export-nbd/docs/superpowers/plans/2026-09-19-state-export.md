# State Export (lato server) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Esporre lo stato di commit di ogni export NBD come file binario persistente da 4 KiB servito da un'unit dedicata `nbd-export-state.service`, con un nuovo subcommand `state` in `nbd-export.sh` (init/export/set/show/remove), così che client su altri host possano leggere il marker `committing` prima di toccare un disco (contratto in `docs/plan-state-export.md` §3).

**Architecture:** Una sola unit systemd `nbd-export-state.service` per host esegue `nbdkit --foreground file dir=$STATE_DIR --port=$STATE_PORT --ipaddr=$STATE_ADDR` — niente `--filter=limit`/`--filter=multi-conn`/`--readonly` (spec §2), hardened come le unit dati. Ogni export dati `<name>` ha un file `<name>.status` di 4 KiB (`magic NBDST`, version 1, stato 0/1/2, owner hostname/UUID, hash overlay, ctime LE). Il client launcher (repo `launch-nbd`) scrive il marker via NBD + `NBD_CMD_FLUSH`; lato server `state set` è il percorso amministrativo e scrive **solo il byte di stato** (offset 9), senza toccare i campi del client. Tutta la logica binaria è in helper bash puri testabili via sourcing.

**Tech Stack:** bash 4+ (`set -euo pipefail`), systemd, nbdkit 1.36.3 (plugin `file`), libnbd tools (`nbdsh`, `nbdinfo`) per i test, `dd`/`od` coreutils.

**Spec:** `docs/plan-state-export.md` (design approvato; open questions §8 escluse dal v1 — scelta dall'utente, opzione A).

## Global Constraints

- **Solo ruolo server** (AGENT.md §1.2): nessun codice client; il contratto è il formato file 4 KiB. Lato client = altro repo (`launch-nbd/docs/plan-go.md` §5.10).
- **Single bash script** (AGENT.md §1.1): state lives in unit generate + `STATE_DIR`; nessun file di config.
- **Un solo server di stato per host** (§2): unit unica `nbd-export-state.service` (`UNIT_PREFIX-state`), serve tutti i `<export>.status`.
- **Porta dedicata fissa** (§2): `STATE_PORT` default `10819`.
- **Nessun** `--filter=limit` / `--filter=multi-conn` / `--readonly` sull'unit di stato (§2) — più client leggono lo stato in concorrenza senza consumare lo slot `limit=1` dei dati.
- **Export name** `<name>.status` (§3); URI `nbd://<addr>:<STATE_PORT>/<name>.status`. Il plugin `file dir=` mappa l'export name su `$STATE_DIR/<name>.status` (selezione per nome; l'advertising multi-export ≥1.38 è best-effort, non requisito).
- **Formato file fisso 4 KiB** (§3), inizializzato con `truncate` all'init; **mai `rm` durante il run** (solo resize via `truncate`). Tabella offset:

  | offset | size | campo |
  |---|---|---|
  | 0 | 8 | magic `NBDST\0\0\0\1` |
  | 8 | 1 | version (1) |
  | 9 | 1 | stato `enum {0=clean, 1=committing, 2=committed}` |
  | 10 | 6 | riservato (zero) |
  | 16 | 64 | owner: hostname (zero-padded) |
  | 80 | 36 | owner: UUID sessione (zero-padded) |
  | 116 | 256 | overlay hash (Base64URL) |
  | 372 | 8 | ctime unix (little-endian) |
  | 380 | 3716 | padding zero |

- **Interpretazione magic (decisione del piano):** i byte 0-7 sono `NBDST` + 3 NUL (8 byte); il `\1` finale nella dicitura della spec è il **version byte = 0x01 a offset 8** (la spec mette version a offset 8 = 1). La sequenza completa 0-9 è `4e 42 44 53 54 00 00 00 01 00`.
- **Stato ≠ lock** (§3): il marker non blocca; il fail-stop è responsabilità dei client. `committed` = commit completato ma marker non azzerato (diagnosi).
- **Default** (§4.1): `STATE_DIR=/var/lib/launch-nbd/state`, `STATE_PORT=10819`, `STATE_ADDR=0.0.0.0`; override via `.env` (pattern `DEFAULT_PORT`).
- **Nessuna creazione automatica** dello stato dopo `export <path>` (§4.2): esplicito con `state export <name>`; `--with-state` fuori v1 (§8 opzione A).
- **Unit di stato** (§4.3): `--user/--group` = proprietario di `STATE_DIR` (modello "runs as owner"); hardening da `write_unit`: `NoNewPrivileges=yes`, `PrivateTmp=yes`, `ProtectHome=yes`, `ProtectSystem=full`, `RestrictAddressFamilies=AF_INET AF_UNIX`, `Restart=on-failure`, `After=network-online.target` + `Wants=network-online.target`.
- **TLS** (§4.4): stesse opzioni degli export dati — `--tls=require|on`, `--tls-certificates=$TLS_DIR_DEFAULT`; nessun `--psk` in v1.
- **Nome riservato:** `state` non può essere un export dati (colliderebbe con `nbd-export-state.service`) — guardia esplicita in `cmd_export` (requisito aggiunto dal piano, vedi Review Focus).
- **Root**: `state init` richiede root (scrive unit + systemctl); `state export/set/show/remove` operano solo su file e falliscono con messaggio chiaro su dir non scrivibile. `list`/`status`/`show` sono read-only.
- `NAME_RE='^[a-zA-Z0-9_.-]+$'` — `<name>.status` è un nome valido (i dot sono ammessi).

## Review Focus

Le cinque classi di input/fail mode più probabili che la spec non testa esplicitamente:

1. **Export dati col nome `state`** — scavalcherebbe `nbd-export-state.service`. Comportamento atteso: `nbd-export export ... --name state` fallisce con errore chiaro ("nome riservato"). → test in Task 6 (`reserved_name_check`).
2. **`list`/`status` con l'unit di stato attiva** — `extract_image` parserizzerebbe `file dir=<STATE_DIR>` come immagine (`dir=/...`). Comportamento atteso: `list` mostra solo export dati; `status state` mostra un blocco dedicato (dir, porta, tls), mai un'immagine `dir=...`. → test in Task 6 (`is_state_unit`).
3. **`state set` su un record toccato dal client** — non deve sovrascrivere owner/hash/ctime. Comportamento atteso: cambia solo il byte a offset 9, size resta 4096, gli altri byte identici. → test no-clobber in Task 5.
4. **File di stato di size diversa da 4096** (rimosso/ricreato male, troncato) — deve tornare a 4 KiB via `truncate` (mai `rm` durante il run); un file 4 KiB con magic sbagliato NON va toccato (errore). → test in Task 5; size-follows-file documentato in Task 1.
5. **Valore di stato non valido o file mancante** — `state set demo bogus` / `state show nope` devono uscire non-zero con messaggio pulito, senza scrivere nulla (nessuna scrittura parziale). → test in Task 5.

---

### Task 1: Verifica nbdkit 1.36.3 (spec §5) — gate prima del codice

**Files:**
- Create: `tests/test-state-nbdkit-verify.sh`
- Modify: `docs/plan-state-export.md` (spunta §5)
- Modify: `AGENT.md` (fatti confermati in §4)

**Interfaces:**
- Consumes: niente dal codice (gira contro nbdkit di sistema, NON contro `nbd-export.sh`).
- Produces: evidenza su cui poggia tutto il piano. Se un check fallisce → STOP, riportare all'utente (il design andrebbe rivisto): questo è il gate prescritto dalla spec §5 ("da confermare sul sistema target prima di implementare").

- [ ] **Step 1: Scrivi lo script di verifica**

```bash
#!/usr/bin/env bash
#
# Verifica dei fatti nbdkit 1.36.3 necessari all'export di stato
# (docs/plan-state-export.md §5). GATE: se un check fallisce, NON
# implementare la feature: riportare il risultato e rivedere il design.
#
# - selezione per nome (<name>.status) con file dir= ... senza multi-export list
# - flush/fua attivi per regular file (durabilità client via NBD_CMD_FLUSH)
# - size esposta = stat del file (4 KiB; segue truncate, mai rm durante il run)
# - nessun --filter=limit: client concorrenti accettati
#
set -u
[ "$(id -u)" = 0 ] || { echo "SKIP: richiede root" >&2; exit 0; }

echo "nbdkit: $(nbdkit --version)"
D="$(mktemp -d)"
P=10829
fail=0

hex() { dd if="$1" bs=1 skip="${2:-0}" count="${3:-8}" 2>/dev/null | od -An -tx1 | tr -d ' \n'; }

truncate -s 4096 "$D/demo.status"

nbdkit --foreground file dir="$D" --port=$P --ipaddr=127.0.0.1 &
NB_PID=$!
trap 'kill $NB_PID 2>/dev/null; wait $NB_PID 2>/dev/null; rm -rf "$D"' EXIT
sleep 1

echo "== 1. selezione per nome (niente list multi-export) =="
sz="$(nbdsh -u "nbd://127.0.0.1:$P/demo.status" -c 'print(h.get_size())' 2>&1)"
if [ "$sz" = 4096 ]; then echo "ok: exportname demo.status -> size 4096"; else echo "FAIL: $sz"; fail=1; fi

echo "== 2. flush disponibile per regular file =="
fl="$(nbdsh -u "nbd://127.0.0.1:$P/demo.status" -c 'print(h.can_flush())' 2>&1)"
if [ "$fl" = True ]; then echo "ok: can_flush = True"; else echo "FAIL: can_flush=$fl"; fail=1; fi

echo "== 3. size resta 4096 dopo scrittura client + FLUSH =="
w="$(nbdsh -u "nbd://127.0.0.1:$P/demo.status" \
     -c 'h.pwrite(b"\x01", 9); h.flush(); print("wrote+flush")' 2>&1)"
if [ "$(stat -c %s "$D/demo.status")" = 4096 ] && [ "$(hex "$D/demo.status" 9 1)" = 01 ]; then
    echo "ok: $w ; byte 9 scritto, size 4096"
else
    echo "FAIL: $w ; size=$(stat -c %s "$D/demo.status") byte9=$(hex "$D/demo.status" 9 1)"
    fail=1
fi

echo "== 4. size esposta segue il file (documenta perche' si usa sempre truncate) =="
rm -f "$D/demo.status"; truncate -s 100 "$D/demo.status"
sz2="$(nbdsh -u "nbd://127.0.0.1:$P/demo.status" -c 'print(h.get_size())' 2>&1)"
if [ "$sz2" = 100 ]; then
    echo "ok: size esposta = stat del file ($sz2) -> il piano ripristina sempre 4096"
else
    echo "FAIL: sz2=$sz2"; fail=1
fi
truncate -s 4096 "$D/demo.status"

echo "== 5. nessun limit: due client concorrenti accettati =="
nbdsh -u "nbd://127.0.0.1:$P/demo.status" -c 'import time; time.sleep(4)' &
C1=$!
sleep 1
c2="$(nbdsh -u "nbd://127.0.0.1:$P/demo.status" -c 'print("secondo client ok")' 2>&1)"
if printf '%s\n' "$c2" | grep -q 'secondo client ok'; then
    echo "ok: secondo client concorrente accettato"
else
    echo "FAIL: $c2"; fail=1
fi
wait $C1

exit $fail
```

- [ ] **Step 2: Esegui lo script**

Run: `tests/test-state-nbdkit-verify.sh`
Expected: 3/3/4/5 righe verdi `ok:` e uscita 0. Nota: lo script gira nbdkit direttamente e non tocca `nbd-export.sh` — può girare in qualunque momento prima dell'implementazione.

- [ ] **Step 3: Gate — se un check fallisce**

Se un check fallisce (es. `file dir=` non seleziona per nome su 1.36.3, o `can_flush` False), STOP: riporta l'output all'utente e non procedere con i Task 2+ (la spec §5 lo prescrive). Non improvvisare workaround.

- [ ] **Step 4: Spunta la checklist §5 della spec**

In `docs/plan-state-export.md`, sezione §5, porta le check-`[ ]` a `[x]` con l'esito reale, mantenendo il testo originale.

- [ ] **Step 5: Documenta i fatti confermati in AGENT.md §4**

Aggiungi in `AGENT.md` §4 (dopo la riga su nbdkit 1.36.3) una riga, ad es.:

```markdown
- `nbdkit file dir=<DIR>` seleziona l'export per nome (`<name>.status`) già su
  1.36.3 (senza list multi-export); `can_flush=True` su regular file; size
  esposta = stat del file (sempre 4 KiB via `truncate`; mai `rm` durante il run) —
  verificato in `tests/test-state-nbdkit-verify.sh` (docs/plan-state-export.md §5).
```

- [ ] **Step 6: Commit**

```bash
git add tests/test-state-nbdkit-verify.sh docs/plan-state-export.md AGENT.md
git commit -m "state-export: verify nbdkit 1.36.3 facts (spec §5) before implementing"
```

---

### Task 2: Costanti STATE_* + helper binari del record di stato

**Files:**
- Modify: `nbd-export.sh` (costanti dopo `DEFAULT_ADDR` ~riga 49; nuova sezione helper dopo `extract_image()` ~riga 132)
- Modify: `.env.example`
- Create: `tests/test-state-file.sh`

**Interfaces:**
- Consumes: `die`, `warn` (già esistenti), `hostname`, `/proc/sys/kernel/random/uuid`.
- Produces (usate dai Task 3-7):
  - `STATE_DIR` / `STATE_PORT` / `STATE_ADDR` (costanti, override via .env)
  - `state_path <name> [dir]` → `dir/<name>.status` (dir default `$STATE_DIR`)
  - `state_init_file <name> [dir]` → crea/azera record 4 KiB: magic+version+stato=clean, owner hostname/UUID, ctime=now LE
  - `state_set_marker <name> <clean|committing|committed> [dir]` → scrive solo il byte offset 9
  - `state_name_of <0|1|2|...>` → nome stato (per `show`)
  - `write_u64_le <file> <offset> <valore>` / `read_u64_le <file> <offset>` — ctime LE
  - `state_write_str <file> <offset> <maxbytes> <valore>` — scrittura zero-padded field

- [ ] **Step 1: Scrivi il test (falling)**

```bash
#!/usr/bin/env bash
#
# Test (no root): helper binari del record di stato (docs/plan-state-export.md §3).
# Il record deve essere SEMPRE 4 KiB, con prefix esatto:
#   magic(8)="NBDST\0\0\0" + version(1)=0x01 + stato(1) a offset 0..9
#   -> hex: 4e 42 44 53 54 00 00 00 01 00
set -u

NBD_EXPORT_SOURCED=1
. "$(dirname "$0")/../nbd-export.sh"

D="$(mktemp -d)"
fail=0

hex() { dd if="$1" bs=1 skip="${2:-0}" count="${3:-8}" 2>/dev/null | od -An -tx1 | tr -d ' \n'; }
u8()  { dd if="$1" bs=1 skip="$2" count=1 2>/dev/null | od -An -tu1 | tr -d ' '; }
strf() { dd if="$1" bs=1 skip="$2" count="$3" 2>/dev/null | tr -d '\0'; }

F="$(state_path demo "$D")"

echo "== state_path =="
[ "$F" = "$D/demo.status" ] && echo "ok: state_path demo -> $F" || { echo "FAIL: $F"; fail=1; }

echo "== state_init_file: 4 KiB + prefix esatto =="
state_init_file demo "$D"
[ "$(stat -c %s "$F")" = 4096 ] && echo "ok: size 4096" || { echo "FAIL: size=$(stat -c %s "$F")"; fail=1; }
[ "$(hex "$F" 0 10)" = "4e42445354000000000100" ] \
    && echo "ok: prefix 0..9 = magic+version+stato(clean)" \
    || { echo "FAIL: prem=$(hex "$F" 0 10)"; fail=1; }
[ "$(u8 "$F" 8)" = 1 ] && echo "ok: version=1" || { echo "FAIL: version=$(u8 "$F" 8)"; fail=1; }
[ "$(u8 "$F" 9)" = 0 ] && echo "ok: stato iniziale clean" || { echo "FAIL: stato=$(u8 "$F" 9)"; fail=1; }

echo "== owner hostname/UUID (offsets 16/80) =="
host="$(strf "$F" 16 64)"
uuid="$(strf "$F" 80 36)"
[ "$host" = "$(hostname)" ] && echo "ok: owner hostname '$host'" || { echo "FAIL: host='$host'"; fail=1; }
if printf '%s\n' "$uuid" | grep -qE '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'; then
    echo "ok: owner UUID '$uuid'"
else
    echo "FAIL: uuid='$uuid'"; fail=1
fi

echo "== ctime LE a offset 372, leggi e riscrivi (round-trip) =="
now="$(date +%s)"
ct="$(read_u64_le "$F" 372)"
[ "$ct" -ge "$((now - 60))" ] && [ "$ct" -le "$((now + 60))" ] \
    && echo "ok: ctime=$ct ~ now" || { echo "FAIL: ctime=$ct now=$now"; fail=1; }
write_u64_le "$F" 372 72623859790382856
[ "$(read_u64_le "$F" 372)" = 72623859790382856 ] && echo "ok: write/read u64 LE round-trip" \
    || { echo "FAIL: round-trip"; fail=1; }

echo "== state_set_marker: tocca solo il byte 9 =="
snap0="$(hex "$F" 0 9)"       # magic+s... fino a 8
snapctime="$(hex "$F" 372 8)"
state_set_marker demo committing "$D"
[ "$(u8 "$F" 9)" = 1 ] && echo "ok: committing -> byte 9 = 1" || { echo "FAIL: b9=$(u8 "$F" 9)"; fail=1; }
state_set_marker demo committed "$D"
[ "$(u8 "$F" 9)" = 2 ] && echo "ok: committed -> byte 9 = 2" || { echo "FAIL: b9=$(u8 "$F" 9)"; fail=1; }
state_set_marker demo clean "$D"
[ "$(u8 "$F" 9)" = 0 ] && echo "ok: clean -> byte 9 = 0" || { echo "FAIL: b9=$(u8 "$F" 9)"; fail=1; }
[ "$(hex "$F" 0 9)" = "$snap0" ] && [ "$(hex "$F" 372 8)" = "$snapctime" ] \
    && [ "$(stat -c %s "$F")" = 4096 ] \
    && echo "ok: altri byte e size invariati" || { echo "FAIL: no-clobber violato"; fail=1; }

echo "== nome stato =="
[ "$(state_name_of 0)" = clean ] && [ "$(state_name_of 1)" = committing ] \
    && [ "$(state_name_of 2)" = committed ] && echo "ok: state_name_of 0/1/2" \
    || { echo "FAIL: state_name_of"; fail=1; }

echo "== errori puliti =="
if state_set_marker demo bogus "$D" 2>/dev/null; then echo "FAIL: bogus accettato"; fail=1; else echo "ok: 'bogus' rifiutato"; fi
if state_set_marker nope clean "$D" 2>/dev/null; then echo "FAIL: file mancante accettato"; fail=1; else echo "ok: file mancante -> errore"; fi

rm -rf "$D"
exit $fail
```

- [ ] **Step 2: Esegui per vedere fallire**

Run: `tests/test-state-file.sh`
Expected: vari `FAIL:` perché `state_path`, `state_init_file`, ecc. non esistono ancora (e lo script esce con `fail=1`).

- [ ] **Step 3: Implementa — costanti e helper**

In `nbd-export.sh`, dopo `DEFAULT_ADDR="0.0.0.0"` (blocco costanti):

```bash
# state-export: marker di commit in <name>.status su unit dedicata
# (docs/plan-state-export.md; contratto formato 4 KiB in §3)
STATE_DIR="/var/lib/launch-nbd/state"
STATE_PORT=10819
STATE_ADDR="0.0.0.0"
```

Dopo `extract_image()` (nuova sezione, prima di `tls_dir_validate`):

```bash
# ------------------------------------------------- state-export helpers
# Record di stato: file binario fisso 4 KiB <name>.status.
#   offset 0:  magic "NBDST\0\0\0" (8 byte)   [il "\1" della spec §3 e'
#               il version byte qui sotto, a offset 8]
#   offset 8:  version = 1
#   offset 9:  stato 0=clean 1=committing 2=committed
#   offset 16: owner hostname (64), 80: owner UUID (36), 116: hash (256)
#   offset 372: ctime unix little-endian (8); pad a 4096 (truncate a 4 KiB)
# Il file nasce tutto zero; i campi scritti sono quindi zero-padded.
state_path() {
    local name="$1" dir="${2:-$STATE_DIR}"
    printf '%s/%s.status' "${dir%/}" "$name"
}

state_name_of() {
    case "$1" in
        0) echo clean ;;
        1) echo committing ;;
        2) echo committed ;;
        *) echo "unknown($1)" ;;
    esac
}

# scrive 8 byte little-endian a <off> (ctime)
write_u64_le() {
    local file="$1" off="$2" v="$3" hex="" out="" i
    hex="$(printf '%016x' "$v")"
    for ((i = 14; i >= 0; i -= 2)); do out+="\\x${hex:$i:2}"; done
    printf '%b' "$out" | dd of="$file" bs=1 seek="$off" conv=notrunc status=none
}

read_u64_le() {
    local file="$1" off="$2" hex="" rev="" i
    hex="$(dd if="$file" bs=1 skip="$off" count=8 2>/dev/null | od -An -tx1 | tr -d ' \n')"
    for ((i = 14; i >= 0; i -= 2)); do rev+="${hex:$i:2}"; done
    printf '%d' "0x$rev"
}

# scrive una stringa al massimo <max> byte (il resto del campo resta zero)
state_write_str() {
    local file="$1" off="$2" max="$3" val="$4"
    [ "${#val}" -le "$max" ] || die "state: value too long (max $max bytes)"
    printf '%s' "$val" | dd of="$file" bs=1 seek="$off" conv=notrunc status=none
}

# inizializza (o re-inizializza) un record di stato a 4 KiB, stato=clean
state_init_file() {
    local name="$1" dir="${2:-$STATE_DIR}" f host
    f="$(state_path "$name" "$dir")"
    truncate -s 4096 "$f" || die "state: cannot create $f"
    # magic(8) + version(1) + stato clean(1) -> byte 0..9
    printf '%b\x01\x00' 'NBDST\0\0\0' | dd of="$f" bs=1 conv=notrunc status=none
    host="$(hostname)"
    state_write_str "$f" 16 64 "$host"
    state_write_str "$f" 80 36 "$(cat /proc/sys/kernel/random/uuid)"
    write_u64_le "$f" 372 "$(date +%s)"
}

# scrive SOLO il byte di stato (offset 9): non tocca owner/hash/ctime
state_set_marker() {
    local name="$1" state="$2" dir="${3:-$STATE_DIR}" f enum
    case "$state" in
        clean)      enum=0 ;;
        committing) enum=1 ;;
        committed)  enum=2 ;;
        *) die "state set: invalid state '$state' (use clean|committing|committed)" ;;
    esac
    f="$(state_path "$name" "$dir")"
    [ -f "$f" ] || die "state set: no state file $f (use: nbd-export state export $name)"
    printf '%b' "\\x0$enum" | dd of="$f" bs=1 seek=9 conv=notrunc status=none
}
```

- [ ] **Step 4: Esegui il test per vedere passare**

Run: `tests/test-state-file.sh`
Expected: tutte le righe `ok:` e uscita 0.

- [ ] **Step 5: Aggiungi le variabili a `.env.example`**

In `.env.example`, dopo la sezione TLS:

```bash
# --- state export (marker di commit; docs/plan-state-export.md) --------------
# Directory dei file <name>.status serviti da nbd-export-state.service
# STATE_DIR=/var/lib/launch-nbd/state
# Porta e indirizzo dell'unit di stato (dedicata, niente --filter=limit)
# STATE_PORT=10819
# STATE_ADDR=0.0.0.0
```

- [ ] **Step 6: Commit**

```bash
git add nbd-export.sh .env.example tests/test-state-file.sh
git commit -m "state-export: constants and 4 KiB state-record helpers (spec §3)"
```

---

### Task 3: `write_state_unit` — generazione dell'unit di stato

**Files:**
- Modify: `nbd-export.sh` (nuova funzione dopo `write_unit()` ~riga 267)
- Create: `tests/test-state-unit.sh`

**Interfaces:**
- Consumes: `unit_path state`, `exec_escape` (Task esistenti).
- Produces: `write_state_unit <dir> <port> <addr> <tls_mode> <tls_dir> <user> <group>` → scrive `$(unit_path state)` = `$UNIT_DIR/nbd-export-state.service` (usata da `cmd_state_init`, Task 4).

- [ ] **Step 1: Scrivi il test (falling)**

```bash
#!/usr/bin/env bash
#
# Test (no root): write_state_unit genera nbd-export-state.service con:
#   - ExecStart: /usr/bin/nbdkit --foreground file dir=<DIR> --port=.. --ipaddr=..
#   - NIENTE --filter=limit / --filter=multi-conn / --readonly / --exportname
#   - hardening identico a write_unit (ma ProtectSystem=full, scrivibile)
#   - tls: --tls=require -> + --tls-verify-peer --tls-certificates
set -u

NBD_EXPORT_SOURCED=1
. "$(dirname "$0")/../nbd-export.sh"

UNIT_DIR="$(mktemp -d)"   # override DOPO il sourcing: serve solo il path
D="$(mktemp -d)"          # finto STATE_DIR
fail=0

u() { "$UNIT_DIR/nbd-export-state.service"; }
exec_line() { sed -n 's/^ExecStart=//p' "$(u)"; }

echo "== unit di base (tls off, root:root) =="
write_state_unit "$D" 10819 0.0.0.0 off /etc/pki/nbdkit root root
[ -f "$(u)" ] || { echo "FAIL: unit non creata"; fail=1; }

ex="$(exec_line)"
[ "$ex" = "/usr/bin/nbdkit --foreground file dir=$D --port=10819 --ipaddr=0.0.0.0 --user=root --group=root" ] \
    && echo "ok: ExecStart esatto" || { echo "FAIL: $ex"; fail=1; }

for banned in --filter=limit --filter=multi-conn --readonly --exportname; do
    if grep -qF "$banned" "$(u)"; then echo "FAIL: '$banned' presente"; fail=1; else echo "ok: senza $banned"; fi
done
for line in 'NoNewPrivileges=yes' 'PrivateTmp=yes' 'ProtectHome=yes' 'ProtectSystem=full' \
            'RestrictAddressFamilies=AF_INET AF_UNIX' 'Restart=on-failure' \
            'After=network-online.target' 'Wants=network-online.target'; do
    if grep -qF "$line" "$(u)"; then echo "ok: $line"; else echo "FAIL: manca '$line'"; fail=1; fi
done

echo "== variante tls=require =="
write_state_unit "$D" 10819 0.0.0.0 require /etc/pki/nbdkit root root
ex="$(exec_line)"
printf '%s\n' "$ex" | grep -qF -- '--tls=require' \
    && printf '%s\n' "$ex" | grep -qF -- '--tls-verify-peer' \
    && printf '%s\n' "$ex" | grep -qF -- '--tls-certificates=/etc/pki/nbdkit' \
    && echo "ok: tls=require + verify-peer + certificates" \
    || { echo "FAIL: $ex"; fail=1; }

echo "== variante tls=on (niente verify-peer) =="
write_state_unit "$D" 10819 0.0.0.0 on /etc/pki/nbdkit root root
ex="$(exec_line)"
printf '%s\n' "$ex" | grep -qF -- '--tls=on' \
    && ! printf '%s\n' "$ex" | grep -qF -- '--tls-verify-peer' \
    && echo "ok: tls=on senza verify-peer" || { echo "FAIL: $ex"; fail=1; }

rm -rf "$UNIT_DIR" "$D"
exit $fail
```

- [ ] **Step 2: Esegui per vedere fallire**

Run: `tests/test-state-unit.sh`
Expected: `FAIL: unit non creata` (funzione mancante).

- [ ] **Step 3: Implementa in `nbd-export.sh` (dopo `write_unit`)**

```bash
# unit dedicata allo state export: niente limit/multi-conn/readonly (spec §2);
# gira come proprietario di STATE_DIR; TLS come gli export dati (spec §4.4)
write_state_unit() {
    local dir="$1" port="$2" addr="$3" tls_mode="$4" tls_dir="$5" user="$6" group="$7"
    local unit ex tls_args=""
    unit="$(unit_path state)"
    ex="/usr/bin/nbdkit --foreground file dir=$(exec_escape "$dir") --port=$port --ipaddr=$(exec_escape "$addr")"
    if [ "$tls_mode" != off ]; then
        tls_args="--tls=$tls_mode --tls-certificates=$(exec_escape "$tls_dir")"
        [ "$tls_mode" = require ] && tls_args="$tls_args --tls-verify-peer"
    fi
    [ -n "$user" ] && ex="$ex --user=$(exec_escape "$user")"
    [ -n "$group" ] && ex="$ex --group=$(exec_escape "$group")"
    {
        printf '[Unit]\n'
        printf 'Description=NBD state export (nbdkit file dir=%s)\n' "$dir"
        printf 'After=network-online.target\n'
        printf 'Wants=network-online.target\n'
        printf '\n[Service]\n'
        printf 'Type=simple\n'
        printf 'ExecStart=%s %s\n' "$ex" "$tls_args"
        printf 'Restart=on-failure\n'
        printf 'RestartSec=2\n'
        printf 'NoNewPrivileges=yes\n'
        printf 'PrivateTmp=yes\n'
        printf 'ProtectHome=yes\n'
        printf 'ProtectSystem=full\n'
        printf 'RestrictAddressFamilies=AF_INET AF_UNIX\n'
        printf '\n[Install]\n'
        printf 'WantedBy=multi-user.target\n'
    } > "$unit"
    chmod 644 "$unit"
}
```

- [ ] **Step 4: Esegui il test per vedere passare**

Run: `tests/test-state-unit.sh`
Expected: tutte `ok:` e uscita 0.

- [ ] **Step 5: Commit**

```bash
git add nbd-export.sh tests/test-state-unit.sh
git commit -m "state-export: write_state_unit (no limit filters, hardening, TLS)"
```

---

### Task 4: Subcommand `state` (dispatch + usage) e `state init`

**Files:**
- Modify: `nbd-export.sh` — `cmd_state()` dopo `cmd_tls` (~riga 520), `usage_state()` dopo `usage_tls` (~riga 685), `main()` case, commento header, lista comandi `usage()`
- Create: `tests/test-state-init.sh` (root)

**Interfaces:**
- Consumes: `require_root`, `port_free`, `tls_dir_validate`, `write_state_unit`, `unit_path`.
- Produces: `cmd_state <sub> [args...]` (dispatcher), `usage_state`, `cmd_state_init <--dir|--port|--addr|--tls|--force>` — chiamato da `main()` (`state)` branch). `cmd_state_export/set/show/remove` vengono nel Task 5.

- [ ] **Step 1: Scrivi il test (root; fallisce: subcommand sconosciuto)**

```bash
#!/usr/bin/env bash
#
# Test (root): nbd-export state init — unit/attiva/ascolto + guardia nome
# riservato 'state'. Usa un STATE_DIR temporaneo e porta dedicata 10839.
set -u
[ "$(id -u)" = 0 ] || { echo "SKIP: richiede root" >&2; exit 0; }

SCRIPT="$(cd "$(dirname "$0")/.." && pwd)/nbd-export.sh"
D="$(mktemp -d)"
P=10839
UNIT=/etc/systemd/system/nbd-export-state.service
fail=0

cleanup() {
    systemctl stop nbd-export-state.service >/dev/null 2>&1 || true
    rm -f "$UNIT"; systemctl daemon-reload; rm -rf "$D"
}
trap cleanup EXIT
cleanup

echo "== state init di base =="
out="$("$SCRIPT" state init --dir "$D" --port $P 2>&1)"; rc=$?
echo "$out"
if [ $rc -eq 0 ] && [ -f "$UNIT" ]; then echo "ok: init exit 0, unit scritta"; else echo "FAIL: rc=$rc"; fail=1; fi

ex="$(sed -n 's/^ExecStart=//p' "$UNIT")"
printf '%s\n' "$ex" | grep -qF "file dir=$D --port=$P" \
    && echo "ok: ExecStart file dir=$D --port=$P" || { echo "FAIL: $ex"; fail=1; }
for banned in --filter=limit --filter=multi-conn; do
    grep -qF "$banned" "$UNIT" && { echo "FAIL: '$banned' presente"; fail=1; } || echo "ok: senza $banned"
done
state="$(systemctl is-active nbd-export-state.service 2>/dev/null || true)"
[ "$state" = active ] && echo "ok: servizio active" || { echo "FAIL: state=$state"; fail=1; }
ss -ltnH | grep -qE ":$P\b" && echo "ok: in ascolto su $P" || { echo "FAIL: niente in ascolto su $P"; fail=1; }

echo "== guardia: unit esistente NON di stato (export dati 'state') =="
printf '[Service]\nExecStart=/usr/bin/nbdkit --foreground file /tmp/x --exportname=state --port=10909\n' > "$UNIT"
systemctl daemon-reload
if "$SCRIPT" state init --dir "$D" --port $P 2>&1; then echo "FAIL: sovrascritta senza --force"; fail=1; else echo "ok: rifiutata senza --force"; fi

echo "== --force sostituisce l'unit =="
"$SCRIPT" state init --dir "$D" --port $P --force >/dev/null 2>&1
ex="$(sed -n 's/^ExecStart=//p' "$UNIT")"
printf '%s\n' "$ex" | grep -qF "file dir=$D" && echo "ok: --force ha scritto l'unit di stato" || { echo "FAIL: $ex"; fail=1; }

exit $fail
```

- [ ] **Step 2: Esegui per vedere fallire**

Run: `tests/test-state-init.sh`
Expected: `error: unknown command 'state'` (main non lo conosce ancora) e `fail=1`.

- [ ] **Step 3: Implementa — dispatcher, usage e init**

A) In `nbd-export.sh`, dopo `cmd_tls()`:

```bash
# --------------------------------------------------------------- state cmd
cmd_state() {
    local sub="${1:-help}"; shift || true
    case "$sub" in
        init)   cmd_state_init "$@" ;;
        export) cmd_state_export "$@" ;;
        set)    cmd_state_set "$@" ;;
        show)   cmd_state_show "$@" ;;
        remove) cmd_state_remove "$@" ;;
        -h|--help|help) usage_state; exit 0 ;;
        *) die "state: unknown subcommand '$sub' (see: nbd-export state help)" ;;
    esac
}

cmd_state_init() {
    local dir="$STATE_DIR" port="$STATE_PORT" addr="$STATE_ADDR"
    local tls_mode=off tls_dir="$TLS_DIR_DEFAULT" force=0 user="" group=""
    while [ $# -gt 0 ]; do
        case "$1" in
            --dir)       dir="$2";  shift 2 ;;
            --port)      port="$2"; shift 2 ;;
            --addr)      addr="$2"; shift 2 ;;
            --tls)       tls_mode=require; shift ;;
            --tls=on)    tls_mode=on; shift ;;
            --tls=require) tls_mode=require; shift ;;
            --tls=off)   tls_mode=off; shift ;;
            --tls-dir)   tls_dir="$2"; shift 2 ;;
            --force)     force=1; shift ;;
            -h|--help)   usage_state; exit 0 ;;
            -*) die "state init: unknown option $1" ;;
            *) die "state init: unexpected argument '$1'" ;;
        esac
    done
    require_root
    [[ "$port" =~ ^[0-9]+$ ]] && [ "$port" -ge 1 ] && [ "$port" -le 65535 ] \
        || die "state init: invalid port '$port'"
    [ -e "$dir" ] && [ ! -d "$dir" ] && die "state init: $dir exists and is not a directory"
    local unit ex
    unit="$(unit_path state)"
    if [ -f "$unit" ]; then
        ex="$(sed -n 's/^ExecStart=//p' "$unit")"
        if ! printf '%s\n' "$ex" | grep -q -- ' file dir='; then
            [ "$force" = 1 ] || die "state init: $unit is a DATA export ('state' is a reserved name; use --force to replace)"
        fi
    fi
    if [ -f "$unit" ] && [ "$force" = 1 ]; then
        :   # sostituiamo l'unit attiva: la sua porta e' nostra
    else
        port_free "$port" || die "state init: port $port already in use (use --force to replace the running unit)"
    fi
    mkdir -p "$dir" || die "state init: cannot create $dir"
    user="$(stat -c %U "$dir")"; group="$(stat -c %G "$dir")"
    [ "$tls_mode" != off ] && tls_dir_validate "$tls_dir"
    write_state_unit "$dir" "$port" "$addr" "$tls_mode" "$tls_dir" "$user" "$group"
    systemctl daemon-reload
    if [ "$force" = 1 ] && systemctl is-active --quiet "$UNIT_PREFIX-state.service"; then
        systemctl restart "$UNIT_PREFIX-state.service"
    elif ! systemctl start "$UNIT_PREFIX-state.service"; then
        echo "unit failed to start; check: journalctl -u $UNIT_PREFIX-state.service -e" >&2
        exit 1
    fi
    echo "state export serving $dir on nbd://$addr:$port/<name>.status"
    echo "  unit:     $UNIT_PREFIX-state.service"
    echo "  runs as:  $user:$group"
    [ "$tls_mode" != off ] && echo "  tls:      $tls_mode (certs: $tls_dir)"
}
```

B) Dopo `usage_tls()`, `usage_state()`:

```bash
usage_state() {
    cat <<'EOF'
usage: nbd-export state init [--dir DIR] [--port N] [--addr IP] [--tls] [--force]
       nbd-export state export <name> [--dir DIR]
       nbd-export state set <name> <clean|committing|committed> [--dir DIR]
       nbd-export state show <name> [--dir DIR]
       nbd-export state remove <name> [--dir DIR]

Commit-state marker per gli export NBD (docs/plan-state-export.md). Un file
binario fisso 4 KiB <name>.status servito da un'unit dedicata
(nbd-export-state.service, SENZA --filter=limit) cosi' che client su altri
host vedano 'committing' prima di toccare un disco. Il launcher client scrive
il marker via NBD + FLUSH; 'state set' e' il percorso amministrativo (scrive
solo il byte di stato, non tocca owner/hash/ctime).
  --dir DIR    directory di stato (default /var/lib/launch-nbd/state)
  --port N     porta dell'unit di stato (default 10819)
  --addr IP    indirizzo di ascolto (default 0.0.0.0)
  --tls        richiedi TLS + verifica certificato client (--tls=on: solo cifratura)
  --tls-dir D  directory certificati (default /etc/pki/nbdkit)
  --force      sostituisci un'unit di stato esistente
EOF
}
```

C) In `usage()` (lista comandi, dopo la riga `tls ...`):

```
  state init|export|set|show|remove   commit-state marker (4 KiB <name>.status)
```

D) In `main()` (dopo il case `tls)`):

```bash
        state) cmd_state "$@" ;;
```

E) Nel commento header dello script (dopo la riga `#   nbd-export.sh tls create|status|remove [options]`):

```
#   nbd-export.sh state init|export|set|show|remove   # marker di commit (spec)
```

- [ ] **Step 4: Esegui `bash -n` e i test**

Run: `bash -n nbd-export.sh && tests/test-state-init.sh`
Expected: nessun errore sintattico; tutte le `ok:` del test init (attiva, in ascolto su 10839, guardia nome riservato, `--force`).

Nota: `cmd_state_export/set/show/remove` sono referenziati dal dispatcher ma esistono solo dal Task 5 — finché quel task non è fatto, invocare quei subcommand darà `unknown command`; il test init non li tocca.

- [ ] **Step 5: Commit**

```bash
git add nbd-export.sh tests/test-state-init.sh
git commit -m "state-export: 'state' subcommand dispatcher + state init (unit, guardia 'state')"
```

---

### Task 5: `state export|set|show|remove` (operazioni sui file)

**Files:**
- Modify: `nbd-export.sh` (quattro funzioni dopo `cmd_state_init`)
- Create: `tests/test-state-cmd.sh` (non root, `--dir` su tmpdir)

**Interfaces:**
- Consumes: `state_init_file`, `state_set_marker`, `state_dump` (Task 2), `name_valid`, `die`, `warn`.
- Produces: `cmd_state_export <name> [--dir]`, `cmd_state_set <name> <stato> [--dir]`, `cmd_state_show <name> [--dir]`, `cmd_state_remove <name> [--dir]` + helper interno `state_dump <name> [dir]`.

- [ ] **Step 1: Scrivi il test (falling: subcommand sconosciuti)**

```bash
#!/usr/bin/env bash
#
# Test (no root): state export|set|show|remove su STATE_DIR temporaneo via --dir.
# Copre anche: idempotenza export, resize a 4 KiB, rifiuto file estraneo,
# no-clobber di set, errori puliti.
set -u

SCRIPT="$(cd "$(dirname "$0")/.." && pwd)/nbd-export.sh"
D="$(mktemp -d)"
fail=0

hex() { dd if="$1" bs=1 skip="${2:-0}" count="${3:-8}" 2>/dev/null | od -An -tx1 | tr -d ' \n'; }
u8()  { dd if="$1" bs=1 skip="$2" count=1 2>/dev/null | od -An -tu1 | tr -d ' '; }

echo "== state export crea 4 KiB pulito (magic + clean) =="
"$SCRIPT" state export demo --dir "$D" >/dev/null
F="$D/demo.status"
[ "$(stat -c %s "$F")" = 4096 ] && [ "$(hex "$F" 0 9)" = "4e424453540000000001" ] \
    && [ "$(u8 "$F" 9)" = 0 ] && echo "ok: file 4 KiB, magic ok, clean" \
    || { echo "FAIL: size=$(stat -c %s "$F") prem=$(hex "$F" 0 9) b9=$(u8 "$F" 9)"; fail=1; }

echo "== idempotente: secondo export non re-inizializza (ctime invariato) =="
ct_before="$(hex "$F" 372 8)"
"$SCRIPT" state export demo --dir "$D" >/dev/null
ct_after="$(hex "$F" 372 8)"
[ "$ct_before" = "$ct_after" ] && echo "ok: ctime invariato (no re-init)" || { echo "FAIL: $ct_before != $ct_after"; fail=1; }

echo "== file < 4 KiB -> riportato a 4 KiB con truncate (mai rm) =="
truncate -s 100 "$D/small"
"$SCRIPT" state export small --dir "$D" >/dev/null
[ "$(stat -c %s "$D/small.status")" = 4096 ] && [ "$(hex "$D/small.status" 0 5)" = "4e42445354" ] \
    && echo "ok: ri-inizializzato a 4 KiB" || { echo "FAIL: resize"; fail=1; }

echo "== file 4 KiB con magic estraneo -> rifiutato, intatto =="
printf 'x%.0s' {1..4096} > "$D/foreign"
before="$(hex "$D/foreign" 0 4)"
if "$SCRIPT" state export foreign --dir "$D" >/dev/null 2>&1; then echo "FAIL: accettato"; fail=1; else echo "ok: rifiutato"; fi
[ "$(hex "$D/foreign" 0 4)" = "$before" ] && echo "ok: file intatto" || { echo "FAIL: file modificato"; fail=1; }

echo "== state set: only byte 9, no-clobber su ctime, round-trip =="
"$SCRIPT" state set demo committing --dir "$D" >/dev/null
[ "$(u8 "$F" 9)" = 1 ] && echo "ok: committing -> 1" || { echo "FAIL: b9=$(u8 "$F" 9)"; fail=1; }
ct="$(hex "$F" 372 8)"; prem="$(hex "$F" 0 9)"
"$SCRIPT" state set demo committed --dir "$D" >/dev/null
[ "$(u8 "$F" 9)" = 2 ] && [ "$(hex "$F" 372 8)" = "$ct" ] && [ "$(hex "$F" 0 9)" = "$prem" ] \
    && [ "$(stat -c %s "$F")" = 4096 ] && echo "ok: committed (byte 9), ctime/prefix/size intatti" \
    || { echo "FAIL: no-clobber violato"; fail=1; }
"$SCRIPT" state set demo clean --dir "$D" >/dev/null
[ "$(u8 "$F" 9)" = 0 ] && echo "ok: clean round-trip" || { echo "FAIL: b9=$(u8 "$F" 9)"; fail=1; }

echo "== errori puliti (exit != 0, file intatto) =="
b9="$(u8 "$F" 9)"
if "$SCRIPT" state set demo bogus --dir "$D" >/dev/null 2>&1; then echo "FAIL: 'bogus' accettato"; fail=1; else echo "ok: 'bogus' rifiutato"; fi
[ "$(u8 "$F" 9)" = "$b9" ] && echo "ok: nessuna scrittura parziale" || { echo "FAIL: byte modificato"; fail=1; }
if "$SCRIPT" state show nope --dir "$D" >/dev/null 2>&1; then echo "FAIL: show su file mancante"; fail=1; else echo "ok: show su file mancante -> errore"; fi
if "$SCRIPT" state set nope clean --dir "$D" >/dev/null 2>&1; then echo "FAIL: set su file mancante"; fail=1; else echo "ok: set su file mancante -> errore"; fi

echo "== state show: dump interpretato =="
"$SCRIPT" state set demo committing --dir "$D" >/dev/null
"$SCRIPT" state show demo --dir "$D" | grep -qF 'state:    committing' \
    && "$SCRIPT" state show demo --dir "$D" | grep -qF 'file:     '$F \
    && "$SCRIPT" state show demo --dir "$D" | grep -qF 'hash:     -' 2>/dev/null \
    && echo "ok: show (state, file, hash vuoto)" \
    || { echo "FAIL: output show"; fail=1; }

echo "== state remove: file via; dir vuota rimossa =="
"$SCRIPT" state remove demo --dir "$D" >/dev/null
[ ! -f "$F" ] && echo "ok: file rimosso" || { echo "FAIL: file ancora presente"; fail=1; }
D2="$(mktemp -d)"
"$SCRIPT" state export solo --dir "$D2" >/dev/null
"$SCRIPT" state remove solo --dir "$D2" >/dev/null
[ ! -d "$D2" ] && echo "ok: dir vuota rimossa" || { echo "FAIL: dir non rimossa"; fail=1; }

rm -rf "$D"
exit $fail
```

- [ ] **Step 2: Esegui per vedere fallire**

Run: `tests/test-state-cmd.sh`
Expected: `error: unknown subcommand 'export'` (dispatcher presente, funzioni no) e `fail=1`.

- [ ] **Step 3: Implementa in `nbd-export.sh` (dopo `cmd_state_init`)**

```bash
# dump interpretato del record di stato (per 'show')
state_dump() {
    local name="$1" dir="${2:-$STATE_DIR}" f size st v ctime host uuid h
    f="$(state_path "$name" "$dir")"
    [ -f "$f" ] || die "state show: no state file $f (use: nbd-export state export $name)"
    size="$(stat -c %s "$f")"
    [ "$size" = 4096 ] || warn "state show: $f is $size bytes (expected 4096)"
    st="$(dd if="$f" bs=1 skip=9 count=1 2>/dev/null | od -An -tu1 | tr -d ' ')"; st="${st:-?}"
    v="$(dd if="$f" bs=1 skip=8 count=1 2>/dev/null | od -An -tu1 | tr -d ' ')"; v="${v:-?}"
    ctime="$(read_u64_le "$f" 372)"
    host="$(dd if="$f" bs=1 skip=16 count=64 2>/dev/null | tr -d '\0')"
    uuid="$(dd if="$f" bs=1 skip=80 count=36 2>/dev/null | tr -d '\0')"
    h="$(dd if="$f" bs=1 skip=116 count=256 2>/dev/null | tr -d '\0')"
    printf 'file:     %s\n' "$f"
    printf 'size:     %s\n' "$size"
    printf 'magic:    NBDST (version %s)\n' "$v"
    printf 'state:    %s\n' "$(state_name_of "$st")"
    printf 'owner:    %s\n' "${host:--}"
    printf 'owner-id: %s\n' "${uuid:--}"
    printf 'hash:     %s\n' "${h:--}"
    printf 'ctime:    %s (%s)\n' "$ctime" "$(date -d "@$ctime" '+%F %T' 2>/dev/null || echo invalid)"
}

cmd_state_export() {
    local name="" dir="$STATE_DIR" f size magic
    while [ $# -gt 0 ]; do
        case "$1" in
            --dir)  dir="$2"; shift 2 ;;
            -h|--help) usage_state; exit 0 ;;
            -*) die "state export: unknown option $1" ;;
            *) [ -z "$name" ] && name="$1" || die "state export: too many arguments"; shift ;;
        esac
    done
    [ -n "$name" ] || die "state export: missing <name>"
    name_valid "$name" || die "state export: invalid name '$name'"
    [ -d "$dir" ] || die "state export: state directory missing: $dir (run: nbd-export state init --dir $dir)"
    f="$(state_path "$name" "$dir")"
    if [ -f "$f" ]; then
        size="$(stat -c %s "$f")"
        magic="$(dd if="$f" bs=1 count=5 2>/dev/null | od -An -tc | tr -d ' \n')"
        if [ "$size" -ne 4096 ]; then
            warn "state export: $f is $size bytes, re-initialising to 4 KiB"
            state_init_file "$name" "$dir"
        elif [ "$magic" != "NBDST" ]; then
            die "state export: $f exists but is not a state record (magic missing)"
        fi
    else
        state_init_file "$name" "$dir"
    fi
    if [ "$(id -u)" = 0 ]; then
        chown "$(stat -c %U "$dir"):$(stat -c %G "$dir")" "$f" 2>/dev/null || true
    elif [ "$(stat -c %U "$f")" != "$(stat -c %U "$dir")" ]; then
        warn "state export: file owner differs from state dir owner; the state unit may not serve it"
    fi
    echo "state file ready: $f (clean)"
}

cmd_state_set() {
    local name="" state="" dir="$STATE_DIR"
    while [ $# -gt 0 ]; do
        case "$1" in
            --dir)  dir="$2"; shift 2 ;;
            -h|--help) usage_state; exit 0 ;;
            -*) die "state set: unknown option $1" ;;
            *) if [ -z "$name" ]; then name="$1"
               elif [ -z "$state" ]; then state="$1"
               else die "state set: too many arguments"; fi
               shift ;;
        esac
    done
    [ -n "$name" ] || die "state set: missing <name>"
    [ -n "$state" ] || die "state set: missing <clean|committing|committed>"
    state_set_marker "$name" "$state" "$dir"
    echo "state $name: $state"
}

cmd_state_show() {
    local name="" dir="$STATE_DIR"
    while [ $# -gt 0 ]; do
        case "$1" in
            --dir)  dir="$2"; shift 2 ;;
            -h|--help) usage_state; exit 0 ;;
            -*) die "state show: unknown option $1" ;;
            *) [ -z "$name" ] && name="$1" || die "state show: too many arguments"; shift ;;
        esac
    done
    [ -n "$name" ] || die "state show: missing <name>"
    state_dump "$name" "$dir"
}

cmd_state_remove() {
    local name="" dir="$STATE_DIR" f
    while [ $# -gt 0 ]; do
        case "$1" in
            --dir)  dir="$2"; shift 2 ;;
            -h|--help) usage_state; exit 0 ;;
            -*) die "state remove: unknown option $1" ;;
            *) [ -z "$name" ] && name="$1" || die "state remove: too many arguments"; shift ;;
        esac
    done
    [ -n "$name" ] || die "state remove: missing <name>"
    name_valid "$name" || die "state remove: invalid name '$name'"
    f="$(state_path "$name" "$dir")"
    [ -f "$f" ] || die "state remove: no state file $f"
    rm -f "$f"
    rmdir "$dir" 2>/dev/null && echo "removed state file $f and empty dir $dir" \
        || echo "removed state file $f"
}
```

- [ ] **Step 4: Esegui i test**

Run: `tests/test-state-cmd.sh`
Expected: tutte le `ok:` e uscita 0 (incluse idempotenza, resize, no-clobber, errori puliti).

- [ ] **Step 5: Commit**

```bash
git add nbd-export.sh tests/test-state-cmd.sh
git commit -m "state-export: state export/set/show/remove (file ops, no-clobber)"
```

---

### Task 6: Integrazione `list`/`status` + nome riservato in `cmd_export`

**Files:**
- Modify: `nbd-export.sh` — helper `is_state_unit` / `reserved_name_check`, `cmd_list()`, `cmd_status()`, `cmd_export()` (check nome)
- Create: `tests/test-state-list-status.sh` (non root, UNIT_DIR temporaneo)

**Interfaces:**
- Consumes: `unit_path`, `extract_image` (esistenti).
- Produces: `is_state_unit <unitfile>` (true se ExecStart contiene ` file dir=`), `reserved_name_check <name>` (die se `state`). L'export dati non può più chiamarsi `state`.

- [ ] **Step 1: Scrivi il test (falling)**

```bash
#!/usr/bin/env bash
#
# Test (no root): list/status non confondono l'unit di stato con un export dati
# (spec §4.3: extract_image produrrebbe IMAGE='dir=...'); nome 'state' riservato.
set -u

NBD_EXPORT_SOURCED=1
. "$(dirname "$0")/../nbd-export.sh"

U="$(mktemp -d)"   # UNIT_DIR temporaneo, DOPO il sourcing
D="$(mktemp -d)"
fail=0

write_fake_unit() {  # $1=nome, $2=exec
    printf '[Service]\nExecStart=%s\n' "$2" > "$U/$UNIT_PREFIX-$1.service"
}

write_fake_unit disk '/usr/bin/nbdkit --foreground --filter=limit file /srv/disk.img --exportname=disk --port=10809 --user=root --group=root'
write_fake_unit state '/usr/bin/nbdkit --foreground file dir='"$D"' --port=10819 --ipaddr=0.0.0.0 --user=root --group=root'

echo "== list esclude l'unit di stato =="
out="$(cmd_list)"
printf '%s\n' "$out" | grep -qF 'disk' && echo "ok: export dati listato" || { echo "FAIL: disk assente"; fail=1; }
printf '%s\n' "$out" | grep -qE 'dir=.*status' && { echo "FAIL: unit di stato in list"; fail=1; } || echo "ok: unit di stato esclusa"
printf '%s\n' "$out" | grep -qF 'dir=' && { echo "FAIL: colonna IMAGE con dir="; fail=1; } || echo "ok: nessuna IMAGE 'dir='"

echo "== status state: blocco dedicato, mai IMAGE dir= =="
out="$(cmd_status state)"
printf '%s\n' "$out" | grep -qF 'state-export unit' && echo "ok: nota unit di stato" || { echo "FAIL: $out"; fail=1; }
printf '%s\n' "$out" | grep -qF "state dir: $D" && echo "ok: state dir mostrata" || { echo "FAIL: $out"; fail=1; }
printf '%s\n' "$out" | grep -qE 'image:|dir=' && { echo "FAIL: immagine spurie"; fail=1; } || echo "ok: nessuna immagine spuria"

echo "== status disk continua a funzionare =="
cmd_status disk >/dev/null && echo "ok: status disk ok" || { echo "FAIL: status disk"; fail=1; }

echo "== nome 'state' riservato per gli export dati =="
reserved_name_check disk && echo "FAIL: disk rifiutato" || echo "ok: disk ammesso"
if reserved_name_check state 2>/dev/null; then echo "FAIL: 'state' ammesso"; fail=1; else echo "ok: 'state' rifiutato"; fi

rm -rf "$U" "$D"
exit $fail
```

- [ ] **Step 2: Esegui per vedere fallire**

Run: `tests/test-state-list-status.sh`
Expected: `FAIL:` su `is_state_unit`/`reserved_name_check` mancanti e list che mostra `dir=`.

- [ ] **Step 3: Implementa**

A) Nuovi helper (sezione state-export helpers, dopo `state_set_marker`):

```bash
# true se <unitfile> e' l'unit di stato (nbdkit file dir=...) e NON un export dati
is_state_unit() {
    grep -q -- ' file dir=' "$1" 2>/dev/null
}

# gli export dati non possono chiamarsi 'state' (collide con nbd-export-state.service)
reserved_name_check() {
    [ "$1" != "state" ] || die "export: name 'state' is reserved (state-export unit $UNIT_PREFIX-state.service)"
}
```

B) In `cmd_export()`, subito dopo `name_valid "$name" || die ...`:

```bash
    reserved_name_check "$name"
```

C) In `cmd_list()` — dentro il `for`, dopo `exec="$(sed -n ...)"`:

```bash
        is_state_unit "$u" && continue   # unit di stato: non e' un export (vedi: nbd-export state)
```

D) In `cmd_status()` — dopo `exec="$(sed -n 's/^ExecStart=//p' "$unit")"`, prima di `img=...`:

```bash
    if is_state_unit "$unit"; then
        local sdir sport stls
        sdir="$(printf '%s\n' "$exec" | grep -oP -- 'dir=\K[^ ]+' | head -1)"
        sport="$(printf '%s\n' "$exec" | grep -oP -- '--port=\K[0-9]+' | head -1)"
        stls="$(printf '%s\n' "$exec" | grep -oP -- '--tls=\K[a-z]+' | head -1 || true)"
        printf 'unit:      %s\n' "$unit"
        printf 'state dir: %s\n' "$sdir"
        printf 'port:      %s\n' "${sport:-?}"
        printf 'tls:       %s\n' "${stls:-off}"
        printf 'state:     %s\n' "$(systemctl is-active "$UNIT_PREFIX-state.service" 2>/dev/null || echo unknown)"
        printf 'notice:    state-export unit (data exports: nbd-export list)\n'
        return 0
    fi
```

Nota: `cmd_status` dichiara `local ... img port state mainpid ru ru_g` in testa; il blocco state usa nomi locali propri (`sdir/sport/stls`) per non collidere — mantieni la dichiarazione `local` esistente e aggiungi questa sezione subito dopo la lettura di `exec`.

- [ ] **Step 4: Esegui i test**

Run: `tests/test-state-list-status.sh` (e `bash -n nbd-export.sh`)
Expected: tutte `ok:`, uscita 0.

- [ ] **Step 5: Commit**

```bash
git add nbd-export.sh tests/test-state-list-status.sh
git commit -m "state-export: list/status isolano l'unit di stato; nome 'state' riservato"
```

---

### Task 7: Test end-to-end root (spec §7) + procedura AGENT §6.9

**Files:**
- Create: `tests/test-state-export.sh` (root)
- Modify: `AGENT.md` (nuova §6.9)

**Interfaces:**
- Consumes: tutto (Task 2-6). Verifica i punti spec §7: init+export, show round-trip, scrittura via NBD + FLUSH con persistenza dopo riavvio unit, nbdinfo sullo stato mentre la slot dati `limit=1` è occupata, remove senza toccare l'export dati.

- [ ] **Step 1: Scrivi il test end-to-end**

```bash
#!/usr/bin/env bash
#
# Test end-to-end (root): docs/plan-state-export.md §7.
# - state init + state export -> file 4 KiB, magic ok, unit attiva su 10819
# - scrittura del marker via NBD (come il launcher client) + FLUSH
# - riavvio di nbd-export-state.service -> lo stato persiste
# - nbdinfo sullo stato mentre la slot limit=1 dei dati e' occupata
# - state set clean/admin round-trip; state remove lascia intatto l'export dati
#
# Porte: 10819 (stato, spec §2) e 10809 (export dati demo, solo durante il test).
set -u
[ "$(id -u)" = 0 ] || { echo "SKIP: richiede root" >&2; exit 0; }

SCRIPT="$(cd "$(dirname "$0")/.." && pwd)/nbd-export.sh"
D="$(mktemp -d)"
NAME=demo
SP=10819
DP=10809
fail=0

cleanup() {
    kill $HOLDER 2>/dev/null; wait $HOLDER 2>/dev/null
    "$SCRIPT" remove "$NAME" >/dev/null 2>&1 || true
    systemctl stop nbd-export-state.service >/dev/null 2>&1 || true
    rm -f /etc/systemd/system/nbd-export-state.service
    systemctl daemon-reload
    rm -rf "$D"
}
HOLDER=0
trap cleanup EXIT
cleanup

echo "== §7.1 state init + state export =="
"$SCRIPT" state init --dir "$D" --port $SP >/dev/null
"$SCRIPT" state export "$NAME" --dir "$D" >/dev/null
F="$D/$NAME.status"
[ "$(stat -c %s "$F")" = 4096 ] || { echo "FAIL: size"; fail=1; }
dd if="$F" bs=1 count=5 2>/dev/null | grep -q NBDST && echo "ok: file 4 KiB con magic" || { echo "FAIL: magic"; fail=1; }
systemctl is-active nbd-export-state.service >/dev/null || { echo "FAIL: unit non attiva"; fail=1; }
ss -ltnH | grep -qE ":$SP\b" && echo "ok: unit in ascolto su $SP" || { echo "FAIL: ascolto"; fail=1; }

echo "== §7.2 state show = clean =="
"$SCRIPT" state show "$NAME" --dir "$D" | grep -qF 'state:    clean' && echo "ok: show clean" || { echo "FAIL: show"; fail=1; }

echo "== §7.3 marker via NBD + FLUSH, riavvio unit, persistenza =="
nbdsh -u "nbd://127.0.0.1:$SP/$NAME.status" -c 'h.pwrite(b"\x01", 9); h.flush(); print("marker committing scritto")'
b9="$(dd if="$F" bs=1 skip=9 count=1 2>/dev/null | od -An -tu1 | tr -d ' ')"
[ "$b9" = 1 ] && echo "ok: byte 9 = committing dopo scrittura NBD" || { echo "FAIL: b9=$b9"; fail=1; }
systemctl restart nbd-export-state.service
sleep 1
b9="$(dd if="$F" bs=1 skip=9 count=1 2>/dev/null | od -An -tu1 | tr -d ' ')"
[ "$b9" = 1 ] && echo "ok: committing persistito dopo riavvio unit" || { echo "FAIL: b9=$b9 dopo restart"; fail=1; }
"$SCRIPT" state show "$NAME" --dir "$D" | grep -qF 'state:    committing' && echo "ok: show reflette committing" || { echo "FAIL: show"; fail=1; }

echo "== §7.4 nbdinfo sullo stato mentre la slot dati limit=1 e' occupata =="
truncate -s 1M "$D/data.img"
"$SCRIPT" export "$D/data.img" --name "$NAME" --port $DP >/dev/null
nbdsh -u "nbd://127.0.0.1:$DP/$NAME" -c 'import time; time.sleep(6)' &
HOLDER=$!
sleep 2
if timeout 5 nbdsh -u "nbd://127.0.0.1:$DP/$NAME" -c 'print("secondo")' >/dev/null 2>&1; then
    echo "warning: la slot dati non ha rifiutato il 2° client in questo test"  # comportamento atteso: rifiutato
fi
nbdinfo "nbd://127.0.0.1:$SP/$NAME.status" >/dev/null 2>&1 \
    && echo "ok: stato leggibile via nbdinfo con slot dati occupata" || { echo "FAIL: nbdinfo stato"; fail=1; }

echo "== §7.5 state set round-trip (percorso admin) =="
"$SCRIPT" state set "$NAME" clean --dir "$D" >/dev/null
"$SCRIPT" state show "$NAME" --dir "$D" | grep -qF 'state:    clean' && echo "ok: round-trip clean" || { echo "FAIL: round-trip"; fail=1; }

echo "== §7.6 state remove: file via, unit attiva, export dati intatto =="
"$SCRIPT" state remove "$NAME" --dir "$D" >/dev/null
[ ! -f "$F" ] && echo "ok: file di stato rimosso" || { echo "FAIL: file presente"; fail=1; }
systemctl is-active nbd-export-state.service >/dev/null && echo "ok: unit di stato ancora attiva" || { echo "FAIL: unit fermata"; fail=1; }
systemctl is-active nbd-export-$NAME.service >/dev/null && echo "ok: export dati intatto" || { echo "FAIL: export dati fermato"; fail=1; }
kill $HOLDER 2>/dev/null; wait $HOLDER 2>/dev/null; HOLDER=0

exit $fail
```

- [ ] **Step 2: Esegui il test end-to-end**

Run: `tests/test-state-export.sh`
Expected: tutte le sezioni §7.1-§7.6 in `ok:` e uscita 0. (Il test crea e pulisce l'export dati `demo` su 10809 e l'unit di stato — niente residui.)

- [ ] **Step 3: Aggiungi la procedura §6.9 in AGENT.md**

In `AGENT.md` §6, dopo la §6.8, aggiungi la sezione di verifica (derivata dal test):

````markdown
### 6.9 State export (docs/plan-state-export.md §7)
```bash
./nbd-export.sh state init --dir /tmp/state-test --port 10819
./nbd-export.sh state export demo --dir /tmp/state-test
./nbd-export.sh state show demo --dir /tmp/state-test        # clean
nbdsh -u nbd://127.0.0.1:10819/demo.status -c 'h.pwrite(b"\x01", 9); h.flush()'
systemctl restart nbd-export-state.service && sleep 1
./nbd-export.sh state show demo --dir /tmp/state-test        # committing (persistito)
nbdinfo nbd://127.0.0.1:10819/demo.status                    # non tocca limit=1 dei dati
./nbd-export.sh state set demo clean --dir /tmp/state-test   # round-trip
./nbd-export.sh state remove demo --dir /tmp/state-test      # unit resta attiva
./nbd-export.sh state init --dir /tmp/state-test --port 10819 --force 2>/dev/null
systemctl stop nbd-export-state.service
rm -f /etc/systemd/system/nbd-export-state.service && systemctl daemon-reload && rm -rf /tmp/state-test
```
Prima di tutto: `tests/test-state-nbdkit-verify.sh` (fatti nbdkit §5, gate).
````

- [ ] **Step 4: Commit**

```bash
git add tests/test-state-export.sh AGENT.md
git commit -m "state-export: end-to-end root test (spec §7) + AGENT §6.9 procedure"
```

---

### Task 8: Documentazione finale + verifica completa della suite

**Files:**
- Modify: `AGENT.md` (§2 design decisions, §3 privilegi, §4 gotchas, §5 struttura script)
- Modify: `docs/plan-state-export.md` (nota scope §8)
- Modify: `nbd-export.sh` (nessuna modifica funzionale — solo verifica)

**Interfaces:** nessuna nuova; chiude il piano.

- [ ] **Step 1: AGENT.md §5 — struttura script**

Aggiungi dopo la riga `└── cmd_tls           tls create|status|remove`:

```
└── cmd_state         state init|export|set|show|remove (marker 4 KiB, spec)
```

e dopo `Key helpers: ...` aggiungi: `write_state_unit`, `state_init_file`,
`state_set_marker`, `state_dump`, `is_state_unit`, `reserved_name_check`
(state-record 4 KiB, spec docs/plan-state-export.md §3).

- [ ] **Step 2: AGENT.md §2 e §3 — una riga ciascuno**

§2 Design decisions (in fondo alla tabella):

| Decision | Rationale |
|---|---|
| state unit = `nbdkit file dir=$STATE_DIR` su porta dedicata, NO `--filter=limit`/`multi-conn` | marker `committing` leggibile da client su altri host senza consumare la slot `limit=1` (docs/plan-state-export.md) |

§3 Privilege model (dopo la riga dei block device):

- **State** (`state init`): gira come proprietario di `STATE_DIR` (`--user/--group` =
  owner della dir, default `root:root`); `state export` chowna il file al proprietario
  della dir se eseguito da root.

- [ ] **Step 3: AGENT.md §4 — gotchas**

Aggiungi (in fondo alla lista):

```
- `state` e' un nome di export RISERVATO (collide con nbd-export-state.service):
  `export --name state` e' rifiutato (reserved_name_check).
- Lo stato NON e' un lock: `committing` e' un marker; il fail-stop e' dei client
  che lo leggono (spec §3).
```

- [ ] **Step 4: Spec §8 — nota di scope**

In `docs/plan-state-export.md` §8, sopra la lista, aggiungi:

> Stato v1 (2026-09-19): le tre open question restano FUORI scope (confermato
> con l'utente, opzione A). Il piano di implementazione:
> `docs/superpowers/plans/2026-09-19-state-export.md`.

- [ ] **Step 5: Verifica finale della suite**

```bash
bash -n nbd-export.sh
tests/test-state-file.sh && tests/test-state-unit.sh && tests/test-state-cmd.sh \
    && tests/test-state-list-status.sh && tests/test-state-nbdkit-verify.sh \
    && tests/test-state-init.sh && tests/test-state-export.sh
```

Expected: uscita 0 da ognuno; poi il vecchio suite regressione:
`tests/test-path-normalize.sh`, `tests/test-multi-conn-filter.sh`, gli altri
test AGENT §6 non devono rompersi (nessuna modifica ai path esistenti).

- [ ] **Step 6: Commit**

```bash
git add AGENT.md docs/plan-state-export.md
git commit -m "state-export: docs (AGENT.md, spec §8 scope) + suite verde"
```

---

## Self-review (checklist)

- **Spec coverage:** §1-2 (topologia/decisioni) → Task 2-7; §3 contratto formato → Task 2-5; §4.1-4.4 → Task 2/3/4; §5 verifiche → Task 1 (gate) + Task 5 (size) + Task 6 (list/status); §7 test → Task 7; §6 client → fuori scope (altro repo); §8 → escluso (opzione A), nota in Task 8. Nessun gap.
- **Placeholder scan:** nessun TBD; ogni step ha codice/atteso/esito.
- **Type consistency:** `state_path/state_init_file/state_set_marker/state_name_of/write_u64_le/read_u64_le/state_write_str` (Task 2) usati da Task 5 e dal test Task 2; `write_state_unit` (Task 3) da Task 4; `cmd_state_*` (Task 4-5) dal dispatcher Task 4 e dal main; `is_state_unit/reserved_name_check` (Task 6) usati in list/status/export. Nomi identici tra i task.