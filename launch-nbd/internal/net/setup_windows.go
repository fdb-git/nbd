//go:build windows

package net

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// SetupTap: implementazione Windows (TAP-Windows6 via tapctl.exe + netsh).
//
// Best-effort: richiede privilegi admin e gli asset del driver (tapctl.exe).
// Il NAT (RRAS "netsh routing ip nat") non è implementato: viene emesso un
// warning e il guest usa la rete user-mode per internet (modo dual/nat).
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
	if cfg.Windows.TapCtl == "" {
		return nil, fmt.Errorf("net: tapctl.exe non disponibile: popola gli asset TAP-Windows6 (Fase B M6) o usa net_mode=hostonly/nat")
	}
	s := &Session{log: logf, warn: warnf}
	run := func(name string, args ...string) error { return sys.Run(ctx, name, args...) }

	created := false
	// riuso: se un adapter con quel nome esiste, non ricrearlo
	if err := run(cfg.Windows.TapCtl, "list"); err != nil {
		// lista non disponibile: procediamo comunque con il tentativo di create
		warnf("[net] tapctl list non riuscito: provo comunque la creazione")
	}
	if err := run(cfg.Windows.TapCtl, "create", "--name", cfg.Dev); err != nil {
		warnf("[net] tapctl create: " + err.Error() + " (adapter già esistente? lo riuso)")
	} else {
		created = true
		s.addRevert(func() { _ = run(cfg.Windows.TapCtl, "delete", "--name", cfg.Dev) })
		logf("[net] creato adapter TAP-Windows6 " + cfg.Dev)
	}

	// indirizzo statico + subnet mask
	if err := run("netsh", "interface", "ip", "set", "address",
		"name="+cfg.Dev, "static", hostIP, cidrMask(mask)); err != nil {
		if created {
			s.Revert()
		}
		return nil, fmt.Errorf("net: netsh set address su %s fallito (admin richiesto): %w", cfg.Dev, err)
	}
	s.addRevert(func() {
		_ = run("netsh", "interface", "ip", "set", "address", "name="+cfg.Dev, "dhcp")
	})

	// interfaccia up
	if err := run("netsh", "interface", "set", "interface", "name="+cfg.Dev, "admin=enable"); err != nil {
		warnf("[net] impossibile abilitare " + cfg.Dev + ": " + err.Error())
	}

	// NAT: non implementato (richiede RRAS) — warning esplicito
	warnf("[net] NAT Windows non configurato: per internet nel guest usa net_mode=dual/nat (rete user-mode)")

	logf(fmt.Sprintf("[net] TAP-Windows6: host %s/%s <-> guest %s/%s", hostIP, mask, guestIP, mask))
	return s, nil
}

// cidrMask: "24" -> "255.255.255.0".
func cidrMask(ones string) string {
	n, err := strconv.Atoi(ones)
	if err != nil || n < 0 || n > 32 {
		return "255.255.255.0"
	}
	m := net.CIDRMask(n, 32)
	return fmt.Sprintf("%d.%d.%d.%d", m[0], m[1], m[2], m[3])
}

// SetupBridge: bridged non supportato su Windows (TAP-Windows6 è L3/L2 ma il
// bridge nativo non è gestito): errore chiaro.
func SetupBridge(_ context.Context, _ System, _ Config, bridgeIF string) (*Session, error) {
	return nil, fmt.Errorf("net: net_mode=bridged non supportato su Windows (usa hostonly/nat); bridge richiesto: %s", strings.TrimSpace(bridgeIF))
}
