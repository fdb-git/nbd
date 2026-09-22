#!/usr/bin/env bash
#
# Test (root): nbd-export state init — unit/attiva/ascolto + guardia nome
# riservato 'state'. Usa un STATE_DIR temporaneo e porta dedicata 10839.
set -u
[ "$(id -u)" = 0 ] || { echo "SKIP: richiede root" >&2; exit 0; }

SCRIPT="$(cd "$(dirname "$0")/.." && pwd)/nbd-export.sh"
# STATE_DIR sotto /var/lib: l'unit usa PrivateTmp=yes + ProtectHome=yes (hardening
# spec §4.3) che isolano /tmp, /var/tmp, /home, /root dal processo dell'unit
D="/var/lib/launch-nbd-verify.$$"
mkdir -p "$D"
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
    grep -qF -- "$banned" "$UNIT" && { echo "FAIL: '$banned' presente"; fail=1; } || echo "ok: senza $banned"
done
state="$(systemctl is-active nbd-export-state.service 2>/dev/null || true)"
[ "$state" = active ] && echo "ok: servizio active" || { echo "FAIL: state=$state"; fail=1; }
ok_listen=0
for i in 1 2 3 4 5 6 7 8 9 10; do   # nbdkit bind avviene dopo lo start (Type=simple)
    ss -ltnH | grep -qE ":$P\b" && { ok_listen=1; break; }
    sleep 0.5
done
[ "$ok_listen" = 1 ] && echo "ok: in ascolto su $P" || { echo "FAIL: niente in ascolto su $P"; fail=1; }

echo "== guardia: unit esistente NON di stato (export dati 'state') =="
printf '[Service]\nExecStart=/usr/bin/nbdkit --foreground file /tmp/x --exportname=state --port=10909\n' > "$UNIT"
systemctl daemon-reload
if "$SCRIPT" state init --dir "$D" --port $P 2>&1; then echo "FAIL: sovrascritta senza --force"; fail=1; else echo "ok: rifiutata senza --force"; fi

echo "== --force sostituisce l'unit =="
"$SCRIPT" state init --dir "$D" --port $P --force >/dev/null 2>&1
ex="$(sed -n 's/^ExecStart=//p' "$UNIT")"
printf '%s\n' "$ex" | grep -qF "file dir=$D" && echo "ok: --force ha scritto l'unit di stato" || { echo "FAIL: $ex"; fail=1; }

exit $fail