//go:build linux

package assets

import (
	"embed"
	"io/fs"
)

// albero asset per Linux; "all:" include i dotfile (.keep) così l'embed
// compila anche quando è presente solo il marker.
//
//go:embed all:linux
var fsys embed.FS

func platformFS() fs.FS { return fsys }

func platformDir() string { return "linux" }

// platformBinary: nome logico -> nome file Linux.
var platformNames = map[string]string{
	"qemu":          "qemu-system-x86_64",
	"qemu-img":      "qemu-img",
	"viewer":        "remote-viewer",
	"bridge-helper": "qemu-bridge-helper",
}

func platformBinary(logical string) string { return platformNames[logical] }

func platformExpected() []string {
	return []string{"qemu-system-x86_64", "qemu-img", "remote-viewer", "qemu-bridge-helper"}
}
