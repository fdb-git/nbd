#!/usr/bin/env bash
# =============================================================================
# launch-nbd.sh
#
# Boot a QEMU VM whose disks are:
#   1) CD-ROM : distro install/live ISO, opzionale via --iso <file|URL>
#   2) Boot   : the NBD export served by nbdkit on server:10809 (raw)
#
# The NBD connection is made by the *host* QEMU process; the guest only sees a
# normal local virtio disk, so no special guest networking is required.
# --iso <URL> scarica (curl -C -: riprende i file incompleti), verifica SHA256 e
# avvia la VM; --iso <file> avvia direttamente da un ISO locale.
#
# Usage:
#   ./launch-nbd.sh               # boot the installed OS from the NBD disk
#   ./launch-nbd.sh --overlay     # local qcow2 on top of the NBD disk:
#                                        #   writes stay local until committed -
#                                        #   push them to the server with:
#                                        #   ./launch-nbd.sh --commit
#                                        #   (or online: monitor> block-commit disk0)
#   ./launch-nbd.sh --snapshot    # throwaway overlay, writes discarded on exit
#   ./launch-nbd.sh --cache writeback|none|unsafe|writethrough|directsync
#   ./launch-nbd.sh --aio io_uring|native|threads
#   ./launch-nbd.sh --display gtk|sdl|vnc|spice|none  # default: spice (clipboard condiviso)
#   ./launch-nbd.sh --vga virtio|std|qxl|vmware|none   # virtio-gpu = best perf
#   ./launch-nbd.sh --gl on|off   # enable virgl 3D accel w/ virtio vga (needs host GL)
#   ./launch-nbd.sh --audio hda|virtio|none   # sound card (backend = host pipewire/pa)
#   ./launch-nbd.sh --net dual|nat|hostonly|tap|bridged|none   # network model (default dual)
#   ./launch-nbd.sh --tap tap0                # TAP device for --net tap/dual (default tap0)
#   ./launch-nbd.sh --tap-subnet 192.168.100.0/24  # private net: host .1 <-> guest .2
#   ./launch-nbd.sh --bridge br0             # bridge ifname for --net bridged
#   ./launch-nbd.sh --port tcp:2222:22       # forward host:port -> guest:port (repeatable)
#   ./launch-nbd.sh --iso <file|URL>         # ISO di installazione (URL => download+checksum)
#   ./launch-nbd.sh --daemon      # background the VM
#   ./launch-nbd.sh --monitor-socket /tmp/vm-mon.sock  # HMP socket: system_wakeup/system_powerdown
#   ./launch-nbd.sh --help
#
# Caching: QEMU caches guest writes per the device write-cache (default on) and
# tolerates short NBD outages via reconnect-delay=10.  --overlay adds a local
# qcow2 layer with metadata cache (64M) so hot blocks are served from local
# disk while the rest is streamed from the server.  nbdkit server-side filters
# (--filter=cache/readahead on the server) also exist, but nbdkit's docs say
# client-side caching is usually more effective.
#
# Senza --iso si avvia direttamente dal disco NBD.
# =============================================================================
set -euo pipefail

# ---------------------------------------------------------------------------
# Configurazione: tutti i default stanno in .env (gitignorato), copia di
# .env.example. Il file viene eseguito come shell, quindi:
#   - i flag CLI (parsing più sotto) hanno precedenza sui valori di .env
#   - le variabili d'ambiente esportate vengono sovrascritte da .env
# ---------------------------------------------------------------------------
ISO_URL="${ISO_URL:-}"          # solo se impostato esplicitamente (--iso <URL>, env)
ISO_DIR="$(cd "$(dirname "$0")" && pwd)"
ISO_FILE="${ISO_FILE:-}"        # vuoto = nessun ISO; --iso <file|URL> lo imposta

if [ -f "$ISO_DIR/.env" ]; then
    set -a
    # shellcheck disable=SC1090
    . "$ISO_DIR/.env"
    set +a
else
    echo "[warn] ${ISO_DIR}/.env non trovato: uso le variabili d'ambiente (vedi .env.example)" >&2
fi

# NBD_URI è derivato da NBD_HOST/NBD_PORT/NBD_EXPORT; override possibile in .env
NBD_URI="${NBD_URI:-nbd://${NBD_HOST:-}:${NBD_PORT:-}/${NBD_EXPORT:-}}"

ISO_BOOT=0               # 1 = --iso dato: il CD-ROM boota prima del disco
DAEMON=0
PF=()                          # port forwards: "tcp:hostport:guestport" (ripetibile)

# --- stato overlay / monitor (non configurabile via .env) -------------------
OVERLAY=0                 # 0 = raw NBD (writes go straight to the server), 1 = overlay
SNAPSHOT=0                # 1 = throwaway overlay (writes discarded on exit)
COMMIT=0                  # 1 = qemu-img commit overlay back to the server, then exit
MONITOR=0                 # 1 = -monitor stdio (per block-commit, ecc.)
MON_SOCK=""               # opzionale: ~ HMP su socket unix (wakeup/powerdown remoto)
OVERLAY_FILE="${ISO_DIR}/overlay.qcow2"

# ---------------------------------------------------------------------------
# Argument parsing
# ---------------------------------------------------------------------------
usage() {
    sed -n '2,44p' "$0" | sed 's/^# \{0,1\}//'
    exit 0
}
while [ $# -gt 0 ]; do
    case "$1" in
        --daemon)    DAEMON=1 ;;
        --vnc)       DISPLAY_MODE="vnc" ;;
        --display)   shift; DISPLAY_MODE="$1" ;;
        --vga)       shift; VGA_MODE="$1" ;;
        --gl)        shift; GL="$1" ;;
        --audio)     shift; AUDIO_MODE="$1" ;;
        --net)       shift; NET_MODE="$1" ;;
        --bridge)    shift; BRIDGE_IF="$1" ;;
        --tap)       shift; TAP_DEV="$1" ;;
        --tap-subnet) shift; TAP_SUBNET="$1" ;;
        --port)      shift; PF+=("$1") ;;
        --tcg)       ACCEL="tcg" ;;
        --kvm)       ACCEL="kvm" ;;
        --overlay)   OVERLAY=1 ;;
        --snapshot)  SNAPSHOT=1 ;;
        --commit)    COMMIT=1 ;;
        --monitor)   MONITOR=1 ;;
        --monitor-socket) shift; MON_SOCK="$1" ;;
        --cache)     shift; CACHE_MODE="$1" ;;
        --aio)       shift; AIO="$1" ;;
        --no-reconnect) RECONNECT_DELAY="0" ;;
        --iso)       shift; ISO_FILE="$1"
                     ISO_BOOT=1
                     case "$ISO_FILE" in
                         http://*|https://*)
                             ISO_URL="$ISO_FILE"
                             ISO_FILE="${ISO_DIR}/$(basename "$ISO_FILE")" ;;
                     esac ;;
        --mem)       shift; MEM_MB="$1" ;;
        --cpus)      shift; CPUS="$1" ;;
        --help|-h)   usage ;;
        *) echo "unknown option: $1" >&2; usage ;;
    esac
    shift
done

# ---------------------------------------------------------------------------
# Checks
# ---------------------------------------------------------------------------
# Tutti i default vivono in .env (vedi .env.example): fallire subito se manca
# qualcosa di necessario. I flag CLI qui sotto hanno già avuto la precedenza.
for v in NBD_HOST NBD_PORT NBD_EXPORT NBD_URI QEMU QEMU_IMG MEM_MB CPUS ACCEL \
         NET_MODE TAP_DEV TAP_SUBNET BRIDGE_IF \
         DISPLAY_MODE SPICE_PORT VGA_MODE GL AUDIO_MODE AUDIO_DRV \
         CACHE_MODE AIO RECONNECT_DELAY QCACHE_MB L2_CACHE_MB; do
    if [ -z "${!v:-}" ]; then
        echo "error: variabile mancante: $v" >&2
        echo "       copia .env.example in .env e adatta i valori (o esporta $v)" >&2
        exit 1
    fi
done

command -v "$QEMU" >/dev/null 2>&1 || {
    echo "error: $QEMU not found. Install it with:"
    echo "  sudo rpm-ostree install qemu-system-x86 && sudo systemctl reboot"
    exit 1
}

# KVM acceleration (needs /dev/kvm and hardware virt support)
if [ "$ACCEL" = "auto" ]; then
    [ -e /dev/kvm ] && ACCEL="kvm" || ACCEL="tcg"
fi
if [ "$ACCEL" = "kvm" ] && [ ! -e /dev/kvm ]; then
    echo "[warn] /dev/kvm not found - falling back to TCG emulation (slow)"
    ACCEL="tcg"
elif [ "$ACCEL" = "kvm" ]; then
    echo "[ok] KVM available - using hardware acceleration"
fi
# NOTE: this QEMU build rejects '-accel=kvm'; the option must be two tokens
ACCEL_ARGS=(-accel "$ACCEL")

# cache mode -> device write-cache / blockdev cache.direct / cache.no-flush
# (QEMU >= 5: cache.writeback is no longer a -blockdev option; it is the
#  'write-cache' property of the guest device, e.g. virtio-blk)
case "$CACHE_MODE" in
    writeback)    WC="on";  CD="off"; CNF="off" ;;
    none)         WC="on";  CD="on";  CNF="off" ;;
    writethrough) WC="off"; CD="off"; CNF="off" ;;
    directsync)   WC="off"; CD="on";  CNF="off" ;;
    unsafe)       WC="on";  CD="off"; CNF="on" ;;
    *) echo "error: unknown --cache mode '$CACHE_MODE'" >&2; exit 1 ;;
esac
case "$AIO" in
    io_uring|native|threads) ;;
    *) echo "error: unknown --aio mode '$AIO'" >&2; exit 1 ;;
esac

# ---------------------------------------------------------------------------
# --commit: push the local overlay back into the remote NBD disk (offline)
# ---------------------------------------------------------------------------
if [ "$COMMIT" = 1 ]; then
    command -v "$QEMU_IMG" >/dev/null 2>&1 || { echo "error: qemu-img not found" >&2; exit 1; }
    [ -f "$OVERLAY_FILE" ] || { echo "error: $OVERLAY_FILE does not exist" >&2; exit 1; }
    echo "[commit] pushing $OVERLAY_FILE back to ${NBD_URI} ..."
    echo "[commit] WARNING: no VM may be using the overlay while committing!"
    "$QEMU_IMG" commit "$OVERLAY_FILE"
    echo "[commit] done - $OVERLAY_FILE is empty again"
    exit 0
fi

# ---------------------------------------------------------------------------
# ISO: download (resume), checksum, boot order
# ---------------------------------------------------------------------------
if [ "$ISO_BOOT" = 1 ] && [ -n "$ISO_URL" ]; then
    ISOBASE="$(basename "$ISO_FILE")"
    ISO_DIRURL="${ISO_URL%/${ISOBASE}}"
    SHA=""
    for SURL in "${ISO_DIRURL}/SHA256SUMS" "${ISO_DIRURL}/SHA256SUMS.txt" "${ISO_URL}.sha256" "${ISO_DIR}/${ISOBASE}.sha256"; do
        SHA="$(curl -fsSL --max-time 20 "$SURL" 2>/dev/null \
            | awk -v iso="$ISOBASE" 'tolower($0) ~ tolower(iso) {print $1; exit}')"
        [ -n "$SHA" ] && { echo "[iso] checksum da $SURL"; break; }
    done
    # decide se serve (ri)scaricare: file mancante, oppure file presente ma
    # il checksum non torna (= incompleto/corrotto) -> curl -C - riprende
    NEED_DL=0
    if [ ! -f "$ISO_FILE" ]; then
        NEED_DL=1
    elif [ -n "$SHA" ]; then
        ACTUAL="$(sha256sum "$ISO_FILE" | awk '{print $1}')"
        if [ "$ACTUAL" != "$SHA" ]; then
            echo "[iso] file incompleto o corrotto - riprendo il download"
            NEED_DL=1
        fi
    fi
    if [ "$NEED_DL" = 1 ]; then
        echo "[iso] downloading $ISO_URL ..."
        curl -fL -C - --retry 3 --progress-bar -o "$ISO_FILE" "$ISO_URL"
    fi
    echo "[iso] verifying SHA256 ..."
    if [ -n "$SHA" ]; then
        ACTUAL="$(sha256sum "$ISO_FILE" | awk '{print $1}')"
        if [ "$ACTUAL" != "$SHA" ]; then
            echo "error: SHA256 mismatch for $ISO_FILE" >&2
            echo "  expected: $SHA" >&2
            echo "  actual:   $ACTUAL" >&2
            echo "  cancella il file e riprova: rm -f '$ISO_FILE'" >&2
            exit 1
        fi
        echo "[iso] checksum OK"
    else
        echo "[iso] no checksum found - skipping"
    fi
elif [ "$ISO_BOOT" = 1 ] && [ -f "$ISO_FILE" ]; then
    # ISO locale: checksum opzionale da un eventuale SHA256SUMS nella stessa dir
    if [ -f "${ISO_DIR}/SHA256SUMS" ]; then
        ISOBASE="$(basename "$ISO_FILE")"
        SHA="$(awk -v iso="$ISOBASE" 'tolower($0) ~ tolower(iso) {print $1; exit}' "${ISO_DIR}/SHA256SUMS")"
        echo "[iso] verifying SHA256 (locale) ..."
        ACTUAL="$(sha256sum "$ISO_FILE" | awk '{print $1}')"
        if [ -n "$SHA" ] && [ "$ACTUAL" != "$SHA" ]; then
            echo "error: SHA256 mismatch for $ISO_FILE" >&2
            exit 1
        fi
        echo "[iso] checksum OK"
    fi
    echo "[iso] using local ISO: $ISO_FILE"
elif [ "$ISO_BOOT" = 1 ]; then
    echo "error: --iso file not found: $ISO_FILE" >&2
    exit 1
fi

# ---------------------------------------------------------------------------
# Firmware: prefer UEFI (OVMF), fall back to SeaBIOS
# ---------------------------------------------------------------------------
OVMF_CODE="/usr/share/edk2/ovmf/OVMF_CODE.fd"
OVMF_VARS="/usr/share/edk2/ovmf/OVMF_VARS.fd"
VARS_COPY="${ISO_DIR}/OVMF_VARS.fd"
FW=()
if [ -f "$OVMF_CODE" ] && [ -f "$OVMF_VARS" ]; then
    [ -f "$VARS_COPY" ] || cp "$OVMF_VARS" "$VARS_COPY"
    FW=(-drive "if=pflash,format=raw,readonly=on,file=${OVMF_CODE}"
        -drive "if=pflash,format=raw,file=${VARS_COPY}")
    echo "[fw] UEFI (OVMF)"
else
    echo "[fw] UEFI not found, using SeaBIOS"
fi

# ---------------------------------------------------------------------------
# Local overlay on the NBD disk (caching / copy-on-write)
#   --overlay : persistent overlay.qcow2, backing = NBD export
#   --snapshot: throwaway /tmp overlay, deleted when the VM exits
# Writes go to the local overlay; reads of unmodified blocks go to the server.
# ---------------------------------------------------------------------------
DISK_NODE="nbd0"          # guest-visible disk node (nbd0 = raw NBD, disk0 = overlay)
if [ "$SNAPSHOT" = 1 ] && [ "$OVERLAY" = 1 ]; then
    echo "[cache] --snapshot overrides --overlay: using throwaway overlay"
    OVERLAY=0
fi
if [ "$SNAPSHOT" = 1 ]; then
    OVERLAY_FILE="/tmp/nbd-snap-$$.qcow2"
    echo "[cache] snapshot mode: writes are discarded on exit"
    if [ "$DAEMON" = 0 ]; then
        TRAP_CMDS="${TRAP_CMDS:-:}; rm -f '${OVERLAY_FILE}'"
        trap "$TRAP_CMDS" EXIT
    fi
elif [ "$OVERLAY" = 1 ]; then
    echo "[cache] overlay mode: local qcow2 on top of the NBD disk"
else
    echo "[cache] direct NBD (cache=${CACHE_MODE}, aio=${AIO}, reconnect=${RECONNECT_DELAY}s)"
fi
if [ "$SNAPSHOT" = 1 ] || [ "$OVERLAY" = 1 ]; then
    command -v "$QEMU_IMG" >/dev/null 2>&1 || {
        echo "error: $QEMU_IMG not found (needed for overlay/snapshot)." >&2
        echo "       Install it with: sudo rpm-ostree install qemu-img" >&2
        exit 1
    }
    if [ ! -f "$OVERLAY_FILE" ]; then
        echo "[cache] creating $OVERLAY_FILE (backing ${NBD_URI})..."
        "$QEMU_IMG" create -f qcow2 -b "${NBD_URI}" -F raw "${OVERLAY_FILE}" >/dev/null
    fi
    DISK_NODE="disk0"
fi

# ---------------------------------------------------------------------------
# Boot order: bootindex=0 wins (works reliably with UEFI and SeaBIOS)
# ---------------------------------------------------------------------------
CD_DRIVE=()
if [ "$ISO_BOOT" = 1 ] && [ -f "$ISO_FILE" ]; then
    # --iso: il CD-ROM boota per primo (install/live), il disco NBD è secondo
    CD_DRIVE=(-drive "file=${ISO_FILE},media=cdrom,readonly=on,if=none,id=cd0"
              -device "ide-cd,drive=cd0,bootindex=0")
    DISK_BOOTINDEX=1
else
    # boot dal disco NBD; ISO presente come CD secondario (se c'è)
    DISK_BOOTINDEX=0
    if [ -f "$ISO_FILE" ]; then
        CD_DRIVE=(-drive "file=${ISO_FILE},media=cdrom,readonly=on,if=none,id=cd0"
                  -device "ide-cd,drive=cd0,bootindex=1")
    fi
fi

# ---------------------------------------------------------------------------
# Disk block graph:
#   direct:  virtio-blk -> nbd0 (nbd://server:10809, reconnect + cache)
#           (writes go straight to the server; QEMU flushes on guest flush/shutdown)
#   overlay: virtio-blk -> disk0 (qcow2 overlay, metadata cache)
#                         -> nbd0 (NBD backing)
#           (writes are local; push back with --commit or monitor block-commit disk0)
# ---------------------------------------------------------------------------
if [ "$SNAPSHOT" = 1 ] || [ "$OVERLAY" = 1 ]; then
    DISK_BLOCKDEV=(
        -blockdev "driver=nbd,node-name=nbd0,server.type=inet,server.host=${NBD_HOST},server.port=${NBD_PORT},reconnect-delay=${RECONNECT_DELAY}"
        -blockdev "driver=qcow2,node-name=disk0,file.driver=file,file.filename=${OVERLAY_FILE},file.aio=${AIO},backing=nbd0,cache-size=$((QCACHE_MB*1048576)),l2-cache-size=$((L2_CACHE_MB*1048576)),cache.direct=${CD},cache.no-flush=${CNF}"
    )
else
    DISK_BLOCKDEV=(
        -blockdev "driver=nbd,node-name=nbd0,server.type=inet,server.host=${NBD_HOST},server.port=${NBD_PORT},reconnect-delay=${RECONNECT_DELAY},cache.direct=${CD},cache.no-flush=${CNF}"
    )
fi

# ---------------------------------------------------------------------------
# Display: default spice (headless SPICE + Remote Viewer, clipboard condiviso).
#   auto -> GTK window if a graphical session is present, else VNC
#   gtk/sdl -> local window (needs DISPLAY/WAYLAND_DISPLAY on the host)
#   vnc -> headless, view with a VNC client (vncviewer 127.0.0.1:5900)
#   spice -> headless SPICE server su 127.0.0.1:5930 + Remote Viewer lanciato
#            automaticamente (clipboard condiviso; TCP così funziona anche da
#            flatpak sandbox, a differenza della socket unix di spice-app)
#   none -> fully headless
# ---------------------------------------------------------------------------
if [ "$DISPLAY_MODE" = "auto" ]; then
    if [ -n "${DISPLAY:-}" ] || [ -n "${WAYLAND_DISPLAY:-}" ]; then
        DISPLAY_MODE="gtk"
    else
        DISPLAY_MODE="vnc"
    fi
fi
CLIPBOARD=1
DISPLAY_ARG=()
SPICE_ARGS=()
case "$DISPLAY_MODE" in
    gtk)   DISPLAY_ARG=(-display gtk)
           echo "[clipboard] ATTENZIONE: questa build QEMU non ha il clipboard GTK compilato"
           echo "[clipboard] usa --display spice per il copia/incolla con l'host" ;;
    sdl)   DISPLAY_ARG=(-display sdl) ;;
    vnc)   DISPLAY_ARG=(-display vnc=127.0.0.1:0);
           echo "[vnc] view with:  vncviewer 127.0.0.1:5900" ;;
    spice) DISPLAY_ARG=(-display none)
           SPICE_ARGS=(-spice "port=${SPICE_PORT},addr=127.0.0.1,disable-ticketing=on")
           if command -v remote-viewer >/dev/null 2>&1; then
               SPICE_CLIENT=(remote-viewer "spice://127.0.0.1:${SPICE_PORT}")
           elif command -v flatpak >/dev/null 2>&1 && flatpak info org.virt_manager.virt-viewer >/dev/null 2>&1; then
               SPICE_CLIENT=(flatpak run org.virt_manager.virt-viewer "spice://127.0.0.1:${SPICE_PORT}")
           else
               echo "[warn] nessun client SPICE trovato - installa:"
               echo "[warn]   flatpak install -y flathub org.virt_manager.virt-viewer"
               SPICE_CLIENT=()
           fi
           echo "[spice] client: spice://127.0.0.1:${SPICE_PORT} (clipboard condiviso host<->guest)" ;;
    none)  DISPLAY_ARG=(-display none) ;;
    *)     echo "error: unknown --display mode '$DISPLAY_MODE'" >&2; exit 1 ;;
esac

# --- VGA / 3D acceleration ------------------------------------------------
# virtio-gpu (virtio-vga) is the modern paravirtualized display: lowest CPU
# overhead, clean dirty-rect updates. qxl is in maintenance-only mode upstream
# and has known perf cliffs at high resolutions. std is the safe fallback.
case "$VGA_MODE" in
    virtio|std|qxl|vmware|none) ;;
    *) echo "error: unknown --vga mode '$VGA_MODE'" >&2; exit 1 ;;
esac
VGA_ARG=(-vga "$VGA_MODE")
# virgl 3D acceleration only makes sense with the virtio gpu.
if [ "$VGA_MODE" = "virtio" ]; then
    if [ "$GL" = "auto" ]; then
        [ -n "${DISPLAY:-}" ] || [ -n "${WAYLAND_DISPLAY:-}" ] && GL="on" || GL="off"
    fi
    case "$GL" in
        on)  case "$DISPLAY_MODE" in
                 gtk) DISPLAY_ARG=(-display gtk,gl=on) ;;
                 sdl) DISPLAY_ARG=(-display sdl,gl=on) ;;
             esac
             echo "[gl] virgl 3D acceleration enabled (needs guest virtio-gpu driver)" ;;
        off) echo "[gl] 3D acceleration off (2D virtio-gpu)" ;;
        *)   echo "error: unknown --gl mode '$GL'" >&2; exit 1 ;;
    esac
else
    echo "[gl] skipped (only virtio vga supports virgl)"
fi

# Virtio-serial for spice-vdagent (clipboard sharing)
# NOTE: the port MUST be named com.redhat.spice.0.  Il chardev:
#   spice mode -> spicevmc: il canale agent va al client SPICE (Remote Viewer)
#                 che fa copia/incolla con l'host (funziona su questa build)
#   gtk/vnc    -> qemu-vdagent: passa dal clipboard manager interno di QEMU
#                 (richiede build con --enable-gtk-clipboard, assente in Fedora)
if [ "$DISPLAY_MODE" = "spice" ] || [ "$DISPLAY_MODE" = "gtk" ] || [ "$DISPLAY_MODE" = "vnc" ]; then
    VIRTIO_SERIAL_CTRL=(-device virtio-serial-pci)
    VIRTIO_SERIAL_PORT=(-device virtserialport,chardev=charchannel0,id=channel0,name=com.redhat.spice.0)
    if [ "$DISPLAY_MODE" = "spice" ]; then
        CHARDEV=(-chardev spicevmc,id=charchannel0,name=vdagent)
    else
        CHARDEV=(-chardev qemu-vdagent,id=charchannel0,name=vdagent,clipboard=on)
    fi
    echo "[virtio-serial] spice-vdagent clipboard sharing enabled (port com.redhat.spice.0)"
fi

# ---------------------------------------------------------------------------
# Audio: pick the host backend (PipeWire first, then PulseAudio), then the
# guest sound card.  hda (Intel HDA + duplex codec) works in every Linux
# guest; virtio-sound is the modern paravirtualized option (needs snd_virtio,
# in the kernel since 5.18 so fine on modern distros).
# ---------------------------------------------------------------------------
case "$AUDIO_MODE" in
    hda|virtio|none) ;;
    *) echo "error: unknown --audio mode '$AUDIO_MODE'" >&2; exit 1 ;;
esac
if [ "$AUDIO_DRV" = "auto" ]; then
    if [ -e "/run/user/$(id -u)/pipewire-0" ]; then
        AUDIO_DRV="pipewire"
    elif [ -e "/run/user/$(id -u)/pulse/native" ]; then
        AUDIO_DRV="pa"
    else
        AUDIO_DRV="none"
    fi
fi
SOUND_ARGS=()
if [ "$AUDIO_MODE" != "none" ] && [ "$AUDIO_DRV" != "none" ]; then
    SOUND_ARGS=(-audiodev "${AUDIO_DRV},id=audio0")
    case "$AUDIO_MODE" in
        hda)    SOUND_ARGS+=(-device intel-hda -device "hda-duplex,audiodev=audio0") ;;
        virtio) SOUND_ARGS+=(-device "virtio-sound-pci,audiodev=audio0") ;;
    esac
    echo "[audio] ${AUDIO_MODE} card via ${AUDIO_DRV} backend (guest: PipeWire/PulseAudio will see it)"
else
    echo "[audio] disabled (--audio none or no host audio server found)"
fi

# ---------------------------------------------------------------------------
# Networking: pick the guest network model and host->guest port forwards.
#   dual     : TWO NICs (default): ens3 user-mode NAT to the internet
#              (10.0.2.15) + ens4 private TAP net host<->guest (host .1 vs
#              guest .2 on --tap-subnet): the host reaches ANY guest port
#              directly at the TAP IP (--port forwards still attach to the
#              NAT interface if wanted).
#   nat      : single NIC, user-mode SLIRP NAT.  Guest: 10.0.2.15 with internet
#              via the host, but the host CANNOT reach the guest unless a port
#              is forwarded (--port).
#   hostonly : single NIC, same user-mode net with restrict=on: the guest talks
#              only to the host (and anything forwarded) - no internet.  This
#              is the user-space equivalent of a host-only adapter.
#   bridged  : tap device attached to a host bridge (default br0): the guest
#              joins your LAN with its own address.  Needs root or
#              qemu-bridge-helper; the script sets the tap up and tears it
#              down again on exit.
#   none     : no network device.
#   tap      : private host<->guest network on a TAP device (no bridge, no
#              hostfwd): host = TAP_DEV with 192.168.100.1/24 (--tap/--tap-subnet),
#              guest = static .2 on the same subnet -> the host can reach ANY
#              guest TCP/UDP port directly at that IP.  On first use the user
#              is granted rw access to /dev/net/tun and the TAP is created
#              (root/sudo); everything is reverted to the original state on exit.
# Internet is always available (user-mode nets: NAT out; tap mode: ip_forward +
# MASQUERADE so the guest can reach the internet through the host).
# Forwards use SLIRP syntax: hostfwd=proto::hostport-:guestport
# ---------------------------------------------------------------------------
case "$NET_MODE" in
    dual|nat|hostonly|tap|bridged|none) ;;
    *) echo "error: unknown --net mode '$NET_MODE' (dual|nat|hostonly|tap|bridged|none)" >&2; exit 1 ;;
esac
HOSTFWD=()
for rule in "${PF[@]}"; do
    case "$rule" in
        tcp:*|udp:*)
            PROTO="${rule%%:*}"
            HP="${rule#*:}"; HP="${HP%%:*}"
            GP="${rule##*:}"
            HOSTFWD+=("hostfwd=${PROTO}::${HP}-:${GP}")
            echo "[net] forward ${PROTO}: host:${HP} -> guest:${GP}"
            ;;
        *)
            echo "error: bad --port spec '$rule' (use e.g. tcp:2222:22 or udp:53:53)" >&2
            exit 1 ;;
    esac
done
PFX=""
[ "${#HOSTFWD[@]}" -gt 0 ] && PFX=",$(IFS=,; echo "${HOSTFWD[*]}")"

# --- private host<->guest network on a TAP device (no bridge, no hostfwd) ---
# Host: TAP_DEV with ${TAP_PREFIX}.1; guest: static ${TAP_PREFIX}.2.  Every
# TCP/UDP port of the guest is reachable from the host directly at that IP.
# On first use the user is granted rw access to /dev/net/tun and the TAP is
# created (root/sudo); everything is reverted on exit (unless --daemon).
setup_tap_net() {
    TAP_MASK="${TAP_SUBNET#*/}"
    TAP_PREFIX="${TAP_SUBNET%/*}"
    TAP_PREFIX="${TAP_PREFIX%.*}"
    TAP_HOST_IP="${TAP_PREFIX}.1"
    TAP_GUEST_IP="${TAP_PREFIX}.2"
    if [ "$(id -u)" = "0" ]; then
        SUDO=""
    else
        command -v sudo >/dev/null 2>&1 || {
            echo "error: TAP private net needs root privileges (sudo not found)" >&2
            exit 1
        }
        SUDO="sudo"
        $SUDO -v 2>/dev/null || true   # cache credentials for the session
    fi
    # a) first-use: grant the user rw access to /dev/net/tun (save mode)
    TUN_ORIG=""; TUN_CHANGED=0
    if [ ! -r /dev/net/tun ] || [ ! -w /dev/net/tun ]; then
        TUN_ORIG="$(stat -c %a /dev/net/tun 2>/dev/null || echo 600)"
        $SUDO chmod 666 /dev/net/tun
        TUN_CHANGED=1
        echo "[net] /dev/net/tun: granted rw access (was ${TUN_ORIG})"
    else
        echo "[net] /dev/net/tun already accessible"
    fi
    # b) create the TAP if missing (owned by us so QEMU can attach; we
    #    delete it again on exit -> not persistent)
    TAP_CREATED=0
    if ! ip link show "$TAP_DEV" >/dev/null 2>&1; then
        $SUDO ip tuntap add dev "$TAP_DEV" mode tap user "$(id -un)" 2>/dev/null || {
            echo "error: cannot create tap $TAP_DEV (root/sudo needed)" >&2
            exit 1
        }
        TAP_CREATED=1
        echo "[net] created TAP $TAP_DEV (owner $(id -un))"
    else
        echo "[net] TAP $TAP_DEV already exists (reusing)"
    fi
    # c) private address + up
    if ! ip addr show dev "$TAP_DEV" 2>/dev/null | grep -q " ${TAP_HOST_IP}/"; then
        $SUDO ip addr add "${TAP_HOST_IP}/${TAP_MASK}" dev "$TAP_DEV" 2>/dev/null || true
    fi
    $SUDO ip link set "$TAP_DEV" up 2>/dev/null || echo "[warn] could not bring ${TAP_DEV} up"
    # d) internet via the host (ip_forward + MASQUERADE)
    IPF_ORIG=""; MASQ_ADDED=0
    IPF_ORIG="$(sysctl -n net.ipv4.ip_forward 2>/dev/null || echo 0)"
    $SUDO sysctl -w net.ipv4.ip_forward=1 >/dev/null 2>&1 || true
    if ! $SUDO iptables -t nat -C POSTROUTING -s "${TAP_SUBNET}" ! -o "$TAP_DEV" -j MASQUERADE 2>/dev/null; then
        $SUDO iptables -t nat -A POSTROUTING -s "${TAP_SUBNET}" ! -o "$TAP_DEV" -j MASQUERADE 2>/dev/null \
            && MASQ_ADDED=1 \
            || echo "[warn] iptables MASQUERADE failed - guest has no internet via TAP"
    fi
    echo "[net] internet via host: ip_forward + MASQUERADE (${TAP_SUBNET})"
    echo "[net] private net: host ${TAP_HOST_IP}/${TAP_MASK} <-> guest ${TAP_GUEST_IP}/${TAP_MASK} (all ports open, no hostfwd)"
    echo "[net] nel guest: ip addr add ${TAP_GUEST_IP}/${TAP_MASK} dev <nettap> && ip link set <nettap> up"
    # revert everything to the original state on exit (unless --daemon)
    if [ "$DAEMON" = 0 ]; then
        REV=":"
        [ "$MASQ_ADDED" = 1 ] && REV="${REV}; $SUDO iptables -t nat -D POSTROUTING -s ${TAP_SUBNET} ! -o ${TAP_DEV} -j MASQUERADE 2>/dev/null"
        [ -n "$IPF_ORIG" ] && [ "$IPF_ORIG" != "$(sysctl -n net.ipv4.ip_forward 2>/dev/null)" ] && REV="${REV}; $SUDO sysctl -w net.ipv4.ip_forward=${IPF_ORIG} >/dev/null 2>&1"
        [ "$TAP_CREATED" = 1 ] && REV="${REV}; $SUDO ip link set ${TAP_DEV} down 2>/dev/null; $SUDO ip tuntap del dev ${TAP_DEV} 2>/dev/null"
        [ "$TUN_CHANGED" = 1 ] && REV="${REV}; $SUDO chmod ${TUN_ORIG} /dev/net/tun"
        TRAP_CMDS="${TRAP_CMDS:-:}${REV}"
        trap "$TRAP_CMDS" EXIT
    else
        echo "[net] note: --daemon leaves TAP/IP/MASQUERADE up (manual cleanup)"
    fi
}

case "$NET_MODE" in
    dual)
        setup_tap_net
        NIC_ARGS=(-nic "user,model=virtio-net-pci,net=10.0.2.0/24${PFX}"
                  -netdev "tap,id=priv0,ifname=${TAP_DEV},script=no,downscript=no"
                  -device "virtio-net-pci,netdev=priv0")
        echo "[net] dual: ens3 NAT->internet (10.0.2.15) + ens4 TAP private (${TAP_DEV}: ${TAP_HOST_IP} host <-> ${TAP_GUEST_IP} guest)"
        ;;
    nat)
        NIC_ARGS=(-nic "user,model=virtio-net-pci${PFX}")
        echo "[net] user-mode NAT (guest 10.0.2.15, internet via host)"
        ;;
    hostonly)
        NIC_ARGS=(-nic "user,model=virtio-net-pci,restrict=on${PFX}")
        echo "[net] host-only (user net restrict=on: guest<=>host only)"
        ;;
    tap)
        setup_tap_net
        [ -n "$PFX" ] && echo "[warn] --port forwards ignored in tap mode (guest reachable directly at ${TAP_GUEST_IP})"
        NIC_ARGS=(-netdev "tap,id=priv0,ifname=${TAP_DEV},script=no,downscript=no" -device "virtio-net-pci,netdev=priv0")
        echo "[net] tap only: ${TAP_DEV} (${TAP_HOST_IP} host <-> ${TAP_GUEST_IP} guest)"
        ;;
    bridged)
        if [ "$(id -u)" != "0" ]; then
            if command -v qemu-bridge-helper >/dev/null 2>&1; then
                NIC_ARGS=(-netdev "tap,id=mynet0,br=${BRIDGE_IF}" -device "virtio-net-pci,netdev=mynet0")
                echo "[net] bridged via qemu-bridge-helper -> ${BRIDGE_IF}"
            else
                echo "error: --net bridged needs root (or qemu-bridge-helper with" >&2
                echo "       'allow ${BRIDGE_IF}' in /etc/qemu/bridge.conf). Try: sudo ./launch-nbd.sh --net bridged" >&2
                exit 1
            fi
        else
            TAP_DEV="qemu-tap-$$"
            ip tuntap add dev "$TAP_DEV" mode tap user "$(id -un)" 2>/dev/null \
                || { echo "error: cannot create tap $TAP_DEV (need CAP_NET_ADMIN)" >&2; exit 1; }
            ip link set "$TAP_DEV" up
            if ! ip link set "$TAP_DEV" master "$BRIDGE_IF" 2>/dev/null; then
                ip tuntap del dev "$TAP_DEV" 2>/dev/null
                echo "error: bridge '$BRIDGE_IF' not found. Create it first, e.g.:" >&2
                echo "  sudo ip link add ${BRIDGE_IF} type bridge" >&2
                exit 1
            fi
            ip link set "$BRIDGE_IF" up
            NIC_ARGS=(-netdev "tap,id=mynet0,ifname=${TAP_DEV},script=no,downscript=no" -device "virtio-net-pci,netdev=mynet0")
            echo "[net] bridged: tap ${TAP_DEV} on bridge ${BRIDGE_IF}"
            if [ "$DAEMON" = 0 ]; then
                TRAP_CMDS="${TRAP_CMDS:-:}; ip link set ${TAP_DEV} nomaster 2>/dev/null; ip tuntap del dev ${TAP_DEV} 2>/dev/null"
                trap "$TRAP_CMDS" EXIT
            else
                echo "[net] note: --daemon leaves tap ${TAP_DEV} up (cleanup: sudo ip tuntap del dev ${TAP_DEV})"
            fi
        fi
        ;;
    none)
        NIC_ARGS=(-nic none)
        echo "[net] no network device"
        ;;
esac

echo "[display] mode: ${DISPLAY_MODE}"

# ---------------------------------------------------------------------------
# Assemble and run
# ---------------------------------------------------------------------------
echo "[nbd] boot disk:  ${NBD_URI}  (export ${NBD_EXPORT}, raw)"
echo "[nbd] install ISO: ${ISO_FILE}"
if [ "$SNAPSHOT" = 1 ] || [ "$OVERLAY" = 1 ]; then
    echo "[nbd] local layer: ${OVERLAY_FILE} (qcow2, cache=${QCACHE_MB}M, l2=${L2_CACHE_MB}M)"
    echo "[nbd] NOTE: overlay writes are LOCAL until committed (--commit / block-commit)"
fi
MODE_LABEL="disk"; [ "$ISO_BOOT" = 1 ] && MODE_LABEL="iso"
echo "[run] launching ${QEMU} (${MODE_LABEL} mode, ${MEM_MB} MiB, ${CPUS} cpus)..."

ARGS=(
    "${ACCEL_ARGS[@]}"
    -machine q35
    -cpu max
    -smp "${CPUS}"
    -m "${MEM_MB}M"
    "${FW[@]}"
    -object iothread,id=io0
    "${DISK_BLOCKDEV[@]}"
    -device "virtio-blk,drive=${DISK_NODE},iothread=io0,write-cache=${WC},bootindex=${DISK_BOOTINDEX}"
    "${CD_DRIVE[@]}"
    -device qemu-xhci,id=xhci
    -device usb-tablet,bus=xhci.0
    "${NIC_ARGS[@]}"
    "${VIRTIO_SERIAL_CTRL[@]}"
    "${VIRTIO_SERIAL_PORT[@]}"
    "${CHARDEV[@]}"
    "${VGA_ARG[@]}"
    "${SPICE_ARGS[@]}"
    "${DISPLAY_ARG[@]}"
    "${SOUND_ARGS[@]}"
    -rtc base=localtime
)
[ "$MONITOR" = 1 ] && ARGS+=(-monitor stdio)
[ -n "$MON_SOCK" ] && ARGS+=(-monitor "unix:${MON_SOCK},server=on,wait=off")

if [ "$DAEMON" = 1 ]; then
    nohup "$QEMU" "${ARGS[@]}" >/dev/null 2>&1 &
    echo "[run] QEMU PID $! (backgrounded)"
else
    # launch the SPICE client right before QEMU: it will connect as soon as
    # the server starts listening on 127.0.0.1:SPICE_PORT
    if [ "$DISPLAY_MODE" = "spice" ] && [ "${#SPICE_CLIENT[@]}" -gt 0 ]; then
        ( sleep 2; exec "${SPICE_CLIENT[@]}" ) >/dev/null 2>&1 &
        echo "[run] SPICE client PID $!"
    fi
    # run in-process (not exec: the snapshot-cleanup below must still run)
    "$QEMU" "${ARGS[@]}"
    rc=$?
    if [ "$SNAPSHOT" = 1 ]; then
        rm -f "$OVERLAY_FILE"
        echo "[cache] snapshot overlay removed"
    fi
    exit "$rc"
fi
