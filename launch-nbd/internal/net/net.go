// Package net: setup/teardown della rete host per QEMU.
//
// M5 implementa il ramo Linux (TAP privato per tap/dual, TAP su bridge per
// bridged) con revert deterministico; il layer Windows (TAP-Windows6/Wintun)
// è M6. La logica è nel file comune e usa un System iniettato: i test con un
// System finto girano su qualunque host (Windows incluso), mentre le primitive
// reali sono nei file build-tagged sys_linux.go / sys_windows.go.
//
// Traduzione di plan-go.md §5.5/§5.7 e della sezione networking di launch-nbd.sh:
// ogni passo è idempotente (controlla prima di agire) e registra il proprio
// revert; Revert esegue i revert in ordine inverso ed è sempre invocato.
package net

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
)

// System: primitive di sistema (iniettabile per i test).
type System interface {
	// Run: esegue un comando, errore se exit != 0.
	Run(ctx context.Context, name string, args ...string) error
	// Output: esegue un comando e restituisce lo stdout.
	Output(ctx context.Context, name string, args ...string) (string, error)
	// HasCommand: il comando esiste nel PATH?
	HasCommand(name string) bool
	// TunAccess: /dev/net/tun è rw? se no, mode = permessi attuali ("600").
	TunAccess() (readWrite bool, mode string)
	// PID: pid del processo (per nomi univoci dei tap).
	PID() int
	// Username: nome utente corrente (owner del tap).
	Username() string
	// IsRoot: privilegi di root/admin.
	IsRoot() bool
}

// Config: parametri del setup TAP.
type Config struct {
	Dev    string // interfaccia tap (tap0)
	Subnet string // rete privata host<->guest (192.168.100.0/24)
	Log    func(string)
	Warn   func(string)
}

// Session: risorse di rete create; Revert le disfa (idempotente).
type Session struct {
	revert []func()
	log    func(string)
	warn   func(string)
	once   sync.Once

	// TapDev: nome del tap creato (valorizzato da SetupBridge).
	TapDev string
}

func (s *Session) addRevert(f func()) { s.revert = append(s.revert, f) }

// Revert: esegue i revert in ordine inverso (una sola volta).
func (s *Session) Revert() {
	s.once.Do(func() {
		for i := len(s.revert) - 1; i >= 0; i-- {
			s.revert[i]()
		}
	})
}

// parseSubnet: "192.168.100.0/24" -> (mask "24", hostIP ".1", guestIP ".2").
func parseSubnet(cidr string) (mask, hostIP, guestIP string, err error) {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", "", "", fmt.Errorf("tap_subnet non valida '%s': %w", cidr, err)
	}
	ones, _ := ipnet.Mask.Size()
	v4 := ipnet.IP.To4()
	if v4 == nil {
		return "", "", "", fmt.Errorf("tap_subnet non IPv4: '%s'", cidr)
	}
	host := fmt.Sprintf("%d.%d.%d.1", v4[0], v4[1], v4[2])
	guest := fmt.Sprintf("%d.%d.%d.2", v4[0], v4[1], v4[2])
	return fmt.Sprintf("%d", ones), host, guest, nil
}

// tun access helper: true se /dev/net/tun va sbloccato.
func needsTunFix(sys System) (bool, string) {
	ok, mode := sys.TunAccess()
	if ok {
		return false, ""
	}
	if mode == "" {
		mode = "600"
	}
	return true, mode
}

// SetupTap: rete privata host<->guest su un TAP (modalità tap/dual).
// Crea il TAP se mancante, assegna <subnet>.1, lo alza e abilita
// ip_forward + MASQUERADE. Tutto revertibile con Session.Revert.
func SetupTap(ctx context.Context, sys System, cfg Config) (*Session, error) {
	logf := cfg.Log
	if logf == nil {
		logf = func(string) {}
	}
	warnf := cfg.Warn
	if warnf == nil {
		warnf = func(string) {}
	}

	mask, hostIP, guestIP, err := parseSubnet(cfg.Subnet)
	if err != nil {
		return nil, err
	}
	s := &Session{log: logf, warn: warnf}

	sudo := ""
	if !sys.IsRoot() {
		if !sys.HasCommand("sudo") {
			return nil, fmt.Errorf("net: la rete TAP richiede privilegi root (sudo non trovato)")
		}
		sudo = "sudo"
	}
	run := func(name string, args ...string) error {
		if sudo == "" {
			return sys.Run(ctx, name, args...)
		}
		full := append([]string{name}, args...)
		return sys.Run(ctx, sudo, full...)
	}

	// a) primo uso: concedi rw a /dev/net/tun (salvando i permessi)
	if fix, orig := needsTunFix(sys); fix {
		s.addRevert(func() { _ = run("chmod", orig, "/dev/net/tun") })
		_ = run("chmod", "666", "/dev/net/tun")
		logf("[net] /dev/net/tun: accesso rw concesso (era " + orig + ")")
	}

	// b) crea il TAP se mancante (owner = utente corrente)
	if err := sys.Run(ctx, "ip", "link", "show", cfg.Dev); err != nil {
		if err := run("ip", "tuntap", "add", "dev", cfg.Dev, "mode", "tap", "user", sys.Username()); err != nil {
			return nil, fmt.Errorf("net: creazione tap %s fallita (root/sudo richiesto): %w", cfg.Dev, err)
		}
		s.addRevert(func() {
			_ = run("ip", "link", "set", cfg.Dev, "down")
			_ = run("ip", "tuntap", "del", "dev", cfg.Dev)
		})
		logf("[net] creato TAP " + cfg.Dev + " (owner " + sys.Username() + ")")
	} else {
		logf("[net] TAP " + cfg.Dev + " già esistente (riuso)")
	}

	// c) indirizzo privato
	if out, _ := sys.Output(ctx, "ip", "addr", "show", "dev", cfg.Dev); !strings.Contains(out, " "+hostIP+"/") {
		_ = run("ip", "addr", "add", hostIP+"/"+mask, "dev", cfg.Dev)
	}

	// d) up
	if err := run("ip", "link", "set", cfg.Dev, "up"); err != nil {
		warnf("[net] impossibile alzare " + cfg.Dev + ": " + err.Error())
	}

	// e) internet via host: ip_forward + MASQUERADE
	origFwd, _ := sys.Output(ctx, "sysctl", "-n", "net.ipv4.ip_forward")
	origFwd = strings.TrimSpace(origFwd)
	_ = run("sysctl", "-w", "net.ipv4.ip_forward=1")
	if origFwd != "" {
		s.addRevert(func() { _ = run("sysctl", "-w", "net.ipv4.ip_forward="+origFwd) })
	}
	masq := []string{"-t", "nat", "-A", "POSTROUTING", "-s", cfg.Subnet, "!", "-o", cfg.Dev, "-j", "MASQUERADE"}
	check := []string{"-t", "nat", "-C", "POSTROUTING", "-s", cfg.Subnet, "!", "-o", cfg.Dev, "-j", "MASQUERADE"}
	if err := run("iptables", check...); err != nil {
		if err := run("iptables", masq...); err == nil {
			s.addRevert(func() {
				_ = run("iptables", "-t", "nat", "-D", "POSTROUTING", "-s", cfg.Subnet, "!", "-o", cfg.Dev, "-j", "MASQUERADE")
			})
		} else {
			warnf("[net] MASQUERADE fallito: il guest non avrà internet via TAP")
		}
	}
	logf(fmt.Sprintf("[net] rete privata: host %s/%s <-> guest %s/%s", hostIP, mask, guestIP, mask))
	return s, nil
}

// SetupBridge: crea un TAP e lo aggancia a un bridge (modalità bridged, root).
// Ritorna la sessione; il nome del tap creato è in TapDev.
func SetupBridge(ctx context.Context, sys System, cfg Config, bridgeIF string) (*Session, error) {
	logf := cfg.Log
	if logf == nil {
		logf = func(string) {}
	}
	s := &Session{log: logf}
	if !sys.IsRoot() {
		return nil, fmt.Errorf("net: bridged richiede root (oppure usa qemu-bridge-helper)")
	}
	tap := fmt.Sprintf("qemu-tap-%d", sys.PID())
	run := func(name string, args ...string) error { return sys.Run(ctx, name, args...) }

	if err := run("ip", "tuntap", "add", "dev", tap, "mode", "tap", "user", sys.Username()); err != nil {
		return nil, fmt.Errorf("net: creazione tap %s fallita (CAP_NET_ADMIN richiesto): %w", tap, err)
	}
	s.addRevert(func() {
		_ = run("ip", "link", "set", tap, "nomaster")
		_ = run("ip", "tuntap", "del", "dev", tap)
	})
	if err := run("ip", "link", "set", tap, "up"); err != nil {
		s.Revert()
		return nil, err
	}
	if err := run("ip", "link", "set", tap, "master", bridgeIF); err != nil {
		s.Revert()
		return nil, fmt.Errorf("net: bridge '%s' non trovato (crealo: ip link add %s type bridge)", bridgeIF, bridgeIF)
	}
	if err := run("ip", "link", "set", bridgeIF, "up"); err != nil {
		s.Revert()
		return nil, err
	}
	s.TapDev = tap
	logf("[net] bridged: tap " + tap + " sul bridge " + bridgeIF)
	return s, nil
}
