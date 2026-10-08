// Package qemu: costruzione degli argomenti di QEMU.
//
// Traduzione 1:1 della sezione "Assemble and run" di launch-nbd.sh
// (plan-go.md §5.3): stesso ordine, stessi valori. Build è PURA: i fatti che
// dipendono da filesystem/ambiente (asset risolti, display "auto", /dev/kvm,
// ISO, overlay) arrivano in Facts, così gli args sono testabili e confrontabili
// con i golden file in testdata/ (plan-go.md §8/§9 M2).
package qemu

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/fdb-git/nbd/launch-nbd/internal/config"
)

// Mode: modalità di avvio.
type Mode string

const (
	ModeRun     Mode = "run"     // boot dal disco NBD installato (nessun CD di boot)
	ModeInstall Mode = "install" // provisioning: CD-ROM bootindex 0, disco secondo
)

// Facts: fatti risolti fuori dal builder (asset/runtime).
type Facts struct {
	Mode     Mode
	Snapshot bool // overlay throwaway (run): prevale su disk_mode

	// OverlayFile: percorso qcow2 dell'overlay (richiesto con disk_mode=overlay
	// o Snapshot).
	OverlayFile string

	// Firmware UEFI: se OVMFCode è vuoto si usa SeaBIOS (nessun arg pflash).
	// VarsCopy è la copia scrivibile di OVMF_VARS.fd.
	OVMFCode string
	OVMFVars string
	VarsCopy string

	// ISOFile: in ModeInstall è il CD di boot (bootindex 0); in ModeRun, se
	// presente, è un CD secondario (bootindex 1).
	ISOFile string

	// Decisioni runtime già risolte ("auto" → valore). Se vuote, si usano i
	// valori della config e gli "auto" ricadono sui default headless
	// (display=vnc, gl=off, audio=none): la risoluzione reale da ambiente
	// (DISPLAY, socket pipewire/pulse) avviene nel layer di runtime (M5/M6).
	DisplayMode string
	GL          string
	AudioDrv    string

	// KVM: /dev/kvm disponibile (per accel=auto|kvm).
	KVM bool

	// CPU: modello -cpu. Vuoto → "max". Su Windows/WHPX "max"/"host"
	// fermano la vCPU ("WHPX: Unexpected VP exit code 4"): il chiamante usa
	// un modello compatibile (es. Haswell).
	CPU string

	// BridgedTap: tap concreto creato dal layer net per net_mode=bridged (root);
	// se vuoto si usa la forma con qemu-bridge-helper (br=).
	BridgedTap string
}

// Result: args + note di log (il banner dello script; non confrontate dai golden).
type Result struct {
	Args  []string
	Notes []string
}

// Build: compone gli argomenti QEMU. Ordine = ARGS[] dello script.
func Build(cfg config.Cfg, f Facts) (Result, error) {
	var r Result
	add := func(a ...string) { r.Args = append(r.Args, a...) }
	note := func(format string, a ...any) { r.Notes = append(r.Notes, fmt.Sprintf(format, a...)) }

	if err := validateEnums(cfg); err != nil {
		return r, err
	}
	display, gl, audioDrv := resolveRuntime(cfg, f)

	// --- accelerazione -------------------------------------------------------
	accel := cfg.Accel
	switch accel {
	case "auto":
		if f.KVM {
			accel = "kvm"
		} else {
			accel = "tcg"
		}
	case "kvm":
		if !f.KVM {
			note("[warn] /dev/kvm non trovato: fallback TCG (lento)")
			accel = "tcg"
		} else {
			note("[ok] KVM disponibile")
		}
	}
	add("-accel", accel) // due token: qemu rifiuta -accel=kvm

	// --- cache mode -> write-cache / cache.direct / cache.no-flush ------------
	wc, cd, cnf, err := cacheFlags(cfg.CacheMode)
	if err != nil {
		return r, err
	}

	// --- overlay / snapshot ---------------------------------------------------
	overlay := cfg.DiskMode == "overlay" || f.Snapshot
	if cfg.DiskMode == "overlay" && f.Snapshot {
		note("[cache] --snapshot prevale su disk_mode=overlay: overlay throwaway")
	}
	diskNode := "nbd0"
	if overlay {
		diskNode = "disk0"
		if f.OverlayFile == "" {
			return r, fmt.Errorf("qemu: overlay richiesto (disk_mode=overlay o snapshot) ma OverlayFile è vuoto")
		}
	}

	// --- ISO / ordine di boot -------------------------------------------------
	if f.Mode == ModeInstall && f.ISOFile == "" {
		return r, fmt.Errorf("qemu: install richiede un ISO (facts.ISOFile)")
	}

	// --- ISO come CD (boot 0 in install, secondario in run) -------------------
	var cdArgs []string
	diskBoot := 0
	if f.ISOFile != "" {
		if f.Mode == ModeInstall {
			cdArgs = []string{
				"-drive", "file=" + f.ISOFile + ",media=cdrom,readonly=on,if=none,id=cd0",
				"-device", "ide-cd,drive=cd0,bootindex=0",
			}
			diskBoot = 1
		} else {
			cdArgs = []string{
				"-drive", "file=" + f.ISOFile + ",media=cdrom,readonly=on,if=none,id=cd0",
				"-device", "ide-cd,drive=cd0,bootindex=1",
			}
		}
	}

	// --- macchina / cpu / memoria --------------------------------------------
	cpu := f.CPU
	if cpu == "" {
		cpu = "max"
	}
	add("-machine", "q35")
	add("-cpu", cpu)
	add("-smp", strconv.Itoa(cfg.CPUs))
	add("-m", fmt.Sprintf("%dM", cfg.MemMB))

	// --- firmware UEFI (pflash) o SeaBIOS ------------------------------------
	if f.OVMFCode != "" {
		if f.VarsCopy == "" {
			return r, fmt.Errorf("qemu: OVMF presente ma VarsCopy (copia scrivibile VARS) è vuota")
		}
		add("-drive", "if=pflash,format=raw,readonly=on,file="+f.OVMFCode)
		add("-drive", "if=pflash,format=raw,file="+f.VarsCopy)
		note("[fw] UEFI (OVMF)")
	} else {
		note("[fw] UEFI non trovato: SeaBIOS")
	}

	add("-object", "iothread,id=io0")

	// --- grafo disco ----------------------------------------------------------
	nbd := fmt.Sprintf("driver=nbd,node-name=nbd0,server.type=inet,server.host=%s,server.port=%d,reconnect-delay=%d",
		cfg.NBDHost, cfg.NBDPort, cfg.ReconnectDelay)
	if overlay {
		add("-blockdev", nbd)
		add("-blockdev", fmt.Sprintf(
			"driver=qcow2,node-name=disk0,file.driver=file,file.filename=%s,file.aio=%s,backing=nbd0,cache-size=%d,l2-cache-size=%d,cache.direct=%s,cache.no-flush=%s",
			f.OverlayFile, cfg.AIO, cfg.QCacheMB*1048576, cfg.L2CacheMB*1048576, cd, cnf))
	} else {
		add("-blockdev", nbd+",cache.direct="+cd+",cache.no-flush="+cnf)
	}
	add("-device", fmt.Sprintf("virtio-blk,drive=%s,iothread=io0,write-cache=%s,bootindex=%d", diskNode, wc, diskBoot))
	add(cdArgs...)

	// --- USB tablet (input) ---------------------------------------------------
	add("-device", "qemu-xhci,id=xhci")
	add("-device", "usb-tablet,bus=xhci.0")

	// --- rete -----------------------------------------------------------------
	nic, err := netArgs(cfg, f)
	if err != nil {
		return r, err
	}
	add(nic...)

	// --- virtio-serial / vdagent (clipboard) ---------------------------------
	switch display {
	case "spice":
		add("-device", "virtio-serial-pci")
		add("-device", "virtserialport,chardev=charchannel0,id=channel0,name=com.redhat.spice.0")
		add("-chardev", "spicevmc,id=charchannel0,name=vdagent")
		note("[virtio-serial] vdagent via spicevmc (clipboard host<->guest)")
	case "gtk", "vnc":
		add("-device", "virtio-serial-pci")
		add("-device", "virtserialport,chardev=charchannel0,id=channel0,name=com.redhat.spice.0")
		add("-chardev", "qemu-vdagent,id=charchannel0,name=vdagent,clipboard=on")
		note("[virtio-serial] vdagent via qemu-vdagent (clipboard=on)")
	}

	// --- VGA + GL -------------------------------------------------------------
	add("-vga", cfg.VGAMode)
	if cfg.VGAMode == "virtio" {
		switch gl {
		case "on":
			note("[gl] virgl 3D attivo (serve driver virtio-gpu nel guest)")
		case "off":
			note("[gl] 3D disattivato (virtio-gpu 2D)")
		}
	} else {
		note("[gl] saltato (solo vga virtio supporta virgl)")
	}

	// --- display + spice ------------------------------------------------------
	switch display {
	case "gtk":
		if gl == "on" {
			add("-display", "gtk,gl=on")
		} else {
			add("-display", "gtk")
		}
	case "sdl":
		if gl == "on" {
			add("-display", "sdl,gl=on")
		} else {
			add("-display", "sdl")
		}
	case "vnc":
		add("-display", "vnc=127.0.0.1:0")
		note("[vnc] client: vncviewer 127.0.0.1:5900")
	case "spice":
		add("-display", "none")
		add("-spice", fmt.Sprintf("port=%d,addr=127.0.0.1,disable-ticketing=on", cfg.SpicePort))
		note("[spice] client: spice://127.0.0.1:%d", cfg.SpicePort)
	case "none":
		add("-display", "none")
	}

	// --- audio ----------------------------------------------------------------
	audio, err := audioArgs(cfg, audioDrv)
	if err != nil {
		return r, err
	}
	add(audio...)

	add("-rtc", "base=localtime")

	// --- monitor (HMP) --------------------------------------------------------
	if cfg.Monitor {
		add("-monitor", "stdio")
	}
	if cfg.MonSock != "" {
		add("-monitor", monitorSpec(cfg.MonSock))
	}
	return r, nil
}

// monitorSpec: MON_SOCK è un path (→ socket unix) oppure una spec chardev
// completa (es. "tcp:127.0.0.1:4444"). In entrambi i casi il server è già in
// ascolto (server=on) e non blocca l'avvio (wait=off).
func monitorSpec(s string) string {
	if strings.HasPrefix(s, "unix:") || strings.HasPrefix(s, "tcp:") || strings.HasPrefix(s, "socket,") {
		return s + ",server=on,wait=off"
	}
	return "unix:" + s + ",server=on,wait=off"
}

// resolveRuntime: applica i valori risolti runtime o i default headless.
func resolveRuntime(cfg config.Cfg, f Facts) (display, gl, audioDrv string) {
	display = f.DisplayMode
	if display == "" {
		display = cfg.DisplayMode
	}
	if display == "auto" {
		display = "vnc"
	}
	gl = f.GL
	if gl == "" {
		gl = cfg.GL
	}
	if gl == "auto" {
		gl = "off"
	}
	audioDrv = f.AudioDrv
	if audioDrv == "" {
		audioDrv = cfg.AudioDrv
	}
	if audioDrv == "auto" {
		audioDrv = "none"
	}
	return display, gl, audioDrv
}

// cacheFlags: cache_mode -> write-cache / cache.direct / cache.no-flush.
func cacheFlags(mode string) (wc, cd, cnf string, err error) {
	switch mode {
	case "writeback":
		return "on", "off", "off", nil
	case "none":
		return "on", "on", "off", nil
	case "writethrough":
		return "off", "off", "off", nil
	case "directsync":
		return "off", "on", "off", nil
	case "unsafe":
		return "on", "off", "on", nil
	}
	return "", "", "", fmt.Errorf("cache_mode: '%s' non ammesso (writeback|none|writethrough|directsync|unsafe)", mode)
}

// netArgs: NIC_ARGS per NET_MODE, con i port forward (PFX) sulle reti user-mode.
func netArgs(cfg config.Cfg, f Facts) ([]string, error) {
	pfx, err := portForwardPFX(cfg.PF)
	if err != nil {
		return nil, err
	}
	switch cfg.NetMode {
	case "dual":
		return []string{
			"-nic", "user,model=virtio-net-pci,net=10.0.2.0/24" + pfx,
			"-netdev", fmt.Sprintf("tap,id=priv0,ifname=%s,script=no,downscript=no", cfg.TapDev),
			"-device", "virtio-net-pci,netdev=priv0",
		}, nil
	case "nat":
		return []string{"-nic", "user,model=virtio-net-pci" + pfx}, nil
	case "hostonly":
		return []string{"-nic", "user,model=virtio-net-pci,restrict=on" + pfx}, nil
	case "tap":
		// i port forward sono ignorati in tap mode (guest raggiungibile
		// direttamente all'IP del TAP)
		return []string{
			"-netdev", fmt.Sprintf("tap,id=priv0,ifname=%s,script=no,downscript=no", cfg.TapDev),
			"-device", "virtio-net-pci,netdev=priv0",
		}, nil
	case "bridged":
		if f.BridgedTap != "" {
			// tap concreto creato dal layer net (root)
			return []string{
				"-netdev", "tap,id=mynet0,ifname=" + f.BridgedTap + ",script=no,downscript=no",
				"-device", "virtio-net-pci,netdev=mynet0",
			}, nil
		}
		// forma con qemu-bridge-helper (non-root o helper configurato)
		return []string{
			"-netdev", "tap,id=mynet0,br=" + cfg.BridgeIF,
			"-device", "virtio-net-pci,netdev=mynet0",
		}, nil
	case "none":
		return []string{"-nic", "none"}, nil
	}
	return nil, fmt.Errorf("net_mode: '%s' non ammesso (dual|nat|hostonly|tap|bridged|none)", cfg.NetMode)
}

// portForwardPFX: "tcp:2222:22" -> ",hostfwd=tcp::2222-:22[,hostfwd=...]".
func portForwardPFX(pf []string) (string, error) {
	var fwds []string
	for _, rule := range pf {
		parts := strings.Split(rule, ":")
		if len(parts) != 3 || (parts[0] != "tcp" && parts[0] != "udp") {
			return "", fmt.Errorf("port forward '%s' non valido (usa tcp:2222:22 o udp:53:53)", rule)
		}
		hp, err1 := strconv.Atoi(parts[1])
		gp, err2 := strconv.Atoi(parts[2])
		if err1 != nil || err2 != nil || hp < 1 || hp > 65535 || gp < 1 || gp > 65535 {
			return "", fmt.Errorf("port forward '%s' non valido (porte 1..65535)", rule)
		}
		fwds = append(fwds, fmt.Sprintf("hostfwd=%s::%d-:%d", parts[0], hp, gp))
	}
	if len(fwds) == 0 {
		return "", nil
	}
	return "," + strings.Join(fwds, ","), nil
}

// audioArgs: SOUND_ARGS (scheda guest + backend host).
func audioArgs(cfg config.Cfg, audioDrv string) ([]string, error) {
	if cfg.AudioMode == "none" || audioDrv == "none" {
		return nil, nil
	}
	if _, ok := map[string]bool{"pipewire": true, "pa": true, "sdl": true, "wasapi": true, "dsound": true}[audioDrv]; !ok {
		return nil, fmt.Errorf("audio_drv: '%s' non ammesso (auto|pipewire|pa|sdl|none)", audioDrv)
	}
	args := []string{"-audiodev", audioDrv + ",id=audio0"}
	switch cfg.AudioMode {
	case "hda":
		args = append(args, "-device", "intel-hda", "-device", "hda-duplex,audiodev=audio0")
	case "virtio":
		args = append(args, "-device", "virtio-sound-pci,audiodev=audio0")
	default:
		return nil, fmt.Errorf("audio_mode: '%s' non ammesso (hda|virtio|none)", cfg.AudioMode)
	}
	return args, nil
}

// validateEnums: controllo locale dei valori che il builder interpreta (messaggi
// come nello script; config.Validate copre il resto).
func validateEnums(cfg config.Cfg) error {
	for _, c := range []struct {
		key string
		val string
		ok  []string
	}{
		{"accel", cfg.Accel, []string{"auto", "kvm", "whpx", "haxm", "tcg"}},
		{"net_mode", cfg.NetMode, []string{"dual", "nat", "hostonly", "tap", "bridged", "none"}},
		{"display_mode", cfg.DisplayMode, []string{"auto", "gtk", "sdl", "vnc", "spice", "none"}},
		{"vga_mode", cfg.VGAMode, []string{"virtio", "std", "qxl", "vmware", "none"}},
		{"gl", cfg.GL, []string{"auto", "on", "off"}},
		{"aio", cfg.AIO, []string{"io_uring", "native", "threads"}},
		{"audio_mode", cfg.AudioMode, []string{"hda", "virtio", "none"}},
		{"disk_mode", cfg.DiskMode, []string{"direct", "overlay"}},
	} {
		found := false
		for _, v := range c.ok {
			if c.val == v {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%s: '%s' non ammesso (usa: %s)", c.key, c.val, strings.Join(c.ok, "|"))
		}
	}
	return nil
}
