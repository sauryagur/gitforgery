package cmd

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sauryagur/gitforgery/internal/apply"
	"github.com/sauryagur/gitforgery/internal/recipe"
)

func newApplyCmd() *cobra.Command {
	var (
		repoPath   string
		to         string
		signedTags string
		yes        bool
	)

	cmd := &cobra.Command{
		Use:   "apply --recipe <file> --to <repo>",
		Short: "Rewrite history into a NEW repository according to a recipe",
		Long: `apply executes a forgery recipe: it exports the planned range,
transforms identities/dates/messages in the fast-import stream, imports the
result into a fresh bare repository and verifies it (fsck + tree equality)
before reporting.

Safety: apply never touches the source repository's refs; the default output
is a new repository (--to). Rewriting a repository in place is a separate,
explicitly opted-in mode. Mutating runs require --yes.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			rec, err := recipe.ParseFile(recipeFile)
			if err != nil {
				return err
			}
			if to == "" {
				return fmt.Errorf("--to is required (in-place rewriting is not enabled yet)")
			}
			rep, err := apply.Run(context.Background(), apply.Options{
				RepoPath:   repoPath,
				Recipe:     rec,
				To:         to,
				Yes:        yes,
				SignedTags: signedTags,
			})
			if err != nil {
				if errors.Is(err, apply.ErrConfirmRequired) {
					return fmt.Errorf("%w: run 'gitforgery plan' first, then add --yes", err)
				}
				return err
			}
			out := cmd.OutOrStdout()
			_, _ = fmt.Fprintf(out, "applied %s\n", rep.Range)
			_, _ = fmt.Fprintf(out, "source: %s -> target: %s\n", rep.Source, rep.Target)
			_, _ = fmt.Fprintf(out, "commits: %d planned, %d modified, %d cascaded, %d unchanged\n",
				rep.Counts.Total, rep.Counts.Modified, rep.Counts.Cascade, rep.Counts.Total-rep.Counts.Changed)
			_, _ = fmt.Fprintf(out, "verified: fsck --full clean, tree objects untouched\n")
			return nil
		},
	}

	cmd.Flags().StringVar(&recipeFile, "recipe", "", "forgery recipe YAML (required)")
	cmd.Flags().StringVarP(&repoPath, "repo", "C", ".", "source repository path")
	cmd.Flags().StringVar(&to, "to", "", "destination bare repository path (created)")
	cmd.Flags().StringVar(&signedTags, "signed-tags", "strip", "how signed tags are exported: abort|strip|verbatim|warn|warn-strip")
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm the mutation")

	_ = cmd.MarkFlagRequired("recipe")
	return cmd
}

var recipeFile string
