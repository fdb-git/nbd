// Package accel: preflight dell'accelerazione hardware per piattaforma
// (plan-go.md §5.9). Detect() è definita nei file build-tagged:
//
//	detect_linux.go  — /proc/cpuinfo (vmx/svm), /dev/kvm, gruppo kvm
//	detect_windows.go — DLL WHPX in System32 (+ suggerimenti elevati)
package accel

// Result: esito del rilevamento.
type Result struct {
	Available bool     // accelerazione utilizzabile su questo host
	Mode      string   // kvm | whpx | haxm | tcg (tcg = nessuna)
	Enabled   bool     // già attiva/abilitata (false se servono passi)
	Hints     []string // comandi di abilitazione proposti (per conferma utente)
}
