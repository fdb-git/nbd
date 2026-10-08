package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func noWarn(m string) {}

func TestPlatformDefaults(t *testing.T) {
	cfg := PlatformDefaults()
	if cfg.NBDPort != 10809 || cfg.NBDStatePort != 10819 {
		t.Errorf("porte default errate: %+v", cfg)
	}
	if cfg.NBDHost != "" {
		t.Errorf("host di default deve essere vuoto (da wizard/config): %q", cfg.NBDHost)
	}
	if cfg.QEMU == "" || cfg.QEMUImg == "" || cfg.MemMB <= 0 || cfg.CPUs <= 0 {
		t.Errorf("default base incompleti: %+v", cfg)
	}
	if cfg.URI() == "" || !strings.Contains(cfg.URI(), cfg.NBDExport) {
		t.Errorf("URI derivata errata: %s", cfg.URI())
	}
	if errs := cfg.Validate(); len(errs) == 0 {
		t.Errorf("atteso errore per nbd_host vuoto: %+v", cfg)
	}
}

func TestURIDerived(t *testing.T) {
	c := Cfg{NBDHost: "srv", NBDPort: 10809, NBDExport: "fdbnode"}
	if got := c.URI(); got != "nbd://srv:10809/fdbnode" {
		t.Errorf("URI=%s", got)
	}
	c2 := Cfg{NBDHost: "srv", NBDURI: "nbd://other:1/x", NBDPort: 2, NBDExport: "y"}
	if got := c2.URI(); got != "nbd://other:1/x" {
		t.Errorf("URI esplicita ignorata: %s", got)
	}
}

func TestTOMLRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "launch-nbd.toml")
	want := Cfg{NBDHost: "10.0.0.5", NBDPort: 10810, NBDExport: "demo",
		NBDStatePort: 10819, MemMB: 4096, CPUs: 2, Accel: "kvm",
		NetMode: "nat", DisplayMode: "vnc", DiskMode: "overlay",
		OverlayDir: "/tmp/ovl", PF: []string{"tcp:2222:22"}}
	if err := Save(p, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p, noWarn)
	if err != nil {
		t.Fatal(err)
	}
	if got.NBDHost != want.NBDHost || got.NetMode != "nat" || got.PF[0] != "tcp:2222:22" {
		t.Errorf("round-trip divergente: %+v != %+v", got, want)
	}
}

func TestMergePrecedence(t *testing.T) {
	// default <- .env legacy <- TOML: TOML vince su .env
	dir := t.TempDir()
	wd, _ := os.Getwd()
	defer os.Chdir(wd)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(".env", []byte("NBD_HOST=fromenv\nMEM_MB=2048\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("launch-nbd.toml", []byte("nbd_host = \"fromtoml\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load("", noWarn)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.NBDHost != "fromtoml" {
		t.Errorf("TOML deve prevalere su .env: %q", cfg.NBDHost)
	}
	if cfg.MemMB != 2048 {
		t.Errorf(".env deve prevalere su default: MemMB=%d", cfg.MemMB)
	}
}

func TestSetKey(t *testing.T) {
	c := Cfg{}
	if err := c.SetKey("net", "nat"); err != nil || c.NetMode != "nat" {
		t.Errorf("alias net: %+v err=%v", c, err)
	}
	if err := c.SetKey("NBD_HOST", "srv"); err != nil || c.NBDHost != "srv" {
		t.Errorf("env-style key: %+v err=%v", c, err)
	}
	if err := c.SetKey("mem", "8192"); err != nil || c.MemMB != 8192 {
		t.Errorf("alias mem: %+v err=%v", c, err)
	}
	if err := c.SetKey("monitor", "yes"); err != nil || !c.Monitor {
		t.Errorf("bool: %+v err=%v", c, err)
	}
	if err := c.SetKey("net_mode", "bogus"); err == nil {
		t.Errorf("enum invalido accettato")
	}
	if err := c.SetKey("nope", "1"); err == nil {
		t.Errorf("chiave sconosciuta accettata")
	}
	if err := c.SetKey("mem_mb", "abc"); err == nil {
		t.Errorf("intero invalido accettato")
	}
}

func TestValidate(t *testing.T) {
	good := Cfg{NBDHost: "s", NBDPort: 10809, NBDExport: "e", NBDStatePort: 10819,
		QEMU: "q", QEMUImg: "qi", MemMB: 1024, CPUs: 2, Accel: "kvm", NetMode: "dual"}
	if errs := good.Validate(); len(errs) != 0 {
		t.Fatalf("config valida respinta: %v", errs)
	}
	bad := good
	bad.NBDPort = 99999
	bad.SpicePort = 0
	bad.NetMode = "bogus"
	bad.Accel = "bogus"
	bad.CPUs = 0
	bad.NBDHost = ""
	if errs := bad.Validate(); len(errs) < 4 {
		t.Errorf("attesi >=4 errori, trovati %d: %v", len(errs), errs)
	}
	ov := good
	ov.DiskMode = "overlay" // overlay_dir richiesta
	if errs := ov.Validate(); len(errs) != 1 {
		t.Errorf("atteso 1 errore (overlay_dir), trovati %v", errs)
	}
}

func TestEnvFileParse(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".env")
	if err := os.WriteFile(p, []byte(`
# commento
export NBD_HOST="host.with.quotes"
MEM_MB=4096
GL=off
QUOTED='ciao'
`), 0o644); err != nil {
		t.Fatal(err)
	}
	m, ok, err := ParseEnvFile(p)
	if err != nil || !ok {
		t.Fatalf("err=%v ok=%v", err, ok)
	}
	if m["NBD_HOST"] != "host.with.quotes" || m["MEM_MB"] != "4096" || m["QUOTED"] != "ciao" {
		t.Errorf("parse errato: %v", m)
	}
	if _, ok, _ := ParseEnvFile(filepath.Join(dir, "missing")); ok {
		t.Errorf("file assente deve dare ok=false")
	}
}

func TestApplyEnvMapWarnsOnUnknown(t *testing.T) {
	c := Cfg{}
	var warned []string
	c.ApplyEnvMap(map[string]string{"NOPE_X": "1", "NBD_HOST": "srv"}, func(m string) {
		warned = append(warned, m)
	})
	if c.NBDHost != "srv" {
		t.Errorf("NBD_HOST non applicato")
	}
	if len(warned) != 1 {
		t.Errorf("atteso 1 warning, trovato %v", warned)
	}
}

func TestLaunchEnvSkipsAssetsDir(t *testing.T) {
	t.Setenv("LAUNCH_NBD_ASSETS_DIR", `/some/dir`)
	t.Setenv("LAUNCH_NBD_NET_MODE", "nat")
	m := launchEnv()
	if _, ok := m["ASSETS_DIR"]; ok {
		t.Error("ASSETS_DIR non deve diventare una chiave di config")
	}
	if m["NET_MODE"] != "nat" {
		t.Errorf("NET_MODE=%q", m["NET_MODE"])
	}
}
