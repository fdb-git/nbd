// Package audio: rilevamento del backend audio host (plan-go.md §5.6/§5.7).
// Logica pura con dipendenze iniettate: testabile ovunque.
package audio

import "fmt"

// Env: fatti d'ambiente (iniettabili).
type Env struct {
	UID    int               // uid per /run/user/<uid>
	Exists func(string) bool // esistenza di un path
}

// SocketPath: path del socket audio per l'utente.
func SocketPath(uid int, name string) string {
	return fmt.Sprintf("/run/user/%d/%s", uid, name)
}

// Detect: risolve "auto" e valida il backend audio.
// auto → pipewire se presente il socket, poi pulse, altrimenti none.
// Su Windows (o host senza server audio) ricade su none.
func Detect(drv string, env Env) (string, error) {
	switch drv {
	case "auto":
		if env.Exists != nil {
			if env.Exists(SocketPath(env.UID, "pipewire-0")) {
				return "pipewire", nil
			}
			if env.Exists(SocketPath(env.UID, "pulse/native")) {
				return "pa", nil
			}
		}
		return "none", nil
	case "pipewire", "pa", "sdl", "none":
		return drv, nil
	}
	return "", fmt.Errorf("audio_drv: '%s' non ammesso (auto|pipewire|pa|sdl|none)", drv)
}
