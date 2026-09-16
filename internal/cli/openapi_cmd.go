package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/rforced/ostiole/internal/server"
)

func newOpenAPICmd() *cobra.Command {
	var out, url string
	cmd := &cobra.Command{
		Use:   "openapi",
		Short: "Print the OpenAPI description of the API",
		Long: `Writes the OpenAPI description, which is generated from the routes the
server actually registers. A running box serves the same document at
/api/v1/openapi.json; this prints it without one, for generating a client
or checking it into a repository.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			doc, err := server.OpenAPIDocument(url)
			if err != nil {
				return err
			}
			if out == "" || out == "-" {
				_, err := cmd.OutOrStdout().Write(doc)
				return err
			}
			if err := os.WriteFile(out, doc, 0o644); err != nil { //nolint:gosec // a public description
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "wrote %s\n", out)
			return nil
		},
	}
	cmd.Flags().StringVarP(&out, "out", "o", "", "file to write, or - for standard output")
	cmd.Flags().StringVar(&url, "url", "https://firewall.local", "the server URL to name in the description")
	return cmd
}
