package qemu

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

// RunOptions: lancio in foreground di QEMU (plan-go.md §5.4).
type RunOptions struct {
	QEMU    string
	Args    []string
	In      io.Reader
	Out     io.Writer
	Err     io.Writer
	Cleanup func() // snapshot/revert: SEMPRE eseguito (anche su errore/segnale)
	// Signals: segnali inoltrati a QEMU (default: Interrupt + SIGTERM).
	Signals []os.Signal
}

// Run: avvia QEMU in foreground, inoltra i segnali, attende l'uscita e
// restituisce l'exit code. Cleanup è sempre eseguito (defer).
func Run(ctx context.Context, opt RunOptions) (int, error) {
	if opt.Cleanup != nil {
		defer opt.Cleanup()
	}
	cmd := exec.Command(opt.QEMU, opt.Args...)
	cmd.Stdin = opt.In
	cmd.Stdout = opt.Out
	cmd.Stderr = opt.Err
	if err := cmd.Start(); err != nil {
		return -1, fmt.Errorf("avvio %s: %w", opt.QEMU, err)
	}

	sigs := opt.Signals
	if len(sigs) == 0 {
		sigs = []os.Signal{os.Interrupt, syscall.SIGTERM}
	}
	sigCh := make(chan os.Signal, 8)
	signal.Notify(sigCh, sigs...)
	defer signal.Stop(sigCh)

	done := make(chan struct{})
	go func() {
		for {
			select {
			case s := <-sigCh:
				if cmd.Process != nil {
					_ = cmd.Process.Signal(s) // inoltra a QEMU (e al client SPICE via QEMU)
				}
			case <-done:
				return
			case <-ctx.Done():
				if cmd.Process != nil {
					_ = cmd.Process.Kill()
				}
				return
			}
		}
	}()

	err := cmd.Wait()
	close(done)

	rc := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			rc = ee.ExitCode()
			return rc, nil // exit code di QEMU: non è un errore del launcher
		}
		return -1, err
	}
	return rc, nil
}
