//go:build linux

package accel

import (
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// Detect: rilevamento accelerazione Linux (KVM).
//  1. vendor CPU da /proc/cpuinfo: vmx -> Intel VT-x, svm -> AMD SVM
//  2. /dev/kvm assente -> hint modprobe; presente -> check accesso rw
//  3. non scrivibile (fuori gruppo kvm) -> hint usermod (richiede ri-logon)
func Detect() Result {
	r := Result{Mode: "tcg"}

	cpu, err := os.ReadFile("/proc/cpuinfo")
	if err == nil {
		flags := string(cpu)
		switch {
		case strings.Contains(flags, " vmx"):
			r.Mode = "kvm"
			r.Hints = append(r.Hints, "sudo modprobe kvm_intel")
		case strings.Contains(flags, " svm"):
			r.Mode = "kvm"
			r.Hints = append(r.Hints, "sudo modprobe kvm_amd")
		}
	}

	if _, err := os.Stat("/dev/kvm"); err != nil {
		return r // nessun modulo kvm caricato (o host non virtualizzato)
	}
	r.Available = true
	if err := unix.Access("/dev/kvm", unix.R_OK|unix.W_OK); err != nil {
		r.Enabled = false
		r.Hints = append(r.Hints,
			"sudo usermod -aG kvm $USER   # poi ri-logon",
			"in alternativa esegui launch-nbd con sudo")
	} else {
		r.Enabled = true
	}
	return r
}
