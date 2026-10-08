//go:build linux

package config

// PlatformDefaults: preset unificato Linux (plan-go.md §5.1/§7).
// Accel "kvm" è il default di piattaforma; il preflight del wizard (accel §5.9)
// e il runtime di run (accel=auto) lo possono sovrascrivere dinamicamente.
func PlatformDefaults() Cfg {
	return Cfg{
		// disco NBD (host: da wizard/.env/TOML; export: default del piano)
		NBDHost:      "",
		NBDPort:      10809,
		NBDExport:    "fdbnode",
		NBDStatePort: 10819,

		// binari e VM
		QEMU:    "qemu-system-x86_64",
		QEMUImg: "qemu-img",
		MemMB:   6144,
		CPUs:    4,
		Accel:   "kvm",

		// caching / performance
		CacheMode:      "writeback",
		AIO:            "io_uring",
		ReconnectDelay: 10,
		QCacheMB:       64,
		L2CacheMB:      16,

		// disco locale
		DiskMode:   "direct",
		OverlayDir: "overlay",

		// display / vga / audio
		DisplayMode: "spice",
		SpicePort:   5930,
		VGAMode:     "virtio",
		GL:          "auto",
		AudioMode:   "hda",
		AudioDrv:    "auto",

		// rete
		NetMode:   "dual",
		TapDev:    "tap0",
		TapSubnet: "192.168.100.0/24",
		BridgeIF:  "br0",
	}
}
