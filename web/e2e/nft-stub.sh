#!/bin/sh
# Stand-in for nft during UI end-to-end tests: accepts every ruleset and
# reports one loaded table with a counter, plus a foreign table so the
# dashboard has something to warn about. No kernel access needed.
case "$*" in
  --version) echo "nftables v0.0.0 (e2e stub)" ;;
  *"list tables"*) echo '{"nftables":[{"table":{"family":"inet","name":"ostiole"}},{"table":{"family":"ip","name":"nat"}}]}' ;;
  *"list chains"*) echo '{"nftables":[{"metainfo":{"version":"stub"}},{"chain":{"family":"inet","table":"ostiole","name":"zone_lan"}},{"chain":{"family":"ip","table":"nat","name":"POSTROUTING"}}]}' ;;
  *"list table"*) echo '{"nftables":[{"metainfo":{"version":"stub"}},{"rule":{"family":"inet","table":"ostiole","chain":"zone_lan","comment":"id:allow-lan","expr":[{"counter":{"packets":42,"bytes":4200}}]}},{"rule":{"family":"inet","table":"ostiole","chain":"filter_input","comment":"default-drop","expr":[{"counter":{"packets":9,"bytes":540}}]}},{"rule":{"family":"inet","table":"ostiole","chain":"upnp_prerouting","expr":[{"match":{"op":"==","left":{"payload":{"protocol":"udp","field":"dport"}},"right":19132}},{"dnat":{"addr":"192.168.50.40","port":19132}}]}}]}' ;;
  *) cat >/dev/null ;;
esac
exit 0
