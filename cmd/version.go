package cmd

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

var (
	Version   = "2.0.0"
	BuildTime = "unknown"
)

// versionCmd represents the version command
var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Show version information",
	Long:  "Display the version of finddupe, build information, and runtime details.",
	Args:  cobra.NoArgs,
	Run: func(_ *cobra.Command, _ []string) {
		fmt.Printf("finddupe %s (Go rewrite with multi-threading support)\n", Version)
		fmt.Printf("Built: %s\n", BuildTime)
		fmt.Printf("Platform: %s/%s\n", runtime.GOOS, runtime.GOARCH)
		fmt.Printf("Go version: %s\n", runtime.Version())
		fmt.Printf("CPUs: %d\n", runtime.NumCPU())
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
