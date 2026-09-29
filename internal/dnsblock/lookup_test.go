package dnsblock

import (
	"slices"
	"testing"
)

func TestLookupSaysWhichListAndPrefersTheDenyList(t *testing.T) {
	cfg := testConfig()
	cfg.Blocking.Deny = []string{"www.ads.example.com"}
	c := testCache(t)

	f := Lookup(OptionsFor(cfg), c, "deep.ads.example.com")
	if !f.Blocked || f.Reason != ReasonList || f.Matched != "ads.example.com" {
		t.Errorf("list lookup = %+v", f)
	}
	if !slices.Contains(f.Lists, "ads") {
		t.Errorf("Lists = %v; want ads among them", f.Lists)
	}

	// The deny entry is the more specific one, so it is the one reported.
	f = Lookup(OptionsFor(cfg), c, "www.ads.example.com")
	if !f.Blocked || f.Reason != ReasonDeny || f.Matched != "www.ads.example.com" {
		t.Errorf("deny lookup = %+v", f)
	}
}

// With the lists off the deny list and the canary still answer, and a name
// nothing names is reported as "the lists are off", not "no list has it".
func TestLookupWithListsOff(t *testing.T) {
	cfg := testConfig()
	cfg.Blocking.Enabled = false
	cfg.Blocking.Deny = []string{"typed.example.com"}
	cfg.Blocking.Enforce.FirefoxCanary = true
	c := testCache(t)

	if f := Lookup(OptionsFor(cfg), c, "ads.example.com"); f.Blocked || f.Reason != ReasonListsOff {
		t.Errorf("a list name with the lists off = %+v", f)
	}
	if f := Lookup(OptionsFor(cfg), c, "sub.typed.example.com"); !f.Blocked || f.Reason != ReasonDeny {
		t.Errorf("a denied name with the lists off = %+v", f)
	}
	if f := Lookup(OptionsFor(cfg), c, FirefoxCanary); !f.Blocked || f.Reason != ReasonCanary {
		t.Errorf("the canary with the lists off = %+v", f)
	}
}

// With the DNS server off nothing is blocked, whatever else is set.
func TestLookupWithDNSOff(t *testing.T) {
	cfg := testConfig()
	cfg.Services.DNS.Enabled = false
	cfg.Blocking.Deny = []string{"typed.example.com"}
	c := testCache(t)
	for _, name := range []string{"ads.example.com", "typed.example.com"} {
		if f := Lookup(OptionsFor(cfg), c, name); f.Blocked || f.Reason != ReasonOff {
			t.Errorf("%s with the DNS server off = %+v", name, f)
		}
	}
}
