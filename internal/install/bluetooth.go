package install

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
)

// BluetoothConfFile is the modprobe drop-in that keeps the modules out of
// the kernel.
const BluetoothConfFile = "/etc/modprobe.d/ostiole-bluetooth.conf"

// bluetoothModules are unloaded in dependency order: the profiles first,
// then the transports, then the stack.
var bluetoothModules = []string{"bnep", "btusb", "btintel", "btrtl", "btbcm", "btmtk", "bluetooth"}

// BluetoothConf is the drop-in's content. `install … /bin/false` covers
// the case blacklisting does not: a module asked for by name rather than
// by alias.
func BluetoothConf() string {
	return `# Ostiole: a router has no use for Bluetooth.
install bluetooth /bin/false
install btusb /bin/false
blacklist bluetooth
blacklist btusb
blacklist btintel
blacklist btrtl
blacklist btbcm
blacklist btmtk
blacklist bnep
`
}

// BlockBluetooth writes the drop-in and takes the stack out of the kernel
// it is running in. A wifi card and a Bluetooth controller often share one
// chip, one antenna and one firmware; the radio is what a router is for.
//
// Everything after the file itself is best effort: a router with no
// Bluetooth at all has nothing to unload and nothing to block.
func BlockBluetooth(ctx context.Context, run Runner, path string) error {
	if path == "" {
		path = BluetoothConfFile
	}
	// A minimal image may have no modprobe.d at all, and the block has to
	// be there before a module is ever asked for.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // modprobe reads this
		return err
	}
	if err := writeFile(path, BluetoothConf(), 0o644); err != nil {
		return err
	}
	if _, err := exec.LookPath("rfkill"); err == nil {
		_, _ = run.Run(ctx, "rfkill", "block", "bluetooth")
	}
	_, _ = run.Run(ctx, "modprobe", append([]string{"-r"}, bluetoothModules...)...)
	// The package may still be here: the script removes it afterwards.
	_, _ = run.Run(ctx, "systemctl", "mask", "--now", "bluetooth.service")
	return nil
}

// BluetoothState is what the Host page reports: blocked, loaded, or a
// router that never had it.
func BluetoothState() string {
	_, blocked := os.Stat(BluetoothConfFile)
	_, loaded := os.Stat("/sys/module/bluetooth")
	switch {
	case blocked == nil && loaded != nil:
		return "blocked"
	case loaded == nil:
		return "loaded"
	}
	return "absent"
}
