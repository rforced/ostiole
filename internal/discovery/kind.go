package discovery

// Kind is what a relayed packet is.
type Kind string

// Kinds of packet, mDNS then SSDP.
const (
	KindQuery  Kind = "query"  // mDNS, QR 0, source port 5353
	KindLegacy Kind = "legacy" // mDNS, QR 0, any other source port
	KindProbe  Kind = "probe"  // mDNS query carrying authority records
	KindAnswer Kind = "answer" // mDNS, QR 1
	KindSearch Kind = "search" // SSDP M-SEARCH
	KindReply  Kind = "reply"  // SSDP HTTP/1.1 200 OK
	KindAlive  Kind = "alive"  // SSDP NOTIFY ssdp:alive
	KindByebye Kind = "byebye" // SSDP NOTIFY ssdp:byebye
	KindUpdate Kind = "update" // SSDP NOTIFY ssdp:update
)
