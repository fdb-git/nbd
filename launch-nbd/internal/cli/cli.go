// Package cli: dispatch dei sottocomandi e flag residue.
//
// Contratto (plan-go.md §5.2):
//
//	configure [--config <file>] [--force|--yes]
//	install   [--iso <file|URL>] [--config <file>]          (--iso obbligatorio)
//	run       [--snapshot] [--set k=v ...] [--config <file>] [--quiet]  (default)
//	commit    [--config <file>] [--quiet]
//	help / --help / -h                                       -> usage, exit 0
//
// unknown subcommand                                         -> usage stderr, exit 1
// install senza --iso                                        -> uso+exit 2
package cli

import (
	"fmt"
	"io"
	"strings"
)

// Action: sottocomando selezionato.
type Action int

const (
	ActInvalid Action = iota
	ActHelp
	ActConfigure
	ActInstall
	ActRun
	ActCommit
)

// KeyVal: override one-shot `--set k=v`.
type KeyVal struct {
	Key, Val string
}

// Args: risultato del parse di argv.
type Args struct {
	Action     Action
	ConfigPath string // --config <file> (vuoto = default launch-nbd.toml)
	Quiet      bool
	Force      bool // configure: --force/--yes (default e basta, no prompt)
	Snapshot   bool // run: overlay throwaway, prevale su disk_mode
	Set        []KeyVal
	Iso        string // install: --iso <file|URL>
}

// UsageError: uso errato (exit 2).  Fe lo stesso errore fa print dell'usage.
type UsageError struct{ msg string }

func (e UsageError) Error() string { return e.msg }

func usagef(format string, a ...any) error {
	return UsageError{msg: fmt.Sprintf(format, a...)}
}

// UnknownCommandError: comando sconosciuto (exit 1, plan-go.md §5.2).
type UnknownCommandError struct{ cmd string }

func (e UnknownCommandError) Error() string {
	return fmt.Sprintf("comando sconosciuto '%s'", e.cmd)
}

// Parse: argv senza il nome del binario.
func Parse(argv []string) (Args, error) {
	a := Args{Action: ActRun} // default: run
	if len(argv) == 0 {
		return a, nil
	}

	// sottocomando come primo argomento (help/--help/-h inclusi)
	first := argv[0]
	rest := argv[1:]
	switch first {
	case "help", "--help", "-h":
		a.Action = ActHelp
		return a, nil
	case "configure":
		a.Action = ActConfigure
	case "install":
		a.Action = ActInstall
	case "run":
		a.Action = ActRun
	case "commit":
		a.Action = ActCommit
	default:
		if strings.HasPrefix(first, "-") {
			// primo argomento è già un flag: run con quei flag
			rest = argv
		} else {
			return a, UnknownCommandError{cmd: first}
		}
	}

	// flag del sottocomando
	for len(rest) > 0 {
		arg := rest[0]
		switch {
		case arg == "--config":
			if len(rest) < 2 {
				return a, usagef("--config richiede un percorso")
			}
			a.ConfigPath = rest[1]
			rest = rest[2:]
		case strings.HasPrefix(arg, "--config="):
			a.ConfigPath = strings.TrimPrefix(arg, "--config=")
			rest = rest[1:]
		case arg == "--quiet":
			a.Quiet = true
			rest = rest[1:]
		case arg == "--force" || arg == "--yes":
			a.Force = true
			rest = rest[1:]
		case arg == "--snapshot":
			if a.Action != ActRun {
				return a, usagef("--snapshot vale solo per run")
			}
			a.Snapshot = true
			rest = rest[1:]
		case arg == "--iso":
			if a.Action != ActInstall {
				return a, usagef("--iso vale solo per install")
			}
			if len(rest) < 2 {
				return a, usagef("--iso richiede <file|URL>")
			}
			a.Iso = rest[1]
			rest = rest[2:]
		case strings.HasPrefix(arg, "--iso="):
			if a.Action != ActInstall {
				return a, usagef("--iso vale solo per install")
			}
			a.Iso = strings.TrimPrefix(arg, "--iso=")
			rest = rest[1:]
		case arg == "--set":
			if len(rest) < 2 {
				return a, usagef("--set richiede k=v (es. --set net=nat)")
			}
			kv, err := parseKeyVal(rest[1])
			if err != nil {
				return a, err
			}
			a.Set = append(a.Set, kv)
			rest = rest[2:]
		case strings.HasPrefix(arg, "--set="):
			kv, err := parseKeyVal(strings.TrimPrefix(arg, "--set="))
			if err != nil {
				return a, err
			}
			a.Set = append(a.Set, kv)
			rest = rest[1:]
		case arg == "--help" || arg == "-h":
			a.Action = ActHelp
			return a, nil
		default:
			if strings.HasPrefix(arg, "-") {
				return a, usagef("flag sconosciuto '%s'", arg)
			}
			return a, usagef("argomento inatteso '%s'", arg)
		}
	}

	// install senza --iso: uso+exit 2 (contratto M0)
	if a.Action == ActInstall && a.Iso == "" {
		return a, usagef("install richiede --iso <file|URL>")
	}
	return a, nil
}

func parseKeyVal(s string) (KeyVal, error) {
	k, v, ok := strings.Cut(s, "=")
	if !ok || k == "" || v == "" {
		return KeyVal{}, usagef("--set: atteso k=v, trovato '%s'", s)
	}
	return KeyVal{Key: strings.TrimSpace(k), Val: strings.TrimSpace(v)}, nil
}

// Usage: help completo su w.
func Usage(w io.Writer) {
	fmt.Fprintf(w, `usage: launch-nbd <subcommand> [flags]

Boot di una VM QEMU il cui disco è un export NBD del server nbdkit
(riscrittura Go di launch-nbd.sh — piano launch-nbd/docs/plan-go.md).

subcommands:
  configure [--config <file>] [--force|--yes]
          wizard interattivo: genera <file> (default launch-nbd.toml)
          documentato, con preflight accelerazione; --force usa i default
          senza chiedere (per CI/uso non interattivo)
  install  [--config <file>] --iso <file|URL>
          provisioning una-tantum di un export NBD nuovo tramite ISO
          (download+checksum: Fase B M3)
  run      [--config <file>] [--snapshot] [--set k=v ...]  [--quiet]
          (default) boot dal disco NBD; --snapshot = overlay throwaway
          che prevale su disk_mode
  commit   [--config <file>] [--quiet]
          qemu-img commit dell'overlay sull'export NBD (Fase B M4)
  help | --help | -h
          questo testo (exit 0)

global flags:
  --config <file>   file TOML (default: launch-nbd.toml nel cwd)
  --set k=v         override one-shot di una chiave (ripetibile). k accetta
                    la chiave TOML (es. nbd_host, net_mode, mem_mb) o un
                    alias breve: net, display, vga, audio, accel, tap,
                    bridge, cache, gl, mem, cpus
  --quiet           sopprime l'output non di errore

exit codes: 0 ok | 1 errore | 2 uso errato (help su stderr)

configurazione (costrutto da configure):
  launch-nbd.toml   chiavi snake_case (nbd_host, mem_mb, ...)
  .env (legacy)     variabili NBD_* / MEM_MB / ... della vecchia launch-nbd.sh
  ambiente          LAUNCH_NBD_<KEY> (es. LAUNCH_NBD_NET_MODE=dual)

precedenza: --set > TOML > .env legacy > ambiente > default di piattaforma.
`)
}
