// Package display: risoluzione runtime di display/GL e client SPICE
// (plan-go.md §5.6). Logica pura con dipendenze iniettate: testabile ovunque.
package display

import "fmt"

// Env: fatti d'ambiente (iniettabili).
type Env struct {
	Graphical  bool                        // sessione grafica (DISPLAY/WAYLAND)
	GOOS       string                      // "linux" | "windows" | ...
	LookPath   func(string) (string, bool) // ricerca binari
	HasFlatpak func() bool                 // flatpak disponibile (fallback viewer)
}

// Resolve: risolve "auto" e valida la modalità display.
// auto → gtk (Linux) / sdl (Windows) se sessione grafica, altrimenti vnc
// (headless, il default dello script senza DISPLAY).
func Resolve(mode string, env Env) (string, error) {
	switch mode {
	case "auto":
		if !env.Graphical {
			return "vnc", nil
		}
		if env.GOOS == "windows" {
			return "sdl", nil
		}
		return "gtk", nil
	case "gtk", "sdl", "vnc", "spice", "none":
		return mode, nil
	}
	return "", fmt.Errorf("display_mode: '%s' non ammesso (auto|gtk|sdl|vnc|spice|none)", mode)
}

// ResolveGL: risolve "auto" per virgl: on se c'è una sessione grafica, off altrimenti.
func ResolveGL(gl string, env Env) (string, error) {
	switch gl {
	case "auto":
		if env.Graphical {
			return "on", nil
		}
		return "off", nil
	case "on", "off":
		return gl, nil
	}
	return "", fmt.Errorf("gl: '%s' non ammesso (auto|on|off)", gl)
}

// Viewer: comando del client SPICE per la porta locale.
// Ordine (come lo script): remote-viewer, virt-viewer, flatpak virt-viewer.
// Ritorna ok=false se nessuno è disponibile.
func Viewer(env Env, port int) ([]string, bool) {
	uri := fmt.Sprintf("spice://127.0.0.1:%d", port)
	if env.LookPath != nil {
		for _, bin := range []string{"remote-viewer", "virt-viewer"} {
			if _, ok := env.LookPath(bin); ok {
				return []string{bin, uri}, true
			}
		}
	}
	if env.HasFlatpak != nil && env.HasFlatpak() {
		return []string{"flatpak", "run", "org.virt_manager.virt-viewer", uri}, true
	}
	return nil, false
}

// ViewerHint: messaggio di aiuto se il client SPICE manca.
func ViewerHint() string {
	return "nessun client SPICE trovato: installa remote-viewer/virt-viewer " +
		"oppure 'flatpak install -y flathub org.virt_manager.virt-viewer'"
}
