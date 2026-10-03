package iso

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
)

// attempts: numero di tentativi di download (come curl --retry 3).
const attempts = 3

// Download: scarica url in dest con resume (Range: bytes=N-) e retry.
// progress(downloaded, total) è opzionale (total = -1 se sconosciuto).
func Download(ctx context.Context, c *http.Client, url, dest string, progress func(int64, int64)) error {
	var lastErr error
	for i := 0; i < attempts; i++ {
		retry, err := downloadOnce(ctx, c, url, dest, progress)
		if err == nil {
			return nil
		}
		lastErr = err
		if !retry {
			break
		}
	}
	return fmt.Errorf("download %s: %w", url, lastErr)
}

// downloadOnce: un tentativo. Ritorna (retryable, err).
func downloadOnce(ctx context.Context, c *http.Client, url, dest string, progress func(int64, int64)) (bool, error) {
	var offset int64
	if fi, err := os.Stat(dest); err == nil {
		offset = fi.Size()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err
	}
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	resp, err := c.Do(req)
	if err != nil {
		return true, err // errore di rete: ritenta
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable:
		return false, nil // il file è già completo
	case resp.StatusCode == http.StatusPartialContent:
		// append dal byte offset
	case resp.StatusCode == http.StatusOK:
		offset = 0 // il server ignora Range: riscriviamo da capo
	case resp.StatusCode >= 500:
		return true, fmt.Errorf("HTTP %d", resp.StatusCode)
	default:
		return false, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	total := int64(-1)
	if resp.ContentLength >= 0 {
		total = offset + resp.ContentLength
	}
	if progress != nil {
		progress(offset, total)
	}

	f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return false, err
	}
	defer f.Close()
	if offset == 0 {
		if err := f.Truncate(0); err != nil {
			return false, err
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return false, err
		}
	} else if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return false, err
	}

	if err := copyProgress(f, resp.Body, offset, total, progress); err != nil {
		return true, err // interruzione a metà: ritenta (riprende)
	}
	return false, nil
}

func copyProgress(dst io.Writer, src io.Reader, offset, total int64, progress func(int64, int64)) error {
	if progress == nil {
		_, err := io.Copy(dst, src)
		return err
	}
	buf := make([]byte, 1<<20)
	done := offset
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return werr
			}
			done += int64(n)
			progress(done, total)
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
