package cmd

import (
	"finddupe/internal/config"
	"finddupe/internal/pipeline"

	"github.com/spf13/cobra"
)

// findCmd represents the find command.
var findCmd = &cobra.Command{
	Use:   "find [flags] <path/pattern> [path/pattern...]",
	Short: "Find and report duplicate files",
	Long: `finddupe find scans the specified paths/patterns for duplicate files and reports them.
No files are modified.

Examples:
  finddupe find /home/user/photos
  finddupe find /data/**/*.jpg
  finddupe find --hardlink /backup
  finddupe find --sigs /path/to/files
  finddupe find --threads 8 /large/dataset`,
	Args: cobra.MinimumNArgs(1),
	RunE: runFind,
}

var findFlags struct {
	hardlink       bool
	cow            bool
	sigs           bool
	verbose        bool
	zero           bool
	noProgress     bool
	followSymlinks bool
	threads        int
	refPaths       []string
}

func init() {
	rootCmd.AddCommand(findCmd)

	findCmd.Flags().BoolVarP(&findFlags.hardlink, "hardlink", "H", false,
		"Skip already-hardlinked files when reporting duplicates")
	findCmd.Flags().BoolVarP(&findFlags.cow, "cow", "c", false,
		"CoW search mode: find groups of CoW-cloned files")
	findCmd.Flags().BoolVarP(&findFlags.sigs, "sigs", "s", false,
		"Print file signatures only (no duplicate detection)")
	findCmd.Flags().BoolVarP(&findFlags.verbose, "verbose", "v", false,
		"Verbose output: show hardlink counts, file index details")
	findCmd.Flags().BoolVarP(&findFlags.zero, "zero", "z", false,
		"Include zero-length files (skipped by default)")
	findCmd.Flags().BoolVarP(&findFlags.noProgress, "no-progress", "p", false,
		"Hide the progress indicator")
	findCmd.Flags().BoolVarP(&findFlags.followSymlinks, "follow-symlinks", "j", false,
		"Follow symbolic links and reparse points")
	findCmd.Flags().IntVarP(&findFlags.threads, "threads", "t", 0,
		"Number of scanner workers (default: number of CPUs)")
	findCmd.Flags().StringArrayVar(&findFlags.refPaths, "ref", nil,
		"Mark following path as reference (compare against but never act upon); repeatable")
}

// runFind builds the config and runs the pipeline in find mode.
func runFind(cmd *cobra.Command, args []string) error {
	cfg := &config.Config{
		Mode:           config.ModeFind,
		Action:         config.ActionReport,
		Paths:          args,
		RefPaths:       findFlags.refPaths,
		Threads:        findFlags.threads,
		Verbose:        findFlags.verbose,
		PrintSigs:      findFlags.sigs,
		ShowProgress:   !findFlags.noProgress,
		FollowSymlinks: findFlags.followSymlinks,
		IncludeZeroLen: findFlags.zero,
		SkipHardlinked: findFlags.hardlink,
	}

	return pipeline.Run(cmd.Context(), cfg)
}
