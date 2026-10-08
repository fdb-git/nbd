// Package audio: rilevamento del backend audio host (plan-go.md §5.6/§5.7).
// Logica pura con dipendenze iniettate: testabile ovunque.
package audio

import (
	"fmt"
	"os/exec"
	"strings"
)

// Env: fatti d'ambiente (iniettabili).
type Env struct {
	UID    int               // uid per /run/user/<uid> (Linux)
	Exists func(string) bool // esistenza di un path (Linux)
	GOOS   string            // "linux" | "windows" | ...

	// AudioBackends: backend supportati da QEMU (da `qemu -audiodev help`),
	// usati per l'auto-selezione su Windows (wasapi > dsound > sdl).
	AudioBackends []string
}

// SocketPath: path del socket audio per l'utente (Linux).
func SocketPath(uid int, name string) string {
	return fmt.Sprintf("/run/user/%d/%s", uid, name)
}

// Detect: risolve "auto" e valida il backend audio.
//   - Windows: auto → primo tra wasapi, dsound, sdl disponibili in QEMU,
//     altrimenti none (come il launcher di riferimento che sonda i backend).
//   - Linux: auto → pipewire se il socket c'è, poi pulse, altrimenti none.
func Detect(drv string, env Env) (string, error) {
	switch drv {
	case "auto":
		if env.GOOS == "windows" {
			for _, want := range []string{"wasapi", "dsound", "sdl"} {
				if contains(env.AudioBackends, want) {
					return want, nil
				}
			}
			return "none", nil
		}
		if env.Exists != nil {
			if env.Exists(SocketPath(env.UID, "pipewire-0")) {
				return "pipewire", nil
			}
			if env.Exists(SocketPath(env.UID, "pulse/native")) {
				return "pa", nil
			}
		}
		return "none", nil
	case "pipewire", "pa", "wasapi", "dsound", "sdl", "none":
		return drv, nil
	}
	return "", fmt.Errorf("audio_drv: '%s' non ammesso (auto|pipewire|pa|wasapi|dsound|sdl|none)", drv)
}

// SupportedBackends: backend audio accettati da -audiodev.
func SupportedBackends() []string {
	return []string{"pipewire", "pa", "wasapi", "dsound", "sdl", "none"}
}

// ProbeBackends: sonda `qemu -audiodev help` e ritorna i backend noti presenti
// nell'output (usato per l'auto-selezione su Windows).
func ProbeBackends(qemuBin string) []string {
	if qemuBin == "" {
		return nil
	}
	out, err := exec.Command(qemuBin, "-audiodev", "help").CombinedOutput()
	if err != nil {
		return nil
	}
	var found []string
	for _, b := range []string{"wasapi", "dsound", "sdl", "pa", "pipewire"} {
		if strings.Contains(string(out), b) {
			found = append(found, b)
		}
	}
	return found
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
