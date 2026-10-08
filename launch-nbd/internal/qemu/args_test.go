package qemu

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fdb-git/nbd/launch-nbd/internal/config"
)

var update = flag.Bool("update", false, "rigenera i golden file in testdata/")

// baseCfg: fixture esplicita e platform-independent (nessun PlatformDefaults,
// nessun nome binario): i golden devono essere identici su ogni host.
func baseCfg() config.Cfg {
	return config.Cfg{
		NBDHost: "server", NBDPort: 10809, NBDExport: "fdbnode", NBDStatePort: 10819,
		MemMB: 6144, CPUs: 4, Accel: "kvm",
		CacheMode: "writeback", AIO: "io_uring", ReconnectDelay: 10,
		QCacheMB: 64, L2CacheMB: 16,
		DiskMode: "direct", OverlayDir: "overlay",
		DisplayMode: "none", SpicePort: 5930, VGAMode: "virtio", GL: "off",
		AudioMode: "none", AudioDrv: "none",
		NetMode: "nat", TapDev: "tap0", TapSubnet: "192.168.100.0/24", BridgeIF: "br0",
	}
}

func TestGoldenArgs(t *testing.T) {
	cases := []struct {
		name  string
		cfg   func() config.Cfg
		facts Facts
	}{
		{"run-basic", baseCfg, Facts{Mode: ModeRun, KVM: true}},
		{"run-spice-overlay-fwd", func() config.Cfg {
			c := baseCfg()
			c.DisplayMode = "spice"
			c.NetMode = "dual"
			c.AudioMode = "hda"
			c.DiskMode = "overlay"
			c.PF = []string{"tcp:2222:22", "udp:53:53"}
			return c
		}, Facts{Mode: ModeRun, KVM: true, OverlayFile: "/ovl/abcdef.qcow2", AudioDrv: "pipewire"}},
		{"run-snapshot", baseCfg, Facts{Mode: ModeRun, KVM: true, Snapshot: true,
			OverlayFile: "/tmp/nbd-snap-1234.qcow2"}},
		{"install-cd", func() config.Cfg {
			c := baseCfg()
			c.NetMode = "hostonly"
			c.DisplayMode = "vnc"
			return c
		}, Facts{Mode: ModeInstall, KVM: false, ISOFile: "/iso/ubuntu.iso",
			OVMFCode: "/fw/OVMF_CODE.fd", OVMFVars: "/fw/OVMF_VARS.fd", VarsCopy: "/run/OVMF_VARS.fd"}},
		{"run-gtk-gl-hostonly", func() config.Cfg {
			c := baseCfg()
			c.DisplayMode = "gtk"
			c.GL = "on"
			c.NetMode = "hostonly"
			c.AudioMode = "virtio"
			return c
		}, Facts{Mode: ModeRun, KVM: true, AudioDrv: "pa"}},
		{"run-monitor", func() config.Cfg {
			c := baseCfg()
			c.Monitor = true
			c.MonSock = "/tmp/mon.sock"
			return c
		}, Facts{Mode: ModeRun, KVM: true}},
		{"run-windows-whpx", func() config.Cfg {
			c := baseCfg()
			c.Accel = "whpx"
			c.NetMode = "hostonly"
			c.DisplayMode = "sdl"
			c.GL = "off"
			c.AudioMode = "hda"
			c.AudioDrv = "sdl"
			return c
		}, Facts{Mode: ModeRun, CPU: "qemu64"}},
		{"run-tap", func() config.Cfg {
			c := baseCfg()
			c.NetMode = "tap"
			return c
		}, Facts{Mode: ModeRun, KVM: true}},
		{"run-bridged", func() config.Cfg {
			c := baseCfg()
			c.NetMode = "bridged"
			return c
		}, Facts{Mode: ModeRun, KVM: true}},
		{"run-bridged-root", func() config.Cfg {
			c := baseCfg()
			c.NetMode = "bridged"
			return c
		}, Facts{Mode: ModeRun, KVM: true, BridgedTap: "qemu-tap-777"}},
		{"run-writethrough", func() config.Cfg {
			c := baseCfg()
			c.CacheMode = "writethrough"
			return c
		}, Facts{Mode: ModeRun, KVM: true}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Build(tc.cfg(), tc.facts)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			if len(res.Notes) == 0 {
				t.Error("attese note di log")
			}
			got := strings.Join(res.Args, "\n") + "\n"
			path := filepath.Join("testdata", tc.name+".golden")
			if *update {
				if err := os.MkdirAll("testdata", 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (rigenera con: go test ./internal/qemu -update)", err)
			}
			if got != string(want) {
				t.Errorf("args diversi dal golden %s:\n--- got ---\n%s--- want ---\n%s", path, got, want)
			}
		})
	}
}

func TestBuildErrors(t *testing.T) {
	cases := []struct {
		name string
		cfg  func() config.Cfg
		f    Facts
		want string
	}{
		{"install senza ISO", baseCfg, Facts{Mode: ModeInstall}, "install richiede un ISO"},
		{"overlay senza file", func() config.Cfg {
			c := baseCfg()
			c.DiskMode = "overlay"
			return c
		}, Facts{Mode: ModeRun}, "OverlayFile è vuoto"},
		{"OVMF senza VarsCopy", baseCfg, Facts{Mode: ModeRun, OVMFCode: "/fw/CODE.fd"}, "VarsCopy"},
		{"net_mode invalido", func() config.Cfg {
			c := baseCfg()
			c.NetMode = "bogus"
			return c
		}, Facts{Mode: ModeRun}, "net_mode"},
		{"cache_mode invalido", func() config.Cfg {
			c := baseCfg()
			c.CacheMode = "bogus"
			return c
		}, Facts{Mode: ModeRun}, "cache_mode"},
		{"accel invalido", func() config.Cfg {
			c := baseCfg()
			c.Accel = "bogus"
			return c
		}, Facts{Mode: ModeRun}, "accel"},
		{"port forward invalido", func() config.Cfg {
			c := baseCfg()
			c.PF = []string{"bogus"}
			return c
		}, Facts{Mode: ModeRun}, "port forward"},
		{"audio_drv invalido", func() config.Cfg {
			c := baseCfg()
			c.AudioMode = "hda"
			return c
		}, Facts{Mode: ModeRun, AudioDrv: "bogus"}, "audio_drv"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Build(tc.cfg(), tc.f)
			if err == nil {
				t.Fatalf("atteso errore contenente %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("errore=%q, atteso contenente %q", err, tc.want)
			}
		})
	}
}

// TestAccelFallback: accel=kvm senza /dev/kvm -> -accel tcg (come lo script).
func TestAccelFallback(t *testing.T) {
	// kvm senza /dev/kvm -> accel=tcg (nessun fallback)
	res, err := Build(baseCfg(), Facts{Mode: ModeRun, KVM: false})
	if err != nil {
		t.Fatal(err)
	}
	if !hasArgContaining(res.Args, "accel=tcg") {
		t.Errorf("atteso -machine q35,accel=tcg, args=%v", res.Args)
	}

	// kvm disponibile -> kvm:tcg (fallback TCG)
	res2, err := Build(baseCfg(), Facts{Mode: ModeRun, KVM: true})
	if err != nil {
		t.Fatal(err)
	}
	if !hasArgContaining(res2.Args, "accel=kvm:tcg") {
		t.Errorf("atteso accel=kvm:tcg, args=%v", res2.Args)
	}

	// whpx -> whpx:tcg (fallback TCG, come il launcher di riferimento)
	c := baseCfg()
	c.Accel = "whpx"
	res3, err := Build(c, Facts{Mode: ModeRun})
	if err != nil {
		t.Fatal(err)
	}
	if !hasArgContaining(res3.Args, "accel=whpx:tcg") {
		t.Errorf("atteso accel=whpx:tcg, args=%v", res3.Args)
	}
}

// TestInstallBootOrder: in install il CD è bootindex 0 e il disco 1.
func TestInstallBootOrder(t *testing.T) {
	c := baseCfg()
	res, err := Build(c, Facts{Mode: ModeInstall, KVM: true, ISOFile: "/iso/x.iso"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasArg(res.Args, "ide-cd,drive=cd0,bootindex=0") {
		t.Errorf("CD boot 0 mancante: %v", res.Args)
	}
	if !hasArgContaining(res.Args, "virtio-blk,drive=nbd0", "bootindex=1") {
		t.Errorf("disco boot 1 mancante: %v", res.Args)
	}
}

func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func hasArgContaining(args []string, parts ...string) bool {
	for _, a := range args {
		all := true
		for _, p := range parts {
			if !strings.Contains(a, p) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

func hasPair(args []string, k, v string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == k && args[i+1] == v {
			return true
		}
	}
	return false
}

func TestMonitorSpec(t *testing.T) {
	cases := map[string]string{
		"/tmp/mon.sock":      "unix:/tmp/mon.sock,server=on,wait=off",
		"unix:/tmp/m.sock":   "unix:/tmp/m.sock,server=on,wait=off",
		"tcp:127.0.0.1:4444": "tcp:127.0.0.1:4444,server=on,wait=off",
	}
	for in, want := range cases {
		if got := monitorSpec(in); got != want {
			t.Errorf("monitorSpec(%q)=%q, atteso %q", in, got, want)
		}
	}
}
