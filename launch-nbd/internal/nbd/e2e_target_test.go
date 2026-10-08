//go:build e2e

// Test end-to-end contro un server nbdkit reale (NON mock).
//
// Si eseguono solo con LAUNCH_NBD_E2E=1 e girano sul target (o su un host che
// raggiunge il server). Parametri (env):
//
//	NBD_HOST          default 127.0.0.1
//	NBD_DATA_PORT     default 10809  (export dati, es. fdbhome)
//	NBD_STATE_PORT    default 10819  (export di stato)
//	NBD_DATA_EXPORT   default fdbhome
//	NBD_TEST_EXPORT   export usato per le scritture di stato (default e2e-state);
//	                  il suo file <NBD_TEST_EXPORT>.status va creato prima con
//	                  "nbd-export.sh state export <name>".
//
// Nessuna scrittura sull'export dati: solo letture (fingerprint) e scritture
// sul file di stato dell'export di test. Vedi docs/target-verification.md.
package nbd_test

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/fdb-git/nbd/launch-nbd/internal/nbd"
)

func e2eEnv(t *testing.T) (host string, dataPort, statePort int, dataExport, testExport string) {
	t.Helper()
	if os.Getenv("LAUNCH_NBD_E2E") != "1" {
		t.Skip("e2e disabilitato (imposta LAUNCH_NBD_E2E=1)")
	}
	host = envOr("NBD_HOST", "127.0.0.1")
	dataPort = envInt(t, "NBD_DATA_PORT", 10809)
	statePort = envInt(t, "NBD_STATE_PORT", 10819)
	dataExport = envOr("NBD_DATA_EXPORT", "fdbhome")
	testExport = envOr("NBD_TEST_EXPORT", "e2e-state")
	return
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(t *testing.T, k string, def int) int {
	t.Helper()
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		t.Fatalf("%s: %v", k, err)
	}
	return n
}

// TestE2EStateRoundTrip: legge/scrive il marker di stato sull'export reale
// <NBD_TEST_EXPORT>.status, verificando il formato (docs/status-format.md) e la
// durabilità (WriteState invia NBD_CMD_FLUSH).
func TestE2EStateRoundTrip(t *testing.T) {
	host, _, statePort, _, testExport := e2eEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// 1) stato iniziale (file creato da "nbd-export.sh state export")
	rec0, err := nbd.ReadState(ctx, host, statePort, testExport)
	if err != nil {
		t.Fatalf("ReadState(%s): %v (il file .status esiste? usa: nbd-export.sh state export %s)", testExport, err, testExport)
	}
	t.Logf("stato iniziale: %s (owner %q, hash %q, ctime %d)", rec0.State, rec0.OwnerHost, rec0.Hash, rec0.CTime)

	// 2) scrivi committing, rileggi
	want := nbd.Record{
		State:     nbd.StateCommitting,
		OwnerHost: "e2e-go-client",
		OwnerUUID: "00000000-0000-4000-8000-0000000000e2",
		Hash:      "e2ehash123",
		CTime:     uint64(time.Now().Unix()),
	}
	if err := nbd.WriteState(ctx, host, statePort, testExport, want); err != nil {
		t.Fatalf("WriteState(committing): %v", err)
	}
	got, err := nbd.ReadState(ctx, host, statePort, testExport)
	if err != nil {
		t.Fatalf("ReadState dopo write: %v", err)
	}
	if got != want {
		t.Fatalf("round-trip over-the-wire: %+v != %+v", got, want)
	}
	t.Logf("round-trip committing OK")

	// 3) verifica indipendente via una seconda connessione (già fatto: ReadState
	//    apre una nuova connessione ogni volta)

	// 4) reset a clean (durabilità: FLUSH)
	if err := nbd.WriteState(ctx, host, statePort, testExport, nbd.Record{
		State: nbd.StateClean, OwnerHost: "e2e-go-client",
	}); err != nil {
		t.Fatalf("WriteState(clean): %v", err)
	}
	final, err := nbd.ReadState(ctx, host, statePort, testExport)
	if err != nil {
		t.Fatalf("ReadState finale: %v", err)
	}
	if final.State != nbd.StateClean {
		t.Fatalf("stato finale=%v, atteso clean", final.State)
	}
	t.Logf("reset clean OK")
}

// TestE2EFingerprint: il fingerprint del disco reale è stabile e ha la forma
// attesa (SHA256 Base64URL senza padding = 43 char).
func TestE2EFingerprint(t *testing.T) {
	host, dataPort, _, dataExport, _ := e2eEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fp1, err := nbd.Fingerprint(ctx, host, dataPort, dataExport)
	if err != nil {
		t.Fatalf("Fingerprint(%s): %v", dataExport, err)
	}
	if len(fp1) != 43 {
		t.Errorf("fingerprint=%q (len %d, atteso 43)", fp1, len(fp1))
	}
	fp2, err := nbd.Fingerprint(ctx, host, dataPort, dataExport)
	if err != nil {
		t.Fatalf("Fingerprint (2a): %v", err)
	}
	if fp1 != fp2 {
		t.Fatalf("fingerprint non stabile: %s != %s", fp1, fp2)
	}
	t.Logf("fingerprint %s = %s (stabile)", dataExport, fp1)
}

// TestE2EStateExportName: l'export di stato è <export>.status.
func TestE2EStateExportName(t *testing.T) {
	_, _, _, dataExport, _ := e2eEnv(t)
	if got := nbd.StatusExportName(dataExport); got != dataExport+".status" {
		t.Fatalf("StatusExportName=%q", got)
	}
}
