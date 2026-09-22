#!/usr/bin/env bash
#
# Test (no root): list/status non confondono l'unit di stato con un export dati
# (spec §4.3: extract_image produrrebbe IMAGE='dir=...'); nome 'state' riservato.
set -u

NBD_EXPORT_SOURCED=1
. "$(dirname "$0")/../nbd-export.sh"

U="$(mktemp -d)"   # UNIT_DIR temporaneo, DOPO il sourcing
UNIT_DIR="$U"
D="$(mktemp -d)"
fail=0

write_fake_unit() {  # $1=nome, $2=exec
    printf '[Service]\nExecStart=%s\n' "$2" > "$U/$UNIT_PREFIX-$1.service"
}

write_fake_unit disk '/usr/bin/nbdkit --foreground --filter=limit file /srv/disk.img --exportname=disk --port=10809 --user=root --group=root'
write_fake_unit state '/usr/bin/nbdkit --foreground file dir='"$D"' --port=10819 --ipaddr=0.0.0.0 --user=root --group=root'

echo "== list esclude l'unit di stato =="
out="$(cmd_list)"
printf '%s\n' "$out" | grep -qF 'disk' && echo "ok: export dati listato" || { echo "FAIL: disk assente"; fail=1; }
printf '%s\n' "$out" | grep -qE 'dir=.*status' && { echo "FAIL: unit di stato in list"; fail=1; } || echo "ok: unit di stato esclusa"
printf '%s\n' "$out" | grep -qF 'dir=' && { echo "FAIL: colonna IMAGE con dir="; fail=1; } || echo "ok: nessuna IMAGE 'dir='"

echo "== status state: blocco dedicato, mai IMAGE dir= =="
out="$(cmd_status state)"
printf '%s\n' "$out" | grep -qF 'state-export unit' && echo "ok: nota unit di stato" || { echo "FAIL: $out"; fail=1; }
printf '%s\n' "$out" | grep -qF "state dir: $D" && echo "ok: state dir mostrata" || { echo "FAIL: $out"; fail=1; }
printf '%s\n' "$out" | grep -qE 'image:|dir=' && { echo "FAIL: immagine spurie"; fail=1; } || echo "ok: nessuna immagine spuria"

echo "== status disk continua a funzionare =="
cmd_status disk >/dev/null && echo "ok: status disk ok" || { echo "FAIL: status disk"; fail=1; }

echo "== nome 'state' riservato per gli export dati =="
if reserved_name_check disk; then echo "ok: disk ammesso"; else echo "FAIL: disk rifiutato"; fail=1; fi
if ( reserved_name_check state ) 2>/dev/null; then echo "FAIL: 'state' ammesso"; fail=1; else echo "ok: 'state' rifiutato"; fi

rm -rf "$U" "$D"
exit $fail