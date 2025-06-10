package cmd

import (
	"fmt"
	"os"

	"go-image-builder/pkg/builder"
	"go-image-builder/pkg/imageconfig"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

var buildCmd = &cobra.Command{
	Use:   "build",
	Short: "Build an image from a configuration file",
	Long: `Build an image from a configuration file. The configuration file specifies
the package manager, packages to install, and other customization options.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Check for the avialability of user namespaces if invoked as non-root
		if os.Getuid() != 0 {
			if _, err := os.Stat("/proc/sys/kernel/unprivileged_userns_clone"); os.IsNotExist(err) {
				log.Warn("Unprivileged user namespaces are not supported by this kernel. Please enable them in your kernel configuration.")
				return fmt.Errorf("unprivileged user namespaces are not supported by this kernel")
			}
		}

		// Get the config file path
		configFile, err := cmd.Flags().GetString("config")
		if err != nil {
			return fmt.Errorf("failed to get config file path: %w", err)
		}

		// Get the output directory
		outputDir, err := cmd.Flags().GetString("output")
		if err != nil {
			return fmt.Errorf("failed to get output directory: %w", err)
		}

		// Get the squashfs flag
		createSquashfs, err := cmd.Flags().GetBool("squashfs")
		if err != nil {
			return fmt.Errorf("failed to get squashfs flag: %w", err)
		}

		// Get the initrd flag
		createInitrd, err := cmd.Flags().GetBool("initrd")
		if err != nil {
			return fmt.Errorf("failed to get initrd flag: %w", err)
		}

		// Load and validate the configuration
		config, err := imageconfig.LoadConfig(configFile)
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		// Create output directory if it doesn't exist
		if err := os.MkdirAll(outputDir, 0755); err != nil {
			return fmt.Errorf("failed to create output directory: %w", err)
		}

		// Create builder
		builder, err := builder.NewBuilder(config, outputDir, createSquashfs, createInitrd)
		if err != nil {
			return fmt.Errorf("failed to create builder: %w", err)
		}

		// Build image
		if err := builder.Build(); err != nil {
			return fmt.Errorf("failed to build image: %w", err)
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(buildCmd)

	// Add flags
	buildCmd.Flags().StringP("config", "c", "", "Path to the configuration file (required)")
	buildCmd.Flags().StringP("output", "o", "", "Output directory")
	buildCmd.Flags().BoolP("squashfs", "s", false, "Create a squashfs image")
	buildCmd.Flags().BoolP("initrd", "i", true, "Create an initrd image (default: true)")

	// Mark required flags
	buildCmd.MarkFlagRequired("config")
}
