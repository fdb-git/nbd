//go:build windows

package net

import (
	"context"
	"os"
	"os/exec"
	"os/user"
)

// SupportsTap: su Windows la rete TAP arriva con la Fase B M6
// (TAP-Windows6/Wintun): per ora i modi tap/dual/bridged danno un errore chiaro.
const SupportsTap = false

// IsRoot: su Windows la nozione di root non esiste (servono privilegi admin,
// gestiti dal layer M6).
func IsRoot() bool { return false }

// RealSystem: implementazione Windows (solo le primitive comuni; il setup TAP
// non è supportato prima di M6).
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

// TunAccess: non applicabile su Windows.
func (RealSystem) TunAccess() (bool, string) { return true, "" }

func (RealSystem) PID() int { return os.Getpid() }

func (RealSystem) IsRoot() bool { return false }

func (RealSystem) Username() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return os.Getenv("USERNAME")
}
