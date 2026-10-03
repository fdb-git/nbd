package qemu

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fdb-git/nbd/launch-nbd/internal/nbd"
)

func TestOverlayPathAndFind(t *testing.T) {
	dir := t.TempDir()
	if got := OverlayPath(dir, "H", OverlayLive); got != filepath.Join(dir, "H.qcow2") {
		t.Errorf("live=%s", got)
	}
	if got := OverlayPath(dir, "H", OverlayCommitting); got != filepath.Join(dir, "H-committing.qcow2") {
		t.Errorf("committing=%s", got)
	}
	if got := OverlayPath(dir, "H", OverlayCommitted); got != filepath.Join(dir, "H-committed.qcow2") {
		t.Errorf("committed=%s", got)
	}

	if _, _, found := FindOverlay(dir, "H"); found {
		t.Error("nessun overlay atteso")
	}
	if err := os.WriteFile(OverlayPath(dir, "H", OverlayLive), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, st, found := FindOverlay(dir, "H")
	if !found || st != OverlayLive || p != OverlayPath(dir, "H", OverlayLive) {
		t.Errorf("find=%s %v %v", p, st, found)
	}
	// con un marker committing presente, ha priorità
	if err := os.WriteFile(OverlayPath(dir, "H", OverlayCommitting), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, st, _ = FindOverlay(dir, "H")
	if st != OverlayCommitting {
		t.Errorf("priorità committing non rispettata: %v", st)
	}
}

func TestGuardRunBlockedByCommitting(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(OverlayPath(dir, "H", OverlayCommitting), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := GuardRun(dir, "H")
	var be *BlockedError
	if !errors.As(err, &be) {
		t.Fatalf("atteso BlockedError, err=%v", err)
	}
	if !strings.Contains(err.Error(), "commit interrotto") {
		t.Errorf("messaggio: %v", err)
	}
	// senza marker: nessun blocco
	dir2 := t.TempDir()
	if _, _, _, err := GuardRun(dir2, "H"); err != nil {
		t.Errorf("run non deve essere bloccato: %v", err)
	}
}

func TestGuardCommitStates(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := GuardCommit(dir, "H"); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Errorf("atteso errore stale: %v", err)
	}
	if err := os.WriteFile(OverlayPath(dir, "H", OverlayCommitted), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := GuardCommit(dir, "H"); err == nil || !strings.Contains(err.Error(), "già committato") {
		t.Errorf("atteso errore 'già committato': %v", err)
	}
	if err := os.Remove(OverlayPath(dir, "H", OverlayCommitted)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(OverlayPath(dir, "H", OverlayLive), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, st, err := GuardCommit(dir, "H"); err != nil || st != OverlayLive {
		t.Errorf("atteso live senza errore: st=%v err=%v", st, err)
	}
}

// memState: StateAccess in memoria (verifica la sequenza di scritture).
type memState struct {
	writes  []nbd.Record
	readErr error
}

func (m *memState) Read(context.Context) (nbd.Record, error) {
	if m.readErr != nil {
		return nbd.Record{}, m.readErr
	}
	if len(m.writes) == 0 {
		return nbd.Record{State: nbd.StateClean}, nil
	}
	return m.writes[len(m.writes)-1], nil
}
func (m *memState) Write(_ context.Context, r nbd.Record) error {
	m.writes = append(m.writes, r)
	return nil
}

func TestCommitLifecycle(t *testing.T) {
	dir := t.TempDir()
	live := OverlayPath(dir, "H", OverlayLive)
	if err := os.WriteFile(live, []byte("overlay"), 0o644); err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	runImg := func(_ context.Context, args ...string) error {
		calls = append(calls, args)
		return nil
	}
	st := &memState{}
	err := Commit(context.Background(), CommitOptions{
		Dir: dir, Hash: "H", RunImg: runImg, State: st,
		OwnerHost: "host-test", OwnerUUID: "uuid-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0][0] != "commit" {
		t.Errorf("qemu-img calls=%v", calls)
	}
	if _, err := os.Stat(OverlayPath(dir, "H", OverlayCommitted)); err != nil {
		t.Errorf("overlay non rinominato in -committed: %v", err)
	}
	if _, err := os.Stat(live); !os.IsNotExist(err) {
		t.Error("overlay live deve essere sparito")
	}
	if len(st.writes) != 2 || st.writes[0].State != nbd.StateCommitting || st.writes[1].State != nbd.StateClean {
		t.Fatalf("sequenza stato=%+v", st.writes)
	}
	if st.writes[0].Hash != "H" || st.writes[0].OwnerHost != "host-test" {
		t.Errorf("record committing errato: %+v", st.writes[0])
	}
}

func TestCommitResumeInterrupted(t *testing.T) {
	dir := t.TempDir()
	committing := OverlayPath(dir, "H", OverlayCommitting)
	if err := os.WriteFile(committing, []byte("overlay"), 0o644); err != nil {
		t.Fatal(err)
	}
	var committed bool
	runImg := func(_ context.Context, args ...string) error { committed = true; return nil }
	st := &memState{}
	if err := Commit(context.Background(), CommitOptions{Dir: dir, Hash: "H", RunImg: runImg, State: st}); err != nil {
		t.Fatal(err)
	}
	if !committed {
		t.Error("qemu-img non invocato nella ripresa")
	}
	if _, err := os.Stat(OverlayPath(dir, "H", OverlayCommitted)); err != nil {
		t.Errorf("atteso -committed: %v", err)
	}
}

func TestCommitFailureLeavesMarker(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(OverlayPath(dir, "H", OverlayLive), []byte("overlay"), 0o644); err != nil {
		t.Fatal(err)
	}
	runImg := func(_ context.Context, _ ...string) error { return errors.New("qemu-img interrotto") }
	st := &memState{}
	err := Commit(context.Background(), CommitOptions{Dir: dir, Hash: "H", RunImg: runImg, State: st})
	if err == nil {
		t.Fatal("atteso errore")
	}
	if _, err := os.Stat(OverlayPath(dir, "H", OverlayCommitting)); err != nil {
		t.Errorf("il marker -committing deve restare: %v", err)
	}
	if _, err := os.Stat(OverlayPath(dir, "H", OverlayCommitted)); !os.IsNotExist(err) {
		t.Error("non deve esistere -committed")
	}
	if len(st.writes) != 1 || st.writes[0].State != nbd.StateCommitting {
		t.Errorf("stato deve restare committing: %+v", st.writes)
	}
	// e run è bloccato dal marker
	if _, _, _, gerr := GuardRun(dir, "H"); gerr == nil {
		t.Error("run deve essere bloccato dopo un commit interrotto")
	}
}

func TestCommitDeleteAfter(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(OverlayPath(dir, "H", OverlayLive), []byte("o"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := Commit(context.Background(), CommitOptions{
		Dir: dir, Hash: "H", DeleteAfter: true,
		RunImg: func(context.Context, ...string) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(OverlayPath(dir, "H", OverlayCommitted)); !os.IsNotExist(err) {
		t.Error("con DeleteAfter non deve restare -committed")
	}
}

func TestCommitNilStateIsOK(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(OverlayPath(dir, "H", OverlayLive), []byte("o"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Commit(context.Background(), CommitOptions{
		Dir: dir, Hash: "H", State: nil,
		RunImg: func(context.Context, ...string) error { return nil },
	}); err != nil {
		t.Fatal(err)
	}
}

func TestImgArgs(t *testing.T) {
	got := strings.Join(ImgCreateArgs("nbd://h:1/x", "/o/H.qcow2"), " ")
	want := "create -f qcow2 -b nbd://h:1/x -F raw /o/H.qcow2"
	if got != want {
		t.Errorf("ImgCreateArgs=%q", got)
	}
	if strings.Join(ImgCommitArgs("/o/H.qcow2"), " ") != "commit /o/H.qcow2" {
		t.Errorf("ImgCommitArgs=%v", ImgCommitArgs("/o/H.qcow2"))
	}
}

func TestSessionUUIDShape(t *testing.T) {
	u := SessionUUID()
	if len(u) != 36 || u[14] != '4' {
		t.Errorf("UUID=%q (atteso v4 a 36 char)", u)
	}
	if u == SessionUUID() {
		t.Error("UUID non casuale")
	}
}
