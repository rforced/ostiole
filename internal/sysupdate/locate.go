package sysupdate

import (
	"os"
	"path/filepath"
)

// sbinDirs are where package managers put daemons. A service's PATH does
// not always include them, so a binary that is plainly installed can look
// missing to the daemon that needs it.
var sbinDirs = []string{"/usr/sbin", "/sbin", "/usr/local/sbin"}

// Locate finds a command on PATH or in the sbin directories, and returns
// where it is or "".
func Locate(name string) string {
	if p, err := lookPath(name); err == nil {
		return p
	}
	for _, dir := range sbinDirs {
		p := filepath.Join(dir, name)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}
