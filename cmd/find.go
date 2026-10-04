package cmd

import (
	"errors"
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
  finddupe find --listlink /data
  finddupe find --cow /btrfs-volume
  finddupe find --threads 8 /large/dataset`,
	Args: cobra.MinimumNArgs(1),
	RunE: runFind,
}

var findFlags struct {
	hardlink       bool
	listlink       bool
	cow            bool
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
	findCmd.Flags().BoolVarP(&findFlags.listlink, "listlink", "l", false,
		"List hardlink groups (files sharing a physical inode) and exit")
	findCmd.Flags().BoolVarP(&findFlags.cow, "cow", "c", false,
		"CoW search mode: list duplicate files that share physical extents")
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
	modes := 0
	if findFlags.hardlink {
		modes++
	}
	if findFlags.listlink {
		modes++
	}
	if findFlags.cow {
		modes++
	}
	if modes > 1 {
		return errors.New("only one of --hardlink, --listlink, or --cow may be used")
	}

	cfg := &config.Config{
		Mode:           config.ModeFind,
		Action:         config.ActionReport,
		Paths:          args,
		RefPaths:       findFlags.refPaths,
		Threads:        findFlags.threads,
		Verbose:        findFlags.verbose,
		ListLink:       findFlags.listlink,
		CoWDetect:      findFlags.cow,
		ShowProgress:   !findFlags.noProgress,
		FollowSymlinks: findFlags.followSymlinks,
		IncludeZeroLen: findFlags.zero,
		SkipHardlinked: findFlags.hardlink,
	}

	return pipeline.Run(cmd.Context(), cfg)
}
