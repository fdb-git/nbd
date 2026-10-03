//go:build linux

package audio

import "os"

// CurrentUID: uid del processo (per /run/user/<uid>).
func CurrentUID() int { return os.Getuid() }
