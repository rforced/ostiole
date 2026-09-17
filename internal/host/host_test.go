package host

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/rforced/ostiole/internal/install"
	"github.com/rforced/ostiole/internal/iptables"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/nft"
	"github.com/rforced/ostiole/internal/sysupdate"
)

// fakeUnits is a systemd that answers from a table: what is enabled, what
// is active, and which units it has heard of at all.
type fakeUnits struct {
	enabled map[string]string
	active  map[string]string
	known   map[string]bool
	calls   []string
}

func (f *fakeUnits) Run(_ context.Context, args ...string) (string, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	if len(args) < 2 {
		return "", nil
	}
	unit := args[len(args)-1]
	switch args[0] {
	case "is-enabled":
		if state, ok := f.enabled[unit]; ok {
			return state, nil
		}
		return "not-found", errors.New("not found")
	case "is-active":
		if state, ok := f.active[unit]; ok {
			return state, nil
		}
		return "inactive", errors.New("inactive")
	case "cat":
		if f.known[unit] {
			return "[Unit]", nil
		}
		return "", errors.New("no such unit")
	}
	return "", nil
}

// noKernel is an nftables side that has nothing to say, which is what a
// daemon that is not root gets.
type noKernel struct{}

func (noKernel) ListChains(context.Context) ([]nft.ChainRef, error) {
	return nil, errors.New("not permitted")
}
func (noKernel) DeleteTable(context.Context, string, string) error {
	return errors.New("not permitted")
}

// fakeCommands is every command a test lets this package run. Nothing
// reaches the machine the test is running on: a report asks the package
// manager what is installed, and a test that answered that question by
// running rpm would be querying somebody's workstation.
type fakeCommands struct {
	out  map[string]string
	fail map[string]bool
	// calls is the transcript, so a test can insist on what did and did
	// not run.
	calls []string
}

func (f *fakeCommands) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	line := strings.TrimSpace(name + " " + strings.Join(args, " "))
	f.calls = append(f.calls, line)
	if f.fail[line] {
		return nil, errors.New("failed")
	}
	return []byte(f.out[line]), nil
}

// testPackages is a package manager that answers from a table and runs
// nothing. Root is false so no transient unit is raised either, which is
// the other way a test could reach outside itself.
func testPackages(run *fakeCommands) *sysupdate.Manager {
	m := sysupdate.New(sysupdate.Options{
		PackageManager: "dnf",
		Run:            run,
		Root:           true,
		Log:            slog.New(slog.DiscardHandler),
	})
	// New picks the host runner on a root router with systemd, which wraps
	// every command in systemd-run. The fake is what this test talks to.
	m.Run = run
	return m
}

func testDeps(t *testing.T, cfg *model.Config, units *fakeUnits) Deps {
	t.Helper()
	run := &fakeCommands{}
	return Deps{
		Root:        true,
		Units:       units,
		Packages:    testPackages(run),
		Kernel:      noKernel{},
		Config:      func() *model.Config { return cfg },
		TableLoaded: func(context.Context) bool { return true },
		Backend:     "networkd",
		NFT:         "/nonexistent/nft",
		Dir:         t.TempDir(),
		Run:         run,
		// A router with none of the components installed, whatever the
		// machine running the test has on it.
		Locate: func(string) string { return "" },
		Proc:   t.TempDir(),
		Log:    slog.New(slog.DiscardHandler),
	}
}

// What a router needs installed is read from its configuration: one
// that serves no DHCP is not nagged about dnsmasq, and one that does is
// told which setting is asking.
func TestRequiresReadsTheConfiguration(t *testing.T) {
	t.Parallel()
	empty := &model.Config{}
	for _, key := range []string{"dnsmasq", "unbound", "miniupnpd", "pppd", "tc"} {
		if req, _ := requires(empty, key, "networkd"); req {
			t.Errorf("%s is required by an empty configuration", key)
		}
	}
	// The firewall is the exception: nothing works without it.
	if req, why := requires(empty, "nft", "networkd"); !req || why == "" {
		t.Errorf("nft required = %v (%q), want true with a reason", req, why)
	}
	if req, _ := requires(empty, "networkd", "none"); req {
		t.Error("networkd is required on a router whose addresses Ostiole does not configure")
	}

	full := &model.Config{
		Interfaces: []model.Interface{
			{Name: "wan0", PPPoE: &model.PPPoE{}},
			{Name: "lan0", Shaping: &model.Shaping{}},
		},
		Services: model.Services{
			DHCP: model.DHCPService{Enabled: true},
			DNS:  model.DNSServer{Enabled: true, Resolver: model.ResolverValidate},
			UPnP: model.UPnP{Enabled: true},
		},
	}
	for key, want := range map[string]string{
		"dnsmasq":   "DHCP and DNS",
		"unbound":   "validate",
		"miniupnpd": "port mapping",
		"pppd":      "wan0",
		"tc":        "lan0",
	} {
		req, why := requires(full, key, "networkd")
		if !req {
			t.Errorf("%s is not required by a configuration that uses it", key)
			continue
		}
		if !strings.Contains(why, want) {
			t.Errorf("%s reason = %q, want it to mention %q", key, why, want)
		}
	}
	// Forwarding needs nothing but dnsmasq, so unbound is not dragged in
	// by a DNS service that only forwards.
	forwarding := &model.Config{Services: model.Services{
		DNS: model.DNSServer{Enabled: true, Resolver: model.ResolverForward},
	}}
	if req, _ := requires(forwarding, "unbound", "networkd"); req {
		t.Error("unbound is required by a resolver that forwards in the clear")
	}
}

// The gate reads one field, and it has to be right in both directions.
func TestStatusGatesOnWhatIsOutstanding(t *testing.T) {
	t.Parallel()
	cfg := &model.Config{Services: model.Services{DHCP: model.DHCPService{Enabled: true}}}
	units := &fakeUnits{
		enabled: map[string]string{"firewalld.service": "enabled"},
		active:  map[string]string{"firewalld.service": "active"},
		known:   map[string]bool{"firewalld.service": true},
	}
	d := testDeps(t, cfg, units)
	rep := Status(context.Background(), d)
	if rep.Prepared {
		t.Error("a router with firewalld running reported itself prepared")
	}
	firewall := stepOf(t, rep, StepFirewall)
	if firewall.State != StateOutstanding || !strings.Contains(firewall.Detail, "firewalld") {
		t.Errorf("firewall step = %+v, want firewalld outstanding", firewall)
	}

	// Leaving a step alone stops the gate without pretending it is done.
	if err := SetSkip(d.Dir, StepFirewall, "josh", true); err != nil {
		t.Fatal(err)
	}
	rep = Status(context.Background(), d)
	firewall = stepOf(t, rep, StepFirewall)
	if firewall.State != StateSkipped || firewall.By != "josh" {
		t.Errorf("firewall step = %+v, want skipped by josh", firewall)
	}
	if firewall.Detail == "" {
		t.Error("a skipped step forgot what was outstanding")
	}
	// The packages step is still outstanding, so the gate still holds.
	if rep.Prepared {
		t.Error("prepared while dnsmasq is missing")
	}
	if err := SetSkip(d.Dir, StepPackages, "josh", true); err != nil {
		t.Fatal(err)
	}
	if !Status(context.Background(), d).Prepared {
		t.Error("every step skipped or done, and still not prepared")
	}
	// And the skip can be taken back.
	if err := SetSkip(d.Dir, StepPackages, "josh", false); err != nil {
		t.Fatal(err)
	}
	if Status(context.Background(), d).Prepared {
		t.Error("a skip that was taken back is still silencing the gate")
	}
	if err := SetSkip(d.Dir, "not-a-step", "josh", true); err == nil {
		t.Error("skipped a step that does not exist")
	}
}

// A daemon that is not root can do none of this, so it is never sent to
// the page: a dev run and the end-to-end server must not be gated.
func TestStatusNeverGatesWithoutRoot(t *testing.T) {
	t.Parallel()
	units := &fakeUnits{
		enabled: map[string]string{"firewalld.service": "enabled"},
		active:  map[string]string{"firewalld.service": "active"},
		known:   map[string]bool{"firewalld.service": true},
	}
	d := testDeps(t, &model.Config{}, units)
	d.Root = false
	rep := Status(context.Background(), d)
	if !rep.Prepared {
		t.Error("a daemon that is not root was sent to the host page")
	}
	// The facts are still reported, because being unable to fix something
	// is no reason to hide it.
	if stepOf(t, rep, StepFirewall).State != StateOutstanding {
		t.Error("firewalld running was not reported")
	}
}

// A component's unit is the difference between a package being installed
// and a service being set up.
func TestComponentReadinessNeedsTheUnit(t *testing.T) {
	t.Parallel()
	units := &fakeUnits{known: map[string]bool{}}
	d := testDeps(t, &model.Config{}, units)
	st := components(context.Background(), d, &model.Config{}, "dnf")
	for _, c := range st {
		if c.Key != "dnsmasq" {
			continue
		}
		if c.Unit == "" {
			t.Fatal("dnsmasq has no unit of ours")
		}
		if c.Ready {
			t.Error("dnsmasq is ready with no unit written")
		}
		if got, _ := sysupdate.ComponentByKey("dnsmasq"); got.Key == "" {
			t.Error("the catalogue lost dnsmasq")
		}
	}
	// Outstanding is about what this router needs, not about what it has.
	missing := ComponentState{Key: "dnsmasq", Required: true, Unit: "x", Present: true, Ready: false}
	if !missing.Outstanding() {
		t.Error("an installed package with no unit is not outstanding")
	}
	notWanted := ComponentState{Key: "dnsmasq", Required: false, Unit: "x"}
	if notWanted.Outstanding() {
		t.Error("a component nothing asks for is outstanding")
	}
}

func TestCompetitorPackagesPerManager(t *testing.T) {
	t.Parallel()
	for manager, want := range map[string]string{
		"dnf": "NetworkManager", "apt-get": "network-manager", "pacman": "networkmanager",
	} {
		pkgs, note := CompetitorPackages("NetworkManager", manager)
		if len(pkgs) != 1 || pkgs[0] != want || note != "" {
			t.Errorf("NetworkManager on %s = %v (%q), want [%s]", manager, pkgs, note, want)
		}
	}
	// The one competitor that is never removed, because its package is
	// what Ostiole loads its own ruleset with.
	pkgs, note := CompetitorPackages("nftables", "dnf")
	if len(pkgs) != 0 || !strings.Contains(note, "nft command") {
		t.Errorf("nftables = %v (%q), want nothing to remove and a reason", pkgs, note)
	}
	// A manager Ostiole has no name for is said so, not guessed at.
	if pkgs, note := CompetitorPackages("netplan", "dnf"); len(pkgs) != 0 || note == "" {
		t.Errorf("netplan on dnf = %v (%q), want nothing and an explanation", pkgs, note)
	}
	if pkgs, _ := CompetitorPackages("something-else", "dnf"); len(pkgs) != 0 {
		t.Errorf("an unknown service offered %v", pkgs)
	}
}

// Removing the package under a running service is how a router ends up with
// no addresses and no way to get any.
func TestRemovePackagesRefusesARunningCompetitor(t *testing.T) {
	t.Parallel()
	units := &fakeUnits{
		enabled: map[string]string{"firewalld.service": "enabled"},
		active:  map[string]string{"firewalld.service": "active"},
		known:   map[string]bool{"firewalld.service": true},
	}
	d := testDeps(t, &model.Config{}, units)
	_, err := RemovePackages(context.Background(), d, []string{"firewalld"}, true)
	if err == nil || !strings.Contains(err.Error(), "disable and mask") {
		t.Errorf("err = %v, want it to say to retire the service first", err)
	}
	// And a service this router does not have is refused by name.
	if _, err := RemovePackages(context.Background(), d, []string{"ufw"}, true); err == nil ||
		!strings.Contains(err.Error(), "not a competing service") {
		t.Errorf("err = %v, want it to refuse a service that is not here", err)
	}
}

// nftables is masked like the others and its package stays, so the
// removal refuses with the reason rather than with a shrug.
func TestRemovePackagesRefusesNftables(t *testing.T) {
	t.Parallel()
	units := &fakeUnits{
		enabled: map[string]string{"nftables.service": "disabled"},
		active:  map[string]string{"nftables.service": "inactive"},
		known:   map[string]bool{"nftables.service": true},
	}
	d := testDeps(t, &model.Config{}, units)
	_, err := RemovePackages(context.Background(), d, []string{"nftables"}, true)
	if err == nil || !strings.Contains(err.Error(), "nft command") {
		t.Errorf("err = %v, want the reason nftables stays", err)
	}
}

// Masking the old firewall before Ostiole's ruleset is in the kernel
// leaves the router unfiltered.
func TestTakeoverFirewallNeedsALoadedRuleset(t *testing.T) {
	t.Parallel()
	units := &fakeUnits{
		enabled: map[string]string{"firewalld.service": "enabled"},
		active:  map[string]string{"firewalld.service": "active"},
		known:   map[string]bool{"firewalld.service": true},
	}
	d := testDeps(t, &model.Config{}, units)
	d.TableLoaded = func(context.Context) bool { return false }
	if _, err := TakeoverFirewall(context.Background(), d); err == nil ||
		!strings.Contains(err.Error(), "apply and confirm") {
		t.Errorf("err = %v, want it to ask for a ruleset first", err)
	}

	d.TableLoaded = func(context.Context) bool { return true }
	said, err := TakeoverFirewall(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(said, "firewalld") {
		t.Errorf("said %q, which does not name what was masked", said)
	}
	for _, want := range []string{"disable --now firewalld.service", "mask firewalld.service"} {
		found := false
		for _, c := range units.calls {
			if c == want {
				found = true
			}
		}
		if !found {
			t.Errorf("did not run %q: %v", want, units.calls)
		}
	}
	// A network manager is reported but never masked here: that is the
	// network takeover, and it has a revert timer for a reason.
	for _, c := range units.calls {
		if strings.Contains(c, "NetworkManager") && strings.HasPrefix(c, "mask") {
			t.Errorf("the firewall takeover masked a network manager: %v", units.calls)
		}
	}
}

// Nothing acts without root, whatever the request says.
func TestActionsNeedRoot(t *testing.T) {
	t.Parallel()
	d := testDeps(t, &model.Config{}, &fakeUnits{})
	d.Root = false
	for name, err := range map[string]error{
		"takeover": errOf(TakeoverFirewall(context.Background(), d)),
		"remove":   errOf(RemovePackages(context.Background(), d, []string{"firewalld"}, false)),
		"flush":    errOf(FlushLegacy(context.Background(), d, nil)),
		"network":  errOf(ConfirmNetwork(context.Background(), d)),
	} {
		if !errors.Is(err, ErrNotRoot) {
			t.Errorf("%s = %v, want ErrNotRoot", name, err)
		}
	}
}

func errOf(_ string, err error) error { return err }

// stepOf finds one step in a report.
func stepOf(t *testing.T, rep Report, step string) StepState {
	t.Helper()
	for _, s := range rep.Steps {
		if s.Step == step {
			return s
		}
	}
	t.Fatalf("no %s step in %+v", step, rep.Steps)
	return StepState{}
}

// The network step is about who owns the addresses, and a takeover that
// is waiting to be confirmed is the one thing to do next.
func TestNetworkStepFollowsOwnership(t *testing.T) {
	t.Parallel()
	rep := Report{Network: NetworkState{Backend: "networkd", Managers: []string{"NetworkManager"}}}
	if detail := outstanding(rep, StepNetwork); !strings.Contains(detail, "NetworkManager") {
		t.Errorf("detail = %q, want it to name the manager in charge", detail)
	}
	rep.Network.Owned = true
	if detail := outstanding(rep, StepNetwork); detail != "" {
		t.Errorf("detail = %q, want nothing outstanding once Ostiole owns it", detail)
	}
	rep.Network.Pending = true
	if detail := outstanding(rep, StepNetwork); !strings.Contains(detail, "confirmed") {
		t.Errorf("detail = %q, want the waiting confirmation", detail)
	}
	// A router whose addresses Ostiole does not configure has nothing to
	// take over.
	none := Report{Network: NetworkState{Backend: "none", Managers: []string{"NetworkManager"}}}
	if detail := outstanding(none, StepNetwork); detail != "" {
		t.Errorf("detail = %q, want nothing on a router with no network backend", detail)
	}
}

// The sentences the page shows read as sentences, however many things
// they are about.
func TestOutstandingReadsAsASentence(t *testing.T) {
	t.Parallel()
	rep := Report{Legacy: iptables.Report{Tables: []iptables.Table{
		{Family: "ip", Name: "filter", Backend: iptables.NFT},
		{Family: "ip", Name: "nat", Backend: iptables.NFT},
		{Family: "ip6", Name: "filter", Backend: iptables.NFT},
	}}}
	detail := outstanding(rep, StepLegacy)
	if detail != "ip filter, ip nat and ip6 filter were left behind by an older firewall" {
		t.Errorf("detail = %q", detail)
	}
	one := Report{Legacy: iptables.Report{Tables: []iptables.Table{
		{Family: "ip", Name: "nat", Backend: iptables.NFT},
	}}}
	if detail := outstanding(one, StepLegacy); detail != "ip nat was left behind by an older firewall" {
		t.Errorf("detail = %q", detail)
	}
	if detail := outstanding(Report{}, StepLegacy); detail != "" {
		t.Errorf("detail = %q, want nothing", detail)
	}
}

// The record is the only thing kept on disk, and a file somebody else
// wrote badly must not stop the page loading.
func TestRecordRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if got := Load(dir); len(got.Skips) != 0 {
		t.Errorf("a router that has skipped nothing = %+v", got)
	}
	if err := SetSkip(dir, StepLegacy, "josh", true); err != nil {
		t.Fatal(err)
	}
	rec := Load(dir)
	skip, ok := rec.Skips[StepLegacy]
	if !ok || skip.By != "josh" || skip.At.IsZero() {
		t.Errorf("record = %+v", rec)
	}
	if Load("").Skips != nil {
		t.Error("a router with no configuration directory read a record")
	}
	if err := Save("", Record{}); err == nil {
		t.Error("saved a record with nowhere to put it")
	}
}

// install.Service is embedded, so the JSON the page reads has the unit's
// own fields at the top level. A rename there would break the page
// quietly.
func TestCompetitorStateKeepsTheServiceFields(t *testing.T) {
	t.Parallel()
	st := CompetitorState{Service: install.Service{Name: "firewalld", Kind: "firewall", Active: "active", Enabled: "enabled"}}
	st.Conflicts = st.Service.Conflicts()
	if !st.Conflicts || st.Name != "firewalld" || st.Kind != "firewall" {
		t.Errorf("state = %+v", st)
	}
}

// The components that get a unit of Ostiole's also get a directory the
// daemon has to be able to write, and the daemon only sees a new
// directory after a restart. The API needs to know which those are,
// because it restarts the daemon after answering rather than in the
// middle of the request.
func TestRestartsDaemonForTheServiceComponents(t *testing.T) {
	t.Parallel()
	for keys, want := range map[string]bool{
		"dnsmasq":      true,
		"unbound":      true,
		"tc,unbound":   true,
		"tc":           false,
		"nft,networkd": false,
		"":             false,
	} {
		if got := RestartsDaemon(strings.FieldsFunc(keys, func(r rune) bool { return r == ',' })); got != want {
			t.Errorf("RestartsDaemon(%q) = %v, want %v", keys, got, want)
		}
	}
}

// A daemon that is not running needs no restart: it builds the namespace
// it needs when it next starts. One that is running is restarted from a
// transient unit a moment later, never from inside the request.
func TestRestartDaemonSoonOnlyTouchesARunningDaemon(t *testing.T) {
	t.Parallel()
	units := &fakeUnits{active: map[string]string{}}
	d := testDeps(t, &model.Config{}, units)
	run := d.Run.(*fakeCommands)
	if err := RestartDaemonSoon(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if len(run.calls) != 0 {
		t.Errorf("a stopped daemon was restarted: %v", run.calls)
	}

	units.active[install.DaemonUnit] = "active"
	if err := RestartDaemonSoon(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if len(run.calls) != 1 || !strings.HasPrefix(run.calls[0], "systemd-run --on-active=") ||
		!strings.HasSuffix(run.calls[0], "systemctl restart "+install.DaemonUnit) {
		t.Errorf("calls = %v, want one deferred restart in a unit of its own", run.calls)
	}
}

func TestRemovalRefusesToTakeWhatOstioleNeeds(t *testing.T) {
	t.Parallel()
	protected := []string{"nftables", "dnsmasq", "iproute-tc"}
	for name, plan := range map[string]string{
		"dnf table": `Removing:
 firewalld            noarch   2.4.3-4.el10_2     @baseos   2.2 M
Removing unused dependencies:
 nftables             x86_64   1:1.1.5-6.el10_2   @baseos   1.0 M
 python3-nftables     x86_64   1:1.1.5-6.el10_2   @baseos   0.1 M
Transaction Summary`,
		"dnf transaction": "  Erasing          : nftables-1:1.1.5-6.el10_2.x86_64    3/5",
		"apt":             "Remv firewalld [2.4.3-1]\nRemv nftables [1.1.5-1]\n",
		"apk":             "(1/2) Purging firewalld (2.4.3-r0)\n(2/2) Purging nftables (1.1.5-r0)",
		"zypper":          "The following 2 packages are going to be REMOVED:\n  firewalld nftables\n",
	} {
		err := refuseRemoval(plan, []string{"firewalld"}, protected)
		if err == nil || !strings.Contains(err.Error(), "nftables") || strings.Contains(err.Error(), "python3") {
			t.Errorf("%s: err = %v, want a refusal naming nftables and nothing else", name, err)
		}
	}

	for name, plan := range map[string]string{
		"only the competitor": "Removing:\n firewalld  noarch  2.4.3-4.el10_2  @baseos\n",
		"a lookalike":         "Removing unused dependencies:\n python3-nftables  x86_64  1:1.1.5-6.el10_2\n",
		"empty":               "",
	} {
		if err := refuseRemoval(plan, []string{"firewalld"}, protected); err != nil {
			t.Errorf("%s: refused %v", name, err)
		}
	}

	rep := Report{Components: []ComponentState{
		{Key: "nft", Present: false, Packages: []string{"nftables"}},
		{Key: "dnsmasq", Present: true, Packages: []string{"dnsmasq"}},
		{Key: "unbound", Present: false, Packages: []string{"unbound"}},
	}}
	if got := protectedPackages(rep); strings.Join(got, " ") != "nftables dnsmasq" {
		t.Errorf("protected = %v", got)
	}
}

func TestCompetitorRemovedWhenOnlyTheMaskRemains(t *testing.T) {
	t.Parallel()
	units := &fakeUnits{
		enabled: map[string]string{"firewalld.service": "masked", "nftables.service": "disabled"},
		active:  map[string]string{},
	}
	d := testDeps(t, &model.Config{}, units)
	run := d.Run.(*fakeCommands)
	run.out = map[string]string{"rpm -q firewalld": "package firewalld is not installed"}
	rep := Status(context.Background(), d)
	for _, c := range rep.Competitors {
		switch c.Name {
		case "firewalld":
			if c.Installed || !c.Removed || c.Conflicts {
				t.Errorf("firewalld = %+v, want removed and not in conflict", c)
			}
		case "nftables":
			if c.Removed {
				t.Errorf("nftables = %+v, want not removed", c)
			}
		}
	}
	if got := stepOf(t, rep, StepFirewall); got.State != StateDone {
		t.Errorf("firewall step = %+v, want done: a removed competitor is not outstanding", got)
	}
	if got := stepOf(t, rep, StepPackages); got.State != StateOutstanding {
		t.Errorf("packages step = %+v, want outstanding", got)
	}
	if rep.Prepared {
		t.Error("a router with nothing installed was called prepared")
	}
}
