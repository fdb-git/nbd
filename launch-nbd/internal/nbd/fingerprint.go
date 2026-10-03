package nbd

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// FingerprintRegion: quanti byte leggere in testa e in coda (plan-go.md §5.8).
const FingerprintRegion = 64 * 1024

// Fingerprint: identità del disco = SHA256(head 64 KiB + tail 64 KiB) in
// Base64URL senza padding (filesystem-safe). Le regioni contengono GPT/protective
// MBR (disk GUID a offset 96 della LBA1) e la backup GPT in coda: cambiano con
// ogni installazione/resize. Export < 128 KiB → errore (niente overlay, §5.8.8).
func Fingerprint(ctx context.Context, host string, port int, export string) (string, error) {
	c, err := Dial(ctx, host, port, export)
	if err != nil {
		return "", err
	}
	defer c.Close()
	return fingerprintConn(c)
}

func fingerprintConn(c *Conn) (string, error) {
	size := c.Size()
	if size < 2*FingerprintRegion {
		return "", fmt.Errorf("nbd: export troppo piccolo per il fingerprint (%d byte, minimo %d)", size, 2*FingerprintRegion)
	}
	h := sha256.New()
	head := make([]byte, FingerprintRegion)
	if err := c.ReadAt(head, 0); err != nil {
		return "", err
	}
	h.Write(head)
	tail := make([]byte, FingerprintRegion)
	if err := c.ReadAt(tail, size-FingerprintRegion); err != nil {
		return "", err
	}
	h.Write(tail)
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil)), nil
}
