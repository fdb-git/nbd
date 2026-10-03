// Package iso: risoluzione dell'ISO di install (file locale o URL).
//
// Traduzione di plan-go.md §3/§5.2 e della sezione ISO di launch-nbd.sh:
//   - URL: discovery dello SHA256 (SHA256SUMS, SHA256SUMS.txt, <url>.sha256,
//     <dir>/<base>.sha256, <dir>/SHA256SUMS), download con resume
//     (Range: bytes=N-) e retry, verifica crypto/sha256;
//   - file locale: verifica opzionale da SHA256SUMS/<base>.sha256 accanto.
//
// Sostituisce curl -C - (resume) e sha256sum con la stdlib.
package iso

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Options: parametri di Resolve.
type Options struct {
	Arg    string // valore di --iso (file locale o URL http/https)
	Dir    string // dir di download (default: cwd)
	Client *http.Client
	Log    func(string) // messaggi [iso] (può essere nil)
}

// Result: ISO locale pronto all'uso.
type Result struct {
	Path           string // percorso del file ISO verificato
	URL            string // URL sorgente (vuoto per file locale)
	Checksum       string // SHA256 atteso ("" = non trovato)
	ChecksumSource string // da dove viene il checksum
	Downloaded     bool
}

// IsURL: true per http:// o https://.
func IsURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// Resolve: da --iso a un ISO locale verificato (download se URL).
func Resolve(ctx context.Context, opt Options) (Result, error) {
	var res Result
	if opt.Arg == "" {
		return res, fmt.Errorf("iso: argomento vuoto")
	}
	if opt.Dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return res, err
		}
		opt.Dir = wd
	}
	if opt.Client == nil {
		opt.Client = http.DefaultClient
	}
	logf := opt.Log
	if logf == nil {
		logf = func(string) {}
	}

	if !IsURL(opt.Arg) {
		return resolveLocal(opt, logf)
	}

	base := urlBaseName(opt.Arg)
	if base == "" {
		return res, fmt.Errorf("iso: URL senza nome file: %s", opt.Arg)
	}
	dest := filepath.Join(opt.Dir, base)
	res.Path, res.URL = dest, opt.Arg

	sha, source := DiscoverChecksum(ctx, opt.Client, opt.Arg, opt.Dir)
	res.Checksum, res.ChecksumSource = sha, source
	if sha != "" {
		logf("[iso] checksum da " + source)
	} else {
		logf("[iso] nessun checksum trovato: verifica saltata")
	}

	// serve (ri)scaricare? file mancante oppure presente ma checksum errato.
	need := false
	if fi, err := os.Stat(dest); err != nil {
		need = true
	} else if sha != "" {
		actual, err := HashFile(dest)
		if err != nil {
			return res, err
		}
		if actual != sha {
			logf(fmt.Sprintf("[iso] %s incompleto o corrotto (%d byte): riprendo il download", base, fi.Size()))
			need = true
		}
	}
	if need {
		logf("[iso] download " + opt.Arg)
		if err := Download(ctx, opt.Client, opt.Arg, dest, nil); err != nil {
			return res, err
		}
		res.Downloaded = true
	}

	if sha != "" {
		actual, err := HashFile(dest)
		if err != nil {
			return res, err
		}
		if actual != sha {
			return res, fmt.Errorf("iso: SHA256 mismatch per %s\n  atteso: %s\n  attuale: %s\n  rimuovi il file e riprova: rm -f %q", dest, sha, actual, dest)
		}
		logf("[iso] checksum OK")
	}
	return res, nil
}

// resolveLocal: ISO già su disco; verifica opzionale accanto al file.
func resolveLocal(opt Options, logf func(string)) (Result, error) {
	var res Result
	p := opt.Arg
	if fi, err := os.Stat(p); err != nil || fi.IsDir() {
		return res, fmt.Errorf("iso: file non trovato: %s", p)
	}
	res.Path = p
	dir := filepath.Dir(p)
	base := filepath.Base(p)
	if sha, source := DiscoverChecksum(context.Background(), opt.Client, "", dir); sha != "" {
		res.Checksum, res.ChecksumSource = sha, source
		actual, err := HashFile(p)
		if err != nil {
			return res, err
		}
		if actual != sha {
			return res, fmt.Errorf("iso: SHA256 mismatch per %s (atteso %s, attuale %s)", p, sha, actual)
		}
		logf("[iso] checksum OK (" + source + ")")
	} else {
		logf("[iso] usando ISO locale: " + base)
	}
	return res, nil
}

// urlBaseName: ultimo segmento del path dell'URL (senza query); "" se assente.
func urlBaseName(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	b := path.Base(u.Path)
	if b == "." || b == "/" || b == "" {
		return ""
	}
	return b
}

// HashFile: SHA256 esadecimale (minuscolo) di un file.
func HashFile(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
