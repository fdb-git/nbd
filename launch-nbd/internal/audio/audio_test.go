package audio

import "testing"

func exists(set map[string]bool) func(string) bool {
	return func(p string) bool { return set[p] }
}

func TestDetectAuto(t *testing.T) {
	uid := 1000
	pw := SocketPath(uid, "pipewire-0")
	pa := SocketPath(uid, "pulse/native")

	if got, _ := Detect("auto", Env{UID: uid, Exists: exists(map[string]bool{pw: true})}); got != "pipewire" {
		t.Errorf("pipewire atteso, got=%q", got)
	}
	if got, _ := Detect("auto", Env{UID: uid, Exists: exists(map[string]bool{pa: true})}); got != "pa" {
		t.Errorf("pa atteso, got=%q", got)
	}
	if got, _ := Detect("auto", Env{UID: uid, Exists: exists(nil)}); got != "none" {
		t.Errorf("none atteso, got=%q", got)
	}
	// pipewire ha priorità su pa
	if got, _ := Detect("auto", Env{UID: uid, Exists: exists(map[string]bool{pw: true, pa: true})}); got != "pipewire" {
		t.Errorf("priorità pipewire, got=%q", got)
	}
}

func TestDetectExplicit(t *testing.T) {
	for _, d := range []string{"pipewire", "pa", "sdl", "none"} {
		if got, err := Detect(d, Env{}); err != nil || got != d {
			t.Errorf("Detect(%q)=%q,%v", d, got, err)
		}
	}
	if _, err := Detect("bogus", Env{}); err == nil {
		t.Error("atteso errore")
	}
}

func TestSocketPath(t *testing.T) {
	if got := SocketPath(1000, "pipewire-0"); got != "/run/user/1000/pipewire-0" {
		t.Errorf("SocketPath=%q", got)
	}
}

func TestDetectWindowsAuto(t *testing.T) {
	cases := []struct {
		backends []string
		want     string
	}{
		{[]string{"wasapi", "dsound", "sdl"}, "wasapi"},
		{[]string{"dsound", "sdl"}, "dsound"},
		{[]string{"sdl"}, "sdl"},
		{nil, "none"},
	}
	for _, c := range cases {
		got, err := Detect("auto", Env{GOOS: "windows", AudioBackends: c.backends})
		if err != nil || got != c.want {
			t.Errorf("Detect(auto, win, %v)=%q,%v atteso %q", c.backends, got, err, c.want)
		}
	}
}

func TestProbeBackendsEmptyBin(t *testing.T) {
	if ProbeBackends("") != nil {
		t.Error("ProbeBackends(\"\") deve dare nil")
	}
}
