#!/usr/bin/env bash
#
# Test (root): ciclo di vita legato tra export dati e stato di commit.
#  - export crea automaticamente <name>.status (+ auto-init del server se assente)
#  - export --no-state / --no-status NON crea il file
#  - remove <name> rimuove anche il file di stato (silenzioso, modello A:
#    il server di stato resta attivo)
#  - list mostra l'unit di stato come riga singola 'state'
#
# Porte: 10819 (stato, default) e 10859-10862 (export dati di prova).
# Dati: STATO_DIR e unit di stato sono isolate/rimosse dal cleanup.
set -u
[ "$(id -u)" = 0 ] || { echo "SKIP: richiede root" >&2; exit 0; }

SCRIPT="$(cd "$(dirname "$0")/.." && pwd)/nbd-export.sh"
D="/var/lib/launch-nbd-verify.$$"
A="auto$$"; B="nostate$$"; C="nostatus$$"
DP=10859
fail=0
STDIR="/var/lib/launch-nbd/state"

cleanup() {
    "$SCRIPT" remove "$A" >/dev/null 2>&1 || true
    "$SCRIPT" remove "$B" >/dev/null 2>&1 || true
    "$SCRIPT" remove "$C" >/dev/null 2>&1 || true
    systemctl stop nbd-export-state.service >/dev/null 2>&1 || true
    rm -f /etc/systemd/system/nbd-export-state.service
    systemctl daemon-reload
    rm -rf "$D"
}
trap cleanup EXIT
cleanup
mkdir -p "$D"

echo "== export crea .status + auto-init del server di stato =="
truncate -s 64M "$D/a.img"
"$SCRIPT" export "$D/a.img" --name "$A" --port $DP >/dev/null 2>&1 \
    || { echo "FAIL: export dati"; fail=1; }
F="$STDIR/$A.status"
[ -f "$F" ] && [ "$(stat -c %s "$F")" = 4096 ] \
    && echo "ok: .status creato (4 KiB)" || { echo "FAIL: stato non creato"; fail=1; }
systemctl is-active nbd-export-state.service >/dev/null \
    && echo "ok: server di stato attivo (auto-init)" || { echo "FAIL: server non attivo"; fail=1; }
ok=0
for i in $(seq 1 10); do
    ss -ltnH | grep -qE ':10819\b' && { ok=1; break; }; sleep 0.5
done
[ "$ok" = 1 ] && echo "ok: server in ascolto su 10819" || { echo "FAIL: nessun listener 10819"; fail=1; }
"$SCRIPT" state show "$A" 2>/dev/null | grep -qF 'state:    clean' \
    && echo "ok: marker clean" || { echo "FAIL: show non pulito"; fail=1; }
( systemctl is-enabled nbd-export-state.service >/dev/null 2>&1 ) \
    && echo "ok: unit di stato abilitata al boot" || { echo "FAIL: unit non abilitata"; fail=1; }

echo "== list mostra la riga singola 'state' =="
"$SCRIPT" list | grep -qE '^state[[:space:]]+state[[:space:]]+' \
    && echo "ok: riga 'state' in list" || { echo "FAIL: riga state assente"; fail=1; }

echo "== export --no-state / --no-status NON creano il file =="
truncate -s 64M "$D/b.img"; truncate -s 64M "$D/c.img"
"$SCRIPT" export "$D/b.img" --name "$B" --port $((DP+1)) --no-state >/dev/null 2>&1 \
    || { echo "FAIL: export --no-state"; fail=1; }
"$SCRIPT" export "$D/c.img" --name "$C" --port $((DP+2)) --no-status >/dev/null 2>&1 \
    || { echo "FAIL: export --no-status"; fail=1; }
[ ! -f "$STDIR/$B.status" ] && [ ! -f "$STDIR/$C.status" ] \
    && echo "ok: nessun .status creato" || { echo "FAIL: .status creato con opt-out"; fail=1; }

echo "== remove rimuove unit + .status, il server resta attivo (modello A) =="
"$SCRIPT" remove "$A" >/dev/null || { echo "FAIL: remove $A"; fail=1; }
[ ! -f "$F" ] && echo "ok: .status rimosso da remove" || { echo "FAIL: .status ancora presente"; fail=1; }
[ ! -f "/etc/systemd/system/nbd-export-$A.service" ] \
    && echo "ok: unit dati rimossa" || { echo "FAIL: unit dati presente"; fail=1; }
systemctl is-active nbd-export-state.service >/dev/null \
    && echo "ok: server di stato ancora attivo" || { echo "FAIL: server fermato"; fail=1; }

exit $fail