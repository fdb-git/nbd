#!/usr/bin/env bash
#
# Test: path_normalize() deve assolutizzare il path SENZA risolvere i symlink.
#
# Motivo: /dev/disk/by-id/... e /dev/disk/by-uuid/... sono symlink stabili tra
# reboot; /dev/sdXN cambia. L'export deve usare il path stabile fornito dall'utente.
# (Bug: realpath risolve /dev/disk/by-id/...-part3 -> /dev/sdbN)
#
set -u

NBD_EXPORT_SOURCED=1
. "$(dirname "$0")/../nbd-export.sh"

fail=0
# t <expected> <input>
t() {
    local expected="$1" input="$2" got
    got="$(path_normalize "$input")"
    if [ "$got" != "$expected" ]; then
        echo "FAIL: path_normalize '$input' -> '$got' (expected '$expected')" >&2
        fail=1
    else
        echo "ok: '$input' -> '$got'"
    fi
}

# simulazione by-id: symlink verso un device (il symlink non va risolto)
D="$(mktemp -d)"
ln -s /dev/sdb3 "$D/by-id-ata-EXAMPLE_DISK_SERIAL-part3"

# 1. symlink by-id preservato (stabile tra reboot)
t "$D/by-id-ata-EXAMPLE_DISK_SERIAL-part3" "$D/by-id-ata-EXAMPLE_DISK_SERIAL-part3"
# 2. by-uuid preservato (path relativo in CWD non c'entra)
ln -s /dev/sdb1 "$D/by-uuid-ABCD-1234"
t "$D/by-uuid-ABCD-1234" "$D/by-uuid-ABCD-1234"
# 3. path device diretto invariato
t "/dev/sdb1" "/dev/sdb1"
# 4. path relativo esistente -> assoluto rispetto al CWD (systemd esegue con WorkingDirectory=/)
touch "$D/disk.img"
( cd "$D" && t "$D/disk.img" "disk.img" )
# 5. path assoluto esistente invariato
t "$D/disk.img" "$D/disk.img"
rm -rf "$D"

exit $fail