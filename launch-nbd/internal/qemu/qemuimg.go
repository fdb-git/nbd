package qemu

import (
	"context"
	"fmt"
	"os/exec"
)

// ImgRunner: esecutore di qemu-img (iniettabile nei test: niente qemu-img reale).
type ImgRunner func(ctx context.Context, args ...string) error

// ImgCreateArgs: creazione dell'overlay qcow2 con backing NBD raw.
//
//	qemu-img create -f qcow2 -b <backingURI> -F raw <path>
func ImgCreateArgs(backingURI, path string) []string {
	return []string{"create", "-f", "qcow2", "-b", backingURI, "-F", "raw", path}
}

// ImgCommitArgs: commit dell'overlay nel backing.
func ImgCommitArgs(path string) []string {
	return []string{"commit", path}
}

// RunImg: esegue qemu-img con gli argomenti dati (Runner di default).
func RunImg(qemuImg string) ImgRunner {
	return func(ctx context.Context, args ...string) error {
		cmd := exec.CommandContext(ctx, qemuImg, args...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("qemu-img %v: %w\n%s", args, err, out)
		}
		return nil
	}
}
