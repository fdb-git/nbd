package qemu

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestMain: quando LAUNCH_NBD_TEST_CHILD_EXIT è impostata, il binario di test
// si comporta da "QEMU finto" uscendo con quel codice (per testare Run).
func TestMain(m *testing.M) {
	if code := os.Getenv("LAUNCH_NBD_TEST_CHILD_EXIT"); code != "" {
		n, err := strconv.Atoi(code)
		if err != nil {
			os.Exit(99)
		}
		os.Exit(n)
	}
	os.Exit(m.Run())
}

func TestRunForegroundExitCode(t *testing.T) {
	t.Setenv("LAUNCH_NBD_TEST_CHILD_EXIT", "3")
	cleanupCalled := false
	rc, err := Run(context.Background(), RunOptions{
		QEMU:    os.Args[0], // il binario di test stesso
		Args:    []string{"-test.run=TestNothing"},
		Cleanup: func() { cleanupCalled = true },
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rc != 3 {
		t.Errorf("exit code=%d, atteso 3", rc)
	}
	if !cleanupCalled {
		t.Error("Cleanup deve essere sempre eseguito")
	}
}

func TestRunCleanupOnStartError(t *testing.T) {
	cleanupCalled := false
	_, err := Run(context.Background(), RunOptions{
		QEMU:    "binario-che-non-esiste-xyz",
		Cleanup: func() { cleanupCalled = true },
	})
	if err == nil {
		t.Fatal("atteso errore di avvio")
	}
	if !cleanupCalled {
		t.Error("Cleanup deve essere eseguito anche su errore di avvio")
	}
}

func TestRunCleanupOnExitZero(t *testing.T) {
	t.Setenv("LAUNCH_NBD_TEST_CHILD_EXIT", "0")
	var out strings.Builder
	rc, err := Run(context.Background(), RunOptions{
		QEMU: os.Args[0],
		Args: []string{"-test.run=TestNothing"},
		Out:  &out, Err: &out,
	})
	if err != nil || rc != 0 {
		t.Fatalf("rc=%d err=%v", rc, err)
	}
}
