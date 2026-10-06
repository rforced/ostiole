package nft

import (
	"strings"
	"testing"
)

// Two renders of one configuration a refresh apart differ in what the
// fetched sets hold alone, and that is what WithoutFetched takes out. What
// the configuration writes into a set stays.
func TestWithoutFetchedLeavesWhatTheConfigurationWrites(t *testing.T) {
	t.Parallel()
	cfg := loadConfig(t, "testdata/feeds.json")
	before, err := RenderWithFeeds(cfg, map[string][]string{
		"drop": {"192.0.2.0/24"}, "countries": {"198.51.100.0/24"}, "google": {"2001:db8::/32"},
	})
	if err != nil {
		t.Fatal(err)
	}
	after, err := RenderWithFeeds(cfg, map[string][]string{"drop": {"192.0.2.0/25", "2001:db8:1::/48"}})
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("the feeds made no difference to the render")
	}
	plain := WithoutFetched(cfg, before)
	if plain != WithoutFetched(cfg, after) {
		t.Errorf("the renders still differ:\n%s\n---\n%s", plain, WithoutFetched(cfg, after))
	}
	if !strings.Contains(plain, "elements = { 198.51.100.10 }") {
		t.Errorf("the written alias lost its elements:\n%s", plain)
	}
	if strings.Contains(plain, "192.0.2.0") || strings.Contains(plain, "2001:db8") {
		t.Errorf("fetched elements are left:\n%s", plain)
	}
	// The sets themselves stay, as an apply that changed them would change
	// the render.
	if !strings.Contains(plain, "set alias_drop_v4 {") || !strings.Contains(plain, "set alias_google_v6 {") {
		t.Errorf("a fetched set went:\n%s", plain)
	}
}

func TestWithoutFetchedTakesTheBogons(t *testing.T) {
	t.Parallel()
	cfg := loadConfig(t, "testdata/blocked-sources.json")
	with, err := RenderWithFeeds(cfg, map[string][]string{BogonFeed: {"192.0.2.0/24", "2001:db8::/32"}})
	if err != nil {
		t.Fatal(err)
	}
	without, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if WithoutFetched(cfg, with) != without {
		t.Errorf("the bogons are left:\n%s", WithoutFetched(cfg, with))
	}
}
