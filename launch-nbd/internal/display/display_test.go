package display

import "testing"

func lookPath(set map[string]bool) func(string) (string, bool) {
	return func(s string) (string, bool) {
		if set[s] {
			return "/usr/bin/" + s, true
		}
		return "", false
	}
}

func TestResolveAuto(t *testing.T) {
	cases := []struct {
		name string
		env  Env
		want string
	}{
		{"linux grafico -> gtk", Env{Graphical: true, GOOS: "linux"}, "gtk"},
		{"linux headless -> vnc", Env{Graphical: false, GOOS: "linux"}, "vnc"},
		{"windows grafico -> sdl", Env{Graphical: true, GOOS: "windows"}, "sdl"},
		{"windows headless -> vnc", Env{Graphical: false, GOOS: "windows"}, "vnc"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Resolve("auto", c.env)
			if err != nil || got != c.want {
				t.Errorf("Resolve(auto)=%q,%v atteso %q", got, err, c.want)
			}
		})
	}
	for _, m := range []string{"gtk", "sdl", "vnc", "spice", "none"} {
		if got, err := Resolve(m, Env{}); err != nil || got != m {
			t.Errorf("Resolve(%q)=%q,%v", m, got, err)
		}
	}
	if _, err := Resolve("bogus", Env{}); err == nil {
		t.Error("atteso errore su display_mode invalido")
	}
}

func TestResolveGL(t *testing.T) {
	if v, _ := ResolveGL("auto", Env{Graphical: true}); v != "on" {
		t.Errorf("gl auto grafico=%q", v)
	}
	if v, _ := ResolveGL("auto", Env{Graphical: false}); v != "off" {
		t.Errorf("gl auto headless=%q", v)
	}
	if _, err := ResolveGL("bogus", Env{}); err == nil {
		t.Error("atteso errore")
	}
}

func TestViewer(t *testing.T) {
	// remote-viewer preferito
	argv, ok := Viewer(Env{LookPath: lookPath(map[string]bool{"remote-viewer": true, "virt-viewer": true})}, 5930)
	if !ok || argv[0] != "remote-viewer" || argv[1] != "spice://127.0.0.1:5930" {
		t.Errorf("argv=%v ok=%v", argv, ok)
	}
	// fallback virt-viewer
	argv, ok = Viewer(Env{LookPath: lookPath(map[string]bool{"virt-viewer": true})}, 5931)
	if !ok || argv[0] != "virt-viewer" {
		t.Errorf("fallback=%v ok=%v", argv, ok)
	}
	// fallback flatpak
	argv, ok = Viewer(Env{LookPath: lookPath(nil), HasFlatpak: func() bool { return true }}, 5930)
	if !ok || argv[0] != "flatpak" || argv[len(argv)-1] != "spice://127.0.0.1:5930" {
		t.Errorf("flatpak=%v ok=%v", argv, ok)
	}
	// nessuno
	if _, ok := Viewer(Env{LookPath: lookPath(nil), HasFlatpak: func() bool { return false }}, 5930); ok {
		t.Error("atteso ok=false senza client")
	}
}
