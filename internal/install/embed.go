package install

import _ "embed"

// Script is the installer the documentation tells people to pipe into sh.
// `ostiole repair` writes it out and runs it, so a router is repaired by
// the same script that installed it. It lives in this package because an
// embed directive cannot reach outside its own directory.
//
//go:embed install.sh
var Script string
