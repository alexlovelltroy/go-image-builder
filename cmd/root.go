package cmd

import (
	"fmt"
	"os"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

var (
	configFile     string
	outputDir      string
	logLevel       string
	createSquashfs bool
	createInitrd   bool
)

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "go-image-builder",
	Short: "A tool for building container images",
	Long: `A tool for building container images with support for various package managers
and customization options.`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		// Set log level
		level, err := log.ParseLevel(logLevel)
		if err != nil {
			return fmt.Errorf("invalid log level: %w", err)
		}
		log.SetLevel(level)
		// Set output to stdout and disable duplicate logging
		log.SetOutput(os.Stdout)
		log.SetFormatter(&log.TextFormatter{
			DisableTimestamp: false,
			FullTimestamp:    true,
		})
		return nil
	},
}

// Execute adds all child commands to the root command and sets flags appropriately.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		log.Fatal(err)
	}
}

func init() {
	// Global flags
	rootCmd.PersistentFlags().StringVarP(&configFile, "config", "c", "", "Path to the configuration file")
	rootCmd.PersistentFlags().StringVarP(&outputDir, "output", "o", "", "Output directory for the build artifacts")
	rootCmd.PersistentFlags().StringVar(&logLevel, "log-level", "info", "Log level (debug, info, warn, error, fatal)")
	rootCmd.PersistentFlags().BoolVar(&createSquashfs, "create-squashfs", true, "Create a squashfs image")
	rootCmd.PersistentFlags().BoolVar(&createInitrd, "create-initrd", true, "Create an initrd image")
}
