#!/bin/sh
# Stand-in for nft during UI end-to-end tests: accepts every ruleset and
# reports one loaded table with a counter, so no kernel access is needed.
case "$*" in
  --version) echo "nftables v0.0.0 (e2e stub)" ;;
  *"list table"*) echo '{"nftables":[{"metainfo":{"version":"stub"}},{"rule":{"family":"inet","table":"ostiole","chain":"zone_lan","comment":"id:allow-lan","expr":[{"counter":{"packets":42,"bytes":4200}}]}}]}' ;;
  *) cat >/dev/null ;;
esac
exit 0
