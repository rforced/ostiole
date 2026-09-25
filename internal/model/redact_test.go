package model

import (
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// secretName is what a field holding a secret is called. A new field whose
// name matches has to be blanked in Redacted or listed in redactExempt
// with a reason, or TestRedactedBlanksEverySecret fails.
var secretName = regexp.MustCompile(`(?i)(password|passphrase|privatekey|presharedkey|secret|authkey|hmac|keypem|token)$`)

// redactExempt are the matching fields a shared backup may keep, and why.
var redactExempt []string

func TestRedactedBlanksEverySecret(t *testing.T) {
	cfg := &Config{}
	filled := fillSecrets(t, reflect.ValueOf(cfg).Elem(), "", nil)
	if len(filled) == 0 {
		t.Fatal("no secret-shaped fields found; the walk is broken")
	}
	left := findSecrets(t, reflect.ValueOf(cfg.Redacted()).Elem(), "", nil)
	for _, path := range left {
		if !slices.Contains(redactExempt, path) {
			t.Errorf("Redacted() left %s set; blank it in redact.go or list it in redactExempt", path)
		}
	}
}

// TestRedactedBlanksProviderSecrets covers the one map in the model. It
// is written against ProviderKinds, so a kind added later is checked
// without anybody remembering to.
func TestRedactedBlanksProviderSecrets(t *testing.T) {
	t.Parallel()
	cfg := &Config{}
	for _, kind := range ProviderKinds {
		p := DNSProvider{ID: kind.Kind, Kind: kind.Kind, Settings: map[string]string{}}
		for _, f := range kind.Fields {
			p.Settings[f.Key] = "x"
		}
		cfg.ACME.Providers = append(cfg.ACME.Providers, p)
	}
	cfg.ACME.Providers = append(cfg.ACME.Providers,
		DNSProvider{ID: "unknown", Kind: "not-built-in", Settings: map[string]string{"anything": "x"}})

	for i, p := range cfg.Redacted().ACME.Providers {
		kind, known := ProviderKindOf(p.Kind)
		for key, value := range p.Settings {
			f, _ := kind.Field(key)
			switch secret := !known || f.Secret; {
			case secret && value != "":
				t.Errorf("acme.providers[%d].settings.%s = %q, want it blanked", i, key, value)
			case !secret && value == "":
				t.Errorf("acme.providers[%d].settings.%s was blanked; it holds no secret", i, key)
			}
		}
	}
}

func TestRedactedDoesNotTouchTheOriginal(t *testing.T) {
	cfg := &Config{Crons: []Cron{{ID: "backup", Passphrase: "hunter2"}}}
	if got := cfg.Redacted().Crons[0].Passphrase; got != "" {
		t.Errorf("redacted passphrase = %q, want empty", got)
	}
	if got := cfg.Crons[0].Passphrase; got != "hunter2" {
		t.Errorf("original passphrase = %q, want it left alone", got)
	}
}

// fillSecrets sets every secret-shaped string in v and returns their
// paths, building whatever pointers and slice elements it has to walk
// through on the way.
func fillSecrets(t *testing.T, v reflect.Value, path string, seen []reflect.Type) []string {
	return walk(t, v, path, seen, true)
}

// findSecrets returns the paths of the secret-shaped strings that are
// still set.
func findSecrets(t *testing.T, v reflect.Value, path string, seen []reflect.Type) []string {
	return walk(t, v, path, seen, false)
}

func walk(t *testing.T, v reflect.Value, path string, seen []reflect.Type, fill bool) []string {
	t.Helper()
	if slices.Contains(seen, v.Type()) {
		return nil
	}
	seen = append(seen, v.Type())
	var out []string
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			if !fill {
				return nil
			}
			v.Set(reflect.New(v.Type().Elem()))
		}
		return walk(t, v.Elem(), path, seen, fill)
	case reflect.Struct:
		for i := range v.NumField() {
			f := v.Type().Field(i)
			if !f.IsExported() {
				continue
			}
			out = append(out, walk(t, v.Field(i), join(path, f.Name), seen, fill)...)
		}
	case reflect.Slice:
		if v.Len() == 0 {
			if !fill {
				return nil
			}
			v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		}
		for i := range v.Len() {
			out = append(out, walk(t, v.Index(i), path+"[]", seen, fill)...)
		}
	case reflect.Map:
		// A map's keys are not field names, so the walk cannot tell a
		// secret from anything else. Every one needs a rule; the provider
		// settings have TestRedactedBlanksProviderSecrets.
		if path != "ACME.Providers[].Settings" {
			t.Errorf("no redaction rule for the map at %s", path)
		}
	case reflect.String:
		name := path[strings.LastIndex(path, ".")+1:]
		if !secretName.MatchString(name) {
			return nil
		}
		if fill {
			v.SetString("x")
			return []string{path}
		}
		if v.String() != "" {
			return []string{path}
		}
	}
	return out
}

func join(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}
