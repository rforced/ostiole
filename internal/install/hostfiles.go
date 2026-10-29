package install

import (
	"cmp"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"ostiole/internal/atomicfile"
	"ostiole/internal/sysctl"
)

// ProtocolsConfFile is the modprobe drop-in that keeps out the socket
// families no router uses.
const ProtocolsConfFile = "/etc/modprobe.d/ostiole-protocols.conf"

// CoredumpConfFile is the systemd-coredump drop-in that keeps crashed
// programs' memory off the disk.
const CoredumpConfFile = "/etc/systemd/coredump.conf.d/ostiole.conf"

// blockedProtocols are modules any user can have loaded by opening a
// socket of their family, and that no Ostiole feature uses. SCTP is
// still forwarded and translated: nf_conntrack and nf_nat do that without
// sctp.ko. vsock stays for virtual machines, llc and stp for bridges.
var blockedProtocols = []string{
	"dccp", "sctp", "rds", "tipc",
	"atm", "can-raw", "can-bcm", "can-gw", "can-isotp", "can-j1939",
	"rxrpc", "kcm", "smc", "af_key", "ieee802154_socket", "nfc", "n_hdlc",
	"l2tp_ppp", "l2tp_ip", "l2tp_ip6", "l2tp_eth", "l2tp_netlink",
	"appletalk", "ax25", "netrom", "rose", "x25", "phonet",
}

// ProtocolsConf is the drop-in's content. As for Bluetooth, the install
// line covers a module asked for by name, which blacklisting does not.
func ProtocolsConf() string {
	var b strings.Builder
	b.WriteString("# Ostiole: socket families a router has no use for.\n")
	for _, m := range blockedProtocols {
		b.WriteString("install " + m + " /bin/false\n")
	}
	for _, m := range blockedProtocols {
		b.WriteString("blacklist " + m + "\n")
	}
	return b.String()
}

// CoredumpConf is the coredump drop-in's content. A crash still leaves a
// line in the journal; what is not kept is its memory, which for the
// resolvers is the network's DNS names and for hostapd the Wi-Fi
// passphrase.
func CoredumpConf() string {
	return "# Ostiole: log a crash, keep no core.\n[Coredump]\nStorage=none\nProcessSizeMax=0\n"
}

// WriteHostFiles writes what holds the router's host settings across a
// boot: the sysctl file and the module and core dump drop-ins. It loads,
// unloads and restarts nothing, so an update writes them with nobody
// watching and a router installed before a file existed gets it.
func WriteHostFiles(opts Options) error {
	var errs []error
	if opts.SysctlFile != "-" {
		errs = append(errs, sysctl.Persist(opts.SysctlFile))
	}
	errs = append(errs, writeDropIns(opts))
	return errors.Join(errs...)
}

// KdumpUnits are the crash dump services of the distributions that ship
// one. The install script removes their packages; the mask covers a router
// that only updated, where the sysctl refusing kexec fails them at every
// boot.
var KdumpUnits = []string{"kdump.service", "kdump-tools.service"}

// MaskKdump masks the crash dump services without stopping them.
func MaskKdump(ctx context.Context, sc Systemctl) error {
	_, err := sc.Run(ctx, append([]string{"mask"}, KdumpUnits...)...)
	return err
}

// writeDropIns writes the module and core dump drop-ins. A path of "-"
// skips one; empty is its default.
func writeDropIns(opts Options) error {
	var errs []error
	for _, f := range []struct{ path, content string }{
		{cmp.Or(opts.ProtocolsFile, ProtocolsConfFile), ProtocolsConf()},
		{cmp.Or(opts.CoredumpFile, CoredumpConfFile), CoredumpConf()},
	} {
		if f.path == "-" {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(f.path), 0o755); err != nil { //nolint:gosec // modprobe and systemd read these
			errs = append(errs, err)
			continue
		}
		errs = append(errs, atomicfile.Write(f.path, []byte(f.content), 0o644))
	}
	return errors.Join(errs...)
}
