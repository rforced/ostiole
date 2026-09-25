package shaping

import (
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/vishvananda/netlink"

	"github.com/rforced/ostiole/internal/model"
)

// Where a qdisc hangs and what handle it carries, as the kernel writes
// them. A reconcile reads these rather than parsing tc's output: asking
// netlink is cheap enough to do on every tick, and running a command is
// not.
const (
	rootParent    = netlink.HANDLE_ROOT
	ingressParent = netlink.HANDLE_INGRESS
	ingressHandle = 0xFFFF0000
)

// rootHandle is Handle as the kernel stores it.
var rootHandle = netlink.MakeHandle(Handle, 0)

// Qdisc is the little a reconcile needs to know about one.
type Qdisc struct {
	Type   string
	Handle uint32
	Parent uint32
}

// Ours reports whether this is the queue Ostiole installs on a root. The
// handle is the marker: the kernel picks its own from 0x8001 upwards, so
// nothing else arrives at this one by accident.
func (q Qdisc) Ours() bool { return q.Parent == rootParent && q.Handle == rootHandle }

// IsIngress reports whether this is the hook arriving packets are taken
// off, whoever put it there.
func (q Qdisc) IsIngress() bool { return q.Parent == ingressParent }

// hasOurRoot and hasIngress read a device's qdisc list.
func hasOurRoot(qs []Qdisc) bool {
	for _, q := range qs {
		if q.Ours() {
			return true
		}
	}
	return false
}

func hasIngress(qs []Qdisc) bool {
	for _, q := range qs {
		if q.IsIngress() {
			return true
		}
	}
	return false
}

// Kernel is the netlink half of the work: which links exist, what is
// queued on them, and creating or removing the helper devices. It is an
// interface so the reconciler can be tested without a kernel.
type Kernel interface {
	// LinkExists reports whether a device is there. A dialled session that
	// has not come up yet is not an error, it is a later tick's problem.
	LinkExists(name string) (bool, error)
	// Qdiscs lists what is queued on a device.
	Qdiscs(dev string) ([]Qdisc, error)
	// EnsureIFB creates the helper device if it is missing and brings it
	// up. It is an error if something else already has the name.
	EnsureIFB(name string) error
	// DeleteLink removes a device. One that is already gone is not an
	// error: the reconcile wanted it gone either way.
	DeleteLink(name string) error
	// IFBs names the helper devices that are Ostiole's, by name, kind, and
	// the handle on their root.
	IFBs() ([]string, error)
}

// Netlink is the production Kernel.
type Netlink struct{}

var _ Kernel = Netlink{}

// LinkExists implements Kernel.
func (Netlink) LinkExists(name string) (bool, error) {
	_, err := netlink.LinkByName(name)
	switch {
	case err == nil:
		return true, nil
	case notFound(err):
		return false, nil
	}
	return false, fmt.Errorf("look up %s: %w", name, err)
}

// Qdiscs implements Kernel. A device that has gone has nothing queued on
// it, which is the answer the reconciler wants rather than an error.
func (Netlink) Qdiscs(dev string) ([]Qdisc, error) {
	link, err := netlink.LinkByName(dev)
	if err != nil {
		if notFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("look up %s: %w", dev, err)
	}
	list, err := netlink.QdiscList(link)
	if err != nil {
		return nil, fmt.Errorf("list qdiscs on %s: %w", dev, err)
	}
	out := make([]Qdisc, 0, len(list))
	for _, q := range list {
		a := q.Attrs()
		out = append(out, Qdisc{Type: q.Type(), Handle: a.Handle, Parent: a.Parent})
	}
	return out, nil
}

// EnsureIFB implements Kernel.
func (Netlink) EnsureIFB(name string) error {
	link, err := netlink.LinkByName(name)
	switch {
	case err == nil:
		if link.Type() != "ifb" {
			return fmt.Errorf("a device called %s is already here and is not ours", name)
		}
	case notFound(err):
		if err := netlink.LinkAdd(&netlink.Ifb{Name: name}); err != nil {
			return fmt.Errorf("create %s: %w", name, err)
		}
		if link, err = netlink.LinkByName(name); err != nil {
			return fmt.Errorf("look up %s after creating it: %w", name, err)
		}
	default:
		return fmt.Errorf("look up %s: %w", name, err)
	}
	if link.Attrs().Flags&net.FlagUp != 0 {
		return nil
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return fmt.Errorf("bring %s up: %w", name, err)
	}
	return nil
}

// DeleteLink implements Kernel.
func (Netlink) DeleteLink(name string) error {
	link, err := netlink.LinkByName(name)
	if err != nil {
		if notFound(err) {
			return nil
		}
		return fmt.Errorf("look up %s: %w", name, err)
	}
	if err := netlink.LinkDel(link); err != nil {
		return fmt.Errorf("remove %s: %w", name, err)
	}
	return nil
}

// IFBs implements Kernel. All three markers have to line up: the name
// Ostiole gives its helpers, the kind of device it creates, and the
// handle it puts on their root. A foreign ifb0, or an ifb somebody else
// named ifb-eth0, is left where it is.
func (Netlink) IFBs() ([]string, error) {
	links, err := netlink.LinkList()
	if err != nil {
		return nil, fmt.Errorf("list links: %w", err)
	}
	var out []string
	for _, link := range links {
		name := link.Attrs().Name
		if link.Type() != "ifb" || !strings.HasPrefix(name, model.IFBPrefix) {
			continue
		}
		qs, err := netlink.QdiscList(link)
		if err != nil {
			continue
		}
		for _, q := range qs {
			a := q.Attrs()
			if (Qdisc{Type: q.Type(), Handle: a.Handle, Parent: a.Parent}).Ours() {
				out = append(out, name)
				break
			}
		}
	}
	return out, nil
}

func notFound(err error) bool {
	var missing netlink.LinkNotFoundError
	return errors.As(err, &missing)
}

// IFBName is where the shaped ingress of an interface is carried. It is
// the model's so that validation can refuse two interfaces that would
// land on one device; this is the name the rest of the package uses.
func IFBName(iface string) string { return model.IFBName(iface) }
