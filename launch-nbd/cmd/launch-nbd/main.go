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
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/fdb-git/nbd/launch-nbd/internal/accel"
	"github.com/fdb-git/nbd/launch-nbd/internal/assets"
	"github.com/fdb-git/nbd/launch-nbd/internal/audio"
	"github.com/fdb-git/nbd/launch-nbd/internal/cli"
	"github.com/fdb-git/nbd/launch-nbd/internal/config"
	"github.com/fdb-git/nbd/launch-nbd/internal/display"
	"github.com/fdb-git/nbd/launch-nbd/internal/iso"
	"github.com/fdb-git/nbd/launch-nbd/internal/nbd"
	"github.com/fdb-git/nbd/launch-nbd/internal/net"
	"github.com/fdb-git/nbd/launch-nbd/internal/qemu"
)

// run implementa il contratto CLI (exit codes):
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

	ctx := context.Background()
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
		isoPath, code := resolveISO(ctx, args, out, errw)
		if code != 0 {
			return code
		}
		return launchVM(ctx, qemu.ModeInstall, isoPath, args, out, errw, in)

	case cli.ActRun:
		return launchVM(ctx, qemu.ModeRun, "", args, out, errw, in)

	case cli.ActCommit:
		return cmdCommit(ctx, args, out, errw)
	}
	fmt.Fprintln(errw, "error: azione non gestita")
	return 1
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Stdin))
}

// session: risorse temporanee di un'esecuzione, cleanup idempotente.
type session struct {
	workDir  string
	cleanups []func()
	once     sync.Once
}

func newSession() (*session, error) {
	dir, err := os.MkdirTemp("", "launch-nbd-")
	if err != nil {
		return nil, err
	}
	return &session{workDir: dir}, nil
}

func (s *session) add(f func()) { s.cleanups = append(s.cleanups, f) }

func (s *session) cleanup() {
	s.once.Do(func() {
		for i := len(s.cleanups) - 1; i >= 0; i-- {
			s.cleanups[i]()
		}
		if s.workDir != "" {
			_ = os.RemoveAll(s.workDir)
		}
	})
}

// loadCfg: config + --set + validazione. Ritorna (cfg, exit code != 0).
func loadCfg(args cli.Args, errw io.Writer) (config.Cfg, int) {
	cfg, err := config.Load(args.ConfigPath, func(m string) { fmt.Fprintln(errw, "warning:", m) })
	if err != nil {
		fmt.Fprintln(errw, "error:", err)
		return cfg, 1
	}
	for _, kv := range args.Set {
		if err := cfg.SetKey(kv.Key, kv.Val); err != nil {
			fmt.Fprintln(errw, "error:", err)
			return cfg, 1
		}
	}
	if errs := cfg.Validate(); len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintln(errw, "error:", e)
		}
		return cfg, 1
	}
	return cfg, 0
}

// resolveISO: install --iso → ISO locale verificata (M3).
func resolveISO(ctx context.Context, args cli.Args, out, errw io.Writer) (string, int) {
	res, err := iso.Resolve(ctx, iso.Options{
		Arg: args.Iso,
		Log: func(m string) {
			if !args.Quiet {
				fmt.Fprintln(out, m)
			}
		},
	})
	if err != nil {
		fmt.Fprintln(errw, "error:", err)
		return "", 1
	}
	if !args.Quiet {
		fmt.Fprintf(out, "install: ISO pronta: %s\n", res.Path)
	}
	return res.Path, 0
}

// stateGuard: guardia cross-host (§5.10): se lo stato è `committing`, fail-stop.
// Export di stato irraggiungibile → warning (si prosegue con la guardia locale).
func stateGuard(ctx context.Context, cfg config.Cfg, errw io.Writer) int {
	if cfg.NBDStatePort <= 0 {
		return 0
	}
	rec, err := nbd.ReadState(ctx, cfg.NBDHost, cfg.NBDStatePort, cfg.NBDExport)
	if err != nil {
		fmt.Fprintf(errw, "warning: export di stato non raggiungibile (%v): proseguo con la guardia locale\n", err)
		return 0
	}
	if rec.State == nbd.StateCommitting {
		fmt.Fprintf(errw, "error: commit interrotto lato server (owner %s, hash %s): completa prima 'launch-nbd commit'\n",
			rec.OwnerHost, rec.Hash)
		return 1
	}
	return 0
}

// launchVM: run/install → prepara overlay+firmware, costruisce gli args e avvia QEMU.
func launchVM(ctx context.Context, mode qemu.Mode, isoPath string, args cli.Args, out, errw io.Writer, in io.Reader) int {
	cfg, code := loadCfg(args, errw)
	if code != 0 {
		return code
	}
	aset, err := assets.Prepare(assets.Options{DevDir: os.Getenv("LAUNCH_NBD_ASSETS_DIR")})
	if err != nil {
		fmt.Fprintln(errw, "warning: preparazione asset fallita:", err)
		aset = &assets.Set{}
	}
	defer aset.Cleanup()

	sess, err := newSession()
	if err != nil {
		fmt.Fprintln(errw, "error:", err)
		return 1
	}
	defer sess.cleanup()

	if code := stateGuard(ctx, cfg, errw); code != 0 {
		return code
	}

	// overlay locale (disk_mode=overlay o --snapshot) con fingerprint + guardie
	overlayFile, code := prepareOverlay(ctx, cfg, args, aset, sess, errw)
	if code != 0 {
		return code
	}

	_, varsCopy := firmware(aset, sess, errw)

	// display / GL / audio: risoluzione runtime (M6)
	dEnv := display.Env{
		Graphical:  graphicalSession(),
		GOOS:       runtime.GOOS,
		LookPath:   lookPath,
		HasFlatpak: func() bool { _, ok := lookPath("flatpak"); return ok },
	}
	disp, err := display.Resolve(cfg.DisplayMode, dEnv)
	if err != nil {
		fmt.Fprintln(errw, "error:", err)
		return 1
	}
	gl, err := display.ResolveGL(cfg.GL, dEnv)
	if err != nil {
		fmt.Fprintln(errw, "error:", err)
		return 1
	}
	aEnv := audio.Env{
		UID:    audio.CurrentUID(),
		Exists: func(p string) bool { _, err := os.Stat(p); return err == nil },
	}
	adrv, err := audio.Detect(cfg.AudioDrv, aEnv)
	if err != nil {
		fmt.Fprintln(errw, "error:", err)
		return 1
	}

	// rete TAP (M5/M6): setup + revert nella sessione
	bridgedTap := ""
	if code := setupNet(ctx, cfg, aset, sess, errw, &bridgedTap); code != 0 {
		return code
	}

	facts := qemu.Facts{
		Mode:        mode,
		Snapshot:    args.Snapshot,
		OverlayFile: overlayFile,
		ISOFile:     isoPath,
		KVM:         kvmAvailable(),
		CPU:         cpuModel(),
		VarsCopy:    varsCopy,
		BridgedTap:  bridgedTap,
		DisplayMode: disp,
		GL:          gl,
		AudioDrv:    adrv,
	}
	if varsCopy != "" {
		facts.OVMFCode = aset.Resolve("ovmf-code")
		facts.OVMFVars = aset.Resolve("ovmf-vars")
	}
	res, err := qemu.Build(cfg, facts)
	if err != nil {
		fmt.Fprintln(errw, "error:", err)
		return 1
	}
	if !args.Quiet {
		for _, n := range res.Notes {
			fmt.Fprintln(out, n)
		}
	}
	qemuBin := aset.Resolve("qemu")
	if qemuBin == "" {
		qemuBin = cfg.QEMU
	}
	if !args.Quiet {
		fmt.Fprintf(out, "[run] %s (%s, %d MiB, %d cpu)\n", qemuBin, mode, cfg.MemMB, cfg.CPUs)
	}

	// client SPICE: lancio in background (come lo script: attesa 2s che il
	// server ascolti), solo con display=spice
	if disp == "spice" {
		if argv, ok := display.Viewer(dEnv, cfg.SpicePort); ok {
			launchViewer(argv)
			if !args.Quiet {
				fmt.Fprintf(out, "[spice] client: %s\n", strings.Join(argv, " "))
			}
		} else {
			fmt.Fprintln(errw, "warning:", display.ViewerHint())
		}
	}

	rc, err := qemu.Run(ctx, qemu.RunOptions{
		QEMU:    qemuBin,
		Args:    res.Args,
		In:      in,
		Out:     out,
		Err:     errw,
		Cleanup: sess.cleanup,
	})
	if err != nil {
		fmt.Fprintln(errw, "error:", err)
		return 1
	}
	return rc
}

// prepareOverlay: risolve/crea l'overlay del disco (o snapshot throwaway).
// Ritorna ("", 0) se il disco è diretto (nessun overlay).
func prepareOverlay(ctx context.Context, cfg config.Cfg, args cli.Args, aset *assets.Set, sess *session, errw io.Writer) (string, int) {
	snapshot := args.Snapshot
	overlayMode := cfg.DiskMode == "overlay"
	if !snapshot && !overlayMode {
		return "", 0
	}
	img := aset.Resolve("qemu-img")
	if img == "" {
		img = cfg.QEMUImg
	}
	uri := cfg.URI()

	if snapshot {
		// throwaway: file in temp, cleanup su exit
		f := filepath.Join(sess.workDir, "nbd-snap.qcow2")
		if err := qemu.RunImg(img)(ctx, qemu.ImgCreateArgs(uri, f)...); err != nil {
			fmt.Fprintln(errw, "error:", err)
			return "", 1
		}
		sess.add(func() { _ = os.Remove(f) })
		return f, 0
	}

	// overlay persistente: fingerprint + guardie (§5.8)
	hash, err := nbd.Fingerprint(ctx, cfg.NBDHost, cfg.NBDPort, cfg.NBDExport)
	if err != nil {
		fmt.Fprintln(errw, "error:", err)
		return "", 1
	}
	dir, err := filepath.Abs(cfg.OverlayDir)
	if err != nil {
		fmt.Fprintln(errw, "error:", err)
		return "", 1
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintln(errw, "error:", err)
		return "", 1
	}
	path, err := qemu.EnsureOverlay(ctx, dir, hash, uri, img, nil, func(m string) {
		fmt.Fprintln(errw, m)
	})
	if err != nil {
		fmt.Fprintln(errw, "error:", err)
		return "", 1
	}
	fmt.Fprintf(errw, "warning: overlay locale %s (le scritture restano locali fino a 'launch-nbd commit')\n", path)
	return path, 0
}

// setupNet: prepara la rete host per i modi che la richiedono.
// tap/dual → TAP privato (Linux: ip/iptables; Windows: TAP-Windows6).
// bridged da root → TAP sul bridge (bridgedTap); su Windows → errore chiaro.
// Il revert è registrato nella sessione (sempre eseguito).
func setupNet(ctx context.Context, cfg config.Cfg, aset *assets.Set, sess *session, errw io.Writer, bridgedTap *string) int {
	switch cfg.NetMode {
	case "tap", "dual":
		s, err := net.SetupTap(ctx, net.NewSystem(), net.Config{
			Dev:     cfg.TapDev,
			Subnet:  cfg.TapSubnet,
			Windows: net.WindowsConfig{TapCtl: aset.Resolve("tapctl")},
			Log:     func(m string) { fmt.Fprintln(errw, m) },
			Warn:    func(m string) { fmt.Fprintln(errw, "warning:", m) },
		})
		if err != nil {
			fmt.Fprintln(errw, "error:", err)
			return 1
		}
		sess.add(s.Revert)
	case "bridged":
		if net.IsRoot() {
			s, err := net.SetupBridge(ctx, net.NewSystem(), net.Config{
				Log:  func(m string) { fmt.Fprintln(errw, m) },
				Warn: func(m string) { fmt.Fprintln(errw, "warning:", m) },
			}, cfg.BridgeIF)
			if err != nil {
				fmt.Fprintln(errw, "error:", err)
				return 1
			}
			sess.add(s.Revert)
			*bridgedTap = s.TapDev
		}
	}
	return 0
}

// cpuModel: modello -cpu per la piattaforma. Su Windows/WHPX "max" e "host"
// fermano la vCPU ("WHPX: Unexpected VP exit code 4"): si usa un modello
// compatibile (Haswell). Su Linux "max" (KVM/TCG lo supportano).
func cpuModel() string {
	if runtime.GOOS == "windows" {
		return "Haswell"
	}
	return "max"
}

// graphicalSession: c'è una sessione grafica? (DISPLAY/WAYLAND su Unix; sempre
// vero su Windows, dove l'ambiente desktop è implicito).
func graphicalSession() bool {
	if runtime.GOOS == "windows" {
		return true
	}
	return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
}

// lookPath: ricerca binari nel PATH.
func lookPath(name string) (string, bool) {
	p, err := exec.LookPath(name)
	return p, err == nil
}

// launchViewer: avvia il client SPICE in background dopo 2s (il server deve
// prima mettersi in ascolto). Fire-and-forget, come il subshell dello script.
func launchViewer(argv []string) {
	go func() {
		time.Sleep(2 * time.Second)
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		_ = cmd.Start()
	}()
}

// firmware: OVMF dagli asset; copia scrivibile della VARS nella workDir.
// Se manca, ritorna ("", "") → SeaBIOS.
func firmware(aset *assets.Set, sess *session, errw io.Writer) (string, string) {
	code := aset.Resolve("ovmf-code")
	vars := aset.Resolve("ovmf-vars")
	if code == "" || vars == "" {
		return "", ""
	}
	dst := filepath.Join(sess.workDir, "OVMF_VARS.fd")
	if err := qemu.CopyFile(vars, dst); err != nil {
		fmt.Fprintln(errw, "warning: copia OVMF_VARS fallita, uso SeaBIOS:", err)
		return "", ""
	}
	return code, dst
}

// kvmAvailable: /dev/kvm utilizzabile (per accel=auto/kvm).
func kvmAvailable() bool {
	res := accel.Detect()
	return res.Available && res.Mode == "kvm"
}

// cmdCommit: espelle l'overlay del disco corrente nel backing NBD (§5.8/§5.10).
func cmdCommit(ctx context.Context, args cli.Args, out, errw io.Writer) int {
	cfg, code := loadCfg(args, errw)
	if code != 0 {
		return code
	}
	aset, err := assets.Prepare(assets.Options{DevDir: os.Getenv("LAUNCH_NBD_ASSETS_DIR")})
	if err != nil {
		fmt.Fprintln(errw, "warning: preparazione asset fallita:", err)
		aset = &assets.Set{}
	}
	defer aset.Cleanup()

	img := aset.Resolve("qemu-img")
	if img == "" {
		img = cfg.QEMUImg
	}
	hash, err := nbd.Fingerprint(ctx, cfg.NBDHost, cfg.NBDPort, cfg.NBDExport)
	if err != nil {
		fmt.Fprintln(errw, "error:", err)
		return 1
	}
	dir, err := filepath.Abs(cfg.OverlayDir)
	if err != nil {
		fmt.Fprintln(errw, "error:", err)
		return 1
	}
	fmt.Fprintln(errw, "warning: nessuna VM deve usare l'overlay durante il commit")

	var state qemu.StateAccess
	if cfg.NBDStatePort > 0 {
		state = qemu.NBDStateAccess{Host: cfg.NBDHost, Port: cfg.NBDStatePort, Export: cfg.NBDExport}
	}
	err = qemu.Commit(ctx, qemu.CommitOptions{
		Dir:     dir,
		Hash:    hash,
		QEMUImg: img,
		State:   state,
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
		fmt.Fprintln(out, "commit: completato")
	}
	return 0
}
