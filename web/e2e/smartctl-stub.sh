#!/bin/sh
# Stand-in for smartctl during UI end-to-end tests: answers from the
# documents the real one printed on a router, so the page is built from
# the same JSON the parser is tested against. No drive is touched.
DATA="$(dirname "$0")/../../internal/smart/testdata"
case " $* " in
  *" --scan-open "*) cat "$DATA/scan.json" ;;
  *" -t "* | *" -X "*) echo '{"smartctl":{"version":[7,5],"exit_status":0}}' ;;
  *" --json=c "*) cat "$DATA/sat-ssd.json" ;;
  *)
    echo "smartctl 7.5 2025-04-30 r5714 [x86_64-linux] (e2e stub)"
    echo "Device Model:     GOFATOO 256GB SSD"
    ;;
esac
exit 0
