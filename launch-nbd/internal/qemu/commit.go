package qemu

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"time"

	"github.com/fdb-git/nbd/launch-nbd/internal/nbd"
)

// StateAccess: accesso all'export di stato cross-host (§5.10), iniettabile.
type StateAccess interface {
	Read(ctx context.Context) (nbd.Record, error)
	Write(ctx context.Context, r nbd.Record) error
}

// NBDStateAccess: implementazione reale via mini client NBD.
type NBDStateAccess struct {
	Host   string
	Port   int
	Export string
}

func (a NBDStateAccess) Read(ctx context.Context) (nbd.Record, error) {
	return nbd.ReadState(ctx, a.Host, a.Port, a.Export)
}

func (a NBDStateAccess) Write(ctx context.Context, r nbd.Record) error {
	return nbd.WriteState(ctx, a.Host, a.Port, a.Export, r)
}

// CommitOptions: parametri del comando commit (plan-go.md §5.8/§5.10).
type CommitOptions struct {
	Dir     string // OVERLAY_DIR
	Hash    string // fingerprint del disco NBD
	QEMUImg string
	RunImg  ImgRunner   // iniettabile (default: RunImg(QEMUImg))
	State   StateAccess // nil = export di stato non disponibile (solo marker locale)
	Log     func(string)

	OwnerHost string // default: hostname
	OwnerUUID string // default: UUID di sessione casuale

	DeleteAfter bool // a fine commit elimina invece di rinominare in -committed
}

// Commit: espelle l'overlay del disco <hash> nel backing NBD.
//
// Sequenza (§5.8): rename live→committing PRIMA di qemu-img (il marker resta
// se il processo è ucciso), qemu-img commit, poi →committed. In parallelo
// scrive lo stato cross-host (§5.10): committing+FLUSH → clean+FLUSH.
func Commit(ctx context.Context, opt CommitOptions) error {
	logf := opt.Log
	if logf == nil {
		logf = func(string) {}
	}
	if opt.RunImg == nil {
		opt.RunImg = RunImg(opt.QEMUImg)
	}
	if opt.OwnerHost == "" {
		opt.OwnerHost, _ = os.Hostname()
	}
	if opt.OwnerUUID == "" {
		opt.OwnerUUID = SessionUUID()
	}

	path, st, err := GuardCommit(opt.Dir, opt.Hash)
	if err != nil {
		return err
	}

	// marker di commit: rename atomico live→committing prima di qemu-img
	if st == OverlayLive {
		committing := OverlayPath(opt.Dir, opt.Hash, OverlayCommitting)
		if err := os.Rename(path, committing); err != nil {
			return fmt.Errorf("commit: rename %s -> %s: %w", path, committing, err)
		}
		path = committing
		st = OverlayCommitting
		logf("[commit] marker: " + committing)
	} else {
		logf("[commit] ripresa di un commit interrotto: " + path)
	}

	// stato cross-host: committing + FLUSH
	if opt.State != nil {
		rec := nbd.Record{
			State:     nbd.StateCommitting,
			OwnerHost: opt.OwnerHost,
			OwnerUUID: opt.OwnerUUID,
			Hash:      opt.Hash,
			CTime:     nowUnix(),
		}
		if err := opt.State.Write(ctx, rec); err != nil {
			return fmt.Errorf("commit: scrittura stato 'committing': %w", err)
		}
		logf("[commit] stato cross-host: committing")
	}

	logf("[commit] qemu-img commit " + path)
	if err := opt.RunImg(ctx, ImgCommitArgs(path)...); err != nil {
		// interruzione: il marker -committing resta (ripresa possibile)
		return fmt.Errorf("commit fallito (overlay lasciato in %s, ri-eseguibile): %w", path, err)
	}

	// successo: →committed (o delete)
	if opt.DeleteAfter {
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("commit: rimozione %s: %w", path, err)
		}
		logf("[commit] overlay eliminato")
	} else {
		done := OverlayPath(opt.Dir, opt.Hash, OverlayCommitted)
		if err := os.Rename(path, done); err != nil {
			return fmt.Errorf("commit: rename %s -> %s: %w", path, done, err)
		}
		logf("[commit] completato: " + done)
	}

	// stato cross-host: clean + FLUSH
	if opt.State != nil {
		rec := nbd.Record{
			State:     nbd.StateClean,
			OwnerHost: opt.OwnerHost,
			OwnerUUID: opt.OwnerUUID,
			Hash:      opt.Hash,
			CTime:     nowUnix(),
		}
		if err := opt.State.Write(ctx, rec); err != nil {
			return fmt.Errorf("commit: scrittura stato 'clean' (commit già completato): %w", err)
		}
		logf("[commit] stato cross-host: clean")
	}
	return nil
}

// SessionUUID: UUID v4 casuale (crypto/rand) per identificare la sessione.
func SessionUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000000-0000-4000-8000-000000000000"
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(b[0:4]), hex.EncodeToString(b[4:6]),
		hex.EncodeToString(b[6:8]), hex.EncodeToString(b[8:10]),
		hex.EncodeToString(b[10:16]))
}

func nowUnix() uint64 {
	return uint64(time.Now().Unix())
}
