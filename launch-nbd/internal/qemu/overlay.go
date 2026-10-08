package qemu

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// Lifecycle dell'overlay (plan-go.md §5.8): identità E stato nel filename,
// niente sidecar.
//
//	OVERLAY_DIR/<hash>.qcow2             live      (deltas non ancora espulsi)
//	OVERLAY_DIR/<hash>-committing.qcow2  commit in corso o interrotto (marker)
//	OVERLAY_DIR/<hash>-committed.qcow2   commit completato (candidato a delete)
//
// <hash> = fingerprint del disco NBD (nbd.Fingerprint), Base64URL.

// OverlayState: stato codificato nel nome del file.
type OverlayState int

const (
	OverlayLive OverlayState = iota
	OverlayCommitting
	OverlayCommitted
)

func (s OverlayState) String() string {
	switch s {
	case OverlayLive:
		return "live"
	case OverlayCommitting:
		return "committing"
	case OverlayCommitted:
		return "committed"
	}
	return "unknown"
}

func (s OverlayState) suffix() string {
	switch s {
	case OverlayCommitting:
		return "-committing"
	case OverlayCommitted:
		return "-committed"
	}
	return ""
}

// OverlayPath: percorso dell'overlay per (dir, hash, stato).
func OverlayPath(dir, hash string, s OverlayState) string {
	return filepath.Join(dir, hash+s.suffix()+".qcow2")
}

// FindOverlay: cerca l'overlay del disco <hash> nella dir, in ordine di
// priorità committing > live > committed (il marker di commit va visto per
// primo). Ritorna found=false se non esiste alcun overlay per quell'hash.
func FindOverlay(dir, hash string) (string, OverlayState, bool) {
	for _, s := range []OverlayState{OverlayCommitting, OverlayLive, OverlayCommitted} {
		p := OverlayPath(dir, hash, s)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p, s, true
		}
	}
	return "", OverlayLive, false
}

// BlockedError: operazione bloccata da un marker di commit interrotto.
type BlockedError struct {
	Path string
}

func (e *BlockedError) Error() string {
	return fmt.Sprintf("commit interrotto (marker %s): ri-esegui 'launch-nbd commit' per completarlo prima di avviare la VM", e.Path)
}

// GuardRun: guardia per run/direct/snapshot. Un overlay `-committing` presente
// per l'hash corrente blocca l'avvio (disco in stato ibrido). Ritorna lo stato
// trovato (o found=false se nessun overlay).
func GuardRun(dir, hash string) (string, OverlayState, bool, error) {
	p, st, found := FindOverlay(dir, hash)
	if found && st == OverlayCommitting {
		return p, st, true, &BlockedError{Path: p}
	}
	return p, st, found, nil
}

// GuardCommit: guardia per il comando commit. Ritorna il percorso da
// committare e lo stato; errori per: nessun overlay (stale/assente).
func GuardCommit(dir, hash string) (string, OverlayState, error) {
	p, st, found := FindOverlay(dir, hash)
	if !found {
		return "", st, fmt.Errorf("nessun overlay per il disco attuale (fingerprint %s) in %s: niente da committare (overlay di un'altra installazione = stale)", hash, dir)
	}
	if st == OverlayCommitted {
		return p, st, fmt.Errorf("overlay %s è già committato (vuoto): nulla da committare; puoi eliminarlo", p)
	}
	return p, st, nil
}

// CopyFile: copia semplice (usata per la VARS scrivibile del firmware).
func CopyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}

// EnsureOverlay: prepara l'overlay del disco <hash> per un avvio in modalità
// overlay. Gestisce i marker (§5.8):
//   - <hash>-committing presente  → BlockedError (fail-stop: commit interrotto)
//   - <hash>-committed presente   → torna "live" (nuovi delta sopra l'overlay vuoto)
//   - <hash>.qcow2 (live)         → riuso
//   - nessuno                     → crea con qemu-img (backing = backingURI)
//
// Ritorna il percorso dell'overlay live; errore se la creazione fallisce.
func EnsureOverlay(ctx context.Context, dir, hash, backingURI, qemuImg string, runImg ImgRunner, log func(string)) (string, error) {
	if log == nil {
		log = func(string) {}
	}
	if runImg == nil {
		runImg = RunImg(qemuImg)
	}
	path, st, found, err := GuardRun(dir, hash)
	if err != nil {
		return "", err
	}
	switch {
	case found && st == OverlayCommitted:
		live := OverlayPath(dir, hash, OverlayLive)
		if err := os.Rename(path, live); err != nil {
			return "", fmt.Errorf("overlay: rename %s -> %s: %w", path, live, err)
		}
		log("[cache] overlay committato riportato a live: " + live)
		return live, nil
	case found && st == OverlayLive:
		log("[cache] riuso overlay: " + path)
		return path, nil
	default:
		live := OverlayPath(dir, hash, OverlayLive)
		if err := runImg(ctx, ImgCreateArgs(backingURI, live)...); err != nil {
			return "", err
		}
		log("[cache] creato overlay: " + live)
		return live, nil
	}
}
