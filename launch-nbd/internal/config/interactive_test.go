package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fdb-git/nbd/launch-nbd/internal/accel"
)

// determinismo nei test: nessun hint di abilitazione, accel base "tcg"
// (evita domande y/N e comandi sudo/PowerShell sulle macchine di CI).
func init() {
	detectAccel = func() accel.Result { return accel.Result{Mode: "tcg"} }
}

// wizard con stdin simulato: tutte righe vuote = accetta i default.
func TestWizardDefaults(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "launch-nbd.toml")
	n := len(wizardOpts) + 1 // risposta per ogni prompt + eventuale finale
	in := strings.Repeat("\n", n)
	s := PromptStreams{In: strings.NewReader(in), Out: &strings.Builder{}, Err: &strings.Builder{}}
	if err := Generate(p, false, s); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "nbd_host") {
		t.Errorf("TOML generato incompleto:\n%s", b)
	}
	// ricarica: deve passare la validazione con host vuoto? No — l'host era vuoto
	// nel default e il wizard non lo ha riempito: atteso errore di Validate.
	cfg, err := Load(p, noWarn)
	if err != nil {
		t.Fatal(err)
	}
	if errs := cfg.Validate(); len(errs) != 1 || !strings.Contains(errs[0].Error(), "nbd_host") {
		t.Errorf("atteso solo errore nbd_host mancante, trovati: %v", errs)
	}
}

// wizard interattivo: valore custom, valore invalido (re-prompt), default.
func TestWizardCustomAndReprompt(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "launch-nbd.toml")
	// feed con default dappertutto tranne: nbd_host="10.0.0.7" e, al prompt
	// net_mode (indice 7), "bogus" poi re-prompt "nat".
	feed := []string{"10.0.0.7"}
	for i := 1; i < len(wizardOpts); i++ {
		if wizardOpts[i].key == "net_mode" {
			feed = append(feed, "bogus", "nat")
		} else {
			feed = append(feed, "")
		}
	}
	in := strings.NewReader(strings.Join(feed, "\n") + "\n")
	s := PromptStreams{In: in, Out: &strings.Builder{}, Err: &strings.Builder{}}
	if err := Generate(p, false, s); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p, noWarn)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.NBDHost != "10.0.0.7" || cfg.NetMode != "nat" {
		t.Errorf("valori custom non applicati: host=%q net=%q", cfg.NBDHost, cfg.NetMode)
	}
}

// wizard --force: scrive la config coi default senza interazione.
func TestWizardForce(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "launch-nbd.toml")
	s := PromptStreams{In: strings.NewReader(""), Out: &strings.Builder{}, Err: &strings.Builder{}}
	if err := Generate(p, true, s); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("config non scritta: %v", err)
	}
	// seconda run --force: no-clobber (SaveNotExist)
	var out strings.Builder
	s2 := PromptStreams{In: strings.NewReader(""), Out: &out, Err: &strings.Builder{}}
	if err := Generate(p, true, s2); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "già presente") {
		t.Errorf("atteso avviso no-clobber, out=%q", out.String())
	}
}

// wizard: help esteso "?" e fine input inattesa.
func TestWizardHelpAndEOF(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "l.toml")
	// "?" al primo prompt: deve stampare help esteso e ri-chiedere
	feed := strings.Repeat("?\n", 1) + strings.Repeat("\n", len(wizardOpts)+1)
	var out strings.Builder
	s := PromptStreams{In: strings.NewReader(feed), Out: &out, Err: &strings.Builder{}}
	if err := Generate(p, false, s); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "help nbd_host") {
		t.Errorf("help esteso non stampato:\n%s", out.String())
	}

	// EOF durante un prompt (input finito): errore pulito, nessun file
	p2 := filepath.Join(dir, "l2.toml")
	s2 := PromptStreams{In: strings.NewReader(""), Out: &strings.Builder{}, Err: &strings.Builder{}}
	if err := Generate(p2, false, s2); err == nil {
		t.Errorf("atteso errore su input vuoto (EOF)")
	}
	if _, err := os.Stat(p2); !os.IsNotExist(err) {
		t.Errorf("file non deve esistere dopo errore: %v", err)
	}
}
