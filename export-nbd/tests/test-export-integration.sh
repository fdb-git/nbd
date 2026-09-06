#!/usr/bin/env bash
#
# Test d'integrazione (root): export di un block device by-id.
# - deve uscire con 0 (set -e non deve uccidere lo script dopo le stampe)
# - l'unit deve contenere il path by-id stabile, NON /dev/sdXN
# - l'unit NON deve usare --filter=multi-conn (assente in nbdkit 1.24)
# - il servizio deve risultare active
#
# Richiede i valori reali in export-nbd/.env (gitignorato):
#   TEST_EXPORT_NAME=<nome export reale>
#   TEST_BYID=/dev/disk/by-id/<serial>-part3
#   TEST_SCRIPT=<path a nbd-export.sh>   (default: ./nbd-export.sh)
#
set -u
[ "$(id -u)" = 0 ] || { echo "SKIP: richiede root" >&2; exit 0; }

CONF="$(cd "$(dirname "$0")/.." && pwd)/.env"
[ -f "$CONF" ] && { set -a; . "$CONF"; set +a; }

NAME="${TEST_EXPORT_NAME:-}"
BYID="${TEST_BYID:-}"
SCRIPT="${TEST_SCRIPT:-$(cd "$(dirname "$0")/.." && pwd)/nbd-export.sh}"
if [ -z "$NAME" ] || [ -z "$BYID" ]; then
    echo "SKIP: imposta TEST_EXPORT_NAME e TEST_BYID in export-nbd/.env" >&2
    exit 0
fi
UNIT=/etc/systemd/system/nbd-export-$NAME.service
fail=0

echo "== comando export (--force) =="
output="$("$SCRIPT" export "$BYID" --name "$NAME" --force 2>&1)"
rc=$?
echo "$output"
if [ "$rc" -eq 0 ]; then
    echo "ok: exit code 0"
else
    echo "FAIL: exit code $rc"
    fail=1
fi

echo "== unit usa il path by-id (non /dev/sdXN) =="
if grep -qF "file $BYID " "$UNIT" && ! grep -qE 'file /dev/sd[a-z][0-9]' "$UNIT"; then
    echo "ok: ExecStart contiene $BYID"
else
    echo "FAIL: ExecStart=$(grep '^ExecStart=' "$UNIT")"
    fail=1
fi

echo "== niente --filter=multi-conn (filtro assente in nbdkit 1.24) =="
if grep -q -- '--filter=multi-conn' "$UNIT"; then
    echo "FAIL: l'unit richiede un filtro non installato"
    fail=1
else
    echo "ok: nessun --filter=multi-conn"
fi

echo "== servizio active =="
state="$(systemctl is-active nbd-export-$NAME.service 2>/dev/null || true)"
if [ "$state" = active ]; then
    echo "ok: servizio active"
else
    echo "FAIL: servizio $state"
    fail=1
fi

exit $fail
