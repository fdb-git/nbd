#!/usr/bin/env bash
#
# Test (root): export_state_auto riusa la config esistente dell'unit di stato
# (soluzione C). Se l'unit esiste con --dir/--port custom e il servizio e' giu',
# 'export' deve RIAVVIARLA senza riscriverla:
#  - ExecStart byte-identico (nessuna riscrittura: dir/port/TLS preservati)
#  - server ripartito sulla porta custom, .status nella dir custom
#  - la porta di stato di DEFAULT (10819) non viene mai consultata (qui e'
#    occupata da un listener estraneo: l'export deve riuscire comunque)
#
# Porte: 10919 (stato custom), 10859 (export dati), 10819 (occupata di proposito).
set -u
[ "$(id -u)" = 0 ] || { echo "SKIP: richiede root" >&2; exit 0; }

SCRIPT="$(cd "$(dirname "$0")/.." && pwd)/nbd-export.sh"
D="/var/lib/launch-nbd-reuse.$$"
CUST="$D/state-custom"        # dir di stato custom (diversa da STATE_DIR)
XP=10919                      # porta di stato custom (diversa da 10819)
A="reuse$$"
DP=10859
fail=0
unit="/etc/systemd/system/nbd-export-state.service"
STDIR="/var/lib/launch-nbd/state"
httpid=""

cleanup() {
    "$SCRIPT" remove "$A" >/dev/null 2>&1 || true
    systemctl stop nbd-export-state.service >/dev/null 2>&1 || true
    rm -f "$unit"
    systemctl daemon-reload
    [ -n "$httpid" ] && kill "$httpid" >/dev/null 2>&1 || true
    rm -rf "$D" "$STDIR/$A.status"
}
trap cleanup EXIT
cleanup
mkdir -p "$CUST"

echo "== setup: unit di stato custom (dir $CUST, porta $XP), poi la fermo =="
"$SCRIPT" state init --dir "$CUST" --port $XP >/dev/null 2>&1 \
    || { echo "FAIL: state init custom"; fail=1; }
ex_before="$(sed -n 's/^ExecStart=//p' "$unit")"
printf '%s\n' "$ex_before" | grep -q "dir=$CUST" \
    || { echo "FAIL: unit senza dir custom"; fail=1; }
systemctl stop nbd-export-state.service >/dev/null 2>&1 || true
systemctl is-active nbd-export-state.service >/dev/null && { echo "FAIL: unit non fermata"; fail=1; }

echo "== occupo 10819 (porta di stato DEFAULT) con un listener estraneo =="
python3 -m http.server 10819 --bind 127.0.0.1 >/dev/null 2>&1 &
httpid=$!
ok=0
for i in $(seq 1 10); do
    ss -ltnH 2>/dev/null | grep -qE ':10819\b' && { ok=1; break; }; sleep 0.3
done
[ "$ok" = 1 ] && echo "ok: 10819 occupato" || { echo "SKIP: non sono riuscito a occupare 10819 (test fragile)"; }

echo "== export dati: deve riuscire e NON riscrivere l'unit =="
truncate -s 64M "$D/a.img"
"$SCRIPT" export "$D/a.img" --name "$A" --port $DP >/dev/null 2>&1 \
    || { echo "FAIL: export dati (porta default consultata?)"; fail=1; }

ex_after="$(sed -n 's/^ExecStart=//p' "$unit")"
[ -n "$ex_after" ] || { echo "FAIL: unit di stato sparita"; fail=1; }
[ "$ex_before" = "$ex_after" ] \
    && echo "ok: ExecStart byte-identico (config custom preservata)" || {
        echo "FAIL: unit RISCRITTA:"; echo "  prima: $ex_before"; echo "  dopo:  $ex_after"; fail=1; }

echo "== server ripartito sulla porta custom, .status nella dir custom =="
systemctl is-active nbd-export-state.service >/dev/null \
    && echo "ok: server di stato attivo" || { echo "FAIL: server non attivo"; fail=1; }
[ -f "$CUST/$A.status" ] && [ "$(stat -c %s "$CUST/$A.status")" = 4096 ] \
    && echo "ok: $A.status nella dir custom (4 KiB)" || { echo "FAIL: .status assente in $CUST"; fail=1; }
[ -f "$STDIR/$A.status" ] \
    && { echo "FAIL: .status finito nella STATE_DIR di default"; fail=1; } \
    || echo "ok: nessun .status nella dir default"

echo "== lettura: state show --dir custom =="
"$SCRIPT" state show "$A" --dir "$CUST" 2>/dev/null | grep -qF 'state:    clean' \
    && echo "ok: marker clean" || { echo "FAIL: show non pulito"; fail=1; }

exit $fail