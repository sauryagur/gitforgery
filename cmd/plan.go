package cmd

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/sauryagur/gitforgery/internal/plan"
	"github.com/sauryagur/gitforgery/internal/recipe"
)

func newPlanCmd() *cobra.Command {
	var (
		planRecipe string
		planRepo   string
		planRange  string
	)

	cmd := &cobra.Command{
		Use:   "plan --recipe <file> [--repo <path>] [--range <rev>]",
		Short: "Preview a recipe's rewrite as an old→new hash table",
		Long: `plan resolves a recipe's range, matches its rules against every commit,
and prints the old→new commit hash table the rewrite would produce, without
writing anything. Read-only. Exits 0 when the recipe changes nothing,
and matches the counts the apply command reports for the same recipe.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			rec, err := recipe.ParseFile(planRecipe)
			if err != nil {
				return err
			}
			p, err := plan.Build(planRepo, rec, planRange)
			if err != nil {
				return err
			}
			printPlan(cmd.OutOrStdout(), p)
			return nil
		},
	}

	cmd.Flags().StringVar(&planRecipe, "recipe", "", "forgery recipe YAML (required)")
	cmd.Flags().StringVarP(&planRepo, "repo", "C", ".", "source repository path")
	cmd.Flags().StringVar(&planRange, "range", "", "override the recipe's range (git rev-list expression)")
	_ = cmd.MarkFlagRequired("recipe")
	return cmd
}

func printPlan(out io.Writer, p *plan.Plan) {
	c := p.Counts()
	fmt.Fprintf(out, "plan %s (%d commits: %d modified, %d cascaded, %d unchanged)\n",
		p.RangeDesc, c.Total, c.Modified, c.Cascade, c.Total-c.Changed)
	fmt.Fprintf(out, "%-12s %-12s  %-8s %s\n", "old", "new", "kind", "changes")
	for _, cp := range p.Commits {
		kind := "unchanged"
		switch cp.Kind {
		case plan.KindModified:
			kind = "modified"
		case plan.KindCascade:
			kind = "cascade"
		}
		fmt.Fprintf(out, "%-12s %-12s  %-8s %s\n",
			cp.Old.String()[:12], cp.New.String()[:12], kind, joinChanges(cp.Changes))
	}
	if len(p.AffectedRefs) > 0 {
		fmt.Fprintf(out, "refs: %v\n", p.AffectedRefs)
	}
	fmt.Fprintf(out, "verified: %d commit(s) would move; source untouched\n", c.Changed)
}

func joinChanges(changes []string) string {
	s := ""
	for i, ch := range changes {
		if i > 0 {
			s += " "
		}
		s += ch
	}
	return s
}
