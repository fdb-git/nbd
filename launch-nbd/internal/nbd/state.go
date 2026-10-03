package nbd

import (
	"context"
	"encoding/binary"
	"fmt"
	"strings"
)

// Formato del record di stato: contratto canonico docs/status-format.md.
//
//	offset 0   (8)  magic "NBDST\0\0\0"
//	offset 8   (1)  version = 1
//	offset 9   (1)  stato: 0=clean 1=committing 2=committed
//	offset 10  (6)  riservato
//	offset 16  (64) owner hostname (zero-padded)
//	offset 80  (36) owner UUID sessione
//	offset 116 (256) hash overlay Base64URL
//	offset 372 (8)  ctime unix little-endian
//	offset 380 (3716) padding
//
// Il record è SEMPRE 4096 byte; scrittura mono-blocco + NBD_CMD_FLUSH.

// StateBlockSize: dimensione fissa del record (4 KiB).
const StateBlockSize = 4096

// State: valore del byte di stato.
type State byte

const (
	StateClean      State = 0
	StateCommitting State = 1
	StateCommitted  State = 2
)

func (s State) String() string {
	switch s {
	case StateClean:
		return "clean"
	case StateCommitting:
		return "committing"
	case StateCommitted:
		return "committed"
	}
	return fmt.Sprintf("unknown(%d)", byte(s))
}

// ParseState: da stringa (clean|committing|committed).
func ParseState(s string) (State, error) {
	switch s {
	case "clean":
		return StateClean, nil
	case "committing":
		return StateCommitting, nil
	case "committed":
		return StateCommitted, nil
	}
	return StateClean, fmt.Errorf("stato '%s' non valido (clean|committing|committed)", s)
}

var stateMagic = [8]byte{'N', 'B', 'D', 'S', 'T', 0, 0, 0}

const stateVersion = 1

// Record: contenuto del file di stato.
type Record struct {
	State     State
	OwnerHost string // hostname del client che ha scritto il marker
	OwnerUUID string // UUID della sessione del client
	Hash      string // hash overlay (Base64URL, §5.8)
	CTime     uint64 // unix time (little-endian su disco)
}

// StatusExportName: nome dell'export di stato per un export dati.
func StatusExportName(export string) string { return export + ".status" }

// EncodeRecord: serializza il record a 4096 byte (zero-padded).
func EncodeRecord(r Record) []byte {
	b := make([]byte, StateBlockSize)
	copy(b[0:8], stateMagic[:])
	b[8] = stateVersion
	b[9] = byte(r.State)
	putStr(b, 16, 64, r.OwnerHost)
	putStr(b, 80, 36, r.OwnerUUID)
	putStr(b, 116, 256, r.Hash)
	binary.LittleEndian.PutUint64(b[372:380], r.CTime)
	return b
}

// DecodeRecord: interpreta 4096 byte; errore su magic/version errati.
func DecodeRecord(b []byte) (Record, error) {
	var r Record
	if len(b) < 380 {
		return r, fmt.Errorf("nbd: record di stato troppo corto (%d byte)", len(b))
	}
	if !equalMagic(b[0:8]) {
		return r, fmt.Errorf("nbd: magic del record di stato errato")
	}
	if b[8] != stateVersion {
		return r, fmt.Errorf("nbd: versione record di stato non supportata (%d)", b[8])
	}
	r.State = State(b[9])
	r.OwnerHost = getStr(b, 16, 64)
	r.OwnerUUID = getStr(b, 80, 36)
	r.Hash = getStr(b, 116, 256)
	r.CTime = binary.LittleEndian.Uint64(b[372:380])
	return r, nil
}

// ReadState: legge il record dall'export <export>.status.
func ReadState(ctx context.Context, host string, port int, export string) (Record, error) {
	var r Record
	c, err := Dial(ctx, host, port, StatusExportName(export))
	if err != nil {
		return r, err
	}
	defer c.Close()
	return ReadStateConn(c)
}

// ReadStateConn: come ReadState su una connessione già aperta.
func ReadStateConn(c *Conn) (Record, error) {
	if c.Size() < StateBlockSize {
		return Record{}, fmt.Errorf("nbd: export di stato di %d byte (attesi %d)", c.Size(), StateBlockSize)
	}
	b := make([]byte, StateBlockSize)
	if err := c.ReadAt(b, 0); err != nil {
		return Record{}, err
	}
	return DecodeRecord(b)
}

// WriteState: scrive il record (blocco singolo) + FLUSH per la durabilità.
func WriteState(ctx context.Context, host string, port int, export string, r Record) error {
	c, err := Dial(ctx, host, port, StatusExportName(export))
	if err != nil {
		return err
	}
	defer c.Close()
	return WriteStateConn(c, r)
}

// WriteStateConn: come WriteState su una connessione già aperta.
func WriteStateConn(c *Conn, r Record) error {
	if c.Size() < StateBlockSize {
		return fmt.Errorf("nbd: export di stato di %d byte (attesi %d)", c.Size(), StateBlockSize)
	}
	if err := c.WriteAt(EncodeRecord(r), 0); err != nil {
		return err
	}
	return c.Flush()
}

func putStr(b []byte, off, max int, s string) {
	if len(s) > max {
		s = s[:max]
	}
	copy(b[off:off+max], s)
}

func getStr(b []byte, off, max int) string {
	return strings.TrimRight(string(b[off:off+max]), "\x00")
}

func equalMagic(b []byte) bool {
	for i := range stateMagic {
		if b[i] != stateMagic[i] {
			return false
		}
	}
	return true
}
