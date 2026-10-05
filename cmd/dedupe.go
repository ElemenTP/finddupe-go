package cmd

import (
	"errors"
	"finddupe/internal/config"
	"finddupe/internal/pipeline"

	"github.com/spf13/cobra"
)

// dedupeCmd represents the dedupe command.
var dedupeCmd = &cobra.Command{
	Use:   "dedupe [flags] <path/pattern> [path/pattern...]",
	Short: "Find and eliminate duplicate files",
	Long: `finddupe dedupe scans the specified paths/patterns for duplicate files
and takes action on them. You must specify exactly one action flag.

Examples:
  finddupe dedupe --delete /data
  finddupe dedupe --hardlink /backup
  finddupe dedupe --cow --threads 8 /btrfs-volume
  finddupe dedupe --delete --ref /originals -- /copies

Keeper selection: when several files have identical content, one of them (the
keeper) keeps its data and the others are eliminated. Which file becomes the
keeper is not deterministic - it is whichever file finishes hashing first on a
multi-core scan. Use --ref to mark the files that must be kept: a reference file
is never eliminated and always becomes the keeper of its content group, so its
duplicates elsewhere are the ones removed.

Note for users of the original Windows finddupe: there, -ref was a terminator
("everything after it is a reference") and references were never preferred over
a normal copy, so the copy outside the reference set was the one kept. Here the
reference path holds the file that is kept.`,
	Args: cobra.MinimumNArgs(1),
	RunE: runDedupe,
}

var dedupeFlags struct {
	delete           bool
	hardlink         bool
	cow              bool
	verbose          bool
	zero             bool
	noProgress       bool
	followSymlinks   bool
	rdonly           bool
	preferCompressed bool
	interactive      bool
	threads          int
	refPaths         []string
}

func init() {
	rootCmd.AddCommand(dedupeCmd)

	dedupeCmd.Flags().BoolVarP(&dedupeFlags.delete, "delete", "d", false,
		"Delete duplicate files")
	dedupeCmd.Flags().BoolVarP(&dedupeFlags.hardlink, "hardlink", "H", false,
		"Replace duplicates with hardlinks to the original")
	dedupeCmd.Flags().BoolVarP(&dedupeFlags.cow, "cow", "c", false,
		"Replace duplicates with CoW (Copy-on-Write) clones")
	dedupeCmd.Flags().BoolVarP(&dedupeFlags.verbose, "verbose", "v", false,
		"Verbose output")
	dedupeCmd.Flags().BoolVarP(&dedupeFlags.zero, "zero", "z", false,
		"Include zero-length files (skipped by default)")
	dedupeCmd.Flags().BoolVarP(&dedupeFlags.noProgress, "no-progress", "p", false,
		"Hide the progress indicator")
	dedupeCmd.Flags().BoolVarP(&dedupeFlags.followSymlinks, "follow-symlinks", "j", false,
		"Follow symbolic links and reparse points")
	dedupeCmd.Flags().BoolVarP(&dedupeFlags.rdonly, "rdonly", "r", false,
		"Also operate on read-only files (skipped by default on every platform)")
	dedupeCmd.Flags().BoolVarP(&dedupeFlags.preferCompressed, "prefer-compressed", "C", false,
		"Keep a compressed member as the CoW clone source (--cow only)")
	dedupeCmd.Flags().BoolVarP(&dedupeFlags.interactive, "interactive", "i", false,
		"Ask which file to keep for every identical-content group (needs a terminal)")
	dedupeCmd.Flags().IntVarP(&dedupeFlags.threads, "threads", "t", 0,
		"Number of scanner workers (default: 2 x CPUs, max 1024)")
	dedupeCmd.Flags().StringArrayVar(&dedupeFlags.refPaths, "ref", nil,
		"Protect this path: its files become the keeper of their content group and are never eliminated; repeatable")
}

// runDedupe builds the config and runs the pipeline in dedupe mode.
func runDedupe(cmd *cobra.Command, args []string) error {
	// Determine the action.
	action, err := validateDedupeFlags()
	if err != nil {
		return err
	}

	cfg := &config.Config{
		Action:           action,
		Paths:            args,
		RefPaths:         dedupeFlags.refPaths,
		Threads:          dedupeFlags.threads,
		Verbose:          dedupeFlags.verbose,
		ShowProgress:     !dedupeFlags.noProgress,
		FollowSymlinks:   dedupeFlags.followSymlinks,
		IncludeZeroLen:   dedupeFlags.zero,
		IncludeReadonly:  dedupeFlags.rdonly,
		PreferCompressed: dedupeFlags.preferCompressed,
		Interactive:      dedupeFlags.interactive,
	}

	return pipeline.Run(cmd.Context(), cfg)
}

// validateDedupeFlags ensures exactly one action flag is set.
func validateDedupeFlags() (config.Action, error) {
	count := 0
	action := config.ActionReport
	if dedupeFlags.delete {
		count++
		action = config.ActionDelete
	}
	if dedupeFlags.hardlink {
		count++
		action = config.ActionHardlink
	}
	if dedupeFlags.cow {
		count++
		action = config.ActionCoWClone
	}

	if count == 0 {
		return config.ActionReport, errors.New("no action specified: use --delete, --hardlink, or --cow")
	}
	if count > 1 {
		return config.ActionReport, errors.New("only one action flag allowed: --delete, --hardlink, or --cow")
	}
	if dedupeFlags.preferCompressed && action != config.ActionCoWClone {
		return config.ActionReport, errors.New(
			"--prefer-compressed only applies to --cow: the clone source is what decides the layout",
		)
	}

	return action, nil
}
