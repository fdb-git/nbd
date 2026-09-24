#!/usr/bin/env bash
#
# Test end-to-end (root): docs/plan-state-export.md §7.
# - state init + state export -> file 4 KiB, magic ok, unit attiva su 10819
# - scrittura del marker via NBD (come il launcher client) + FLUSH
# - riavvio di nbd-export-state.service -> lo stato persiste
# - nbdinfo sullo stato mentre la slot limit=1 dei dati e' occupata
# - state set clean/admin round-trip; state remove lascia intatto l'export dati
#
# Porte: 10819 (stato, spec §2) e 10849 (export dati demo, SOLO durante il test:
# 10809 e' occupata dalla produzione fdbnode).
# STATE_DIR sotto /var/lib: l'unit usa PrivateTmp=yes + ProtectHome=yes (hardening
# spec §4.3) che isolano /tmp, /var/tmp, /home, /root dal processo nbdkit.
# ESECUZIONE: PATH con SOLO i tool libnbd (~/opt/libnbd-tools/usr/bin), NON la build
# home di nbdkit 1.36.3: have_filter sonda 'nbdkit' dal PATH ma le unit avviano
# /usr/bin/nbdkit (1.24 di sistema) -> se PATH ha il 1.36.3 (che HA il filtro
# multi-conn) l'unit dati viene scritta con --filter=multi-conn e fallisce su 1.24.
set -u
[ "$(id -u)" = 0 ] || { echo "SKIP: richiede root" >&2; exit 0; }

SCRIPT="$(cd "$(dirname "$0")/.." && pwd)/nbd-export.sh"
D="/var/lib/launch-nbd-verify.$$"
mkdir -p "$D"
NAME=demo
SP=10819
DP=10849
fail=0

cleanup() {
    if [ "${HOLDER:-0}" -gt 0 ]; then kill "$HOLDER" 2>/dev/null || true; wait "$HOLDER" 2>/dev/null || true; fi
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
ok_listen=0
for i in 1 2 3 4 5 6 7 8 9 10; do   # bind asincrono dopo lo start (Type=simple)
    ss -ltnH | grep -qE ":$SP\b" && { ok_listen=1; break; }
    sleep 0.5
done
[ "$ok_listen" = 1 ] && echo "ok: unit in ascolto su $SP" || { echo "FAIL: ascolto"; fail=1; }

poll_port() {  # $1=porta; attende fino a 5s
    local i
    for i in 1 2 3 4 5 6 7 8 9 10; do
        ss -ltnH | grep -qE ":$1\b" && return 0
        sleep 0.5
    done
    return 1
}

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
# --no-state: il test gestisce lo stato esplicitamente con --dir $D (il default
# auto di export userebbe STATE_DIR di sistema; la dir di stato qui e' $D)
"$SCRIPT" export "$D/data.img" --name "$NAME" --port $DP --no-state 2>&1 || { echo "FAIL: export dati"; fail=1; }
poll_port $DP && echo "ok: export dati in ascolto su $DP" || { echo "FAIL: export dati non in ascolto"; fail=1; }
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
if [ "$HOLDER" -gt 0 ]; then kill "$HOLDER" 2>/dev/null || true; wait "$HOLDER" 2>/dev/null || true; fi

exit $fail