// Package main is ostiole-proxy: Caddy with the Coraza and layer 4
// modules compiled in, which is what xcaddy would have produced. Ostiole
// writes its configuration and drives its unit; nothing here is meant to
// be run by hand.
package main

import (
	"fmt"

	caddycmd "github.com/caddyserver/caddy/v2/cmd"

	_ "github.com/caddyserver/caddy/v2/modules/standard"
	_ "github.com/corazawaf/coraza-caddy/v2"
	_ "github.com/mholt/caddy-l4"

	"github.com/rforced/ostiole/internal/version"
)

// `caddy version` reports Caddy's. The install script and the status page
// need the Ostiole release this was built beside.
func init() {
	caddycmd.RegisterCommand(caddycmd.Command{
		Name:  "release",
		Short: "Print the Ostiole release this binary came with",
		Func: func(caddycmd.Flags) (int, error) {
			fmt.Println(version.Version)
			return 0, nil
		},
	})
}

func main() { caddycmd.Main() }
