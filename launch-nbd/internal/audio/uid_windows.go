//go:build windows

package audio

// CurrentUID: su Windows non esiste /run/user/<uid>; il rilevamento "auto"
// ricade su none (nessun server pipewire/pulse).
func CurrentUID() int { return -1 }
