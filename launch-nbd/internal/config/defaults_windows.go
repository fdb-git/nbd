//go:build windows

package config

// PlatformDefaults: preset unificato Windows (plan-go.md §5.1/§7).
// Accel "whpx" è il default; il preflight del wizard (e il runtime di run)
// ripiega su tcg se WHPX non è disponibile. Rete "hostonly" (TAP/Wintun).
func PlatformDefaults() Cfg {
	return Cfg{
		NBDHost:      "",
		NBDPort:      10809,
		NBDExport:    "fdbnode",
		NBDStatePort: 10819,

		// binari e VM (estensioni .exe; risolti dagli asset embed in M1)
		QEMU:    "qemu-system-x86_64.exe",
		QEMUImg: "qemu-img.exe",
		MemMB:   6144,
		CPUs:    4,
		Accel:   "whpx",

		// caching (io_uring non esiste su Windows)
		CacheMode:      "writeback",
		AIO:            "native",
		ReconnectDelay: 10,
		QCacheMB:       64,
		L2CacheMB:      16,

		DiskMode:   "direct",
		OverlayDir: "overlay",

		DisplayMode: "spice",
		SpicePort:   5930,
		VGAMode:     "virtio",
		GL:          "auto",
		AudioMode:   "hda",
		AudioDrv:    "wasapi",

		NetMode:   "hostonly",
		TapDev:    "tap0",
		TapSubnet: "192.168.100.0/24",
		BridgeIF:  "br0",
	}
}
