// Package config: configurazione del launcher.
//
// Merge (precedenza crescente, plan-go.md §5.1):
//
//	default di piattaforma -> ambiente (LAUNCH_NBD_*) -> .env legacy -> TOML -> CLI --set
//
// Chiavi TOML snake_case; ogni chiave ha l'equivalente env LAUNCH_NBD_<UPPER>.
// Il file legacy .env (launch-nbd.sh) usa variabili NBD_*, MEM_MB, ... ed è
// interpretato da envfile.go (subset di sintassi shell: commenti, quotes).
package config

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// Cfg: configurazione effettiva (default..--set già mergiati).
type Cfg struct {
	// --- disco NBD ---
	NBDHost      string `toml:"nbd_host"`
	NBDPort      int    `toml:"nbd_port"`
	NBDExport    string `toml:"nbd_export"`
	NBDStatePort int    `toml:"nbd_state_port"`
	NBDURI       string `toml:"nbd_uri,omitempty"` // derivata da host/port/export se vuota

	// --- binari e risorse VM ---
	QEMU    string `toml:"qemu"`
	QEMUImg string `toml:"qemu_img"`
	MemMB   int    `toml:"mem_mb"`
	CPUs    int    `toml:"cpus"`
	Accel   string `toml:"accel"` // auto|kvm|tcg|whpx|haxm

	// --- caching / performance ---
	CacheMode      string `toml:"cache_mode"`
	AIO            string `toml:"aio"`
	ReconnectDelay int    `toml:"reconnect_delay"`
	QCacheMB       int    `toml:"qcache_mb"`
	L2CacheMB      int    `toml:"l2_cache_mb"`

	// --- disco locale (chiavi solo TOML; --snapshot è CLI-only) ---
	DiskMode   string `toml:"disk_mode"`   // direct|overlay
	OverlayDir string `toml:"overlay_dir"` // dir degli overlay persistenti

	// --- display / vga / audio ---
	DisplayMode string `toml:"display_mode"`
	SpicePort   int    `toml:"spice_port"`
	VGAMode     string `toml:"vga_mode"`
	GL          string `toml:"gl"` // auto|on|off
	AudioMode   string `toml:"audio_mode"`
	AudioDrv    string `toml:"audio_drv"`

	// --- rete ---
	NetMode   string `toml:"net_mode"`
	TapDev    string `toml:"tap_dev"`
	TapSubnet string `toml:"tap_subnet"`
	BridgeIF  string `toml:"bridge_if"`

	// --- monitor ---
	Monitor bool   `toml:"monitor"`            // -monitor stdio (HMP)
	MonSock string `toml:"mon_sock,omitempty"` // HMP su socket unix

	// --- port forward (ripetibile) ---
	PF []string `toml:"port_forwards,omitempty"`
}

// fieldMeta: descrittore di un campo di Cfg (via reflection, chiave TOML).
type fieldMeta struct {
	name    string       // nome Go
	key     string       // chiave TOML / env
	typ     reflect.Kind // tipo del campo
	aliases []string     // alias --set brevi
	enums   []string     // valori ammessi (per string enum)
}

// envKey: LAUNCH_NBD_<UPPER> per la chiave TOML.
func (f fieldMeta) envKey() string { return "LAUNCH_NBD_" + strings.ToUpper(f.key) }

var enumSets = map[string][]string{
	"accel":        {"auto", "kvm", "whpx", "haxm", "tcg"},
	"net_mode":     {"dual", "nat", "hostonly", "tap", "bridged", "none"},
	"display_mode": {"auto", "gtk", "sdl", "vnc", "spice", "none"},
	"vga_mode":     {"virtio", "std", "qxl", "vmware", "none"},
	"gl":           {"auto", "on", "off"},
	"cache_mode":   {"writeback", "none", "unsafe", "writethrough", "directsync"},
	"aio":          {"io_uring", "native", "threads"},
	"audio_mode":   {"hda", "virtio", "none"},
	"audio_drv":    {"auto", "pipewire", "pa", "sdl", "wasapi", "dsound", "none"},
	"disk_mode":    {"direct", "overlay"},
}

var keyAliases = map[string]string{
	"net": "net_mode", "display": "display_mode", "vga": "vga_mode",
	"audio": "audio_mode", "accel": "accel", "tap": "tap_dev",
	"bridge": "bridge_if", "cache": "cache_mode", "gl": "gl",
	"mem": "mem_mb", "cpus": "cpus",
}

// fields: meta del tipo Cfg, indicizzato per chiave TOML.
func fields() map[string]fieldMeta {
	meta := map[string]fieldMeta{}
	typ := reflect.TypeOf(Cfg{})
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		tag, ok := f.Tag.Lookup("toml")
		if !ok {
			continue
		}
		key := strings.Split(tag, ",")[0]
		fm := fieldMeta{
			name: f.Name, key: key, typ: f.Type.Kind(),
		}
		if en, ok := enumSets[key]; ok {
			fm.enums = en
		}
		for ak, canon := range keyAliases {
			if canon == key {
				fm.aliases = append(fm.aliases, ak)
			}
		}
		meta[key] = fm
	}
	return meta
}

// resolveKey: chiave TOML canonica da input (chiave, alias, o env-style).
func (c *Cfg) resolveKey(k string) (string, error) {
	meta := fields()
	key := strings.ToLower(strings.TrimSpace(k))
	key = strings.TrimPrefix(key, "LAUNCH_NBD_")
	if alias, ok := keyAliases[key]; ok {
		key = alias
	}
	if _, ok := meta[key]; ok {
		return key, nil
	}
	return "", fmt.Errorf("chiave sconosciuta '%s' (vedi: launch-nbd help)", k)
}

// SetKey: applica k=v su una chiave TOML (o alias), conversione tipizzata.
func (c *Cfg) SetKey(k, v string) error {
	meta := fields()
	key, err := c.resolveKey(k)
	if err != nil {
		return err
	}
	fm := meta[key]
	rv := reflect.ValueOf(c).Elem().FieldByName(fm.name)
	switch fm.typ {
	case reflect.String:
		if len(fm.enums) > 0 && !enumOK(fm.enums, v) {
			return fmt.Errorf("%s: valore '%s' non ammesso (usa: %s)", key, v, strings.Join(fm.enums, "|"))
		}
		rv.SetString(v)
	case reflect.Int:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return fmt.Errorf("%s: intero atteso, trovato '%s'", key, v)
		}
		rv.SetInt(int64(n))
	case reflect.Bool:
		b, err := parseBool(v)
		if err != nil {
			return fmt.Errorf("%s: booleano atteso, trovato '%s'", key, v)
		}
		rv.SetBool(b)
	default:
		return fmt.Errorf("%s: tipo non supportato da --set", key)
	}
	return nil
}

// ApplyEnvMap: applica una mappa nome->valore (dal .env legacy o dall'ambiente),
// ignorando con warning (tramite warnf) le chiavi sconosciute o non convertibili.
func (c *Cfg) ApplyEnvMap(m map[string]string, warnf func(string)) {
	meta := fields()
	for k, v := range m {
		key := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(k), "LAUNCH_NBD_"))
		fm, ok := meta[key]
		if !ok {
			if warnf != nil {
				warnf(fmt.Sprintf("chiave config ignorata: %s", k))
			}
			continue
		}
		rv := reflect.ValueOf(c).Elem().FieldByName(fm.name)
		switch fm.typ {
		case reflect.String:
			rv.SetString(v)
		case reflect.Int:
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				if warnf != nil {
					warnf(fmt.Sprintf("%s: ignoro valore '%s'", k, v))
				}
				continue
			}
			rv.SetInt(int64(n))
		case reflect.Bool:
			b, err := parseBool(v)
			if err != nil {
				if warnf != nil {
					warnf(fmt.Sprintf("%s: ignoro valore '%s'", k, v))
				}
				continue
			}
			rv.SetBool(b)
		}
	}
}

// URI: URI completo dell'export (NBD_URI se impostata, altrimenti derivata).
func (c *Cfg) URI() string {
	if c.NBDURI != "" {
		return c.NBDURI
	}
	return fmt.Sprintf("nbd://%s:%d/%s", c.NBDHost, c.NBDPort, c.NBDExport)
}

// Validate: errori di integrità della config (elenco, mai messaggi parziali).
func (c *Cfg) Validate() []error {
	meta := fields()
	var errs []error
	check := func(key string, ok bool, msg string) {
		if !ok {
			errs = append(errs, fmt.Errorf("%s: %s", key, msg))
		}
	}
	// string enum + obbligatorie
	for key, fm := range meta {
		rv := reflect.ValueOf(c).Elem().FieldByName(fm.name)
		switch fm.typ {
		case reflect.String:
			val := rv.String()
			if len(fm.enums) > 0 && val != "" && !enumOK(fm.enums, val) {
				errs = append(errs, fmt.Errorf("%s: '%s' non ammesso (usa: %s)",
					key, val, strings.Join(fm.enums, "|")))
			}
			switch key {
			case "nbd_host":
				check(key, val != "", "host NBD mancante (imposta nbd_host o NBD_HOST)")
			case "nbd_export":
				check(key, val != "", "export NBD mancante (imposta nbd_export o NBD_EXPORT)")
			case "qemu", "qemu_img":
				check(key, val != "", "binario mancante")
			}
		}
	}
	// range numerici e porte
	port := func(key string, v int) {
		check(key, v >= 1 && v <= 65535, fmt.Sprintf("porta fuori range (1..65535): %d", v))
	}
	port("nbd_port", c.NBDPort)
	port("nbd_state_port", c.NBDStatePort)
	if c.DisplayMode == "spice" {
		port("spice_port", c.SpicePort) // solo se usata
	}
	check("mem_mb", c.MemMB > 0, fmt.Sprintf("memoria non valida: %d", c.MemMB))
	check("cpus", c.CPUs >= 1 && c.CPUs <= 128, fmt.Sprintf("cpus fuori range: %d", c.CPUs))
	check("reconnect_delay", c.ReconnectDelay >= 0, "deve essere >= 0")
	check("qcache_mb", c.QCacheMB >= 0, "deve essere >= 0")
	check("l2_cache_mb", c.L2CacheMB >= 0, "deve essere >= 0")
	if c.NBDURI != "" && !(strings.HasPrefix(c.NBDURI, "nbd://") || strings.HasPrefix(c.NBDURI, "nbs://")) {
		errs = append(errs, fmt.Errorf("nbd_uri: deve iniziare con nbd:// o nbs://: '%s'", c.NBDURI))
	}
	if c.DiskMode == "overlay" && c.OverlayDir == "" {
		errs = append(errs, fmt.Errorf("overlay_dir: richiesta con disk_mode=overlay"))
	}
	return errs
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// enumOK: valore ammesso; per gli acceleratori è accettata anche la forma con
// proprietà "base,prop=val,..." (es. "tcg,thread=multi,tb-size=512").
func enumOK(list []string, v string) bool {
	if contains(list, v) {
		return true
	}
	if i := strings.IndexByte(v, ','); i > 0 {
		return contains(list, v[:i])
	}
	return false
}

func parseBool(v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	}
	return false, fmt.Errorf("booleano non riconosciuto: %s", v)
}
