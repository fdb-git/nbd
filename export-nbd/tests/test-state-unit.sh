#!/usr/bin/env bash
#
# Test (no root): write_state_unit genera nbd-export-state.service con:
#   - ExecStart: /usr/bin/nbdkit --foreground file dir=<DIR> --port=.. --ipaddr=..
#   - NIENTE --filter=limit / --filter=multi-conn / --readonly / --exportname
#   - hardening identico a write_unit (ma ProtectSystem=full, scrivibile)
#   - tls: --tls=require -> + --tls-verify-peer --tls-certificates
set -u

NBD_EXPORT_SOURCED=1
. "$(dirname "$0")/../nbd-export.sh"

UNIT_DIR="$(mktemp -d)"   # override DOPO il sourcing: serve solo il path
D="$(mktemp -d)"          # finto STATE_DIR
fail=0

u() { printf '%s\n' "$UNIT_DIR/nbd-export-state.service"; }
exec_line() { sed -n 's/^ExecStart=//p' "$(u)"; }

echo "== unit di base (tls off, root:root) =="
write_state_unit "$D" 10819 0.0.0.0 off /etc/pki/nbdkit root root
[ -f "$(u)" ] || { echo "FAIL: unit non creata"; fail=1; }

ex="$(exec_line)"
[ "$ex" = "/usr/bin/nbdkit --foreground file dir=$D --port=10819 --ipaddr=0.0.0.0 --user=root --group=root" ] \
    && echo "ok: ExecStart esatto" || { echo "FAIL: $ex"; fail=1; }

for banned in --filter=limit --filter=multi-conn --readonly --exportname; do
    if grep -qF -- "$banned" "$(u)"; then echo "FAIL: '$banned' presente"; fail=1; else echo "ok: senza $banned"; fi
done
for line in 'NoNewPrivileges=yes' 'PrivateTmp=yes' 'ProtectHome=yes' 'ProtectSystem=full' \
            'RestrictAddressFamilies=AF_INET AF_UNIX' 'Restart=on-failure' \
            'After=network-online.target' 'Wants=network-online.target'; do
    if grep -qF "$line" "$(u)"; then echo "ok: $line"; else echo "FAIL: manca '$line'"; fail=1; fi
done

echo "== variante tls=require =="
write_state_unit "$D" 10819 0.0.0.0 require /etc/pki/nbdkit root root
ex="$(exec_line)"
printf '%s\n' "$ex" | grep -qF -- '--tls=require' \
    && printf '%s\n' "$ex" | grep -qF -- '--tls-verify-peer' \
    && printf '%s\n' "$ex" | grep -qF -- '--tls-certificates=/etc/pki/nbdkit' \
    && echo "ok: tls=require + verify-peer + certificates" \
    || { echo "FAIL: $ex"; fail=1; }

echo "== variante tls=on (niente verify-peer) =="
write_state_unit "$D" 10819 0.0.0.0 on /etc/pki/nbdkit root root
ex="$(exec_line)"
printf '%s\n' "$ex" | grep -qF -- '--tls=on' \
    && ! printf '%s\n' "$ex" | grep -qF -- '--tls-verify-peer' \
    && echo "ok: tls=on senza verify-peer" || { echo "FAIL: $ex"; fail=1; }

rm -rf "$UNIT_DIR" "$D"
exit $fail