package config

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// ParseEnvFile: interprete del ".env" legacy della vecchia launch-nbd.sh.
// Subset di sintassi shell supportato: commenti #, righe KEY=VALUE, facoltativo
// "export", virgolette singole/doppie, CR finali. NON esegue codice (a
// differenza del source bash dello script): niente espansioni, niente `$()`,
// niente variabili multilinea.
func ParseEnvFile(path string) (map[string]string, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	defer f.Close()

	m := map[string]string{}
	sc := bufio.NewScanner(f)
	line := 0
	for sc.Scan() {
		line++
		raw := strings.TrimSpace(sc.Text())
		if raw == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		raw = strings.TrimPrefix(raw, "export ")
		k, v, ok := strings.Cut(raw, "=")
		if !ok {
			return nil, false, fmt.Errorf("riga %d: attesa KEY=VALUE: %q", line, raw)
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		if k == "" {
			return nil, false, fmt.Errorf("riga %d: chiave vuota", line)
		}
		m[k] = v
	}
	return m, true, sc.Err()
}
