package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
)

// DefaultConfigName: file TOML cercato quando --config è assente.
const DefaultConfigName = "launch-nbd.toml"

// launchEnv: variabili d'ambiente LAUNCH_NBD_* -> chiavi TOML (senza prefisso).
func launchEnv() map[string]string {
	m := map[string]string{}
	for _, kv := range os.Environ() {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || !strings.HasPrefix(k, "LAUNCH_NBD_") {
			continue
		}
		m[strings.TrimPrefix(k, "LAUNCH_NBD_")] = v
	}
	return m
}

// Load: merge default → ambiente → .env legacy → TOML (precedenza crescente).
// File TOML assente (e non richiesto esplicitamente): warning + default.
func Load(path string, warnf func(string)) (Cfg, error) {
	cfg := PlatformDefaults()

	// 1) ambiente LAUNCH_NBD_*
	cfg.ApplyEnvMap(launchEnv(), warnf)

	// 2) .env legacy della vecchia launch-nbd.sh (solo nel cwd; warning se rotto)
	if m, ok, err := ParseEnvFile(".env"); err != nil {
		if warnf != nil {
			warnf(fmt.Sprintf(".env: %v (ignorato)", err))
		}
	} else if ok {
		cfg.ApplyEnvMap(m, warnf)
	}

	// 3) TOML (launch-nbd.toml nel cwd o --config)
	if path == "" {
		path = DefaultConfigName
	}
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := toml.Unmarshal(b, &cfg); err != nil {
			return cfg, fmt.Errorf("%s: %w", path, err)
		}
	case os.IsNotExist(err):
		if path != DefaultConfigName {
			return cfg, fmt.Errorf("config %s assente", path)
		}
		if warnf != nil {
			warnf(fmt.Sprintf("config %s assente: uso i default di piattaforma (esegui: launch-nbd configure)", path))
		}
	default:
		return cfg, err
	}
	return cfg, nil
}

// Save: scrive la config in TOML con intestazione documentata.
func Save(path string, c Cfg) error {
	hdr := `# launch-nbd — configurazione (generata da: launch-nbd configure)
#
# Chiavi snake_case; equivalente env per ogni chiave: LAUNCH_NBD_<UPPER>.
# Precedenza: --set > questo file > .env legacy > ambiente > default.
# La vecchia launch-nbd.sh leggeva ".env": questo file la sostituisce.
#
`
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteString(hdr); err != nil {
		return err
	}
	if err := toml.NewEncoder(f).Encode(c); err != nil {
		return err
	}
	return f.Sync()
}

// SaveNotExist: salva solo se il file non esiste già (no-clobber).
func SaveNotExist(path string, c Cfg) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if err := Save(path, c); err != nil {
		return false, err
	}
	return true, nil
}
