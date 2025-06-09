package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"go-image-builder/pkg/builder"
	"go-image-builder/pkg/imageconfig"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

var buildCmd = &cobra.Command{
	Use:   "build",
	Short: "Build a system image",
	Long:  `Build a system image based on the provided configuration file.`,
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

		// Load and validate the configuration
		config, err := imageconfig.LoadConfig(configFile)
		if err != nil {
			return fmt.Errorf("failed to load configuration: %w", err)
		}

		// Get the output directory
		outputDir, err := cmd.Flags().GetString("output")
		if err != nil {
			return fmt.Errorf("failed to get output directory: %w", err)
		}

		// Create output directory if it doesn't exist
		if err := os.MkdirAll(outputDir, 0755); err != nil {
			return fmt.Errorf("failed to create output directory: %w", err)
		}

		// Create a temporary directory for the rootfs
		rootfs := filepath.Join(outputDir, "rootfs")
		if err := os.MkdirAll(rootfs, 0755); err != nil {
			return fmt.Errorf("failed to create rootfs directory: %w", err)
		}

		// Create the builder
		b, err := builder.NewBuilder(config, rootfs)
		if err != nil {
			return fmt.Errorf("failed to create builder: %w", err)
		}

		// Build the image
		if err := b.Build(); err != nil {
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

	// Mark required flags
	buildCmd.MarkFlagRequired("config")
}
