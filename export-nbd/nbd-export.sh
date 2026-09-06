#!/usr/bin/env bash
#
# nbd-export.sh - manage NBD exports (nbdkit) as systemd services
#
# Manage file-based and block-device NBD exports with systemd units.
# Optional local config (.env, gitignored); all state lives in the generated
# unit files. Copy .env.example to .env only to override defaults.
#
# Usage:
#   nbd-export.sh create <path> --size N [--allocated] [--fstype F] [--force]
#   nbd-export.sh export <path> [options]           # create unit + start
#   nbd-export.sh start|stop|restart <name>
#   nbd-export.sh enable|disable <name> [--now]
#   nbd-export.sh status [name]                     # no live NBD probes (limit=1!)
#   nbd-export.sh list
#   nbd-export.sh remove <name>
#   nbd-export.sh tls create|status|remove [options]
#   nbd-export.sh help
#
# Default security behaviour:
#   - clients limited to 1 concurrent connection: --filter=limit limit=1
#   - multi-conn disabled: --filter=multi-conn multi-conn-mode=disable
#   - file exports run as the file owner (nbdkit drops privileges; the export
#     file is opened AFTER the drop - verified in nbdkit source)
#   - block device exports run as root unless the user is in the 'disk' group
#     (nbdkit clears ALL supplementary groups: setgroups(1, &gid))
#
# Client usage (TLS):
#   nbdinfo --tls=require --tls-certificates=/path/to/client nbd://HOST:PORT/NAME
#   qemu-system-x86_64 -object tls-creds-x509,id=creds,endpoint=client,dir=/path \
#       -drive file=nbd:HOST:PORT:exportname=NAME:tls-creds=creds,format=raw
#
set -euo pipefail

# ---------------------------------------------------------------- constants
UNIT_PREFIX="nbd-export"
UNIT_DIR="/etc/systemd/system"
DEFAULT_PORT=10809
DEFAULT_ADDR="0.0.0.0"
TLS_DIR_DEFAULT="/etc/pki/nbdkit"
FSTYPES="ext4 xfs btrfs vfat"
NAME_RE='^[a-zA-Z0-9_.-]+$'
SIZE_RE='^[0-9]+[kKmMgGtTpP]?$'
# Default UUID for raw partition export (leave empty to require explicit path)
TARGET_UUID=""

# ------------------------------------------------------------------ .env config
# Optional local config (gitignored): copy .env.example to .env and override
# the defaults above (e.g. DEFAULT_PORT, UNIT_DIR, TLS_DIR_DEFAULT). Loaded
# only if present; the script still works with no .env at all.
if [ -f "$(dirname "${BASH_SOURCE[0]}")/.env" ]; then
    set -a
    # shellcheck disable=SC1090
    . "$(dirname "${BASH_SOURCE[0]}")/.env"
    set +a
fi

# ------------------------------------------------------------------ helpers
die() { echo "error: $*" >&2; exit 1; }
warn() { echo "warning: $*" >&2; }

require_root() {
    [ "$(id -u)" = 0 ] || die "this operation requires root (try: sudo $0 $*)"
}

name_valid() { [[ "$1" =~ $NAME_RE ]]; }
unit_path() { printf '%s/%s-%s.service' "$UNIT_DIR" "$UNIT_PREFIX" "$1"; }

# absolute path WITHOUT resolving symlinks: realpath follows them, turning a
# stable /dev/disk/by-id/... (or by-uuid) export into an unstable /dev/sdXN.
# systemd units run with WorkingDirectory=/ so relative paths must still be
# absolutized - realpath -s does that while keeping by-id/by-uuid intact.
path_normalize() {
    realpath -s -- "$1"
}

# is an nbdkit filter .so available? (NBDKIT_FILTER_DIR overrides for tests)
have_filter() {
    local name="$1" dir="${NBDKIT_FILTER_DIR:-}"
    if [ -z "$dir" ]; then
        dir="$(nbdkit --dump-config 2>/dev/null | awk -F= '$1=="filterdir"{print $2}')"
    fi
    [ -n "$dir" ] && [ -f "$dir/nbdkit-$name-filter.so" ]
}

# quote a value for use inside a systemd ExecStart line (shell-like quoting)
exec_escape() {
    local s="$1"
    case "$s" in
        *"'"*|*$'\n'*) die "unsupported character in value: $s" ;;
    esac
    if [[ "$s" =~ ^[A-Za-z0-9_./:+=,%-]+$ ]]; then
        printf '%s' "$s"
    else
        printf "'%s'" "$s"
    fi
}

size_bytes() {
    local s="$1" num
    [[ "$s" =~ $SIZE_RE ]] || die "invalid size: '$s' (use e.g. 1G, 500M, 4096)"
    num="${s%[kKmMgGtTpP]}"
    case "${s: -1}" in
        k|K) echo $((num * 1024)) ;;
        m|M) echo $((num * 1024 ** 2)) ;;
        g|G) echo $((num * 1024 ** 3)) ;;
        t|T) echo $((num * 1024 ** 4)) ;;
        p|P) echo $((num * 1024 ** 5)) ;;
        *)   echo "$num" ;;
    esac
}

port_free() { ! ss -ltnH 2>/dev/null | awk '{print $4}' | grep -qE ":$1\$"; }

port_auto() {
    local p
    for p in $(seq "$DEFAULT_PORT" $((DEFAULT_PORT + 100))); do
        if port_free "$p"; then printf '%s' "$p"; return 0; fi
    done
    die "no free port found in range $DEFAULT_PORT-$((DEFAULT_PORT + 100))"
}

# extract the image path from a unit's ExecStart line
extract_image() {
    local exec="$1" img
    img="$(printf '%s\n' "$exec" | sed -n "s/.* file '\\([^']*\\)'.*/\\1/p")"
    if [ -z "$img" ]; then
        img="$(printf '%s\n' "$exec" | sed -n 's/.* file \([^ ]*\).*/\1/p')"
    fi
    printf '%s' "$img"
}

# --------------------------------------------------------------- validation
tls_dir_validate() {
    local d="$1" f
    for f in ca-cert.pem server-cert.pem server-key.pem client-cert.pem client-key.pem; do
        [ -f "$d/$f" ] || die "missing TLS file $d/$f (run: nbd-export tls create)"
    done
}

# check LUKS version when --luks-key is used (nbdkit luks filter = LUKS1 only)
luks_version_check() {
    local path="$1" magic
    magic="$(dd if="$path" bs=1 count=8 2>/dev/null | od -An -tx1 | tr -d ' \n')"
    case "$magic" in
        4c554b53babe0001) : ;;                       # LUKS1 - supported
        4c554b53babe0002) warn "source is LUKS2; nbdkit luks filter supports LUKS1 only - client will see raw encrypted data" ;;
        *) warn "--luks-key given but '$path' has no LUKS header at offset 0" ;;
    esac
}

# ------------------------------------------- privilege model (per source type)
# file  -> --user/--group = owner of the image (stat)
# block -> root by default, unless: user in 'disk' group, --as-root, or the
#          dedicated nbdkit system user is auto-created and added to 'disk'
#          (nbdkit clears ALL supplementary groups: setgroups(1, &gid))

# a dedicated unprivileged user for block device exports (drop interno:
# the unit runs as root, nbdkit performs the internal --user/--group drop)
ensure_nbdkit_user() {
    if ! id nbdkit >/dev/null 2>&1; then
        useradd --system --no-create-home --shell /usr/sbin/nologin nbdkit \
            || die "failed to create system user 'nbdkit'"
    fi
    if ! getent group disk | grep -qw nbdkit; then
        usermod -aG disk nbdkit || die "failed to add user 'nbdkit' to 'disk' group"
    fi
}

derive_user_group() {
    local path="$1" type="$2"
    user=""; group=""
    if [ -n "$opt_user" ]; then
        user="$opt_user"
        group="${opt_group:-$(id -gn "$opt_user" 2>/dev/null || true)}"
        if [ "$type" = block ] && [ "$user" != root ]; then
            if ! getent group disk | grep -qw "$user"; then
                die "user '$user' cannot open block devices (not in 'disk' group); grant: usermod -aG disk $user"
            fi
        fi
    elif [ "$type" = file ]; then
        user="$(stat -c %U "$path")"
        group="$(stat -c %G "$path")"
        id -u "$user" >/dev/null 2>&1 || { warn "owner '$user' of $path not a valid user - running as root"; user=""; group=""; }
    else
        # block device, no explicit user: run de-privileged as nbdkit (drop
        # interno); --as-root keeps the legacy all-root behaviour
        if [ "$as_root" != 1 ]; then
            ensure_nbdkit_user
            user="nbdkit"
            group="disk"
        fi
    fi
}

# ------------------------------------------------------------ unit generation
build_exec() {
    local path="$1" name="$2" port="$3" addr="$4"
    local filters="" exec mc_filter=0
    [ "$opt_log"       = 1 ] && filters="$filters --filter=log"
    [ "$opt_exit_last" = 1 ] && filters="$filters --filter=exitlast"
    [ -n "$opt_allow" ] && filters="$filters --filter=ip"
    [ "$opt_limit" -gt 0 ] && filters="$filters --filter=limit"
    # multi-conn filter is missing from older nbdkit (e.g. 1.24 in jammy);
    # without it nbdkit's safe default also does not advertise multi-conn,
    # so we degrade gracefully instead of producing a broken unit
    mc_filter=0
    have_filter multi-conn && mc_filter=1
    [ "$mc_filter" = 1 ] && filters="$filters --filter=multi-conn"
    [ -n "$opt_luks" ] && filters="$filters --filter=luks"

    exec="/usr/bin/nbdkit --foreground$filters"
    [ "$opt_readonly" = 1 ] && exec="$exec --readonly"
    exec="$exec file $(exec_escape "$path")"
    exec="$exec --exportname=$(exec_escape "$name") --port=$port --ipaddr=$(exec_escape "$addr")"

    [ -n "$opt_allow" ] && exec="$exec allow=$(exec_escape "$opt_allow") deny=all"
    [ "$opt_limit" -gt 0 ] && exec="$exec limit=$opt_limit"
    if [ "$mc_filter" = 1 ]; then
        [ "$opt_multi_conn" = 1 ] && exec="$exec multi-conn-mode=auto" || exec="$exec multi-conn-mode=disable"
    elif [ "$opt_multi_conn" = 1 ]; then
        warn "--multi-conn requested but nbdkit multi-conn filter is unavailable - keeping safe default (no multi-conn advertisement)"
    fi
    [ -n "$opt_luks" ] && exec="$exec passphrase=+$(exec_escape "$opt_luks")"
    [ -n "$opt_threads" ] && exec="$exec --threads=$opt_threads"

    if [ "$tls_mode" != off ]; then
        exec="$exec --tls=$tls_mode"
        if [ -n "$opt_psk" ]; then
            exec="$exec --tls-psk=$(exec_escape "$opt_psk")"
        else
            exec="$exec --tls-certificates=$(exec_escape "$tls_dir")"
            [ "$tls_mode" = require ] && exec="$exec --tls-verify-peer"
        fi
    fi
    [ -n "$user" ] && exec="$exec --user=$(exec_escape "$user")"
    [ -n "$group" ] && exec="$exec --group=$(exec_escape "$group")"
    EXEC="$exec"
}

write_unit() {
    local name="$1" image="$2" unit restarts
    unit="$(unit_path "$name")"
    if [ "$opt_exit_last" = 1 ]; then restarts="always"; else restarts="on-failure"; fi
    {
        printf '[Unit]\n'
        printf 'Description=NBD export %s (%s)\n' "$name" "$(basename "$image")"
        printf 'After=network-online.target\n'
        printf 'Wants=network-online.target\n'
        printf '\n[Service]\n'
        printf 'Type=simple\n'
        printf 'ExecStart=%s\n' "$EXEC"
        printf 'Restart=%s\n' "$restarts"
        printf 'RestartSec=%s\n' "$([ "$opt_exit_last" = 1 ] && echo 5 || echo 2)"
        printf 'NoNewPrivileges=yes\n'
        printf 'PrivateTmp=yes\n'
        printf 'ProtectHome=yes\n'
        printf 'ProtectSystem=%s\n' "$([ "$opt_readonly" = 1 ] && echo strict || echo full)"
        [ "$opt_readonly" = 1 ] && printf 'ReadOnlyPaths=%s\n' "$(exec_escape "$image")"
        printf 'RestrictAddressFamilies=AF_INET AF_UNIX\n'
        printf '\n[Install]\n'
        printf 'WantedBy=multi-user.target\n'
    } > "$unit"
    chmod 644 "$unit"
}

# ---------------------------------------------------------------- subcommands
cmd_create() {
    local path="" size="" fstype="" alloc=0 force=0
    while [ $# -gt 0 ]; do
        case "$1" in
            --size)      size="$2";  shift 2 ;;
            --fstype)    fstype="$2"; shift 2 ;;
            --allocated) alloc=1;    shift ;;
            --force)     force=1;    shift ;;
            -h|--help)   usage_create; exit 0 ;;
            -*) die "create: unknown option $1" ;;
            *) [ -z "$path" ] && path="$1" || die "create: too many arguments"; shift ;;
        esac
    done
    [ -n "$path" ] || die "create: missing path"
    [ -n "$size" ] || die "create: --size required"
    size_bytes "$size" >/dev/null
    if [ -e "$path" ]; then
        [ -f "$path" ] || die "create: $path exists and is not a regular file"
        [ "$force" = 1 ] || die "create: $path already exists (use --force)"
    fi
    if [ -n "$fstype" ]; then
        case " $FSTYPES " in *" $fstype "*) ;; *) die "create: unsupported fstype '$fstype' (use one of:$FSTYPES)" ;; esac
    fi
    if [ ! -e "$path" ]; then
        if [ "$alloc" = 1 ]; then
            fallocate -l "$size" "$path" || die "create: fallocate failed"
            echo "created (preallocated): $path ($size)"
        else
            truncate -s "$size" "$path" || die "create: truncate failed"
            echo "created (sparse): $path ($size)"
        fi
    fi
    if [ -n "$fstype" ]; then
        mkfs.$fstype -q "$path" || die "create: mkfs.$fstype failed"
        echo "formatted: $path as $fstype"
    fi
}

cmd_export() {
    local path="" name="" port="$DEFAULT_PORT" addr="$DEFAULT_ADDR"
    local opt_user="" opt_group="" opt_readonly=0 opt_create=0 size="" fstype=""
    local opt_limit=1 opt_multi_conn=0 opt_allow="" opt_luks="" opt_exit_last=0 opt_log=0
    local tls_mode=off tls_dir="$TLS_DIR_DEFAULT" opt_psk="" force=0 as_root=0 opt_threads="" opt_uuid=""
    local type="" unit

    while [ $# -gt 0 ]; do
        case "$1" in
            --name)      name="$2";  shift 2 ;;
            --port)      port="$2";  shift 2 ;;
            --addr)      addr="$2";  shift 2 ;;
            --user)      opt_user="$2"; shift 2 ;;
            --group)     opt_group="$2"; shift 2 ;;
            --readonly)  opt_readonly=1; shift ;;
            --create)    opt_create=1; shift ;;
            --size)      size="$2";  shift 2 ;;
            --fstype)    fstype="$2"; shift 2 ;;
            --limit)     opt_limit="$2"; shift 2 ;;
            --multi-conn) opt_multi_conn=1; shift ;;
            --allow)     opt_allow="$2"; shift 2 ;;
            --luks-key)  opt_luks="$2"; shift 2 ;;
            --exit-last) opt_exit_last=1; shift ;;
            --log)       opt_log=1; shift ;;
            --tls)       tls_mode=require; shift ;;
            --tls=on)    tls_mode=on; shift ;;
            --tls=require) tls_mode=require; shift ;;
            --tls=off)   tls_mode=off; shift ;;
            --tls-dir)   tls_dir="$2"; shift 2 ;;
            --psk)       opt_psk="$2"; shift 2 ;;
            --force)     force=1; shift ;;
            --as-root)   as_root=1; shift ;;
            --threads)   opt_threads="$2"; shift 2 ;;
            --uuid)      opt_uuid="$2"; shift 2 ;;
            -h|--help)   usage_export; exit 0 ;;
            -*) die "export: unknown option $1" ;;
            *) [ -z "$path" ] && path="$1" || die "export: too many arguments"; shift ;;
        esac
    done
    require_root
    [ -n "$path" ] || die "export: missing image path"
    # resolve UUID if provided
    if [ -n "$opt_uuid" ]; then
        [ "$opt_uuid" = "auto" ] && die "export: --uuid auto not supported (use explicit UUID or omit)"
        path="/dev/disk/by-uuid/${opt_uuid}"
    elif [ -n "$TARGET_UUID" ]; then
        path="/dev/disk/by-uuid/${TARGET_UUID}"
    fi
    [ -e "$path" ] || die "export: UUID not found: ${TARGET_UUID:-$opt_uuid}"
    name="${name:-$(basename "$path")}"
    name_valid "$name" || die "export: invalid name '$name' (use [a-zA-Z0-9_-])"
    [ -f "$(unit_path "$name")" ] && [ "$force" = 0 ] && die "export: '$name' already exists (use --force to replace)"

    # create the image if requested / missing
    if [ ! -e "$path" ]; then
        [ "$opt_create" = 1 ] || die "export: $path does not exist (use --create --size N)"
        [ -n "$size" ] || die "export: --size required with --create"
        cmd_create "$path" --size "$size" ${fstype:+--fstype "$fstype"} --force
    fi
    # absolute path, but keep by-id/by-uuid symlinks as-is for persistence
    # across reboots (systemd units run with WorkingDirectory=/)
    path="$(path_normalize "$path")"
    [ -f "$path" ] || [ -b "$path" ] || die "export: $path is not a regular file or block device"
    type="block"; [ -f "$path" ] && type="file"
    # warn if LUKS2 (header at offset 0, not supported by nbdkit luks filter)
    local luks_magic
    luks_magic="$(dd if="$path" bs=1 count=8 2>/dev/null | od -An -tx1 | tr -d ' \n')"
    case "$luks_magic" in
        4c554b53babe0001) ;;  # LUKS1
        4c554b53babe0002) warn "source is LUKS2; nbdkit luks filter supports LUKS1 only" ;;
        *) ;;
    esac

    # block devices: refuse mounted sources unless readonly / force
    if [ "$type" = block ] && findmnt -n -o SOURCE "$path" >/dev/null 2>&1; then
        if [ "$opt_readonly" = 1 ]; then
            warn "exporting mounted block device $path read-only (cache-coherency hazard)"
        elif [ "$force" = 1 ]; then
            warn "exporting mounted block device $path with --force (data-corruption hazard!)"
        else
            die "refusing to export mounted block device $path (host page cache vs client writes); use --readonly or --force"
        fi
    fi

    # port
    if [ "$port" = auto ]; then port="$(port_auto)"; fi
    [[ "$port" =~ ^[0-9]+$ ]] && [ "$port" -ge 1 ] && [ "$port" -le 65535 ] || die "export: invalid port '$port'"
    if [ -f "$(unit_path "$name")" ] && [ "$force" = 1 ]; then
        :   # replacing an existing unit - its own port is ours (we restart it)
    else
        port_free "$port" || die "export: port $port already in use (pick another or --port auto)"
    fi

    # numeric options
    [[ "$opt_limit" =~ ^[0-9]+$ ]] || die "export: invalid --limit '$opt_limit'"
    [ -n "$opt_threads" ] && { [[ "$opt_threads" =~ ^[0-9]+$ ]] || die "export: invalid --threads"; }

    # luks key
    if [ -n "$opt_luks" ]; then
        [ -r "$opt_luks" ] || die "export: --luks-key file not readable: $opt_luks"
        luks_version_check "$path"
    fi
    # tls
    if [ "$tls_mode" != off ]; then
        if [ -n "$opt_psk" ]; then
            [ -r "$opt_psk" ] || die "export: --psk file not readable: $opt_psk"
        else
            tls_dir_validate "$tls_dir"
        fi
    fi
    # ip filter
    [ -n "$opt_allow" ] && [[ "$opt_allow" =~ ^[A-Za-z0-9./,:_-]+$ ]] || { [ -z "$opt_allow" ] || die "export: invalid --allow '$opt_allow'"; }

    derive_user_group "$path" "$type"
    [ -n "$user" ] && { id -u "$user" >/dev/null 2>&1 || die "export: invalid user '$user'"; }
    [ -n "$group" ] && { getent group "$group" >/dev/null || die "export: invalid group '$group'"; }

    build_exec "$path" "$name" "$port" "$addr"
    write_unit "$name" "$path"
    systemctl daemon-reload
    if [ "$force" = 1 ] && systemctl is-active --quiet "$UNIT_PREFIX-$name.service"; then
        systemctl restart "$UNIT_PREFIX-$name.service"
    elif ! systemctl start "$UNIT_PREFIX-$name.service"; then
        echo "unit failed to start; check: journalctl -u $UNIT_PREFIX-$name.service -e" >&2
        exit 1
    fi
    echo "exported: $path -> nbd://$addr:$port/$name  (unit: $UNIT_PREFIX-$name.service)"
    echo "  client:  nbdinfo nbd://$addr:$port/$name"
    if [ "$tls_mode" != off ]; then
        echo "  tls:     $tls_mode (certs: $tls_dir${opt_psk:+ / psk: $opt_psk})"
    fi
    if [ -n "$user" ]; then
        echo "  runs as: $user${group:+:$group}"
    fi
    return 0
}

cmd_simple() {
    local action="$1" name="$2" unit
    unit="$(unit_path "$name")"
    name_valid "$name" || die "$action: invalid name '$name'"
    [ -f "$unit" ] || die "$action: no export named '$name' (use: nbd-export export ...)"
    shift 2
    systemctl "$action" "$UNIT_PREFIX-$name.service" "$@"
}

cmd_remove() {
    local name="$1" unit
    unit="$(unit_path "$name")"
    name_valid "$name" || die "remove: invalid name '$name'"
    [ -f "$unit" ] || die "remove: no export named '$name'"
    systemctl disable "$UNIT_PREFIX-$name.service" >/dev/null 2>&1 || true
    systemctl stop "$UNIT_PREFIX-$name.service" >/dev/null 2>&1 || true
    rm -f "$unit"
    systemctl daemon-reload
    echo "removed export '$name' (image file untouched)"
}

cmd_list() {
    local units name exec port img type u
    units=("$UNIT_DIR"/$UNIT_PREFIX-*.service)
    [ -e "${units[0]}" ] || { echo "no exports configured"; return 0; }
    printf '%-20s %-6s %-9s %-6s %s\n' NAME TYPE STATE PORT IMAGE
    for u in "${units[@]}"; do
        name="${u##*/$UNIT_PREFIX-}"; name="${name%.service}"
        exec="$(sed -n 's/^ExecStart=//p' "$u")"
        port="$(printf '%s\n' "$exec" | grep -oP -- '--port=\K[0-9]+' | head -1)"
        img="$(extract_image "$exec")"
        type="file"; [ -b "$img" ] && type="block"
        printf '%-20s %-6s %-9s %-6s %s\n' "$name" "$type" \
            "$(systemctl is-active "$UNIT_PREFIX-$name.service" 2>/dev/null || echo unknown)" \
            "${port:-?}" "$img"
    done
}

cmd_status() {
    local name="$1" unit exec img port state mainpid ru ru_g
    if [ -z "$name" ]; then cmd_list; return; fi
    unit="$(unit_path "$name")"
    name_valid "$name" || die "status: invalid name '$name'"
    [ -f "$unit" ] || die "status: no export named '$name'"
    exec="$(sed -n 's/^ExecStart=//p' "$unit")"
    img="$(extract_image "$exec")"
    port="$(printf '%s\n' "$exec" | grep -oP -- '--port=\K[0-9]+' | head -1)"
    state="$(systemctl is-active "$UNIT_PREFIX-$name.service" 2>/dev/null || echo unknown)"
    mainpid="$(systemctl show -p MainPID --value "$UNIT_PREFIX-$name.service" 2>/dev/null || echo -)"
    ru="$(printf '%s\n' "$exec" | grep -oP -- '--user=\K[^ ]+' | head -1 || true)"
    ru_g="$(printf '%s\n' "$exec" | grep -oP -- '--group=\K[^ ]+' | head -1 || true)"
    printf 'export:    %s\n' "$name"
    printf 'unit:      %s\n' "$unit"
    printf 'image:     %s (%s)\n' "$img" "$([ -b "$img" ] && echo block || { [ -f "$img" ] && echo file || echo missing; })"
    printf 'port:      %s\n' "${port:-?}"
    printf 'runs as:   %s\n' "${ru:-root}${ru_g:+:$ru_g}"
    printf 'state:     %s\n' "$state"
    printf 'main pid:  %s\n' "$mainpid"
    if [ "$state" = active ] && [ -n "$port" ]; then
        ss -ltnH 2>/dev/null | awk -v p=":$port\$" '$4 ~ p {printf "listening:  yes (%s)\n", $4; f=1} END{if (!f) print "listening:  NO"}'
    fi
    if printf '%s\n' "$exec" | grep -q -- '--tls='; then
        printf 'tls:       %s\n' "$(printf '%s\n' "$exec" | grep -oP -- '--tls=\K[a-z]+' || echo on)"
    fi
    if printf '%s\n' "$exec" | grep -q -- '--filter=limit'; then
        lim="$(printf '%s\n' "$exec" | grep -oP -- 'limit=\K[0-9]+' | head -1 || true)"
        printf 'limit:     %s client%s\n' "${lim:-?}" "$([ "${lim:-0}" = 1 ] && echo "" || echo s)"
    fi
}

# ------------------------------------------------------------------- tls cmd
cmd_tls() {
    local sub="${1:-help}"; shift || true
    case "$sub" in
        create) cmd_tls_create "$@" ;;
        status) cmd_tls_status "$@" ;;
        remove) require_root; cmd_tls_remove "$@" ;;
        -h|--help|help) usage_tls; exit 0 ;;
        *) die "tls: unknown subcommand '$sub' (see: nbd-export tls help)" ;;
    esac
}

cmd_tls_create() {
    local dir="$TLS_DIR_DEFAULT" host="" days=3650 force=0 san="" ip="" t
    while [ $# -gt 0 ]; do
        case "$1" in
            --dir)   dir="$2";  shift 2 ;;
            --host)  host="$2"; shift 2 ;;
            --days)  days="$2"; shift 2 ;;
            --force) force=1;   shift ;;
            -h|--help) usage_tls; exit 0 ;;
            -*) die "tls create: unknown option $1" ;;
            *) die "tls create: unexpected argument '$1'" ;;
        esac
    done
    require_root
    [[ "$days" =~ ^[0-9]+$ ]] || die "tls create: invalid --days '$days'"
    [ -e "$dir" ] && [ ! -d "$dir" ] && die "tls create: $dir exists and is not a directory"
    if [ -d "$dir" ] && [ -f "$dir/ca-cert.pem" ] && [ "$force" = 0 ]; then
        die "tls create: certificates already exist in $dir (use --force to regenerate)"
    fi
    [ -z "$host" ] && host="$(hostname)"
    if [[ "$host" =~ ^[0-9.]+$ ]] || [[ "$host" =~ ^[0-9a-fA-F:]+$ ]]; then
        san="IP:$host"
        ip="$(hostname -I 2>/dev/null | awk '{print $1}')"
        [ -n "$ip" ] && [ "$ip" != "$host" ] && san="$san,DNS:$(hostname)"
    else
        san="DNS:$host"
        ip="$(hostname -I 2>/dev/null | awk '{print $1}')"
        [ -n "$ip" ] && san="$san,IP:$ip"
    fi
    mkdir -p "$dir" && chmod 700 "$dir"
    umask 077
    openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:3072 -out "$dir/ca-key.pem" 2>/dev/null
    openssl req -x509 -new -key "$dir/ca-key.pem" -sha256 -days "$days" \
        -out "$dir/ca-cert.pem" -subj "/CN=nbdkit CA" \
        -addext "basicConstraints=critical,CA:TRUE" \
        -addext "keyUsage=critical,keyCertSign,cRLSign" \
        -addext "subjectKeyIdentifier=hash"
    openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out "$dir/server-key.pem" 2>/dev/null
    openssl req -new -key "$dir/server-key.pem" -sha256 -out "$dir/server.csr" -subj "/CN=$host"
    cat > "$dir/ext-server.cnf" <<EOF
extendedKeyUsage=serverAuth
subjectAltName=$san
keyUsage=digitalSignature,keyEncipherment
basicConstraints=CA:FALSE
EOF
    openssl x509 -req -in "$dir/server.csr" -CA "$dir/ca-cert.pem" -CAkey "$dir/ca-key.pem" \
        -CAcreateserial -sha256 -days "$days" -out "$dir/server-cert.pem" \
        -extfile "$dir/ext-server.cnf" 2>/dev/null
    openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out "$dir/client-key.pem" 2>/dev/null
    openssl req -new -key "$dir/client-key.pem" -sha256 -out "$dir/client.csr" -subj "/CN=nbd-client"
    cat > "$dir/ext-client.cnf" <<EOF
extendedKeyUsage=clientAuth
keyUsage=digitalSignature,keyEncipherment
basicConstraints=CA:FALSE
EOF
    openssl x509 -req -in "$dir/client.csr" -CA "$dir/ca-cert.pem" -CAkey "$dir/ca-key.pem" \
        -CAcreateserial -sha256 -days "$days" -out "$dir/client-cert.pem" \
        -extfile "$dir/ext-client.cnf" 2>/dev/null
    rm -f "$dir/server.csr" "$dir/client.csr" "$dir/ca-cert.srl" "$dir/ext-server.cnf" "$dir/ext-client.cnf"
    rm -f "$dir/server.csr" "$dir/client.csr" "$dir/ca-cert.srl"
    chmod 600 "$dir"/ca-key.pem "$dir"/server-key.pem "$dir"/client-key.pem
    chmod 644 "$dir"/ca-cert.pem "$dir"/server-cert.pem "$dir"/client-cert.pem
    echo "TLS certificates created in $dir"
    echo "  server cert SAN: $san"
    echo "  distribute to clients: $dir/ca-cert.pem $dir/client-cert.pem $dir/client-key.pem"
}

cmd_tls_status() {
    local dir="$TLS_DIR_DEFAULT" f extra
    if [ "${1:-}" = --dir ]; then dir="$2"; shift 2; fi
    if [ ! -d "$dir" ]; then echo "no TLS directory: $dir"; return 0; fi
    for f in ca-cert.pem server-cert.pem server-key.pem client-cert.pem client-key.pem; do
        if [ -f "$dir/$f" ]; then
            extra=""
            case "$f" in *-cert.pem)
                extra=" $(openssl x509 -noout -subject -enddate -in "$dir/$f" 2>/dev/null | tr '\n' ' ')"
            ;; esac
            printf '%-16s present%s\n' "$f" "$extra"
        else
            printf '%-16s MISSING\n' "$f"
        fi
    done
}

cmd_tls_remove() {
    local dir="$TLS_DIR_DEFAULT" force=0
    while [ $# -gt 0 ]; do
        case "$1" in
            --dir)   dir="$2"; shift 2 ;;
            --force) force=1;  shift ;;
            *) die "tls remove: unknown argument '$1'" ;;
        esac
    done
    [ "$force" = 1 ] || die "tls remove: use --force to delete $dir"
    [ -d "$dir" ] || die "tls remove: no directory $dir"
    rm -rf "$dir"
    echo "removed $dir"
}

# --------------------------------------------------------------------- usage
usage_create() {
    cat <<'EOF'
usage: nbd-export create <path> --size N [--allocated] [--fstype F] [--force]

Create an image file (sparse by default) optionally formatted.
  --size N      size with suffix K/M/G/T/P (e.g. 40G)
  --allocated   preallocate with fallocate instead of sparse truncate
  --fstype F    format with mkfs (ext4 xfs btrfs vfat)
  --force       overwrite an existing file
EOF
}

usage_export() {
    cat <<'EOF'
usage: nbd-export export <path> [options]

Export a file or block device over NBD as a systemd service.
  --name NAME      export name (default: basename of path)
  --port N         tcp port (default 10809, 'auto' = first free >= 10809)
  --addr IP        listen address (default 0.0.0.0)
  --user USER      run nbdkit as USER (default: file owner / root for block)
  --group GROUP    primary group (default: file group / USER's primary group)
  --readonly       export read-only (--filter=readonly)
  --create         create the image file if missing (needs --size)
  --size N         size for --create
  --fstype F       format new image (ext4 xfs btrfs vfat)
  --limit N        max concurrent clients (default 1; 0 = unlimited)
  --multi-conn     allow multi-conn advertisement (default: disabled)
  --allow CIDR     restrict clients by IP (--filter=ip allow=CIDR deny=all)
  --luks-key FILE  decrypt LUKS1 disk via --filter=luks passphrase=+FILE
  --exit-last      exit when last client disconnects (needs reconnect)
  --log            enable --filter=log (debug to journald)
  --tls            require TLS + client certificate verification
  --tls=on         enable TLS encryption only (no client cert check)
  --tls-dir DIR    certificate directory (default /etc/pki/nbdkit)
  --psk FILE       use pre-shared key auth instead of certificates
  --threads N      nbdkit worker threads
  --uuid UUID      use UUID to resolve /dev/disk/by-uuid/<UUID> (optional)
  --as-root        force root context for block devices (no warning)
  --force          replace an existing export of the same name

Privileges (verified against nbdkit source):
  file   -> runs as the file owner (image is opened AFTER privilege drop)
  block  -> runs as root unless --user given; non-root user must be in
            the 'disk' group and --group=disk (nbdkit clears supplementary
            groups via setgroups(1, &gid))
EOF
}

usage_tls() {
    cat <<'EOF'
usage: nbd-export tls create [--dir DIR] [--host HOST] [--days N] [--force]
       nbd-export tls status [--dir DIR]
       nbd-export tls remove --dir DIR --force

Manage TLS certificates for nbdkit (default dir /etc/pki/nbdkit).
Creates: ca-cert.pem, ca-key.pem, server-cert.pem, server-key.pem,
         client-cert.pem, client-key.pem (openssl; server SAN = host+IP).
EOF
}

usage() {
    cat <<'EOF'
nbd-export.sh - manage NBD exports (nbdkit) as systemd services

usage: nbd-export <command> [options]

Commands:
  create <path> --size N [..]   create an image file (optionally formatted)
  export <path> [options]       create unit + start serving (see: export -h)
  start|stop|restart <name>     systemctl lifecycle of an export unit
  enable|disable <name> [--now] enable/disable at boot (systemd convention)
  status [name]                 unit + port state (no live NBD probe)
  list                          table of all configured exports
  remove <name>                 stop + delete unit (image untouched)
  tls create|status|remove      TLS certificate management
  help                          this text

Client examples:
  nbdinfo nbd://HOST:PORT/NAME
  nbdinfo --tls=require --tls-certificates=/path/to/certs nbd://HOST:PORT/NAME
  qemu-system-x86_64 -object tls-creds-x509,id=creds,endpoint=client,dir=/certs \
      -drive file=nbd:HOST:PORT:exportname=NAME:tls-creds=creds,format=raw

Default export security: limit=1, multi-conn-mode=disable.
EOF
}

# ---------------------------------------------------------------------- main
main() {
    local cmd="${1:-help}"
    if [ $# -gt 0 ]; then shift; fi
    case "$cmd" in
        create) cmd_create "$@" ;;
        export) cmd_export "$@" ;;
        start|stop|restart|enable|disable) require_root; cmd_simple "$cmd" "${1:-}" ;;
        status) cmd_status "${1:-}" ;;
        list) cmd_list ;;
        remove) require_root; cmd_remove "${1:-}" ;;
        tls) cmd_tls "$@" ;;
        help|-h|--help) usage; exit 0 ;;
        *) die "unknown command '$cmd' (see: nbd-export help)" ;;
    esac
}
if [ "${NBD_EXPORT_SOURCED:-0}" != 1 ]; then
    main "$@"
fi
