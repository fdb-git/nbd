//go:build windows

package assets

import (
	"embed"
	"io/fs"
)

// albero asset per Windows; "all:" include i dotfile (.keep) così l'embed
// compila anche quando è presente solo il marker.
//
//go:embed all:windows
var fsys embed.FS

func platformFS() fs.FS { return fsys }

func platformDir() string { return "windows" }

// platformBinary: nome logico -> nome file Windows (.exe; niente bridge-helper).
var platformNames = map[string]string{
	"qemu":      "qemu-system-x86_64.exe",
	"qemu-img":  "qemu-img.exe",
	"viewer":    "remote-viewer.exe",
	"ovmf-code": "OVMF_CODE.fd",
	"ovmf-vars": "OVMF_VARS.fd",
	"tapctl":    "tapctl.exe",
}

func platformBinary(logical string) string { return platformNames[logical] }

func platformExpected() []string {
	return []string{"qemu-system-x86_64.exe", "qemu-img.exe", "remote-viewer.exe"}
}
