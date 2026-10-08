// Package main is the ostiole command: a firewall and router appliance manager for Linux nftables.
package main

import (
	"os"

	"ostiole/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:]))
}
