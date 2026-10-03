// Package nbdtest: server NBD mock per i test (test-support, non per produzione).
//
// Implementa il lato server del protocollo newstyle + NBD_OPT_EXPORT_NAME e le
// richieste READ/WRITE/FLUSH su export in memoria. Usato dai test di
// internal/nbd e per l'integrazione di internal/qemu (stato §5.10).
package nbdtest

import (
	"encoding/binary"
	"io"
	"net"
	"sync"
	"time"
)

const (
	magicNBDMAGIC = 0x4e42444d41474943
	magicIHAVEOPT = 0x49484156454f5054
	flagFixed     = 1
	flagNoZeroes  = 2
	optExportName = 1
	reqMagic      = 0x25609513
	repMagic      = 0x67446698
	cmdRead       = 0
	cmdWrite      = 1
	cmdDisc       = 2
	cmdFlush      = 3
)

// Server: server NBD mock in ascolto su 127.0.0.1.
type Server struct {
	ln      net.Listener
	mu      sync.Mutex
	exports map[string][]byte
	flushes map[string]int
	wg      sync.WaitGroup
}

// New: avvia il server con gli export indicati (nome -> contenuto).
func New(exports map[string][]byte) (*Server, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &Server{ln: ln, exports: exports, flushes: map[string]int{}}
	s.wg.Add(1)
	go s.serve()
	return s, nil
}

// Addr: host e porta di ascolto.
func (s *Server) Addr() (string, int) {
	host, portStr, _ := net.SplitHostPort(s.ln.Addr().String())
	port := 0
	for _, c := range portStr {
		port = port*10 + int(c-'0')
	}
	return host, port
}

// FlushCount: quante NBD_CMD_FLUSH ha ricevuto un export.
func (s *Server) FlushCount(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.flushes[name]
}

// Close: chiude il listener e attende le connessioni.
func (s *Server) Close() {
	_ = s.ln.Close()
	s.wg.Wait()
}

func (s *Server) serve() {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer conn.Close()
			s.handle(conn)
		}()
	}
}

func (s *Server) handle(conn net.Conn) {
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], magicNBDMAGIC)
	if _, err := conn.Write(buf[:]); err != nil {
		return
	}
	binary.BigEndian.PutUint64(buf[:], magicIHAVEOPT)
	if _, err := conn.Write(buf[:]); err != nil {
		return
	}
	var hf [2]byte
	binary.BigEndian.PutUint16(hf[:], flagFixed|flagNoZeroes)
	if _, err := conn.Write(hf[:]); err != nil {
		return
	}
	var cf [4]byte
	if _, err := io.ReadFull(conn, cf[:]); err != nil {
		return
	}
	clientFlags := binary.BigEndian.Uint32(cf[:])

	var opt [16]byte
	if _, err := io.ReadFull(conn, opt[:]); err != nil {
		return
	}
	if binary.BigEndian.Uint64(opt[0:8]) != magicIHAVEOPT {
		return
	}
	option := binary.BigEndian.Uint32(opt[8:12])
	nameLen := binary.BigEndian.Uint32(opt[12:16])
	nameBuf := make([]byte, nameLen)
	if _, err := io.ReadFull(conn, nameBuf); err != nil {
		return
	}
	name := string(nameBuf)
	if option != optExportName {
		return
	}
	s.mu.Lock()
	data := s.exports[name]
	s.mu.Unlock()

	var sz [8]byte
	binary.BigEndian.PutUint64(sz[:], uint64(len(data)))
	if _, err := conn.Write(sz[:]); err != nil {
		return
	}
	var tf [2]byte
	if _, err := conn.Write(tf[:]); err != nil {
		return
	}
	if clientFlags&flagNoZeroes == 0 {
		var zero [124]byte
		if _, err := conn.Write(zero[:]); err != nil {
			return
		}
	}

	for {
		var req [28]byte
		if _, err := io.ReadFull(conn, req[:]); err != nil {
			return
		}
		if binary.BigEndian.Uint32(req[0:4]) != reqMagic {
			return
		}
		cmd := binary.BigEndian.Uint16(req[6:8])
		handle := binary.BigEndian.Uint64(req[8:16])
		off := binary.BigEndian.Uint64(req[16:24])
		length := binary.BigEndian.Uint32(req[24:28])

		var errCode uint32
		var readData []byte
		switch cmd {
		case cmdRead:
			s.mu.Lock()
			if int(off)+int(length) <= len(data) {
				readData = append([]byte(nil), data[off:off+uint64(length)]...)
			} else {
				errCode = 5
			}
			s.mu.Unlock()
		case cmdWrite:
			payload := make([]byte, length)
			if _, err := io.ReadFull(conn, payload); err != nil {
				return
			}
			s.mu.Lock()
			if int(off)+int(length) <= len(data) {
				copy(data[off:], payload)
			} else {
				errCode = 5
			}
			s.mu.Unlock()
		case cmdFlush:
			s.mu.Lock()
			s.flushes[name]++
			s.mu.Unlock()
		case cmdDisc:
			return
		default:
			errCode = 22
		}

		var rep [16]byte
		binary.BigEndian.PutUint32(rep[0:4], repMagic)
		binary.BigEndian.PutUint32(rep[4:8], errCode)
		binary.BigEndian.PutUint64(rep[8:16], handle)
		if _, err := conn.Write(rep[:]); err != nil {
			return
		}
		if cmd == cmdRead && errCode == 0 {
			if _, err := conn.Write(readData); err != nil {
				return
			}
		}
	}
}
