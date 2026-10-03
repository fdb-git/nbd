package iso

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const isoBody = "questo e' un finto ISO di test\n0123456789abcdef\n"

func sha256hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func TestIsURL(t *testing.T) {
	for s, want := range map[string]bool{
		"http://x/y.iso": true, "https://x/y.iso": true,
		"/tmp/y.iso": false, "C:\\y.iso": false, "ftp://x": false,
	} {
		if got := IsURL(s); got != want {
			t.Errorf("IsURL(%q)=%v, atteso %v", s, got, want)
		}
	}
}

func TestParseChecksum(t *testing.T) {
	sha := sha256hex([]byte("x"))
	cases := []struct {
		name, content, base, want string
	}{
		{"SHA256SUMS tipico", sha + "  ubuntu.iso\n", "ubuntu.iso", sha},
		{"marker binario", sha + " *ubuntu.iso\n", "ubuntu.iso", sha},
		{"altra riga prima", "aaaa  altro.iso\n" + sha + "  ubuntu.iso\n", "ubuntu.iso", sha},
		{"case-insensitive", strings.ToUpper(sha) + "  Ubuntu.ISO\n", "ubuntu.iso", sha},
		{"non corrisponde", sha + "  altro.iso\n", "ubuntu.iso", ""},
		{"token singolo senza base", sha + "\n", "", sha},
		{"commento ignorato", "# nota\n" + sha + "  u.iso\n", "u.iso", sha},
		{"non-sha", "deadbeef  u.iso\n", "u.iso", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseChecksum(c.content, c.base); got != c.want {
				t.Errorf("parseChecksum=%q, atteso %q", got, c.want)
			}
		})
	}
}

// serverISO: httptest che serve /ubuntu.iso (con Range via ServeContent) e
// /SHA256SUMS.
func serverISO(t *testing.T, body []byte, sums string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/ubuntu.iso", func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "ubuntu.iso", time.Time{}, bytes.NewReader(body))
	})
	if sums != "" {
		mux.HandleFunc("/SHA256SUMS", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(sums))
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestDiscoverChecksumHTTP(t *testing.T) {
	sha := sha256hex([]byte(isoBody))
	srv := serverISO(t, []byte(isoBody), sha+"  ubuntu.iso\n")
	got, src := DiscoverChecksum(context.Background(), srv.Client(), srv.URL+"/ubuntu.iso", "")
	if got != sha {
		t.Errorf("sha=%q, atteso %q", got, sha)
	}
	if !strings.HasSuffix(src, "/SHA256SUMS") {
		t.Errorf("source=%q", src)
	}
}

func TestDiscoverChecksumLocal(t *testing.T) {
	dir := t.TempDir()
	sha := sha256hex([]byte("x"))
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(sha+"  ubuntu.iso\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, src := DiscoverChecksum(context.Background(), http.DefaultClient, "", dir)
	if got != sha || !strings.HasSuffix(src, "SHA256SUMS") {
		t.Errorf("got=%q src=%q", got, src)
	}
}

func TestDownloadResume(t *testing.T) {
	body := []byte(strings.Repeat("abcdefgh", 4096)) // 32 KiB
	srv := serverISO(t, body, "")
	dest := filepath.Join(t.TempDir(), "ubuntu.iso")
	// file parziale: i primi 10000 byte
	if err := os.WriteFile(dest, body[:10000], 0o644); err != nil {
		t.Fatal(err)
	}
	var ranges atomic.Int64
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "" {
			ranges.Add(1)
		}
		http.ServeContent(w, r, "ubuntu.iso", time.Time{}, bytes.NewReader(body))
	})
	if err := Download(context.Background(), srv.Client(), srv.URL+"/ubuntu.iso", dest, nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("contenuto diverso: %d byte (attesi %d)", len(got), len(body))
	}
	if ranges.Load() == 0 {
		t.Error("il client non ha inviato Range (resume)")
	}
}

func TestDownloadRetryOn5xx(t *testing.T) {
	body := []byte("payload")
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 2 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		http.ServeContent(w, r, "ubuntu.iso", time.Time{}, bytes.NewReader(body))
	}))
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "ubuntu.iso")
	if err := Download(context.Background(), srv.Client(), srv.URL+"/ubuntu.iso", dest, nil); err != nil {
		t.Fatalf("download con retry fallito: %v", err)
	}
	got, _ := os.ReadFile(dest)
	if !bytes.Equal(got, body) {
		t.Errorf("contenuto=%q", got)
	}
	if calls.Load() != 3 {
		t.Errorf("calls=%d, attesi 3 (2 fail + 1 ok)", calls.Load())
	}
}

func TestDownloadNoRetryOn4xx(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "ubuntu.iso")
	if err := Download(context.Background(), srv.Client(), srv.URL+"/ubuntu.iso", dest, nil); err == nil {
		t.Fatal("atteso errore 404")
	}
	if calls.Load() != 1 {
		t.Errorf("calls=%d, atteso 1 (4xx non ritentabile)", calls.Load())
	}
}

func TestResolveURL(t *testing.T) {
	body := []byte(isoBody)
	sha := sha256hex(body)
	srv := serverISO(t, body, sha+"  ubuntu.iso\n")
	dir := t.TempDir()
	res, err := Resolve(context.Background(), Options{Arg: srv.URL + "/ubuntu.iso", Dir: dir, Client: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != filepath.Join(dir, "ubuntu.iso") || !res.Downloaded {
		t.Errorf("res=%+v", res)
	}
	if res.Checksum != sha {
		t.Errorf("checksum=%q", res.Checksum)
	}
	if _, err := os.Stat(res.Path); err != nil {
		t.Errorf("file non scaricato: %v", err)
	}
}

func TestResolveURLSecondCallNoDownload(t *testing.T) {
	body := []byte(isoBody)
	sha := sha256hex(body)
	srv := serverISO(t, body, sha+"  ubuntu.iso\n")
	dir := t.TempDir()
	opt := Options{Arg: srv.URL + "/ubuntu.iso", Dir: dir, Client: srv.Client()}
	if _, err := Resolve(context.Background(), opt); err != nil {
		t.Fatal(err)
	}
	res, err := Resolve(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	if res.Downloaded {
		t.Error("seconda chiamata non deve riscaricare")
	}
}

func TestResolveChecksumMismatch(t *testing.T) {
	body := []byte(isoBody)
	wrong := sha256hex([]byte("altro"))
	srv := serverISO(t, body, wrong+"  ubuntu.iso\n")
	dir := t.TempDir()
	_, err := Resolve(context.Background(), Options{Arg: srv.URL + "/ubuntu.iso", Dir: dir, Client: srv.Client()})
	if err == nil || !strings.Contains(err.Error(), "SHA256 mismatch") {
		t.Fatalf("atteso mismatch, err=%v", err)
	}
}

func TestResolveLocalFile(t *testing.T) {
	dir := t.TempDir()
	body := []byte("local iso")
	p := filepath.Join(dir, "custom.iso")
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Fatal(err)
	}
	// senza checksum accanto: ok, nessuna verifica
	res, err := Resolve(context.Background(), Options{Arg: p, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != p || res.Downloaded || res.Checksum != "" {
		t.Errorf("res=%+v", res)
	}
	// con SHA256SUMS corretto: verificato
	sha := sha256hex(body)
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(sha+"  custom.iso\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err = Resolve(context.Background(), Options{Arg: p, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if res.Checksum != sha {
		t.Errorf("checksum=%q", res.Checksum)
	}
	// con SHA256SUMS errato: errore
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(strings.Repeat("0", 64)+"  custom.iso\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(context.Background(), Options{Arg: p, Dir: dir}); err == nil {
		t.Error("atteso mismatch sul file locale")
	}
}

func TestResolveLocalMissing(t *testing.T) {
	_, err := Resolve(context.Background(), Options{Arg: filepath.Join(t.TempDir(), "nope.iso")})
	if err == nil || !strings.Contains(err.Error(), "non trovato") {
		t.Fatalf("err=%v", err)
	}
}
