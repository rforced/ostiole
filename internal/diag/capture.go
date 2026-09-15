package diag

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"time"
)

// CaptureOptions parameterise a packet capture.
type CaptureOptions struct {
	// Interface to listen on; required.
	Interface string
	// Count stops the capture after this many packets.
	Count int
	// Duration stops it after this long, whichever comes first.
	Duration time.Duration
	// Snaplen is how much of each packet to keep.
	Snaplen int
	// Address, when set, keeps only packets to or from it.
	Address string
	// Port, when set, keeps only packets with it as source or destination.
	Port int
}

// Capture limits, so a diagnostic cannot fill the disk or run forever.
const (
	MaxCapturePackets = 5000
	MaxCaptureSeconds = 120
	DefaultSnaplen    = 262144
)

// pcap header constants: microsecond timestamps, Ethernet frames. The
// version is two 16-bit fields, not one 32-bit number, so writing it as
// one produces a file that says version 4.2 and that tcpdump refuses.
const (
	pcapMagic      = 0xa1b2c3d4
	pcapVersionMaj = 2
	pcapVersionMin = 4
	linkTypeEth    = 1
)

// Capture writes a pcap file to w, reading frames straight from an
// AF_PACKET socket so no libpcap (and so no cgo) is needed.
func Capture(ctx context.Context, w io.Writer, o CaptureOptions) (int, error) {
	iface, err := net.InterfaceByName(o.Interface)
	if err != nil {
		return 0, fmt.Errorf("interface %q: %w", o.Interface, err)
	}
	count := o.Count
	if count <= 0 || count > MaxCapturePackets {
		count = MaxCapturePackets
	}
	duration := o.Duration
	if duration <= 0 || duration > MaxCaptureSeconds*time.Second {
		duration = MaxCaptureSeconds * time.Second
	}
	snaplen := o.Snaplen
	if snaplen <= 0 || snaplen > DefaultSnaplen {
		snaplen = DefaultSnaplen
	}

	sock, err := openCaptureSocket(iface.Index)
	if err != nil {
		return 0, err
	}
	defer sock.Close()

	if err := writePcapHeader(w, snaplen); err != nil {
		return 0, err
	}

	deadline := time.Now().Add(duration)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	buf := make([]byte, 65536)
	written := 0
	for written < count && time.Now().Before(deadline) {
		if err := sock.SetReadDeadline(minTime(deadline, time.Now().Add(time.Second))); err != nil {
			return written, err
		}
		n, err := sock.Read(buf)
		if err != nil {
			if isTimeout(err) {
				if ctx.Err() != nil {
					break
				}
				continue
			}
			return written, err
		}
		frame := buf[:n]
		if !keep(frame, o) {
			continue
		}
		if err := writePcapPacket(w, frame, snaplen, time.Now()); err != nil {
			return written, err
		}
		written++
		if f, ok := w.(interface{ Flush() error }); ok {
			_ = f.Flush()
		}
	}
	return written, nil
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func writePcapHeader(w io.Writer, snaplen int) error {
	var h [24]byte
	binary.LittleEndian.PutUint32(h[0:], pcapMagic)
	binary.LittleEndian.PutUint16(h[4:], pcapVersionMaj)
	binary.LittleEndian.PutUint16(h[6:], pcapVersionMin)
	binary.LittleEndian.PutUint32(h[16:], uint32(snaplen)) //nolint:gosec // bounded above
	binary.LittleEndian.PutUint32(h[20:], linkTypeEth)
	_, err := w.Write(h[:])
	return err
}

func writePcapPacket(w io.Writer, frame []byte, snaplen int, at time.Time) error {
	captured := frame
	if len(captured) > snaplen {
		captured = captured[:snaplen]
	}
	var rec [16]byte
	binary.LittleEndian.PutUint32(rec[0:], uint32(at.Unix()))            //nolint:gosec // wall clock
	binary.LittleEndian.PutUint32(rec[4:], uint32(at.Nanosecond()/1000)) //nolint:gosec // below 1e6
	binary.LittleEndian.PutUint32(rec[8:], uint32(len(captured)))        //nolint:gosec // bounded by snaplen
	binary.LittleEndian.PutUint32(rec[12:], uint32(len(frame)))          //nolint:gosec // frame length
	if _, err := w.Write(rec[:]); err != nil {
		return err
	}
	_, err := w.Write(captured)
	return err
}

// keep applies the address and port filter in user space. A capture this
// small does not need a compiled BPF program, and writing one by hand is
// a good way to drop the wrong packets.
func keep(frame []byte, o CaptureOptions) bool {
	if o.Address == "" && o.Port == 0 {
		return true
	}
	src, dst, sport, dport, ok := parseAddresses(frame)
	if !ok {
		return false
	}
	if o.Address != "" {
		want := net.ParseIP(o.Address)
		if want == nil || (!want.Equal(src) && !want.Equal(dst)) {
			return false
		}
	}
	if o.Port != 0 && o.Port != sport && o.Port != dport {
		return false
	}
	return true
}

// parseAddresses pulls the addresses and ports out of an Ethernet frame,
// far enough to filter on: IPv4 and IPv6, TCP and UDP.
func parseAddresses(frame []byte) (src, dst net.IP, sport, dport int, ok bool) {
	if len(frame) < 14 {
		return nil, nil, 0, 0, false
	}
	ethType := binary.BigEndian.Uint16(frame[12:14])
	payload := frame[14:]
	if ethType == 0x8100 { // VLAN tag
		if len(payload) < 4 {
			return nil, nil, 0, 0, false
		}
		ethType = binary.BigEndian.Uint16(payload[2:4])
		payload = payload[4:]
	}
	var proto byte
	var rest []byte
	switch ethType {
	case 0x0800:
		if len(payload) < 20 {
			return nil, nil, 0, 0, false
		}
		hdr := int(payload[0]&0x0f) * 4
		if hdr < 20 || len(payload) < hdr {
			return nil, nil, 0, 0, false
		}
		src, dst = net.IP(payload[12:16]), net.IP(payload[16:20])
		proto, rest = payload[9], payload[hdr:]
	case 0x86dd:
		if len(payload) < 40 {
			return nil, nil, 0, 0, false
		}
		src, dst = net.IP(payload[8:24]), net.IP(payload[24:40])
		proto, rest = payload[6], payload[40:]
	default:
		return nil, nil, 0, 0, false
	}
	if (proto == 6 || proto == 17) && len(rest) >= 4 {
		sport = int(binary.BigEndian.Uint16(rest[0:2]))
		dport = int(binary.BigEndian.Uint16(rest[2:4]))
	}
	return src, dst, sport, dport, true
}
