# AGENT.md — NBD export manager (nbdkit / systemd)

Project directory: `./` (vedi `nbd-export.sh`)
Script: `nbd-export.sh` — single bash file, no install; configurazione locale
opzionale in `.env` (gitignorato, copia di `.env.example`).

---

## 1. Requirements (as agreed with the user)

1. **Minimalistic approach** — one bash script, no installer, no config files,
   state lives only in generated systemd unit files.
2. **Scope: server role only** — manage nbdkit exports. NO nbd-client, NO mounts,
   NO client management.
3. **Manage file-based devices**: create image files (`truncate` sparse default,
   `fallocate` with `--allocated`, optional `mkfs`) AND export them.
4. **Manage block devices** in `/dev` (e.g. `/dev/md0`) via the `file` plugin.
5. **Default client limit = 1**: `--filter=limit limit=1` + `--filter=multi-conn
   multi-conn-mode=disable` baked into every export (user-specified exact syntax).
6. **TLS certificate management**: `tls create|status|remove` subcommands.
7. **Everything as systemd services** — systemd conventions: `start/stop/restart`,
   `enable/disable [--now]` separate operations.
8. **Per-source-type privilege model** (see §3).

## 2. Design decisions

| Decision | Rationale |
|----------|-----------|
| Generated units `/etc/systemd/system/nbd-export-<name>.service` | no config files; unit IS the state |
| `export` = write unit + daemon-reload + start; `enable` = boot persistence | systemd custom |
| `remove` = disable+stop, delete unit, daemon-reload; **image file never touched** | safety |
| `status`/`list` never live-probe via nbdinfo | a probe would consume the single `limit=1` slot |
| Filters order: log, exitlast, ip, limit, multi-conn, luks (last = closest to plugin) | nbdkit filter semantics |
| `--readonly` uses nbdkit built-in `-r` | `readonly` filter not packaged in Ubuntu |
| `--allow CIDR` → `--filter=ip allow=CIDR deny=all` | access control without TLS |
| TLS via openssl (CA + server + client, SAN=IP+DNS) in `/etc/pki/nbdkit` | `certtool` not installed; GnuTLS accepts PEM |
| `--luks-key FILE` → `--filter=luks passphrase=+FILE` | LUKS1 only (see §4) |

## 3. Privilege model (verified against nbdkit source)

- nbdkit `change_user()` calls `setgroups(1, &gid)` → **all supplementary groups
  are cleared**; only the primary `--group=` matters.
- The export file/device is opened AFTER the privilege drop (per-connection
  `backend_open()`), so `--user` is real security.
- **File** (`-f`): runs as `--user=<stat owner> --group=<stat group>` (e.g. `<user>:<group>`).
- **Block device** (`-b`): runs as **root** by default (devices are `root:disk 660`;
  the user can't open them). Warning printed with hint:
  `usermod -aG disk <user>` + `--user=<user> --group=disk` (requires explicit `--group=disk`).
- `--user/--group` flags override; `--as-root` forces root for block devices.

## 4. Environment facts & gotchas (IMPORTANT)

- Host: ARM64 (e.g. Odroid class), Ubuntu, nbd driver **built-in**
  (`/proc/devices` shows `43 nbd`; no module file → `modinfo nbd` fails).
- nbdkit 1.36.3; libnbd tools installed: `nbdinfo`, `nbdsh`, `nbdcopy`, `nbddump`,
  `nbdfuse`. `nbd-client` NOT installed.
- nbdkit TLS is GnuTLS — **strict**:
  - CA cert must have `keyUsage=critical,keyCertSign,cRLSign` + `basicConstraints=CA:TRUE`.
  - `openssl x509 -req` does NOT support `-addext`, and `-extfile <(process subst)`
    is silently ignored (pipe not seekable) → must write a real temp ext file.
- nbdkit option is `--ipaddr=` (NOT `--listen=`).
- Image paths in units must be absolute (`realpath`) — units run with `WorkingDirectory=/`.
- The `readonly` FILTER is not installed → use nbdkit's `-r/--readonly` option.
- Export names allow dots (`disk.img`) — `NAME_RE='^[a-zA-Z0-9_.-]+$'`.
- `--force` on export restarts the running service (and skips the port-busy check
  for its own port).
- `/dev/md0` is **LUKS2** — `--filter=luks` supports **LUKS1 only**, so
  `--luks-key` warns and clients see raw encrypted data.
- A manually-started (daemonized, no `--foreground`) nbdkit ignored SIGTERM and
  needed `kill -9`. Managed units always use `--foreground` (no issue).
- Mounted block devices: refused unless `--readonly` (warn) or `--force` (danger).

## 5. Script structure

```
nbd-export.sh
├── cmd_create        create <path> --size N [--allocated] [--fstype F] [--force]
├── cmd_export        export <path> [--name --port --addr --user --group
│                     --readonly --create --size --fstype --limit --multi-conn
│                     --allow --luks-key --exit-last --log --tls --tls=on
│                     --tls-dir --psk --threads --as-root --force]
├── cmd_simple        start|stop|restart|enable|disable <name> [--now]
├── cmd_remove        remove <name>
├── cmd_list          list (systemctl state + port, no live probe)
├── cmd_status        status [name]
└── cmd_tls           tls create|status|remove
```
Key helpers: `derive_user_group` (per-type privileges), `build_exec` (ExecStart
assembly), `write_unit` (systemd unit with hardening: `NoNewPrivileges`,
`PrivateTmp`, `ProtectHome`, `ProtectSystem=strict`+`ReadOnlyPaths` when
`--readonly`), `extract_image`, `exec_escape`.

## 6. Verification procedure (repeat these tests)

All commands as **root**, from the project directory (containing `nbd-export.sh`).

### 6.1 Prerequisites
```bash
bash -n nbd-export.sh                       # syntax check
nbdkit --version                            # 1.36.x expected
nbdinfo --help >/dev/null && nbdsh --help >/dev/null   # libnbd tools
openssl version                             # 3.0.x
ss -ltnH | grep -E '1080[9-9]' || true      # no stale listeners
systemctl is-active nbd-export-test || true
```

### 6.2 File export (create + export + security defaults)
```bash
./nbd-export.sh export disk.img --name test --port 10809   # file, <user>:<group>
./nbd-export.sh status test
# expect: state active, listening yes, runs as <user>:<group>, limit 1 client
nbdinfo --size nbd://127.0.0.1:10809/test                   # 42949672960
PID=$(systemctl show -p MainPID --value nbd-export-test)
grep -E '^(Uid|Gid)' /proc/$PID/status                      # all 1000 (= <user>)
nbdsh -u nbd://127.0.0.1:10809/test -c 'print("mc:", h.can_multi_conn())'  # False
# limit=1: hold a connection, 2nd must be rejected
timeout 8 nbdsh -u nbd://127.0.0.1:10809/test -c 'import time; time.sleep(6)' &
sleep 2
timeout 5 nbdsh -u nbd://127.0.0.1:10809/test -c 'pass'     # FAILS (rejected)
wait
```

### 6.3 create command
```bash
./nbd-export.sh create test2.img --size 512M --fstype ext4   # sparse + mkfs
ls -lh test2.img && du -h test2.img                          # 512M apparent, ~2M real
```

### 6.4 Block device export + mounted-device safety
```bash
./nbd-export.sh export /dev/md0 --name raid --port 10810    # warning about root
nbdinfo --size nbd://127.0.0.1:10810/raid                     # 959936724992
PID=$(systemctl show -p MainPID --value nbd-export-raid)
grep -E '^(Uid|Gid)' /proc/$PID/status                        # 0 0 0 0 (root)
# LUKS2 header visible through NBD:
nbdsh -u nbd://127.0.0.1:10810/raid -c 'print(h.pread(8,0))'  # b'LUKS\xba\xbe\x00\x02'
# mounted device must be refused:
./nbd-export.sh export /dev/mapper/md0_crypt --name crypto --port 10812
#   -> error: refusing to export mounted block device ...
./nbd-export.sh export /dev/mapper/md0_crypt --name crypto --port 10812 --readonly
#   -> warning + allowed; then cleanup:
./nbd-export.sh remove crypto
```

### 6.5 TLS
```bash
./nbd-export.sh tls create --host 127.0.0.1 --force
./nbd-export.sh tls status                       # 5 files present
openssl x509 -in /etc/pki/nbdkit/server-cert.pem -noout -ext subjectAltName
#   -> must show "IP Address:127.0.0.1, DNS:<hostname>" (SAN is the failure mode!)
openssl verify -CAfile /etc/pki/nbdkit/ca-cert.pem /etc/pki/nbdkit/server-cert.pem  # OK
certtool --verify --load-ca-certificate=/etc/pki/nbdkit/ca-cert.pem \
         --infile=/etc/pki/nbdkit/server-cert.pem   # "Verified. The certificate is trusted."

./nbd-export.sh export disk.img --name secure --port 10811 --tls
# positive (mutual TLS):
nbdsh -c 'h.set_tls(2); h.set_tls_certificates("/etc/pki/nbdkit"); \
          h.connect_uri("nbd://127.0.0.1:10811/secure"); print(h.get_size())'
#   -> 42949672960
# negative (no client certs) must fail:
nbdsh -c 'h.set_tls(2); h.connect_uri("nbd://127.0.0.1:10811/secure")'
#   -> gnutls error
./nbd-export.sh remove secure
```

### 6.6 Filter options
```bash
./nbd-export.sh export disk.img --name rotest --port 10813 \
    --readonly --allow 127.0.0.1 --multi-conn --limit 2
sleep 1
nbdsh -u nbd://127.0.0.1:10813/rotest -c 'print(h.is_read_only())'      # True
nbdsh -u nbd://127.0.0.1:10813/rotest -c 'h.pwrite(b"x"*4096, 0)'       # fails (read-only)
nbdsh -u nbd://127.0.0.1:10813/rotest -c 'print(h.can_multi_conn())'    # True
./nbd-export.sh remove rotest
```

### 6.7 Lifecycle & error paths
```bash
./nbd-export.sh list                                  # table of exports
./nbd-export.sh restart test && ./nbd-export.sh stop test
systemctl is-active nbd-export-test                   # inactive
./nbd-export.sh start test && ./nbd-export.sh enable test --now
systemctl is-enabled nbd-export-test                  # enabled
./nbd-export.sh remove test                           # unit gone, disk.img untouched

# errors (each must exit non-zero with a clear message):
./nbd-export.sh export /nonexistent.img
./nbd-export.sh export disk.img --name "bad name"
./nbd-export.sh export disk.img --name dup && ./nbd-export.sh export disk.img --name dup
./nbd-export.sh export disk.img --name x --port 99999
sudo -u <user> ./nbd-export.sh export disk.img --name nope      # "requires root"
```

### 6.8 Cleanup / restore
```bash
# re-create the intended production state:
./nbd-export.sh export ./disk.img --name test --port 10809
./nbd-export.sh enable test --now
./nbd-export.sh list          # expect: test (10809, file) and raid (10810, block)
rm -f test2.img               # if created in 6.3
```

## 7. Out of scope (do NOT implement without explicit user request)
- nbd-client / client role, mounting exported devices
- Config files / installer / daemonization of the script itself
- Partitions, qemu-img, LUKS handling beyond the `--luks-key` LUKS1 filter
- PSK auth (flag `--psk` exists but is untested here)
- `--exit-last` (flag exists, untested; requires clients that reconnect)
