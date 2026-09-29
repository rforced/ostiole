package netlink

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// A one-byte attribute is padded to four, and its length says five. The
// type comes back without the nested flag.
func TestAttributesPadAndNest(t *testing.T) {
	t.Parallel()
	var e encoder
	e.nest(7|unix.NLA_F_NESTED, func() { e.attr(1, []byte{0xaa}) })
	want := []byte{12, 0, 7, 0x80, 5, 0, 1, 0, 0xaa, 0, 0, 0}
	if !bytes.Equal(e.b, want) {
		t.Fatalf("encoded % x, want % x", e.b, want)
	}
	outer, err := attrs(e.b)
	if err != nil || len(outer) != 1 || outer[0].typ != 7 {
		t.Fatalf("outer = %+v, %v", outer, err)
	}
	inner, err := attrs(outer[0].data)
	if err != nil || len(inner) != 1 || inner[0].typ != 1 || !bytes.Equal(inner[0].data, []byte{0xaa}) {
		t.Fatalf("inner = %+v, %v", inner, err)
	}
}

func TestAnAttributeLongerThanItsRoomIsAnError(t *testing.T) {
	t.Parallel()
	if _, err := attrs([]byte{16, 0, 1, 0, 0, 0, 0, 0}); err == nil {
		t.Error("an attribute claiming 16 bytes of 8 was read")
	}
}

// A value that does not fit its field fails the request before anything
// is sent.
func TestAValueTooWideFailsTheRequest(t *testing.T) {
	t.Parallel()
	var e encoder
	e.u32(unix.RTA_PRIORITY, e.n32(-1, "metric"))
	if err := request(unix.RTM_NEWROUTE, 0, &e, nil); err == nil || !strings.Contains(err.Error(), "metric -1") {
		t.Fatalf("err = %v, want the metric named", err)
	}
}

// header builds a netlink message.
func header(typ, flags uint16, seq uint32, body []byte) []byte {
	b := native.AppendUint32(nil, uint32(unix.NLMSG_HDRLEN+len(body)))
	b = native.AppendUint16(b, typ)
	b = native.AppendUint16(b, flags)
	b = native.AppendUint32(b, seq)
	b = native.AppendUint32(b, 0)
	b = append(b, body...)
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return b
}

func TestADatagramSplitsIntoItsMessages(t *testing.T) {
	t.Parallel()
	b := append(header(unix.RTM_NEWLINK, unix.NLM_F_MULTI, 7, []byte{1, 2, 3}), header(unix.NLMSG_DONE, 0, 7, []byte{0, 0, 0, 0})...)
	msgs, err := parseMessages(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0].typ != unix.RTM_NEWLINK || !bytes.Equal(msgs[0].data, []byte{1, 2, 3}) ||
		msgs[1].typ != unix.NLMSG_DONE || msgs[1].seq != 7 {
		t.Fatalf("messages = %+v", msgs)
	}
	if _, err := parseMessages(b[:18]); err == nil {
		t.Error("a message cut short was read")
	}
}

// errno builds the int an acknowledgement opens with.
func errno(e unix.Errno) []byte {
	return native.AppendUint32(nil, uint32(-int32(e)))
}

func TestTheKernelsRefusalKeepsItsErrnoAndItsWords(t *testing.T) {
	t.Parallel()
	request := header(unix.RTM_NEWROUTE, unix.NLM_F_REQUEST, 1, []byte{2, 0, 0, 0})
	var tlv encoder
	tlv.attr(unix.NLMSGERR_ATTR_MSG, []byte("Nexthop has invalid gateway\x00"))
	for _, tc := range []struct {
		name string
		m    message
		want unix.Errno
		msg  string
	}{
		{"ack", message{typ: unix.NLMSG_ERROR, data: native.AppendUint32(nil, 0)}, 0, ""},
		{"bare", message{typ: unix.NLMSG_ERROR, data: append(errno(unix.EEXIST), request[:16]...)}, unix.EEXIST, ""},
		{"capped", message{
			typ: unix.NLMSG_ERROR, flags: unix.NLM_F_CAPPED | unix.NLM_F_ACK_TLVS,
			data: append(append(errno(unix.EINVAL), request[:16]...), tlv.b...),
		}, unix.EINVAL, "Nexthop has invalid gateway"},
		{"whole request echoed", message{
			typ: unix.NLMSG_ERROR, flags: unix.NLM_F_ACK_TLVS,
			data: append(append(errno(unix.EINVAL), request...), tlv.b...),
		}, unix.EINVAL, "Nexthop has invalid gateway"},
		{"end of a dump", message{
			typ: unix.NLMSG_DONE, flags: unix.NLM_F_ACK_TLVS,
			data: append(errno(unix.ENOENT), tlv.b...),
		}, unix.ENOENT, "Nexthop has invalid gateway"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := refusal(tc.m)
			if tc.want == 0 {
				if err != nil {
					t.Fatalf("an acknowledgement read as %v", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if tc.msg != "" && !strings.HasSuffix(err.Error(), ": "+tc.msg) {
				t.Errorf("err = %q, want the kernel's words", err)
			}
		})
	}
}

func TestProtocolNames(t *testing.T) {
	t.Parallel()
	for p, want := range map[Protocol]string{3: "boot", 4: "static", 9: "ra", 16: "dhcp", 186: "bgp", 99: "99"} {
		if got := p.String(); got != want {
			t.Errorf("protocol %d = %q, want %q", p, got, want)
		}
	}
}

// A route goes out as the kernel reads it back: a table past 255 in
// RTA_TABLE, each hop of a multipath route with its gateway, and only the
// flags a request may carry.
func TestRoutesSurviveARoundTrip(t *testing.T) {
	t.Parallel()
	multipath := Route{
		Table: 2201,
		Dst:   &net.IPNet{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)},
		MultiPath: []Nexthop{
			{LinkIndex: 3, Gw: net.ParseIP("203.0.113.1")},
			{LinkIndex: 4, Gw: net.ParseIP("198.51.100.1")},
		},
	}
	var e encoder
	e.route(true, multipath)
	if e.err != nil {
		t.Fatal(e.err)
	}
	if e.b[4] != unix.RT_TABLE_UNSPEC {
		t.Errorf("header table = %d, want RT_TABLE_UNSPEC for a table past 255", e.b[4])
	}
	got, err := parseRoute(e.b)
	if err != nil {
		t.Fatal(err)
	}
	if got.Family != unix.AF_INET || got.Table != 2201 || got.Protocol != unix.RTPROT_BOOT || got.Type != unix.RTN_UNICAST ||
		len(got.MultiPath) != 2 || got.MultiPath[1].LinkIndex != 4 || !got.MultiPath[1].Gw.Equal(net.ParseIP("198.51.100.1")) {
		t.Errorf("read back %+v", got)
	}

	linkdown := Route{
		Gw: net.ParseIP("2001:db8::1"), LinkIndex: 5, Priority: 1_000_010, Protocol: unix.RTPROT_RA,
		Flags: unix.RTNH_F_LINKDOWN | unix.RTNH_F_DEAD | unix.RTNH_F_ONLINK,
	}
	e = encoder{}
	e.route(true, linkdown)
	got, err = parseRoute(e.b)
	if err != nil {
		t.Fatal(err)
	}
	if got.Family != unix.AF_INET6 || got.Table != unix.RT_TABLE_MAIN || got.Priority != 1_000_010 ||
		got.Protocol != unix.RTPROT_RA || got.Flags != unix.RTNH_F_ONLINK || got.Dst.String() != "::/0" {
		t.Errorf("read back %+v", got)
	}

	e = encoder{}
	e.route(false, Route{Dst: &net.IPNet{IP: net.ParseIP("192.0.2.0"), Mask: net.CIDRMask(24, 32)}, Gw: net.ParseIP("2001:db8::1")})
	if e.err == nil {
		t.Error("a route with an IPv4 destination and an IPv6 gateway was encoded")
	}
}

func TestRulesSurviveARoundTrip(t *testing.T) {
	t.Parallel()
	for _, want := range []Rule{
		{Family: unix.AF_INET, Priority: 22000, Mark: 1 << 16, Mask: 0xff0000, Table: unix.RT_TABLE_MAIN, SuppressPrefixlen: 0},
		{Family: unix.AF_INET6, Priority: 22001, Mark: 1 << 16, Mask: 0xff0000, Table: 2201, SuppressPrefixlen: -1},
		{
			Family: unix.AF_INET, Priority: 22450, Src: netip.MustParsePrefix("198.51.100.2/32"),
			IPProto: unix.IPPROTO_UDP, Sport: PortRange{Start: 51820, End: 51820}, Table: unix.RT_TABLE_MAIN, SuppressPrefixlen: 0,
		},
		{
			Family: unix.AF_INET6, Priority: 22451, Src: netip.MustParsePrefix("2001:db8:2::/64"),
			IPProto: unix.IPPROTO_UDP, Sport: PortRange{Start: 51820, End: 51830}, Table: 2425, SuppressPrefixlen: -1,
		},
	} {
		var e encoder
		e.rule(want)
		got, err := parseRule(e.b)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("read back %+v, want %+v", got, want)
		}
	}
}

// addrMessage builds an RTM_NEWADDR body.
func addrMessage(fam, prefixlen int, local, address net.IP, flags uint32) []byte {
	var e encoder
	e.ifaddrmsg(fam, prefixlen, 9)
	if local != nil {
		e.ip(unix.IFA_LOCAL, fam, local)
	}
	if address != nil {
		e.ip(unix.IFA_ADDRESS, fam, address)
	}
	if flags != 0 {
		e.u32(unix.IFA_FLAGS, flags)
	}
	return e.b
}

func TestAnAddressIsTheLocalEnd(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		b          []byte
		addr, peer string
		flags      uint32
	}{
		{"ethernet", addrMessage(unix.AF_INET, 24, net.ParseIP("10.0.0.1"), net.ParseIP("10.0.0.1"), unix.IFA_F_PERMANENT),
			"10.0.0.1/24", "<nil>", unix.IFA_F_PERMANENT},
		{"point to point", addrMessage(unix.AF_INET, 32, net.ParseIP("100.64.0.2"), net.ParseIP("100.64.0.1"), 0),
			"100.64.0.2/32", "100.64.0.1/32", 0},
		{"ipv6", addrMessage(unix.AF_INET6, 64, nil, net.ParseIP("2001:db8::1"), 0),
			"2001:db8::1/64", "<nil>", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, ok, err := parseAddr(tc.b)
			if err != nil || !ok {
				t.Fatalf("parse: %v %v", ok, err)
			}
			if a.IPNet.String() != tc.addr || a.Peer.String() != tc.peer || a.Flags != tc.flags || a.LinkIndex != 9 {
				t.Errorf("got %s peer %s flags %#x link %d", a.IPNet, a.Peer, a.Flags, a.LinkIndex)
			}
		})
	}
}

func TestALinkReadsItsKindAndCounters(t *testing.T) {
	t.Parallel()
	var e encoder
	e.ifinfomsg(12, unix.IFF_UP|unix.IFF_LOOPBACK, 0)
	e.ifname("wan0.10")
	e.u32(unix.IFLA_LINK, 3)
	e.attr(unix.IFLA_OPERSTATE, []byte{byte(OperUp)})
	var stats []byte
	for i := range 24 {
		stats = native.AppendUint64(stats, uint64(i+1))
	}
	e.attr(unix.IFLA_STATS64, stats)
	e.nest(unix.IFLA_LINKINFO, func() {
		e.attr(unix.IFLA_INFO_KIND, []byte("vlan"))
		e.nest(unix.IFLA_INFO_DATA, func() { e.attr(unix.IFLA_VLAN_ID, native.AppendUint16(nil, 10)) })
	})
	l, err := parseLink(e.b)
	if err != nil {
		t.Fatal(err)
	}
	if l.Index != 12 || l.Name != "wan0.10" || l.Kind != "vlan" || l.VLANID != 10 || l.ParentIndex != 3 ||
		l.OperState != OperUp || l.Flags != net.FlagUp|net.FlagLoopback {
		t.Errorf("link = %+v", l)
	}
	if s := l.Statistics; s == nil || s.RxPackets != 1 || s.RxBytes != 3 || s.TxDropped != 8 {
		t.Errorf("statistics = %+v", l.Statistics)
	}
}

// Conntrack's numbers are big-endian, under two levels of nesting.
func TestAFlowReadsItsNestedBigEndianAttributes(t *testing.T) {
	t.Parallel()
	be := binary.BigEndian
	var e encoder
	e.nfgenmsg(unix.AF_INET6, 0)
	tuple := func(typ uint16, src, dst string, sport, dport uint16) {
		e.nest(typ|unix.NLA_F_NESTED, func() {
			e.nest(ctaTupleIP|unix.NLA_F_NESTED, func() {
				e.attr(ctaIPv6Src, net.ParseIP(src))
				e.attr(ctaIPv6Dst, net.ParseIP(dst))
			})
			e.nest(ctaTupleProto|unix.NLA_F_NESTED, func() {
				e.attr(ctaProtoNum, []byte{unix.IPPROTO_TCP})
				e.attr(ctaProtoSrcPort, be.AppendUint16(nil, sport))
				e.attr(ctaProtoDstPort, be.AppendUint16(nil, dport))
			})
			e.attr(3, be.AppendUint16(nil, 0)) // CTA_TUPLE_ZONE, not nested
		})
	}
	tuple(ctaTupleOrig, "2001:db8::2", "2001:db8::53", 40000, 53)
	tuple(ctaTupleReply, "2001:db8::53", "2001:db8::9", 53, 40000)
	e.nest(ctaCountersOrig|unix.NLA_F_NESTED, func() {
		e.attr(ctaCountersPackets, be.AppendUint64(nil, 3))
		e.attr(ctaCountersBytes, be.AppendUint64(nil, 180))
	})
	e.nest(ctaProtoinfo|unix.NLA_F_NESTED, func() {
		e.nest(ctaProtoinfoTCP|unix.NLA_F_NESTED, func() { e.attr(ctaProtoinfoTCPState, []byte{3}) })
	})
	e.attr(ctaTimeout, be.AppendUint32(nil, 431999))
	e.attr(ctaMark, be.AppendUint32(nil, 0x10000))
	e.attr(ctaID, be.AppendUint32(nil, 0xdeadbeef))
	f, err := parseFlow(e.b)
	if err != nil {
		t.Fatal(err)
	}
	if f.Forward.SrcPort != 40000 || f.Forward.DstPort != 53 || f.Forward.Protocol != unix.IPPROTO_TCP ||
		!f.Reverse.DstIP.Equal(net.ParseIP("2001:db8::9")) || f.Forward.Packets != 3 || f.Forward.Bytes != 180 ||
		f.TCPState != 3 || f.Timeout != 431999 || f.Mark != 0x10000 || f.ID != 0xdeadbeef {
		t.Errorf("flow = %+v", f)
	}
}

// A device's dump runs over several messages: its own attributes come in
// the first, and a peer cut short carries on in the next under the same
// key. The two secrets the dump carries are never read.
func TestAWireGuardDumpMergesItsMessagesAndDropsTheSecrets(t *testing.T) {
	t.Parallel()
	private, psk := bytes.Repeat([]byte{0x11}, 32), bytes.Repeat([]byte{0x22}, 32)
	public := bytes.Repeat([]byte{0x33}, 32)
	k1, k2, k3 := bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32), bytes.Repeat([]byte{3}, 32)
	allowed := func(e *encoder, p netip.Prefix) {
		e.nest(unix.WGPEER_A_ALLOWEDIPS|unix.NLA_F_NESTED, func() {
			e.nest(0|unix.NLA_F_NESTED, func() {
				e.attr(unix.WGALLOWEDIP_A_FAMILY, native.AppendUint16(nil, unix.AF_INET))
				e.attr(unix.WGALLOWEDIP_A_IPADDR, p.Addr().AsSlice())
				e.attr(unix.WGALLOWEDIP_A_CIDR_MASK, []byte{byte(p.Bits())})
			})
		})
	}

	var first encoder
	first.genlmsghdr(unix.WG_CMD_GET_DEVICE)
	first.attr(unix.WGDEVICE_A_IFNAME, []byte("wg0\x00"))
	first.attr(unix.WGDEVICE_A_PRIVATE_KEY, private)
	first.attr(unix.WGDEVICE_A_PUBLIC_KEY, public)
	first.attr(unix.WGDEVICE_A_LISTEN_PORT, native.AppendUint16(nil, 51820))
	first.nest(unix.WGDEVICE_A_PEERS|unix.NLA_F_NESTED, func() {
		first.nest(0|unix.NLA_F_NESTED, func() {
			first.attr(unix.WGPEER_A_PUBLIC_KEY, k1)
			first.attr(unix.WGPEER_A_PRESHARED_KEY, psk)
			first.attr(unix.WGPEER_A_ENDPOINT, sockaddrOf(netip.MustParseAddrPort("[2001:db8::7]:51821")))
			first.attr(unix.WGPEER_A_LAST_HANDSHAKE_TIME, native.AppendUint64(native.AppendUint64(nil, 1790000000), 5))
			first.attr(unix.WGPEER_A_RX_BYTES, native.AppendUint64(nil, 1000))
			first.attr(unix.WGPEER_A_TX_BYTES, native.AppendUint64(nil, 2000))
		})
		first.nest(1|unix.NLA_F_NESTED, func() {
			first.attr(unix.WGPEER_A_PUBLIC_KEY, k2)
			first.attr(unix.WGPEER_A_ENDPOINT, sockaddrOf(netip.MustParseAddrPort("192.0.2.9:40000")))
			first.attr(unix.WGPEER_A_LAST_HANDSHAKE_TIME, make([]byte, 16))
			first.attr(unix.WGPEER_A_RX_BYTES, native.AppendUint64(nil, 7))
			allowed(&first, netip.MustParsePrefix("10.66.0.2/32"))
		})
	})
	var second encoder
	second.genlmsghdr(unix.WG_CMD_GET_DEVICE)
	second.nest(unix.WGDEVICE_A_PEERS|unix.NLA_F_NESTED, func() {
		second.nest(0|unix.NLA_F_NESTED, func() {
			second.attr(unix.WGPEER_A_PUBLIC_KEY, k2)
			allowed(&second, netip.MustParsePrefix("192.168.7.0/24"))
		})
		second.nest(1|unix.NLA_F_NESTED, func() { second.attr(unix.WGPEER_A_PUBLIC_KEY, k3) })
	})

	var dev WireGuardDevice
	for _, m := range [][]byte{first.b, second.b} {
		if err := dev.parse(m); err != nil {
			t.Fatal(err)
		}
	}
	if dev.Name != "wg0" || dev.ListenPort != 51820 || !bytes.Equal(dev.PublicKey, public) {
		t.Errorf("device = %s port %d key %x", dev.Name, dev.ListenPort, dev.PublicKey)
	}
	if len(dev.Peers) != 3 {
		t.Fatalf("peers = %+v, want three: the one cut short is one", dev.Peers)
	}
	p := dev.Peers[0]
	if !bytes.Equal(p.PublicKey, k1) || p.Endpoint.String() != "[2001:db8::7]:51821" ||
		!p.LastHandshake.Equal(time.Unix(1790000000, 5)) || p.RxBytes != 1000 || p.TxBytes != 2000 {
		t.Errorf("first peer = %+v", p)
	}
	if p := dev.Peers[1]; !bytes.Equal(p.PublicKey, k2) || p.Endpoint.String() != "192.0.2.9:40000" ||
		!p.LastHandshake.IsZero() || p.RxBytes != 7 {
		t.Errorf("second peer = %+v, want what its first part said and no handshake", p)
	}
	if !bytes.Equal(dev.Peers[2].PublicKey, k3) {
		t.Errorf("third peer = %+v", dev.Peers[2])
	}
	for what, secret := range map[string][]byte{"private key": private, "preshared key": psk} {
		if strings.Contains(fmt.Sprintf("%x", dev), fmt.Sprintf("%x", secret)) {
			t.Errorf("the %s was kept", what)
		}
	}
}
