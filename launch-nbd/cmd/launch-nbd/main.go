// Command launch-nbd: boot a QEMU VM whose disks are NBD exports served by
// nbdkit (see ../export-nbd). Go reimplementation of launch-nbd.sh
// (launch-nbd/docs/plan-go.md).
//
// Subcommands: configure | install | run (default) | commit | help.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/fdb-git/nbd/launch-nbd/internal/assets"
	"github.com/fdb-git/nbd/launch-nbd/internal/cli"
	"github.com/fdb-git/nbd/launch-nbd/internal/config"
	"github.com/fdb-git/nbd/launch-nbd/internal/iso"
)

// run implements the CLI contract (exit codes):
//
//	0 ok | 1 errore generico | 2 uso errato (help su stderr).
func run(argv []string, out, errw io.Writer, in io.Reader) int {
	args, err := cli.Parse(argv)
	if err != nil {
		fmt.Fprintln(errw, "error:", err)
		cli.Usage(errw)
		var unknown cli.UnknownCommandError
		if errors.As(err, &unknown) {
			return 1 // comando sconosciuto: help su stderr + exit 1 (plan §5.2)
		}
		return 2 // uso errato (es. install senza --iso)
	}

	switch args.Action {
	case cli.ActHelp:
		cli.Usage(out)
		return 0

	case cli.ActConfigure:
		streams := config.PromptStreams{In: in, Out: out, Err: errw}
		if err := config.Generate(args.ConfigPath, args.Force, streams); err != nil {
			fmt.Fprintln(errw, "error:", err)
			return 1
		}
		return 0

	case cli.ActInstall:
		// M3: risolve l'ISO (URL -> download resume+checksum; file -> diretto).
		// L'avvio della VM di provisioning (CD boot 0) arriva con M4.
		res, err := iso.Resolve(context.Background(), iso.Options{
			Arg: args.Iso,
			Log: func(m string) {
				if !args.Quiet {
					fmt.Fprintln(out, m)
				}
			},
		})
		if err != nil {
			fmt.Fprintln(errw, "error:", err)
			return 1
		}
		if !args.Quiet {
			fmt.Fprintf(out, "install: ISO pronta: %s\n", res.Path)
			if res.Checksum != "" {
				fmt.Fprintf(out, "  sha256: %s (%s)\n", res.Checksum, res.ChecksumSource)
			}
		}
		fmt.Fprintln(errw, "install: avvio VM di provisioning non ancora implementato (Fase B M4)")
		return 1

	case cli.ActRun, cli.ActCommit:
		cfg, err := config.Load(args.ConfigPath, func(m string) {
			fmt.Fprintln(errw, "warning:", m)
		})
		if err != nil {
			fmt.Fprintln(errw, "error:", err)
			return 1
		}
		for _, kv := range args.Set { // CLI --set: precedenza massima
			if err := cfg.SetKey(kv.Key, kv.Val); err != nil {
				fmt.Fprintln(errw, "error:", err)
				return 1
			}
		}
		if errs := cfg.Validate(); len(errs) > 0 {
			for _, e := range errs {
				fmt.Fprintln(errw, "error:", e)
			}
			return 1
		}
		// asset embeddati (M1): estratti in temp (o dev-dir via
		// LAUNCH_NBD_ASSETS_DIR). Fallback sui binari di sistema se assenti.
		aset, err := assets.Prepare(assets.Options{DevDir: os.Getenv("LAUNCH_NBD_ASSETS_DIR")})
		if err != nil {
			fmt.Fprintln(errw, "warning: preparazione asset fallita:", err)
			aset = &assets.Set{}
		}
		defer aset.Cleanup()
		qemu := aset.Resolve("qemu")
		if qemu == "" {
			qemu = cfg.QEMU
		}
		if args.Action == cli.ActRun {
			if !args.Quiet {
				fmt.Fprintln(out, "launch-nbd run: config effettiva:")
				fmt.Fprintf(out, "  nbd: %s (%s), export %s, state %d\n",
					cfg.NBDHost, cfg.URI(), cfg.NBDExport, cfg.NBDStatePort)
				fmt.Fprintf(out, "  vm:  %d MiB, %d cpu, accel %s, net %s, display %s\n",
					cfg.MemMB, cfg.CPUs, cfg.Accel, cfg.NetMode, cfg.DisplayMode)
				fmt.Fprintf(out, "  qemu: %s\n", qemu)
				fmt.Fprintf(out, "  disk: %s\n", cfg.DiskMode)
			}
			fmt.Fprintln(errw, "run: avvio QEMU non ancora implementato (Fase B M4; builder args in internal/qemu)")
			return 1
		}
		fmt.Fprintln(errw, "commit: overlay commit non ancora implementato (Fase B M4)")
		return 1
	}

	fmt.Fprintln(errw, "error: azione non gestita")
	return 1
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Stdin))
}
