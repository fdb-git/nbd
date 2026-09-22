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
[ "$(stat -c %s "$F")" = 4096 ] && [ "$(hex "$F" 0 9)" = "4e4244535400000001" ] \
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
printf 'x%.0s' {1..4096} > "$D/foreign.status"
before="$(hex "$D/foreign.status" 0 4)"
if "$SCRIPT" state export foreign --dir "$D" >/dev/null 2>&1; then echo "FAIL: accettato"; fail=1; else echo "ok: rifiutato"; fi
[ "$(hex "$D/foreign.status" 0 4)" = "$before" ] && echo "ok: file intatto" || { echo "FAIL: file modificato"; fail=1; }

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