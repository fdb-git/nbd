#!/usr/bin/env bash
#
# Verifica end-to-end del client Go (launch-nbd) contro un server nbdkit reale.
#
# Da eseguire SUL TARGET (Linux) o su un host che lo raggiunge. Richiede root
# per creare/rimuovere il file di stato di test via nbd-export.sh.
#
# Parametri (override via env):
#   NBD_HOST           default 127.0.0.1   (server nbdkit)
#   NBD_DATA_PORT      default 10809       (export dati)
#   NBD_STATE_PORT     default 10819       (export di stato)
#   NBD_DATA_EXPORT    default fdbhome     (NON viene mai scritto)
#   NBD_TEST_EXPORT    default e2e-state   (base dei file .status di test,
#                      creati/rimossi; ai due package e2e sono aggiunti i suffissi
#                      -nbd e -qemu per isolarli, girando con -p 1)
#   NBD_EXPORT_SH      path a nbd-export.sh (ricerca automatica se assente)
#   SKIP_DRYRUN=1      salta il dry-run con QEMU=/bin/echo
#
# Cosa verifica:
#   1. prerequisiti (go, state unit, raggiungibilità porta stato)
#   2. suite unit (go test ./...)
#   3. e2e NBD reale: round-trip stato (committing/clean + FLUSH), fingerprint
#      stabile del disco dati, lifecycle commit (successo/interruzione/ripresa)
#   4. dry-run: fingerprint + overlay reali + args generati (QEMU=/bin/echo)
#
# Non boota alcuna VM e non scrive sull'export dati.
set -u

NBD_HOST="${NBD_HOST:-127.0.0.1}"
NBD_DATA_PORT="${NBD_DATA_PORT:-10809}"
NBD_STATE_PORT="${NBD_STATE_PORT:-10819}"
NBD_DATA_EXPORT="${NBD_DATA_EXPORT:-fdbhome}"
NBD_TEST_EXPORT="${NBD_TEST_EXPORT:-e2e-state}"
SKIP_DRYRUN="${SKIP_DRYRUN:-0}"

here="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "$here/.." && pwd)"          # launch-nbd/
fail=0
step() { echo; echo "==== $* ===="; }
ok()   { echo "ok: $*"; }
bad()  { echo "FAIL: $*"; fail=1; }

find_export_sh() {
    [ -n "${NBD_EXPORT_SH:-}" ] && { printf '%s' "$NBD_EXPORT_SH"; return; }
    for p in "$root/../export-nbd/nbd-export.sh" "$root/../../export-nbd/nbd-export.sh" \
             /root/dev/nbd/export-nbd/nbd-export.sh; do
        [ -f "$p" ] && { printf '%s' "$(cd "$(dirname "$p")" && pwd)/$(basename "$p")"; return; }
    done
}

step "prerequisiti"
[ "$(id -u)" = 0 ] || { echo "SKIP: richiede root (crea/rimuove il file di stato)"; exit 0; }
command -v go >/dev/null 2>&1 || { bad "go non trovato"; exit 1; }
go version
EXPORT_SH="$(find_export_sh)"
[ -n "$EXPORT_SH" ] && [ -x "$EXPORT_SH" ] && ok "nbd-export.sh: $EXPORT_SH" || { bad "nbd-export.sh non trovato (imposta NBD_EXPORT_SH)"; exit 1; }
systemctl is-active nbd-export-state.service >/dev/null 2>&1 && ok "state unit attiva" || bad "nbd-export-state.service non attiva"
if timeout 10 bash -c "exec 3<>/dev/tcp/$NBD_HOST/$NBD_STATE_PORT" 2>/dev/null; then ok "porta stato $NBD_HOST:$NBD_STATE_PORT raggiungibile"; else bad "porta stato non raggiungibile"; fi

# NOTA limit=1: una probe TCP grezza sull'export dati può essere rifiutata da
# --filter=limit (o consumare brevemente lo slot). La verifica autorevole è un
# client NBD vero (nbdinfo o il nostro e2e): qui non si marca FAIL.
# libnbd tools (se non nel PATH): export PATH="$HOME/opt/libnbd-tools/usr/bin:$PATH"
if command -v nbdinfo >/dev/null 2>&1; then
    if timeout 15 nbdinfo --size "nbd://$NBD_HOST:$NBD_DATA_PORT/$NBD_DATA_EXPORT" >/dev/null 2>&1; then
        ok "export dati $NBD_DATA_EXPORT leggibile via NBD (nbdinfo)"
    else
        echo "nota: nbdinfo non ha letto $NBD_DATA_EXPORT (limit=1 con un client connesso?); l'e2e darà la risposta autorevole"
    fi
else
    echo "nota: nbdinfo non nel PATH: salto la probe dell'export dati (opz.: export PATH=\"\$HOME/opt/libnbd-tools/usr/bin:\$PATH\")"
fi

EXP_NBD="${NBD_TEST_EXPORT}-nbd"
EXP_QEMU="${NBD_TEST_EXPORT}-qemu"
step "crea i file di stato di test ($EXP_NBD.status, $EXP_QEMU.status)"
for e in "$EXP_NBD" "$EXP_QEMU"; do
    "$EXPORT_SH" state export "$e" && ok "state export $e" || bad "state export $e fallito"
    "$EXPORT_SH" state set "$e" clean >/dev/null 2>&1 || true
done

step "suite unit (go test ./...)"
( cd "$root" && timeout 300 go test ./... ) && ok "suite unit" || bad "suite unit"

# I due package e2e condividono lo stesso server: giran separatamente (-p 1) con
# un export di stato dedicato ciascuno (nessuna race sul fixture).
step "e2e NBD reale (tag e2e)"
run_e2e() { # pkg export
    ( cd "$root" && LAUNCH_NBD_E2E=1 \
        NBD_HOST="$NBD_HOST" NBD_DATA_PORT="$NBD_DATA_PORT" NBD_STATE_PORT="$NBD_STATE_PORT" \
        NBD_DATA_EXPORT="$NBD_DATA_EXPORT" NBD_TEST_EXPORT="$2" \
        timeout 180 go test -tags e2e -v -count=1 -p 1 -run E2E "$1" )
}
if run_e2e ./internal/nbd "$EXP_NBD" && run_e2e ./internal/qemu "$EXP_QEMU"; then
    ok "e2e NBD reale (nbd + qemu)"
else
    bad "e2e NBD reale"
fi

if [ "$SKIP_DRYRUN" != "1" ]; then
step "dry-run: fingerprint + overlay reali + args (QEMU=/bin/echo)"
    if ! command -v qemu-img >/dev/null 2>&1; then
        echo "skip: qemu-img non presente"
    else
        bin="$root/bin/launch-nbd"
        ( cd "$root" && go build -o "$bin" ./cmd/launch-nbd ) || bad "build del binario"
        tmp="$(mktemp -d)"
        # nessun gate TCP grezzo: se l'export non è raggiungibile, il fingerprint
        # fallisce con un errore chiaro (limit=1: nessun'altra VM connessa)
        ( cd "$tmp" && timeout 120 "$bin" run \
            --set nbd_host="$NBD_HOST" --set nbd_port="$NBD_DATA_PORT" \
            --set nbd_export="$NBD_DATA_EXPORT" --set nbd_state_port="$NBD_STATE_PORT" \
            --set qemu=/bin/echo --set net_mode=none --set display_mode=none \
            --set disk_mode=overlay --set overlay_dir="$tmp/ovl" ) \
            && ok "dry-run completato (overlay creato sul fingerprint reale)" || bad "dry-run"
        rm -rf "$tmp"
    fi
fi

step "cleanup"
for e in "$EXP_NBD" "$EXP_QEMU"; do
    "$EXPORT_SH" state remove "$e" >/dev/null 2>&1 && ok "rimosso $e.status" || echo "nota: state remove $e"
done
"$EXPORT_SH" state show "$NBD_DATA_EXPORT" 2>/dev/null | grep -q "clean" \
    && ok "stato di $NBD_DATA_EXPORT = clean (invariato)" \
    || echo "ATTENZIONE: lo stato di $NBD_DATA_EXPORT non è clean — controllare"

echo
if [ "$fail" = 0 ]; then echo "RISULTATO: TUTTO OK"; else echo "RISULTATO: FALLIMENTI (vedi sopra)"; fi
exit "$fail"