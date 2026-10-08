//go:build e2e

// Test end-to-end del lifecycle commit contro un server nbdkit reale.
// Vedi internal/nbd/e2e_target_test.go per le variabili d'ambiente e
// docs/target-verification.md per la procedura.
//
// Usa il fingerprint REALE del disco dati e scrive lo stato REALE dell'export
// di test; qemu-img NON viene invocato (RunImg è finto) → nessuna modifica al
// disco dati.
package qemu_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/fdb-git/nbd/launch-nbd/internal/nbd"
	"github.com/fdb-git/nbd/launch-nbd/internal/qemu"
)

func e2eEnv(t *testing.T) (host string, dataPort, statePort int, dataExport, testExport string) {
	t.Helper()
	if os.Getenv("LAUNCH_NBD_E2E") != "1" {
		t.Skip("e2e disabilitato (imposta LAUNCH_NBD_E2E=1)")
	}
	get := func(k, def string) string {
		if v := os.Getenv(k); v != "" {
			return v
		}
		return def
	}
	host = get("NBD_HOST", "127.0.0.1")
	dataPort, _ = strconv.Atoi(get("NBD_DATA_PORT", "10809"))
	statePort, _ = strconv.Atoi(get("NBD_STATE_PORT", "10819"))
	dataExport = get("NBD_DATA_EXPORT", "fdbhome")
	testExport = get("NBD_TEST_EXPORT", "e2e-state")
	return
}

// TestE2ECommitLifecycle: commit completo (fingerprint reale → stato committing
// → qemu-img finto → stato clean) e caso di fallimento (marker persistente).
func TestE2ECommitLifecycle(t *testing.T) {
	host, dataPort, statePort, dataExport, testExport := e2eEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	fp, err := nbd.Fingerprint(ctx, host, dataPort, dataExport)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	t.Logf("fingerprint %s = %s", dataExport, fp)

	state := qemu.NBDStateAccess{Host: host, Port: statePort, Export: testExport}
	imgOK := func(context.Context, ...string) error { return nil }
	imgFail := func(context.Context, ...string) error { return os.ErrInvalid }

	// --- caso 1: commit riuscito ---
	dir := t.TempDir()
	live := qemu.OverlayPath(dir, fp, qemu.OverlayLive)
	if err := os.WriteFile(live, []byte("finto overlay"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := qemu.Commit(ctx, qemu.CommitOptions{
		Dir: dir, Hash: fp, RunImg: imgOK, State: state,
		OwnerHost: "e2e-commit", OwnerUUID: "uuid-e2e",
	}); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if _, err := os.Stat(qemu.OverlayPath(dir, fp, qemu.OverlayCommitted)); err != nil {
		t.Errorf("overlay non rinominato in -committed: %v", err)
	}
	rec, err := nbd.ReadState(ctx, host, statePort, testExport)
	if err != nil {
		t.Fatalf("ReadState post-commit: %v", err)
	}
	if rec.State != nbd.StateClean {
		t.Errorf("stato finale=%v, atteso clean", rec.State)
	}
	if rec.Hash != fp {
		t.Errorf("hash nello stato=%q, atteso %q", rec.Hash, fp)
	}
	t.Logf("commit riuscito: stato cross-host clean, overlay -committed")

	// --- caso 2: commit interrotto (qemu-img fallisce) ---
	dir2 := t.TempDir()
	if err := os.WriteFile(qemu.OverlayPath(dir2, fp, qemu.OverlayLive), []byte("o"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := qemu.Commit(ctx, qemu.CommitOptions{
		Dir: dir2, Hash: fp, RunImg: imgFail, State: state,
		OwnerHost: "e2e-commit", OwnerUUID: "uuid-e2e",
	}); err == nil {
		t.Fatal("atteso errore dal commit interrotto")
	}
	if _, err := os.Stat(qemu.OverlayPath(dir2, fp, qemu.OverlayCommitting)); err != nil {
		t.Errorf("il marker -committing deve restare: %v", err)
	}
	rec2, err := nbd.ReadState(ctx, host, statePort, testExport)
	if err != nil {
		t.Fatalf("ReadState dopo interruzione: %v", err)
	}
	if rec2.State != nbd.StateCommitting {
		t.Errorf("stato dopo interruzione=%v, atteso committing", rec2.State)
	}
	t.Logf("commit interrotto: marker -committing e stato committing persistono")

	// --- guardia: run bloccato dal marker ---
	if _, _, _, err := qemu.GuardRun(dir2, fp); err == nil {
		t.Error("GuardRun deve bloccare con un marker -committing presente")
	}

	// --- ripresa: il commit riparte e completa ---
	if err := qemu.Commit(ctx, qemu.CommitOptions{
		Dir: dir2, Hash: fp, RunImg: imgOK, State: state,
		OwnerHost: "e2e-commit", OwnerUUID: "uuid-e2e",
	}); err != nil {
		t.Fatalf("ripresa del commit: %v", err)
	}
	rec3, err := nbd.ReadState(ctx, host, statePort, testExport)
	if err != nil {
		t.Fatalf("ReadState dopo ripresa: %v", err)
	}
	if rec3.State != nbd.StateClean {
		t.Errorf("stato dopo ripresa=%v, atteso clean", rec3.State)
	}
	if _, err := os.Stat(filepath.Join(dir2, fp+"-committed.qcow2")); err != nil {
		t.Errorf("overlay non committato dopo ripresa: %v", err)
	}
	t.Logf("ripresa del commit: completata")
}
