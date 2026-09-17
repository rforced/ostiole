package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/rforced/ostiole/internal/dnsblock"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/services"
	"github.com/rforced/ostiole/internal/version"
)

func newDNSBlockCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dnsblock",
		Short: "Show what DNS blocking is doing",
		Long: `DNS blocking refuses to resolve the names on the lists this router
subscribes to. The lists are fetched on a schedule and cached here, so a
reboot without a working line blocks what it blocked yesterday.

Without arguments this reports each list. "refresh" fetches now, and "why"
answers the question every support conversation starts with.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := g.store().Load()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			switch {
			case !cfg.Blocking.Enabled:
				fmt.Fprintln(out, "DNS blocking is off")
			case !cfg.Services.DNS.Enabled:
				fmt.Fprintln(out, "DNS blocking is on but the DNS server is off, so nothing is being refused")
			default:
				fmt.Fprintf(out, "DNS blocking is on, answering %s\n", cfg.Blocking.BlockMode())
			}
			statuses := g.blocklists().Statuses(cfg)
			if len(statuses) == 0 {
				fmt.Fprintln(out, "no lists are subscribed to")
				return nil
			}
			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "LIST\tON\tNAMES\tFORMAT\tFETCHED\tNOTE")
			for _, s := range statuses {
				note := ""
				switch {
				case s.LastError != "":
					note = s.LastError
				case s.Stale:
					note = "stale"
				case s.Skipped > 0:
					note = fmt.Sprintf("%d lines skipped", s.Skipped)
				}
				fetched := "never"
				if !s.FetchedAt.IsZero() {
					fetched = s.FetchedAt.Local().Format(time.RFC3339)
				}
				fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\t%s\n",
					s.Name, yesNo(s.Enabled), s.Domains, s.Format, fetched, note)
			}
			if err := w.Flush(); err != nil {
				return err
			}
			fmt.Fprintf(out, "\n%d names across %d enabled list(s), before merging\n",
				g.blocklists().Total(cfg), len(cfg.Blocking.EnabledLists()))
			return nil
		},
	}
	cmd.AddCommand(newDNSBlockRefreshCmd(g), newDNSBlockWhyCmd(g), newDNSBlockCatalogCmd(), newDNSBlockRenderCmd(g))
	return cmd
}

func newDNSBlockRenderCmd(g *globals) *cobra.Command {
	var count bool
	c := &cobra.Command{
		Use:   "render",
		Short: "Write the dnsmasq configuration the lists come to",
		Long: `Merges the cached lists exactly as an apply would and writes the result,
so what dnsmasq will be told can be read before it is told. The merged
list is much smaller than the lists added together: they overlap heavily,
and a name whose parent is already blocked is left out.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := g.store().Load()
			if err != nil {
				return err
			}
			o := dnsblock.OptionsFor(cfg)
			w := cmd.OutOrStdout()
			if count {
				w = io.Discard
			}
			res, err := dnsblock.Render(w, o, g.blocklists())
			if err != nil {
				return err
			}
			if count {
				fmt.Fprintf(cmd.OutOrStdout(), "%d names blocked, %d allowed\n", res.Domains, res.Allowed)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&count, "count", false, "report how many names it comes to instead of writing them")
	return c
}

// refresher builds one that writes through to dnsmasq when this is running
// as root, and only to the cache when it is not.
func (g *globals) blockRefresher() *dnsblock.Refresher {
	r := &dnsblock.Refresher{
		Cache:   g.blocklists(),
		Fetcher: dnsblock.NewFetcher(version.Version),
		Source: func() *model.Config {
			cfg, err := g.store().Load()
			if err != nil {
				return nil
			}
			return cfg
		},
	}
	if os.Geteuid() == 0 {
		r.Loader = services.NewDNSBlock(g.blocklists())
	}
	return r
}

func newDNSBlockRefreshCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "refresh [list]",
		Short: "Fetch the blocklists now and hand them to dnsmasq",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r := g.blockRefresher()
			if len(args) == 1 {
				count, err := r.RefreshOne(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s: %d names\n", args[0], count)
				return nil
			}
			r.Tick(cmd.Context(), true)
			cfg, err := g.store().Load()
			if err != nil {
				return err
			}
			for _, s := range g.blocklists().Statuses(cfg) {
				if s.LastError != "" {
					fmt.Fprintf(cmd.ErrOrStderr(), "%s: %s\n", s.Name, s.LastError)
					continue
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s: %d names\n", s.Name, s.Domains)
			}
			return nil
		},
	}
}

func newDNSBlockWhyCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "why <name>",
		Short: "Say whether a name is blocked, and what blocks it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := g.store().Load()
			if err != nil {
				return err
			}
			f := dnsblock.Lookup(dnsblock.OptionsFor(cfg), g.blocklists(), args[0])
			out := cmd.OutOrStdout()
			switch {
			case f.Reason == dnsblock.ReasonNotAName:
				fmt.Fprintf(out, "%q is not a domain name\n", args[0])
			case f.Blocked:
				fmt.Fprintf(out, "%s is blocked (%s), answered %s\n", f.Name, f.Mode, blockedBy(f))
				if len(f.Lists) > 0 {
					fmt.Fprintf(out, "  on: %s\n", strings.Join(f.Lists, ", "))
				}
			case f.Reason == dnsblock.ReasonAllow:
				fmt.Fprintf(out, "%s is not blocked: the allow list has %s\n", f.Name, f.Matched)
			case f.Reason == dnsblock.ReasonNever:
				fmt.Fprintf(out, "%s is not blocked: this router answers for %s itself\n", f.Name, f.Matched)
			case f.Reason == dnsblock.ReasonOff:
				fmt.Fprintf(out, "%s is not blocked: DNS blocking is off\n", f.Name)
			default:
				fmt.Fprintf(out, "%s is not blocked: no list has it\n", f.Name)
			}
			return nil
		},
	}
}

// blockedBy describes the entry that settled it, which is often a parent of
// the name that was asked about.
func blockedBy(f dnsblock.Finding) string {
	switch f.Reason {
	case dnsblock.ReasonDeny:
		return fmt.Sprintf("because the deny list has %s", f.Matched)
	case dnsblock.ReasonCanary:
		return "because it is the name Firefox uses to ask about DNS over HTTPS"
	default:
		if f.Matched != f.Name {
			return fmt.Sprintf("because a list has %s, which covers it", f.Matched)
		}
		return "because a list has it"
	}
}

func newDNSBlockCatalogCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "catalog",
		Short: "List the published blocklists Ostiole can subscribe to",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tNAMES\tFORMAT\tCATEGORY\tURL")
			for _, e := range dnsblock.Catalog() {
				fmt.Fprintf(w, "%s\t~%d\t%s\t%s\t%s\n", e.Name, e.Names, e.Format, e.Category, e.URL)
			}
			return w.Flush()
		},
	}
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
