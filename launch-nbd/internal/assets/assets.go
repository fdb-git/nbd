// Package assets: binari e firmware embeddati nel binario (plan-go.md §4).
//
// Albero embeddato (NON versionato: solo i marker .keep sono in git; il
// contenuto si popola con "make assets" sull'host giusto — docs/assets.md):
//
//	internal/assets/<GOOS>/qemu/     qemu-system-x86_64, qemu-img, bridge-helper
//	internal/assets/<GOOS>/ovmf/     OVMF_CODE.fd, OVMF_VARS.fd (o SeaBIOS)
//	internal/assets/<GOOS>/tools/    remote-viewer | virt-viewer
//	internal/assets/<GOOS>/tap-ovpn/ TAP-Windows6 (solo Windows)
//	internal/assets/<GOOS>/wintun/   wintun.dll/.sys (solo Windows)
//
// Uso a runtime:
//
//	s, err := assets.Prepare(assets.Options{DevDir: os.Getenv("LAUNCH_NBD_ASSETS_DIR")})
//	defer s.Cleanup()
//	qemu := s.Resolve("qemu"); if qemu == "" { qemu = cfg.QEMU }
//
// Senza asset reali (solo .keep) Prepare non estrae nulla e Resolve restituisce
// "" → il chiamante ripiega sui binari di sistema (host di sviluppo).
//
// Nota //go:embed: i pattern non possono risalire a ../ e i file che iniziano
// con "." sono esclusi dal default; per questo (a) l'albero vive DENTRO questo
// package e (b) si usa "all:" (include i dotfile) così il solo .keep basta a
// far compilare l'embed.
package assets

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Options: configurazione di Prepare.
type Options struct {
	// DevDir (env LAUNCH_NBD_ASSETS_DIR): se non vuota, niente estrazione:
	// i file si risolvono direttamente da questa dir (layout <GOOS>/).
	DevDir string
}

// Set: asset pronti all'uso (estratti in temp oppure in dev-dir).
type Set struct {
	Dir   string            // dir radice (temp o dev-dir); vuota se nessun asset
	dev   bool              // true = DevDir (nessuna estrazione, nessun cleanup)
	files map[string]string // basename -> path (solo con estrazione)
}

// Prepare: estrae l'albero embeddato in una dir temp, oppure usa DevDir.
// Se l'albero embed non contiene asset reali (solo marker), restituisce un Set
// vuoto senza errore: il fallback è sui binari di sistema.
func Prepare(opt Options) (*Set, error) {
	if opt.DevDir != "" {
		return &Set{Dir: opt.DevDir, dev: true}, nil
	}
	s := &Set{files: map[string]string{}}
	if !Staged() {
		return s, nil
	}
	dir, err := os.MkdirTemp("", "launch-nbd-assets-")
	if err != nil {
		return s, err
	}
	files, err := extract(platformFS(), platformDir(), dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		return s, err
	}
	s.Dir = dir
	s.files = files
	return s, nil
}

// Resolve: path del binario logico ("qemu", "qemu-img", "viewer",
// "bridge-helper"); "" se non presente tra gli asset.
func (s *Set) Resolve(logical string) string {
	if s == nil || s.Dir == "" {
		return ""
	}
	name := platformBinary(logical)
	if name == "" {
		return ""
	}
	if s.dev {
		for _, rel := range []string{"qemu/" + name, "tools/" + name, name} {
			p := filepath.Join(s.Dir, filepath.FromSlash(rel))
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
				return p
			}
		}
		return ""
	}
	return s.files[name]
}

// Cleanup: rimuove la dir di estrazione (no-op in dev-dir). Su Windows un file
// ancora in uso da un processo figlio blocca DeleteFile: si riprova.
func (s *Set) Cleanup() {
	if s == nil || s.Dir == "" || s.dev {
		return
	}
	for i := 0; i < 10; i++ {
		if err := os.RemoveAll(s.Dir); err == nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// Staged: true se l'albero embed contiene almeno un asset reale (non marker).
func Staged() bool { return stagedIn(platformFS(), platformDir()) }

// stagedIn: come Staged, su un fs.FS arbitrario (testabile).
func stagedIn(fsys fs.FS, root string) bool {
	staged := false
	_ = fs.WalkDir(fsys, root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		if info, e := d.Info(); e == nil && info.Size() > 0 {
			staged = true
			return fs.SkipAll
		}
		return nil
	})
	return staged
}

// extract: copia fsys[root] sotto dest (0755, dotfile saltati) e ritorna
// basename -> path. Parametrizzata su fs.FS per i test (fstest.MapFS).
func extract(fsys fs.FS, root, dest string) (map[string]string, error) {
	out := map[string]string{}
	err := fs.WalkDir(fsys, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == root {
			return err
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil // marker
		}
		rel := strings.TrimPrefix(p, root+"/")
		target := filepath.Join(dest, filepath.FromSlash(rel))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0o755); err != nil {
			return err
		}
		out[d.Name()] = target
		return nil
	})
	return out, err
}

// Expected: nomi dei binari attesi per la piattaforma corrente (check M1).
func Expected() []string { return platformExpected() }
