package wirelesslog

import (
	"encoding/json"
	"slices"
	"testing"

	"ostiole/internal/logsearch"
	"ostiole/internal/logsearch/logsearchtest"
)

func TestParseReadsAClientsComingAndGoing(t *testing.T) {
	t.Parallel()
	for line, want := range map[string]Event{
		"wlan0: AP-STA-CONNECTED 12:34:56:78:9A:BC":                  {Event: Connected, Interface: "wlan0", MAC: "12:34:56:78:9a:bc"},
		"wlan0-1: AP-STA-DISCONNECTED 12:34:56:78:9a:bc":             {Event: Disconnected, Interface: "wlan0-1", MAC: "12:34:56:78:9a:bc"},
		"guest: AP-STA-POSSIBLE-PSK-MISMATCH 12:34:56:78:9a:bc":      {Event: WrongPassword, Interface: "guest", MAC: "12:34:56:78:9a:bc"},
		"wlan0: AP-STA-CONNECTED 12:34:56:78:9a:bc keyid=home aid=1": {Event: Connected, Interface: "wlan0", MAC: "12:34:56:78:9a:bc"},
	} {
		if got, ok := Parse(line); !ok || got != want {
			t.Errorf("%q: %+v, %v", line, got, ok)
		}
	}
	for _, line := range []string{
		"wlan0: AP-ENABLED",
		"wlan0: STA 12:34:56:78:9a:bc IEEE 802.11: associated (aid 1)",
		"wlan0: AP-STA-CONNECTED",
		"wlan0: AP-STA-CONNECTED nonsense",
		"wlan0: AP-STA-POLL-OK 12:34:56:78:9a:bc",
		"Configuration file: /etc/ostiole/wireless/hostapd-wlan0.conf",
	} {
		if e, ok := Parse(line); ok {
			t.Errorf("%q read as %+v", line, e)
		}
	}
}

// The router searches what the Log tab shows, value for value.
func TestSearchValuesAreThePages(t *testing.T) {
	t.Parallel()
	for _, c := range logsearchtest.Cases(t, "wireless") {
		var row struct {
			Event
			Network string `json:"network"`
			Device  string `json:"device"`
		}
		if err := json.Unmarshal(c.Entry, &row); err != nil {
			t.Fatal(err)
		}
		var got logsearchtest.Recorder
		row.Search(&got, nil, row.Network, row.Device)
		if !slices.Equal(got, c.Values) {
			t.Errorf("%s: %q, want %q", c.Why, got, c.Values)
		}
	}
	e, _ := Parse("wlan0: AP-STA-CONNECTED 12:34:56:78:9a:bc")
	names := func(*Event) (string, string) { return "Home", "phone" }
	if !Matcher(logsearch.Parse("home phone connected"), names)(&e) || Matcher(logsearch.Parse("guest"), names)(&e) {
		t.Error("a search did not read the names")
	}
}
