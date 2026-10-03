//go:build windows

package net

import (
	"context"
	"strings"
	"testing"
)

func TestCidrMask(t *testing.T) {
	for in, want := range map[string]string{
		"24": "255.255.255.0", "16": "255.255.0.0", "8": "255.0.0.0", "32": "255.255.255.255",
	} {
		if got := cidrMask(in); got != want {
			t.Errorf("cidrMask(%q)=%q, atteso %q", in, got, want)
		}
	}
	if cidrMask("bogus") != "255.255.255.0" {
		t.Error("mask di fallback attesa")
	}
}

func TestSetupTapWindows(t *testing.T) {
	f := newFake()
	f.root = false
	cfg := Config{
		Dev:    "tap0",
		Subnet: "192.168.100.0/24",
		Windows: WindowsConfig{
			TapCtl: `C:\assets\tapctl.exe`,
			HWID:   "tap0901",
		},
	}
	s, err := SetupTap(context.Background(), f, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`C:\assets\tapctl.exe create --name tap0`,
		"netsh interface ip set address name=tap0 static 192.168.100.1 255.255.255.0",
		"netsh interface set interface name=tap0 admin=enable",
	} {
		if !f.called(want) {
			t.Errorf("comando mancante: %s\ncalls=%v", want, f.calls)
		}
	}
	// revert: adapter eliminato + address tornato dhcp
	f.calls = nil
	s.Revert()
	if !f.called(`C:\assets\tapctl.exe delete --name tap0`) {
		t.Errorf("revert: adapter non eliminato: %v", f.calls)
	}
	if !f.called("netsh interface ip set address name=tap0 dhcp") {
		t.Errorf("revert: address non ripristinato: %v", f.calls)
	}
}

func TestSetupTapWindowsNoTapctl(t *testing.T) {
	f := newFake()
	_, err := SetupTap(context.Background(), f, Config{Dev: "tap0", Subnet: "192.168.100.0/24"})
	if err == nil || !strings.Contains(err.Error(), "tapctl.exe") {
		t.Fatalf("atteso errore su tapctl mancante: %v", err)
	}
}

func TestSetupTapWindowsAddressFailsReverts(t *testing.T) {
	f := newFake()
	f.missing["netsh interface ip set address name=tap0 static 192.168.100.1 255.255.255.0"] = true
	cfg := Config{Dev: "tap0", Subnet: "192.168.100.0/24", Windows: WindowsConfig{TapCtl: "tapctl.exe"}}
	if _, err := SetupTap(context.Background(), f, cfg); err == nil {
		t.Fatal("atteso errore su netsh set address")
	}
	if !f.called("tapctl.exe delete --name tap0") {
		t.Errorf("l'adapter creato deve essere rimosso sul fallimento: %v", f.calls)
	}
}

func TestSetupBridgeWindowsUnsupported(t *testing.T) {
	f := newFake()
	if _, err := SetupBridge(context.Background(), f, Config{}, "br0"); err == nil ||
		!strings.Contains(err.Error(), "non supportato su Windows") {
		t.Fatalf("atteso errore bridged non supportato: %v", err)
	}
}

func TestSupportsTapWindows(t *testing.T) {
	if !SupportsTap {
		t.Error("SupportsTap deve essere true su Windows (M6)")
	}
}
