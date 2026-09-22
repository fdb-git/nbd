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
[ "$(hex "$F" 0 10)" = "4e424453540000000100" ] \
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
snap0="$(hex "$F" 0 9)"       # magic+version fino a 8
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
b9="$(u8 "$F" 9)"
if ( state_set_marker demo bogus "$D" ) >/dev/null 2>&1; then echo "FAIL: bogus accettato"; fail=1; else echo "ok: 'bogus' rifiutato"; fi
[ "$(u8 "$F" 9)" = "$b9" ] && echo "ok: nessuna scrittura parziale" || { echo "FAIL: byte modificato"; fail=1; }
if ( state_set_marker nope clean "$D" ) >/dev/null 2>&1; then echo "FAIL: file mancante accettato"; fail=1; else echo "ok: file mancante -> errore"; fi

rm -rf "$D"
exit $fail