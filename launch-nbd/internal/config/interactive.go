package config

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"

	"github.com/fdb-git/nbd/launch-nbd/internal/accel"
)

// PromptStreams: I/O del wizard configure (iniettabile nei test).
type PromptStreams struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
}

// opt: voce del wizard (chiave TOML, descrizione, help esteso).

// detectAccel: rilevamento accelerazione di piattaforma (iniettabile nei test).
var detectAccel = accel.Detect

type opt struct {
	key     string
	desc    string
	extHelp string
}

// wizardOpts: ordine e testi dei prompt (plan-go.md §5.1: tabella opzioni).
var wizardOpts = []opt{
	{"nbd_host", "host/IP del server nbdkit (export dati)", "es. server, 10.0.0.5, odroid.local"},
	{"nbd_port", "porta dell'export dati", ""},
	{"nbd_export", "nome dell'export (creato con nbd-export.sh export ... --name)", "es. fdbnode"},
	{"nbd_state_port", "porta dell'export di stato .status (contratto docs/status-format.md)", "default 10819"},
	{"mem_mb", "memoria della VM (MiB)", ""},
	{"cpus", "CPU della VM", ""},
	{"accel", "accelerazione", "auto|kvm|whpx|haxm|tcg — la proposta tiene conto del preflight"},
	{"net_mode", "modello di rete", "dual|nat|hostonly|tap|bridged|none"},
	{"tap_dev", "device TAP per net_mode tap/dual", ""},
	{"tap_subnet", "rete privata per TAP (host .1 <-> guest .2)", ""},
	{"bridge_if", "interfaccia bridge per net_mode bridged", ""},
	{"display_mode", "display", "auto|gtk|sdl|vnc|spice|none"},
	{"spice_port", "porta del server SPICE (display_mode=spice)", ""},
	{"vga_mode", "modello VGA (virtio = migliore)", "virtio|std|qxl|vmware|none"},
	{"gl", "accel 3D virgl (solo con vga virtio)", "auto|on|off"},
	{"audio_mode", "scheda audio guest", "hda|virtio|none"},
	{"audio_drv", "backend audio host", "auto|pipewire|pa|sdl|none"},
	{"cache_mode", "cache QEMU per il disco NBD", "writeback|none|unsafe|writethrough|directsync"},
	{"aio", "AIO backend QEMU", "io_uring|native|threads"},
	{"reconnect_delay", "secondi di interruzione NBD tollerati (reconnect-delay)", ""},
	{"qcache_mb", "cache metadata qcow2 overlay (MiB)", ""},
	{"l2_cache_mb", "cache tabelle L2 qcow2 overlay (MiB)", ""},
	{"disk_mode", "modalità disco (solo TOML; --snapshot è CLI-only)", "direct|overlay"},
	{"overlay_dir", "directory degli overlay persistenti (disk_mode=overlay)", ""},
	{"monitor", "monitor HMP su stdio (-monitor stdio)", ""},
}

// Generate: wizard interattivo (force/--yes = usa la catena dei default senza
// chiedere). Base dei default proposta: TOML esistente -> default piattaforma;
// ACCEL è preceduta dal preflight (accel §5.9) che la sovrascrive.
func Generate(path string, force bool, s PromptStreams) error {
	if path == "" {
		path = DefaultConfigName
	}
	base, err := func() (Cfg, error) {
		// base proposta dal wizard = TOML esistente (modifica in-place) o
		// default di piattaforma se il file non esiste (lo stiamo creando).
		if _, serr := os.Stat(path); os.IsNotExist(serr) {
			b := PlatformDefaults()
			b.ApplyEnvMap(launchEnv(), func(m string) { fmt.Fprintln(s.Err, "warning:", m) })
			if m, ok, err := ParseEnvFile(".env"); err == nil && ok {
				b.ApplyEnvMap(m, func(m string) { fmt.Fprintln(s.Err, "warning:", m) })
			}
			return b, nil
		}
		return Load(path, func(m string) {
			fmt.Fprintln(s.Err, "warning:", m)
		})
	}()
	if err != nil {
		return err
	}
	meta := fields()

	// uno scanner condiviso per TUTTI i prompt (== un solo buffer sul reader)
	sc := bufio.NewScanner(s.In)

	// preflight accel: propone l'abilitazione previa conferma, e decide il
	// default del prompt ACCEL (risultato dinamico > config esistente > piattaforma)
	res := detectAccel()
	if !force && len(res.Hints) > 0 {
		for _, h := range res.Hints {
			if askYesNo(sc, s.Out, fmt.Sprintf("abilitare l'accelerazione?  esegui: %s [y/N]", h), false) {
				if out, err := exec.Command("sh", "-c", h).CombinedOutput(); err != nil {
					fmt.Fprintln(s.Err, "warning: comando fallito:", h, "-", strings.TrimSpace(string(out)))
				} else {
					res = detectAccel() // ricontrolla dopo l'esecuzione
				}
			}
		}
	}
	accelDefault := base.Accel
	if accelDefault == "auto" || accelDefault == "" {
		accelDefault = res.Mode
	}

	// --force: nessuna interazione, tutto sui default (catena base + preflight)
	if force {
		if base.Accel == "auto" || base.Accel == "" {
			base.Accel = accelDefault
		}
		exists, err := SaveNotExist(path, base)
		if err != nil {
			return err
		}
		if exists {
			fmt.Fprintf(s.Out, "config scritta: %s (tutti i default)\n", path)
		} else {
			fmt.Fprintf(s.Out, "config %s già presente: non sovrascritta (usa --force: %s esiste già)\n", path, path)
		}
		return nil
	}

	cfg := base
	for _, o := range wizardOpts {
		fm := meta[o.key]
		def := promptDefault(&cfg, fm, o.key, accelDefault)
		descr := o.desc
		if fm.enums != nil {
			descr += " (" + strings.Join(fm.enums, "|") + ")"
		}
		val, err := ask(sc, s.Out, o.key, descr, def, o.extHelp, func(input string) (string, error) {
			return normalizeInput(&cfg, fm, o.key, input)
		})
		if err != nil {
			return err
		}
		if err := setFromString(&cfg, fm, o.key, val); err != nil {
			return err
		}
	}

	if err := Save(path, cfg); err != nil {
		return err
	}
	fmt.Fprintf(s.Out, "config scritta: %s\n", path)
	return nil
}

// promptDefault: valore proposto per una voce (config esistente o default).
func promptDefault(cfg *Cfg, fm fieldMeta, key, accelDefault string) string {
	if key == "accel" && (cfg.Accel == "auto" || cfg.Accel == "") {
		return accelDefault
	}
	return fieldString(cfg, fm)
}

// fieldString: valore corrente del campo come stringa.
func fieldString(cfg *Cfg, fm fieldMeta) string {
	rv := reflect.ValueOf(cfg).Elem().FieldByName(fm.name)
	switch fm.typ {
	case reflect.String:
		return rv.String()
	case reflect.Int:
		return strconv.FormatInt(rv.Int(), 10)
	case reflect.Bool:
		if rv.Bool() {
			return "yes"
		}
		return "no"
	}
	return ""
}

// normalizeInput: valida e normalizza l'input (enum/interi/bool).
func normalizeInput(cfg *Cfg, fm fieldMeta, key, input string) (string, error) {
	input = strings.TrimSpace(input)
	if input == "" || input == "?" {
		return input, nil // gestiti da ask()
	}
	switch fm.typ {
	case reflect.String:
		if len(fm.enums) > 0 && !contains(fm.enums, input) {
			return "", fmt.Errorf("valore non ammesso (usa: %s)", strings.Join(fm.enums, "|"))
		}
	case reflect.Int:
		if _, err := strconv.Atoi(input); err != nil {
			return "", fmt.Errorf("intero atteso")
		}
	case reflect.Bool:
		if _, err := parseBool(input); err != nil {
			return "", fmt.Errorf("atteso y/n o 1/0")
		}
	}
	return input, nil
}

// setFromString: applica una stringa al campo (già normalizzata); i bool
// passano per SetKey (parseBool accetta yes/no/1/0/on/off).
func setFromString(cfg *Cfg, fm fieldMeta, key, val string) error {
	if val == "" {
		return nil
	}
	return cfg.SetKey(key, val)
}

// ask: prompt singolo con default tra [], ?=help esteso, Enter=default.
func ask(sc *bufio.Scanner, out io.Writer, key, descr, def, extHelp string,
	normalize func(string) (string, error)) (string, error) {
	fmt.Fprintf(out, "%s [%s]: ", descr, def)
	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return "", err
		}
		return "", fmt.Errorf("fine dell'input durante il wizard")
	}
	input := strings.TrimSpace(sc.Text())
	if input == "" {
		return def, nil
	}
	if input == "?" {
		if extHelp != "" {
			fmt.Fprintf(out, "  help %s: %s\n", key, extHelp)
		} else {
			fmt.Fprintf(out, "  help %s: valori ammessi: %s\n", key, descr)
		}
		return ask(sc, out, key, descr, def, extHelp, normalize)
	}
	norm, err := normalize(input)
	if err != nil {
		fmt.Fprintf(out, "  error: %v\n", err)
		return ask(sc, out, key, descr, def, extHelp, normalize)
	}
	return norm, nil
}

// askYesNo: conferma y/N (default = no); usa lo scanner condiviso del wizard.
func askYesNo(sc *bufio.Scanner, out io.Writer, prompt string, def bool) bool {
	d := "N"
	if def {
		d = "y"
	}
	fmt.Fprintf(out, "%s [%s]: ", prompt, d)
	if !sc.Scan() {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(sc.Text())) {
	case "y", "yes":
		return true
	case "n", "no":
		return false
	}
	return def
}
