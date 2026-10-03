//go:build linux

package net

import (
	"context"
	"os"
	"os/exec"
	"os/user"

	"golang.org/x/sys/unix"
)

// SupportsTap: su Linux la rete TAP è supportata.
const SupportsTap = true

// IsRoot: euid 0.
func IsRoot() bool { return os.Geteuid() == 0 }

// RealSystem: implementazione Linux delle primitive di sistema.
type RealSystem struct{}

func NewSystem() System { return RealSystem{} }

func (RealSystem) Run(ctx context.Context, name string, args ...string) error {
	return exec.CommandContext(ctx, name, args...).Run()
}

func (RealSystem) Output(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return string(out), err
}

func (RealSystem) HasCommand(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// TunAccess: /dev/net/tun leggibile+scrivibile? altrimenti i permessi attuali.
func (RealSystem) TunAccess() (bool, string) {
	if err := unix.Access("/dev/net/tun", unix.R_OK|unix.W_OK); err == nil {
		return true, ""
	}
	fi, err := os.Stat("/dev/net/tun")
	if err != nil {
		return false, "600" // assente: il chmod fallirà ma il warning è chiaro
	}
	return false, modeString(fi.Mode().Perm())
}

func (RealSystem) PID() int { return os.Getpid() }

func (RealSystem) IsRoot() bool { return os.Geteuid() == 0 }

func (RealSystem) Username() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return "root"
}

func modeString(p os.FileMode) string {
	perm := uint32(p) & 0o777
	return itoa3(perm)
}

func itoa3(n uint32) string {
	return string([]byte{byte('0' + (n/64)%8), byte('0' + (n/8)%8), byte('0' + n%8)})
}
