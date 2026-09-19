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
# porta = prima libera >= 10829: una porta occupata (es. residuo di una run
# precedente) non deve mai produrre evidenza falsa contro un server estraneo
port_free() { ! ss -ltnH 2>/dev/null | awk '{print $4}' | grep -qE ":$1\$"; }
P=10829
for p in $(seq 10829 10859); do
    if port_free "$p"; then P=$p; break; fi
done
port_free "$P" || { echo "FAIL: nessuna porta libera in fascia 10829-10859"; exit 1; }
echo "porta: $P (prima libera >= 10829)"
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