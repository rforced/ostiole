#!/bin/sh
# Stand-in for tc during UI end-to-end tests: swallows every batch and
# reports one of our queues on every device this host has, with the four
# tins a diffserv4 queue carries. The device names come from the host
# because the wizard picks the interfaces from the same list, and a stub
# that named its own would never line up with what was shaped.
#
# Only the queue that hangs off a device itself is reported. The other
# direction hangs off a helper device whose name is hashed when the
# interface name is long, and guessing that in shell is not worth it.
# The server is not root, so nothing here would reach a kernel anyway.

tins() {
  cat <<'JSON'
"tins":[
{"threshold_rate":156250,"sent_bytes":40000,"sent_packets":40,"backlog_bytes":0,"peak_delay_us":21000,"avg_delay_us":9000,"drops":12,"ecn_mark":0,"sparse_flows":0,"bulk_flows":1,"unresponsive_flows":0},
{"threshold_rate":2500000,"sent_bytes":600000,"sent_packets":700,"backlog_bytes":0,"peak_delay_us":4200,"avg_delay_us":1100,"drops":3,"ecn_mark":0,"sparse_flows":2,"bulk_flows":0,"unresponsive_flows":0},
{"threshold_rate":1250000,"sent_bytes":180000,"sent_packets":140,"backlog_bytes":0,"peak_delay_us":1800,"avg_delay_us":700,"drops":0,"ecn_mark":0,"sparse_flows":1,"bulk_flows":0,"unresponsive_flows":0},
{"threshold_rate":625000,"sent_bytes":20000,"sent_packets":20,"backlog_bytes":0,"peak_delay_us":900,"avg_delay_us":300,"drops":0,"ecn_mark":0,"sparse_flows":1,"bulk_flows":0,"unresponsive_flows":0}
]
JSON
}

qdiscs() {
  printf '['
  first=1
  for path in /sys/class/net/*; do
    dev=${path##*/}
    [ "$dev" = "*" ] && continue
    [ "$dev" = "lo" ] && continue
    [ "$first" = 1 ] || printf ','
    first=0
    printf '{"kind":"cake","handle":"571:","dev":"%s","root":true,' "$dev"
    printf '"options":{"bandwidth":2500000,"diffserv":"diffserv4","flowmode":"dual-srchost","nat":true,"ingress":false},'
    printf '"bytes":840000,"packets":900,"drops":0,"backlog":0,"memory_used":4096,"memory_limit":4194304,'
    tins
    printf '}'
  done
  printf ']\n'
}

case "$*" in
  -V) echo "tc utility, iproute2-0.0.0 (e2e stub)" ;;
  *"-batch"*) cat >/dev/null ;;
  *"filter show"*) echo '[]' ;;
  *"qdisc show"*) qdiscs ;;
  *) cat >/dev/null ;;
esac
exit 0
