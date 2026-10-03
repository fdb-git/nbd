package iso

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// DiscoverChecksum: cerca lo SHA256 dell'ISO nelle fonti, nell'ordine dello
// script: <dirURL>/SHA256SUMS, <dirURL>/SHA256SUMS.txt, <url>.sha256,
// <localDir>/<base>.sha256, <localDir>/SHA256SUMS.
// isoURL vuoto → si consultano solo le fonti locali. Ritorna ("","") se nulla.
func DiscoverChecksum(ctx context.Context, c *http.Client, isoURL, localDir string) (sha, source string) {
	base := urlBaseName(isoURL)

	if base != "" {
		dirURL := strings.TrimSuffix(isoURL, "/"+base)
		for _, u := range []string{dirURL + "/SHA256SUMS", dirURL + "/SHA256SUMS.txt", isoURL + ".sha256"} {
			body, err := httpGet(ctx, c, u)
			if err != nil {
				continue
			}
			if s := parseChecksum(body, base); s != "" {
				return s, u
			}
		}
	}
	if localDir != "" {
		local := []string{filepath.Join(localDir, "SHA256SUMS")}
		if base != "" {
			local = append([]string{filepath.Join(localDir, base+".sha256")}, local...)
		}
		for _, p := range local {
			b, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			if s := parseChecksum(string(b), base); s != "" {
				return s, p
			}
		}
	}
	return "", ""
}

// parseChecksum: estrae lo SHA256 relativo a isoBase da un SHA256SUMS/.sha256.
// Righe tipiche: "<sha>  <file>", "<sha> *<file>". Se base è vuoto o nessuna
// riga lo nomina, accetta un contenuto che sia un singolo token SHA256.
func parseChecksum(content, isoBase string) string {
	sc := bufio.NewScanner(strings.NewReader(content))
	var lone string
	n := 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 1 {
			if isSHA256(fields[0]) {
				lone = strings.ToLower(fields[0])
				n++
			}
			continue
		}
		sha := ""
		for _, f := range fields {
			if isSHA256(f) {
				sha = strings.ToLower(f)
				break
			}
		}
		if sha == "" {
			continue
		}
		if isoBase == "" {
			return sha
		}
		name := strings.TrimPrefix(fields[len(fields)-1], "*")
		if strings.EqualFold(filepath.Base(name), isoBase) {
			return sha
		}
	}
	if isoBase == "" && n == 1 {
		return lone
	}
	return ""
}

// isSHA256: 64 caratteri esadecimali.
func isSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}

// httpGet: GET con timeout breve, corpo come stringa (per SHA256SUMS).
func httpGet(ctx context.Context, c *http.Client, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	return string(b), nil
}
