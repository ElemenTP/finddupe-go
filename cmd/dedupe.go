package cmd

import (
	"fmt"

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
  finddupe dedupe --delete --ref /originals -- /copies`,
	Args: cobra.MinimumNArgs(1),
	RunE: runDedupe,
}

var dedupeFlags struct {
	delete         bool
	hardlink       bool
	cow            bool
	sigs           bool
	verbose        bool
	zero           bool
	noProgress     bool
	followSymlinks bool
	rdonly         bool
	threads        int
}

func init() {
	rootCmd.AddCommand(dedupeCmd)

	dedupeCmd.Flags().BoolVarP(&dedupeFlags.delete, "delete", "d", false,
		"Delete duplicate files")
	dedupeCmd.Flags().BoolVarP(&dedupeFlags.hardlink, "hardlink", "H", false,
		"Replace duplicates with hardlinks to the original")
	dedupeCmd.Flags().BoolVarP(&dedupeFlags.cow, "cow", "c", false,
		"Replace duplicates with CoW (Copy-on-Write) clones")
	dedupeCmd.Flags().BoolVarP(&dedupeFlags.sigs, "sigs", "s", false,
		"Print file signatures only (no actions taken)")
	dedupeCmd.Flags().BoolVarP(&dedupeFlags.verbose, "verbose", "v", false,
		"Verbose output")
	dedupeCmd.Flags().BoolVarP(&dedupeFlags.zero, "zero", "z", false,
		"Include zero-length files (skipped by default)")
	dedupeCmd.Flags().BoolVarP(&dedupeFlags.noProgress, "no-progress", "p", false,
		"Hide the progress indicator")
	dedupeCmd.Flags().BoolVarP(&dedupeFlags.followSymlinks, "follow-symlinks", "j", false,
		"Follow symbolic links and reparse points")
	dedupeCmd.Flags().BoolVarP(&dedupeFlags.rdonly, "rdonly", "r", false,
		"Also operate on read-only files (Windows)")
	dedupeCmd.Flags().IntVarP(&dedupeFlags.threads, "threads", "t", 0,
		"Number of scanner workers (default: number of CPUs)")
}

// runDedupe builds the config and runs the pipeline in dedupe mode.
func runDedupe(cmd *cobra.Command, args []string) error {
	// Determine the action.
	action, err := validateDedupeFlags()
	if err != nil {
		return err
	}

	// Separate paths and ref patterns.
	paths, refPaths := splitPaths(args)

	cfg := &config.Config{
		Mode:            config.ModeDedupe,
		Action:          action,
		Paths:           paths,
		RefPaths:        refPaths,
		Threads:         dedupeFlags.threads,
		Verbose:         dedupeFlags.verbose,
		PrintSigs:       dedupeFlags.sigs,
		ShowProgress:    !dedupeFlags.noProgress,
		FollowSymlinks:  dedupeFlags.followSymlinks,
		IncludeZeroLen:  dedupeFlags.zero,
		IncludeReadonly: dedupeFlags.rdonly,
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
		return config.ActionReport, fmt.Errorf("no action specified: use --delete, --hardlink, or --cow")
	}
	if count > 1 {
		return config.ActionReport, fmt.Errorf("only one action flag allowed: --delete, --hardlink, or --cow")
	}

	return action, nil
}
