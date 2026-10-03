package net

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// fakeSys: System finto che registra i comandi (test cross-platform).
type fakeSys struct {
	root     bool
	tunOK    bool
	tunMode  string
	missing  map[string]bool // comandi che devono fallire (es. "ip link show tap0")
	outputs  map[string]string
	calls    []string
	hasCmd   map[string]bool
	username string
	pid      int
}

func newFake() *fakeSys {
	return &fakeSys{
		tunOK: true, tunMode: "600",
		missing: map[string]bool{}, outputs: map[string]string{},
		hasCmd:   map[string]bool{"sudo": true, "ip": true, "iptables": true, "sysctl": true},
		username: "tester", pid: 4242,
	}
}

func (f *fakeSys) key(name string, args ...string) string {
	return strings.Join(append([]string{name}, args...), " ")
}

func (f *fakeSys) Run(_ context.Context, name string, args ...string) error {
	k := f.key(name, args...)
	f.calls = append(f.calls, k)
	if f.missing[k] {
		return fmt.Errorf("comando fallito: %s", k)
	}
	return nil
}

func (f *fakeSys) Output(_ context.Context, name string, args ...string) (string, error) {
	k := f.key(name, args...)
	f.calls = append(f.calls, k)
	if f.missing[k] {
		return "", fmt.Errorf("comando fallito: %s", k)
	}
	return f.outputs[k], nil
}

func (f *fakeSys) HasCommand(name string) bool { return f.hasCmd[name] }
func (f *fakeSys) TunAccess() (bool, string)   { return f.tunOK, f.tunMode }
func (f *fakeSys) PID() int                    { return f.pid }
func (f *fakeSys) Username() string            { return f.username }
func (f *fakeSys) IsRoot() bool                { return f.root }

func (f *fakeSys) called(substr string) bool {
	for _, c := range f.calls {
		if strings.Contains(c, substr) {
			return true
		}
	}
	return false
}

func TestParseSubnet(t *testing.T) {
	mask, host, guest, err := parseSubnet("192.168.100.0/24")
	if err != nil || mask != "24" || host != "192.168.100.1" || guest != "192.168.100.2" {
		t.Fatalf("parseSubnet=%q %q %q err=%v", mask, host, guest, err)
	}
	if _, _, _, err := parseSubnet("non-una-rete"); err == nil {
		t.Error("atteso errore su subnet invalida")
	}
}

func TestSetupTapRootFullFlow(t *testing.T) {
	f := newFake()
	f.root = true
	f.tunOK = false
	f.tunMode = "600"
	f.missing["ip link show tap0"] = true // il tap non esiste -> va creato
	f.missing["iptables -t nat -C POSTROUTING -s 192.168.100.0/24 ! -o tap0 -j MASQUERADE"] = true
	f.outputs["sysctl -n net.ipv4.ip_forward"] = "1\n"

	s, err := setupTapIPTools(context.Background(), f, Config{Dev: "tap0", Subnet: "192.168.100.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"chmod 666 /dev/net/tun",
		"ip tuntap add dev tap0 mode tap user tester",
		"ip addr add 192.168.100.1/24 dev tap0",
		"ip link set tap0 up",
		"sysctl -w net.ipv4.ip_forward=1",
		"iptables -t nat -A POSTROUTING -s 192.168.100.0/24 ! -o tap0 -j MASQUERADE",
	} {
		if !f.called(want) {
			t.Errorf("comando mancante: %s\ncalls=%v", want, f.calls)
		}
	}
	if f.called("sudo") {
		t.Error("da root non si deve usare sudo")
	}

	// revert: deve ripristinare in ordine inverso e non toccare altro
	f.calls = nil
	s.Revert()
	for _, want := range []string{
		"iptables -t nat -D POSTROUTING",
		"sysctl -w net.ipv4.ip_forward=1", // ripristino al valore originale "1"
		"ip tuntap del dev tap0",
		"chmod 600 /dev/net/tun",
	} {
		if !f.called(want) {
			t.Errorf("revert mancante: %s\ncalls=%v", want, f.calls)
		}
	}
	// idempotenza: un secondo Revert non esegue nulla
	f.calls = nil
	s.Revert()
	if len(f.calls) != 0 {
		t.Errorf("Revert non idempotente: %v", f.calls)
	}
}

func TestSetupTapNonRootUsesSudo(t *testing.T) {
	f := newFake()
	f.root = false
	f.missing["ip link show tap0"] = true
	f.missing["sudo iptables -t nat -C POSTROUTING -s 192.168.100.0/24 ! -o tap0 -j MASQUERADE"] = true
	if _, err := setupTapIPTools(context.Background(), f, Config{Dev: "tap0", Subnet: "192.168.100.0/24"}); err != nil {
		t.Fatal(err)
	}
	if !f.called("sudo ip tuntap add dev tap0") {
		t.Errorf("atteso sudo: %v", f.calls)
	}
	if !f.called("sudo iptables -t nat -A POSTROUTING") {
		t.Errorf("atteso sudo su iptables: %v", f.calls)
	}
}

func TestSetupTapNoSudoFails(t *testing.T) {
	f := newFake()
	f.root = false
	f.hasCmd["sudo"] = false
	if _, err := setupTapIPTools(context.Background(), f, Config{Dev: "tap0", Subnet: "192.168.100.0/24"}); err == nil {
		t.Fatal("atteso errore senza sudo")
	}
}

// TestSetupTapIdempotent: tap già esistente, tun rw, MASQUERADE già presente:
// nessuna creazione/aggiunta, solo addr/up.
func TestSetupTapIdempotent(t *testing.T) {
	f := newFake()
	f.root = true
	f.tunOK = true
	f.outputs["ip addr show dev tap0"] = "inet 192.168.100.1/24 scope global tap0\n"
	f.outputs["sysctl -n net.ipv4.ip_forward"] = "0\n"
	// "ip link show tap0" ok (tap esistente), "-C MASQUERADE" ok (già presente)
	s, err := setupTapIPTools(context.Background(), f, Config{Dev: "tap0", Subnet: "192.168.100.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	if f.called("chmod 666") {
		t.Error("tun già rw: nessun chmod atteso")
	}
	if f.called("tuntap add") {
		t.Error("tap esistente: nessuna creazione attesa")
	}
	if f.called("ip addr add") {
		t.Error("indirizzo già presente: nessun add atteso")
	}
	if f.called("iptables -t nat -A") {
		t.Error("MASQUERADE già presente: nessun -A atteso")
	}
	if !f.called("ip link set tap0 up") {
		t.Error("atteso ip link set up")
	}
	// revert: ip_forward ripristinato a 0, niente delete del tap (non creato)
	f.calls = nil
	s.Revert()
	if !f.called("sysctl -w net.ipv4.ip_forward=0") {
		t.Errorf("atteso ripristino ip_forward: %v", f.calls)
	}
	if f.called("tuntap del") {
		t.Error("il tap non era stato creato: nessun del atteso")
	}
}

func TestSetupTapInvalidSubnet(t *testing.T) {
	f := newFake()
	if _, err := setupTapIPTools(context.Background(), f, Config{Dev: "tap0", Subnet: "bogus"}); err == nil {
		t.Fatal("atteso errore su subnet invalida")
	}
}

func TestSetupBridge(t *testing.T) {
	f := newFake()
	f.root = true
	s, err := setupBridgeIPTools(context.Background(), f, Config{Dev: "ignored"}, "br0")
	if err != nil {
		t.Fatal(err)
	}
	wantTap := "qemu-tap-4242"
	if s.TapDev != wantTap {
		t.Errorf("TapDev=%q, atteso %q", s.TapDev, wantTap)
	}
	for _, want := range []string{
		"ip tuntap add dev " + wantTap + " mode tap user tester",
		"ip link set " + wantTap + " up",
		"ip link set " + wantTap + " master br0",
		"ip link set br0 up",
	} {
		if !f.called(want) {
			t.Errorf("comando mancante: %s\ncalls=%v", want, f.calls)
		}
	}
	f.calls = nil
	s.Revert()
	if !f.called("ip link set "+wantTap+" nomaster") || !f.called("ip tuntap del dev "+wantTap) {
		t.Errorf("revert bridged incompleto: %v", f.calls)
	}
}

func TestSetupBridgeNonRootFails(t *testing.T) {
	f := newFake()
	f.root = false
	if _, err := setupBridgeIPTools(context.Background(), f, Config{}, "br0"); err == nil {
		t.Fatal("atteso errore senza root")
	}
}

func TestSetupBridgeMasterFailsReverts(t *testing.T) {
	f := newFake()
	f.root = true
	f.missing["ip link set qemu-tap-4242 master br0"] = true
	if _, err := setupBridgeIPTools(context.Background(), f, Config{}, "br0"); err == nil {
		t.Fatal("atteso errore se il bridge non esiste")
	}
	if !f.called("ip tuntap del dev qemu-tap-4242") {
		t.Errorf("il tap deve essere rimosso sul fallimento: %v", f.calls)
	}
}
