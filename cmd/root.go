// Package cmd wires up the gitforgery command tree.
package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var version = "dev"

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "gitforgery",
		Short: "Craft, fabricate, and audit git history",
		Long: `gitforgery crafts git history: rewrite author/committer identities,
dates, and messages with a declarative recipe (forge), synthesize
plausible histories from nothing (fabricate), and scan a repo for the
residue a rewrite leaves behind (audit).

Metadata in a git commit is forgeable by design; this tool automates
that surface honestly. Signatures cannot be forged — any rewrite
invalidates them — and rewrites always leave detectable fingerprints
(run "gitforgery audit").`,
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	root.SetVersionTemplate("gitforgery {{.Version}}\n")

	root.AddCommand(newVersionCmd())

	return root
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the gitforgery version",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintf(cmd.OutOrStdout(), "gitforgery %s\n", version)
			return nil
		},
	}
}

// Execute runs the root command.
func Execute() error {
	return newRootCmd().Execute()
}
