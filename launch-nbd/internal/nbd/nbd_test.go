package nbd

import (
	"context"
	"testing"

	"github.com/fdb-git/nbd/launch-nbd/internal/nbd/nbdtest"
)

func TestHandshakeReadWriteFlush(t *testing.T) {
	backing := make([]byte, 256*1024)
	for i := range backing {
		backing[i] = byte(i % 251)
	}
	srv, err := nbdtest.New(map[string][]byte{"disk": backing})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	host, port := srv.Addr()

	ctx := context.Background()
	c, err := Dial(ctx, host, port, "disk")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Size() != uint64(len(backing)) {
		t.Fatalf("size=%d, attesa %d", c.Size(), len(backing))
	}
	got := make([]byte, 16)
	if err := c.ReadAt(got, 100); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 16; i++ {
		if got[i] != backing[100+i] {
			t.Fatalf("read mismatch a %d: %d != %d", i, got[i], backing[100+i])
		}
	}
	payload := []byte("CIAO-NBD")
	if err := c.WriteAt(payload, 4096); err != nil {
		t.Fatal(err)
	}
	if err := c.Flush(); err != nil {
		t.Fatal(err)
	}
	if string(backing[4096:4096+len(payload)]) != string(payload) {
		t.Fatalf("write non applicata: %q", backing[4096:4096+len(payload)])
	}
	if srv.FlushCount("disk") != 1 {
		t.Fatalf("flush count=%d, atteso 1", srv.FlushCount("disk"))
	}
}

func TestDialUnknownExportSizeZero(t *testing.T) {
	srv, err := nbdtest.New(map[string][]byte{})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	host, port := srv.Addr()
	c, err := Dial(context.Background(), host, port, "nope")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Size() != 0 {
		t.Fatalf("size=%d, attesa 0", c.Size())
	}
}

func TestFingerprintStableAndSensitive(t *testing.T) {
	backing := make([]byte, 256*1024)
	srv, err := nbdtest.New(map[string][]byte{"disk": backing})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	host, port := srv.Addr()
	ctx := context.Background()

	fp1, err := Fingerprint(ctx, host, port, "disk")
	if err != nil {
		t.Fatal(err)
	}
	fp2, err := Fingerprint(ctx, host, port, "disk")
	if err != nil {
		t.Fatal(err)
	}
	if fp1 != fp2 {
		t.Fatalf("fingerprint non stabile: %s != %s", fp1, fp2)
	}
	if len(fp1) != 43 { // SHA256 base64url senza padding
		t.Errorf("lunghezza fingerprint=%d, attesa 43", len(fp1))
	}

	backing[100] ^= 0xff // testa (GPT)
	fp3, err := Fingerprint(ctx, host, port, "disk")
	if err != nil {
		t.Fatal(err)
	}
	if fp3 == fp1 {
		t.Error("la modifica della testa non ha cambiato il fingerprint")
	}

	backing[100] ^= 0xff
	backing[len(backing)-10] ^= 0xff // coda (backup GPT)
	fp4, err := Fingerprint(ctx, host, port, "disk")
	if err != nil {
		t.Fatal(err)
	}
	if fp4 == fp1 {
		t.Error("la modifica della coda non ha cambiato il fingerprint")
	}
}

func TestFingerprintTooSmall(t *testing.T) {
	srv, err := nbdtest.New(map[string][]byte{"tiny": make([]byte, 64*1024)})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	host, port := srv.Addr()
	if _, err := Fingerprint(context.Background(), host, port, "tiny"); err == nil {
		t.Fatal("atteso errore per export troppo piccolo")
	}
}

func TestEncodeDecodeRecord(t *testing.T) {
	r := Record{
		State:     StateCommitting,
		OwnerHost: "host-a",
		OwnerUUID: "12345678-1234-1234-1234-123456789abc",
		Hash:      "AqJUa4Xw0n",
		CTime:     1712345678,
	}
	b := EncodeRecord(r)
	if len(b) != StateBlockSize {
		t.Fatalf("len=%d, attesa %d", len(b), StateBlockSize)
	}
	if string(b[0:5]) != "NBDST" || b[5] != 0 || b[6] != 0 || b[7] != 0 {
		t.Errorf("magic errato: % x", b[0:8])
	}
	if b[8] != stateVersion || b[9] != byte(StateCommitting) {
		t.Errorf("version=%d state=%d", b[8], b[9])
	}
	for i := 380; i < StateBlockSize; i++ {
		if b[i] != 0 {
			t.Fatalf("padding non zero a %d", i)
		}
	}
	got, err := DecodeRecord(b)
	if err != nil {
		t.Fatal(err)
	}
	if got != r {
		t.Fatalf("round-trip: %+v != %+v", got, r)
	}
}

func TestDecodeRejectsBadMagic(t *testing.T) {
	b := EncodeRecord(Record{State: StateClean})
	b[0] = 'X'
	if _, err := DecodeRecord(b); err == nil {
		t.Fatal("atteso errore su magic errato")
	}
	if _, err := DecodeRecord(make([]byte, 10)); err == nil {
		t.Fatal("atteso errore su record corto")
	}
}

func TestStateOverWire(t *testing.T) {
	status := make([]byte, StateBlockSize)
	srv, err := nbdtest.New(map[string][]byte{"demo.status": status})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	host, port := srv.Addr()
	ctx := context.Background()

	want := Record{State: StateCommitting, OwnerHost: "h1", OwnerUUID: "uuid-1",
		Hash: "hashXYZ", CTime: 42}
	if err := WriteState(ctx, host, port, "demo", want); err != nil {
		t.Fatal(err)
	}
	if srv.FlushCount("demo.status") != 1 {
		t.Fatalf("flush dopo scrittura stato=%d, atteso 1", srv.FlushCount("demo.status"))
	}
	got, err := ReadState(ctx, host, port, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("stato over-the-wire: %+v != %+v", got, want)
	}
	if StatusExportName("demo") != "demo.status" {
		t.Errorf("StatusExportName=%q", StatusExportName("demo"))
	}
}

func TestParseState(t *testing.T) {
	for s, want := range map[string]State{
		"clean": StateClean, "committing": StateCommitting, "committed": StateCommitted,
	} {
		got, err := ParseState(s)
		if err != nil || got != want {
			t.Errorf("ParseState(%q)=%v,%v", s, got, err)
		}
	}
	if _, err := ParseState("bogus"); err == nil {
		t.Error("atteso errore")
	}
	if StateCommitting.String() != "committing" {
		t.Errorf("String()=%q", StateCommitting.String())
	}
}
