// Package nbd: mini client NBD in pura stdlib (plan-go.md §5.8/§5.10).
//
// Implementa il handshake newstyle (fixed) con NBD_OPT_EXPORT_NAME e le
// richieste READ/WRITE/FLUSH. Serve a due usi del launcher:
//   - fingerprint del disco (GPT head/tail) per l'identità dell'overlay (§5.8);
//   - lettura/scrittura del marker di commit cross-host (§5.10, docs/status-format.md).
//
// Non è un client NBD completo: niente TLS, niente NBD_OPT_GO/list, una sola
// richiesta in volo per connessione (sufficiente per blocchi singoli).
package nbd

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

const (
	magicNBDMAGIC = 0x4e42444d41474943 // "NBDMAGIC"
	magicIHAVEOPT = 0x49484156454f5054 // "IHAVEOPT"

	flagFixedNewstyle = 1 // handshake: FIXED_NEWSTYLE / C_FIXED_NEWSTYLE
	flagNoZeroes      = 2 // handshake: NO_ZEROES / C_NO_ZEROES

	optExportName = 1 // NBD_OPT_EXPORT_NAME

	reqMagic = 0x25609513 // NBD_REQUEST_MAGIC
	repMagic = 0x67446698 // NBD_SIMPLE_REPLY_MAGIC

	cmdRead  = 0
	cmdWrite = 1
	cmdDisc  = 2
	cmdFlush = 3
)

// Error: errore riportato dal server NBD (campo error della reply).
type Error struct{ Code uint32 }

func (e *Error) Error() string { return fmt.Sprintf("nbd: errore dal server (code %d)", e.Code) }

// Conn: connessione NBD in fase di trasmissione.
type Conn struct {
	conn   net.Conn
	br     *bufio.Reader
	size   uint64
	handle uint64
}

// Dial: connessione a host:port per l'export indicato (plaintext).
func Dial(ctx context.Context, host string, port int, export string) (*Conn, error) {
	d := net.Dialer{Timeout: 10 * time.Second}
	nc, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return nil, err
	}
	c := &Conn{conn: nc, br: bufio.NewReader(nc)}
	if dl, ok := ctx.Deadline(); ok {
		_ = nc.SetDeadline(dl)
	} else {
		_ = nc.SetDeadline(time.Now().Add(30 * time.Second))
	}
	if err := c.handshake(export); err != nil {
		_ = nc.Close()
		return nil, err
	}
	_ = nc.SetDeadline(time.Time{})
	return c, nil
}

// Size: dimensione dell'export in byte.
func (c *Conn) Size() uint64 { return c.size }

// Close: chiude la connessione (best-effort NBD_CMD_DISC).
func (c *Conn) Close() error {
	_ = c.request(cmdDisc, 0, 0, nil)
	return c.conn.Close()
}

// ReadAt: legge len(p) byte da off.
func (c *Conn) ReadAt(p []byte, off uint64) error { return c.request(cmdRead, off, uint32(len(p)), p) }

// WriteAt: scrive p a partire da off.
func (c *Conn) WriteAt(p []byte, off uint64) error {
	return c.request(cmdWrite, off, uint32(len(p)), p)
}

// Flush: NBD_CMD_FLUSH (durabilità lato server).
func (c *Conn) Flush() error { return c.request(cmdFlush, 0, 0, nil) }

func (c *Conn) readFull(p []byte) error {
	_, err := io.ReadFull(c.br, p)
	return err
}

// handshake: fixed newstyle + NBD_OPT_EXPORT_NAME (lato client).
func (c *Conn) handshake(export string) error {
	var buf [8]byte
	if err := c.readFull(buf[:]); err != nil {
		return fmt.Errorf("nbd: lettura magic iniziale: %w", err)
	}
	if binary.BigEndian.Uint64(buf[:]) != magicNBDMAGIC {
		return fmt.Errorf("nbd: magic iniziale errato (non è un server NBD?)")
	}
	if err := c.readFull(buf[:]); err != nil {
		return err
	}
	if binary.BigEndian.Uint64(buf[:]) != magicIHAVEOPT {
		return fmt.Errorf("nbd: IHAVEOPT mancante")
	}
	var hf [2]byte
	if err := c.readFull(hf[:]); err != nil {
		return err
	}
	serverFlags := binary.BigEndian.Uint16(hf[:])
	if serverFlags&flagFixedNewstyle == 0 {
		return fmt.Errorf("nbd: server senza FIXED_NEWSTYLE")
	}

	clientFlags := uint32(flagFixedNewstyle)
	if serverFlags&flagNoZeroes != 0 {
		clientFlags |= flagNoZeroes
	}
	var cf [4]byte
	binary.BigEndian.PutUint32(cf[:], clientFlags)
	if _, err := c.conn.Write(cf[:]); err != nil {
		return err
	}

	// NBD_OPT_EXPORT_NAME: IHAVEOPT + option + len + nome
	var opt [16]byte
	binary.BigEndian.PutUint64(opt[0:8], magicIHAVEOPT)
	binary.BigEndian.PutUint32(opt[8:12], optExportName)
	binary.BigEndian.PutUint32(opt[12:16], uint32(len(export)))
	if _, err := c.conn.Write(opt[:]); err != nil {
		return err
	}
	if _, err := c.conn.Write([]byte(export)); err != nil {
		return err
	}

	var sz [8]byte
	if err := c.readFull(sz[:]); err != nil {
		return fmt.Errorf("nbd: lettura size export: %w", err)
	}
	c.size = binary.BigEndian.Uint64(sz[:])
	var tf [2]byte
	if err := c.readFull(tf[:]); err != nil {
		return err
	}
	if clientFlags&flagNoZeroes == 0 {
		var zero [124]byte
		if err := c.readFull(zero[:]); err != nil {
			return err
		}
	}
	return nil
}

// request: invia una richiesta e attende la simple reply (con dati per READ).
func (c *Conn) request(cmd uint16, off uint64, length uint32, data []byte) error {
	c.handle++
	h := c.handle

	var req [28]byte
	binary.BigEndian.PutUint32(req[0:4], reqMagic)
	binary.BigEndian.PutUint16(req[4:6], 0) // flags
	binary.BigEndian.PutUint16(req[6:8], cmd)
	binary.BigEndian.PutUint64(req[8:16], h)
	binary.BigEndian.PutUint64(req[16:24], off)
	binary.BigEndian.PutUint32(req[24:28], length)
	if _, err := c.conn.Write(req[:]); err != nil {
		return err
	}
	if cmd == cmdWrite {
		if _, err := c.conn.Write(data); err != nil {
			return err
		}
	}

	var rep [16]byte
	if err := c.readFull(rep[:]); err != nil {
		return err
	}
	if binary.BigEndian.Uint32(rep[0:4]) != repMagic {
		return fmt.Errorf("nbd: reply magic errato")
	}
	if e := binary.BigEndian.Uint32(rep[4:8]); e != 0 {
		return &Error{Code: e}
	}
	if binary.BigEndian.Uint64(rep[8:16]) != h {
		return fmt.Errorf("nbd: handle della reply non corrispondente")
	}
	if cmd == cmdRead {
		if err := c.readFull(data); err != nil {
			return err
		}
	}
	return nil
}
