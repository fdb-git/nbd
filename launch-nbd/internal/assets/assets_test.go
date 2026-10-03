package assets

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
)

func TestStagedIn(t *testing.T) {
	if stagedIn(fstest.MapFS{"linux/.keep": {}}, "linux") {
		t.Error("solo .keep non deve risultare staged")
	}
	withAsset := fstest.MapFS{
		"linux/.keep":                   {},
		"linux/qemu/qemu-system-x86_64": {Data: []byte("bin")},
	}
	if !stagedIn(withAsset, "linux") {
		t.Error("asset reale deve risultare staged")
	}
	if stagedIn(fstest.MapFS{}, "linux") {
		t.Error("albero vuoto non deve risultare staged")
	}
}

func TestExtract(t *testing.T) {
	fsys := fstest.MapFS{
		"linux/.keep":                   {},
		"linux/qemu/qemu-system-x86_64": {Data: []byte("bin")},
		"linux/ovmf/OVMF_CODE.fd":       {Data: []byte("fw")},
	}
	dest := t.TempDir()
	files, err := extract(fsys, "linux", dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("attesi 2 file estratti, trovati %v", files)
	}
	q := files["qemu-system-x86_64"]
	if q == "" {
		t.Fatal("qemu non estratto")
	}
	if b, err := os.ReadFile(q); err != nil || string(b) != "bin" {
		t.Errorf("contenuto errato: %q err=%v", b, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "ovmf", "OVMF_CODE.fd")); err != nil {
		t.Errorf("firmware non estratto: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, ".keep")); !os.IsNotExist(err) {
		t.Errorf("il marker .keep non deve essere estratto")
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(q)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm()&0o111 == 0 {
			t.Errorf("binario estratto non eseguibile: %v", fi.Mode())
		}
	}
}

func TestPrepareDevDir(t *testing.T) {
	dir := t.TempDir()
	name := platformBinary("qemu")
	if err := os.MkdirAll(filepath.Join(dir, "qemu"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "qemu", name)
	if err := os.WriteFile(p, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := Prepare(Options{DevDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Cleanup()
	if got := s.Resolve("qemu"); got != p {
		t.Errorf("Resolve=%q, atteso %q", got, p)
	}
	if s.Resolve("non-logico") != "" {
		t.Error("nome logico sconosciuto deve dare stringa vuota")
	}
	if _, err := os.Stat(p); err != nil {
		t.Errorf("Cleanup deve essere no-op in dev-dir: %v", err)
	}
}

func TestPrepareNoAssetsFallback(t *testing.T) {
	if Staged() {
		t.Skip("host con asset popolati")
	}
	s, err := Prepare(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Cleanup()
	if s.Dir != "" {
		t.Errorf("atteso Set vuoto senza asset, Dir=%q", s.Dir)
	}
	if s.Resolve("qemu") != "" {
		t.Error("Resolve deve dare stringa vuota senza asset")
	}
}

func TestPlatformNames(t *testing.T) {
	name := platformBinary("qemu")
	if name == "" {
		t.Fatal("platformBinary(qemu) vuoto")
	}
	switch runtime.GOOS {
	case "windows":
		if !strings.HasSuffix(name, ".exe") {
			t.Errorf("windows: %q senza .exe", name)
		}
	case "linux":
		if strings.HasSuffix(name, ".exe") {
			t.Errorf("linux: %q con .exe", name)
		}
	}
	if len(Expected()) == 0 {
		t.Error("Expected() vuoto")
	}
}
