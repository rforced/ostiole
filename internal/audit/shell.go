package audit

import (
	"os"
	"os/user"
	"strconv"
	"strings"
)

// ShellActor is whoever runs the command line: the user they logged in
// as, which sudo and su leave in place, and their SSH client when the
// session has one.
func ShellActor() Actor {
	return shellActor(readLoginUID, os.Getenv, lookupUser, os.Getuid())
}

// unsetLoginUID is what the kernel holds for a process no login started.
const unsetLoginUID = "4294967295"

func shellActor(loginUID func() string, getenv func(string) string, lookup func(uid string) string, uid int) Actor {
	a := Actor{Kind: Shell}
	if id := loginUID(); id != "" && id != unsetLoginUID {
		a.Name = lookup(id)
	}
	if a.Name == "" && uid == 0 {
		a.Name = getenv("SUDO_USER")
	}
	if a.Name == "" {
		a.Name = lookup(strconv.Itoa(uid))
	}
	for _, v := range []string{"SSH_CONNECTION", "SSH_CLIENT"} {
		if f := strings.Fields(getenv(v)); len(f) > 0 {
			a.Address = f[0]
			break
		}
	}
	return a
}

func readLoginUID() string {
	raw, err := os.ReadFile("/proc/self/loginuid")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// lookupUser names a user ID, or gives the number back when it names none.
func lookupUser(uid string) string {
	if u, err := user.LookupId(uid); err == nil {
		return u.Username
	}
	return uid
}
