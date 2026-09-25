//go:build windows

package accel

import (
	"os"
	"path/filepath"
)

// Detect: rilevamento accelerazione Windows (WHPX).
// La presenza delle DLL (WinHvPlatform/WinHvEmulation in System32) indica che
// l'API WHPX è installata; non garantisce che l'hypervisor sia attivo (feature
// HypervisorPlatform). Fallback legacy: HAXM (IntelHaxm.sys, deprecato).
func Detect() Result {
	r := Result{Mode: "tcg"}

	sys := os.Getenv("SystemRoot")
	if sys == "" {
		sys = `C:\Windows`
	}
	winHv := false
	for _, dll := range []string{"WinHvPlatform.dll", "WinHvEmulation.dll"} {
		if _, err := os.Stat(filepath.Join(sys, "System32", dll)); err == nil {
			winHv = true
		}
	}
	if winHv {
		r.Available = true
		r.Mode = "whpx"
		r.Enabled = false // DLL presenti = API installata, hypervisor da verificare
		r.Hints = append(r.Hints,
			"PowerShell (admin): Enable-WindowsOptionalFeature -Online -FeatureName HypervisorPlatform -All",
			"verifica firmware: systeminfo | findstr /i \"Virtualization Enabled\"")
		return r
	}
	// HAXM legacy (solo Intel, deprecato)
	if _, err := os.Stat(filepath.Join(sys, "System32", "drivers", "IntelHaxm.sys")); err == nil {
		r.Available = true
		r.Mode = "haxm"
		r.Enabled = true
	}
	return r
}
