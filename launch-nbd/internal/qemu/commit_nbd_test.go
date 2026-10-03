package qemu

import (
	"context"
	"os"
	"testing"

	"github.com/fdb-git/nbd/launch-nbd/internal/nbd"
	"github.com/fdb-git/nbd/launch-nbd/internal/nbd/nbdtest"
)

// TestCommitWithRealNBDState: integrazione commit <-> export di stato NBD reale
// (mock server) — verifica la sequenza committing -> clean sul filo e i FLUSH.
func TestCommitWithRealNBDState(t *testing.T) {
	status := make([]byte, nbd.StateBlockSize)
	srv, err := nbdtest.New(map[string][]byte{"disk.status": status})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	host, port := srv.Addr()

	dir := t.TempDir()
	if err := os.WriteFile(OverlayPath(dir, "HASH1", OverlayLive), []byte("overlay"), 0o644); err != nil {
		t.Fatal(err)
	}
	err = Commit(context.Background(), CommitOptions{
		Dir:       dir,
		Hash:      "HASH1",
		RunImg:    func(context.Context, ...string) error { return nil },
		State:     NBDStateAccess{Host: host, Port: port, Export: "disk"},
		OwnerHost: "host-int", OwnerUUID: "uuid-int",
	})
	if err != nil {
		t.Fatal(err)
	}
	// stato finale sul server: clean
	rec, err := nbd.ReadState(context.Background(), host, port, "disk")
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != nbd.StateClean {
		t.Errorf("stato finale=%v, atteso clean", rec.State)
	}
	if rec.OwnerHost != "host-int" || rec.Hash != "HASH1" {
		t.Errorf("record=%+v", rec)
	}
	// 2 scritture + 1 lettura, ognuna con FLUSH (le scritture)
	if got := srv.FlushCount("disk.status"); got != 2 {
		t.Errorf("flush=%d, attesi 2 (committing + clean)", got)
	}
	if _, err := os.Stat(OverlayPath(dir, "HASH1", OverlayCommitted)); err != nil {
		t.Errorf("overlay non committato: %v", err)
	}
}

// TestGuardRunAgainstRealNBDState: un committing scritto da un "altro host"
// deve essere letto e bloccare l'avvio (fail-stop §5.10).
func TestGuardRunAgainstRealNBDState(t *testing.T) {
	status := make([]byte, nbd.StateBlockSize)
	srv, err := nbdtest.New(map[string][]byte{"disk.status": status})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	host, port := srv.Addr()
	ctx := context.Background()

	// "host remoto" scrive committing
	if err := nbd.WriteState(ctx, host, port, "disk", nbd.Record{
		State: nbd.StateCommitting, OwnerHost: "altro-host", OwnerUUID: "u", Hash: "H",
	}); err != nil {
		t.Fatal(err)
	}
	rec, err := nbd.ReadState(ctx, host, port, "disk")
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != nbd.StateCommitting {
		t.Fatalf("atteso committing, letto %v", rec.State)
	}
	if rec.OwnerHost != "altro-host" {
		t.Errorf("owner=%q", rec.OwnerHost)
	}
}
