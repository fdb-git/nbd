#!/usr/bin/env bash
#
# Test d'integrazione (root): export di block device by-id deve
# - creare l'utente di sistema nbdkit (se manca)
# - metterlo nel gruppo disk (se non c'e')
# - esportare con --user=nbdkit --group=disk (drop interno, niente User= nell'unit)
# - non emettere piu' il warning "as root"
# - essere idempotente (nulla da rifare al secondo export)
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

echo "== export by-id (--force) gestisce l'utente nbdkit =="
output="$("$SCRIPT" export "$BYID" --name "$NAME" --force 2>&1)"
rc=$?
echo "$output"
if [ "$rc" -eq 0 ]; then echo "ok: exit 0"; else echo "FAIL: exit $rc"; fail=1; fi

echo "== utente nbdkit esiste =="
if id nbdkit >/dev/null 2>&1; then echo "ok: esiste"; else echo "FAIL: non esiste"; fail=1; fi

echo "== utente nbdkit nel gruppo disk =="
if getent group disk | grep -qw nbdkit; then echo "ok: in disk"; else echo "FAIL: non in disk"; fail=1; fi

echo "== unit usa --user=nbdkit --group=disk =="
if grep -q -- '--user=nbdkit' "$UNIT" && grep -q -- '--group=disk' "$UNIT"; then
    echo "ok: --user=nbdkit --group=disk"
else
    echo "FAIL: ExecStart=$(grep '^ExecStart=' "$UNIT")"; fail=1
fi
if grep -qE '^User=|^Group=' "$UNIT"; then
    echo "FAIL: unit contiene User=/Group= (atteso solo drop interno nbdkit)"; fail=1
else
    echo "ok: nessuna direttiva User=/Group= nell'unit"
fi

echo "== nessun warning 'as root' in output =="
if printf '%s\n' "$output" | grep -q 'as root'; then echo "FAIL: warning presente"; fail=1; else echo "ok: assente"; fi

echo "== servizio active =="
state="$(systemctl is-active nbd-export-$NAME.service 2>/dev/null || true)"
if [ "$state" = active ]; then echo "ok: active"; else echo "FAIL: $state"; fail=1; fi

echo "== idempotenza: secondo export senza warning di creazione =="
output2="$("$SCRIPT" export "$BYID" --name "$NAME" --force 2>&1)"
rc2=$?
if [ "$rc2" -eq 0 ]; then echo "ok: exit 0"; else echo "FAIL: exit $rc2"; fail=1; fi
if printf '%s\n' "$output2" | grep -q 'as root'; then echo "FAIL: warning 'as root' al secondo giro"; fail=1; else echo "ok: nessun warning"; fi

exit $fail
