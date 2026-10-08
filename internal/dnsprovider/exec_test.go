package dnsprovider

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ostiole/internal/model"
)

// script writes a program that records its arguments in a file, one per
// line, and fails when the first one is "fail".
func script(t *testing.T) (program, argsFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args")
	program = filepath.Join(dir, "hook.sh")
	body := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> " + argsFile + "\necho written\nif [ \"$2\" = fail. ]; then echo 'no such zone' >&2; exit 3; fi\n"
	if err := os.WriteFile(program, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return program, argsFile
}

func runProgram(t *testing.T, mode string, r Record) (string, error) {
	t.Helper()
	path, argsFile := script(t)
	c, err := Build(model.DNSProvider{ID: "hook", Kind: "exec", Settings: map[string]string{"program": path, "mode": mode}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	err = c.AddTXT(context.Background(), r)
	if rerr := c.RemoveTXT(context.Background(), r); err == nil {
		err = rerr
	}
	raw, _ := os.ReadFile(argsFile)
	return strings.TrimSpace(string(raw)), err
}

// The argv scripts written for lego expect: the record's name fully
// qualified and its value, or in RAW mode the challenge itself. Not parallel, nor is any test
// here that writes a program: one written while another test forks can be
// held open in the child and fail to run with "text file busy".
func TestProgramGetsTheArgumentsScriptsExpect(t *testing.T) {
	r := Record{Zone: "example.com", Name: "_acme-challenge.example.com", Value: "v", Domain: "example.com", Token: "tok", KeyAuth: "tok.thumb"}
	got, err := runProgram(t, "", r)
	if err != nil {
		t.Fatal(err)
	}
	if want := "present\n_acme-challenge.example.com.\nv\ncleanup\n_acme-challenge.example.com.\nv"; got != want {
		t.Errorf("argv:\n%s\nwant:\n%s", got, want)
	}
	got, err = runProgram(t, "RAW", r)
	if err != nil {
		t.Fatal(err)
	}
	if want := "present\n--\nexample.com\ntok\ntok.thumb\ncleanup\n--\nexample.com\ntok\ntok.thumb"; got != want {
		t.Errorf("RAW argv:\n%s\nwant:\n%s", got, want)
	}
}

// A program that fails says so with the last thing it printed.
func TestProgramFailureSaysWhy(t *testing.T) {
	_, err := runProgram(t, "", Record{Zone: "fail", Name: "fail", Value: "v"})
	if err == nil || !strings.HasSuffix(err.Error(), "present: exit status 3: no such zone") {
		t.Errorf("err = %v", err)
	}
}
