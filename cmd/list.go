package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/spf13/cobra"
)

var (
	insecure bool
)

type ImageInfo struct {
	Repository  string
	Tag         string
	Created     time.Time
	KernelVer   string
	HasKernel   bool
	HasInitrd   bool
	BuildDate   string
	Description string
}

var listCmd = &cobra.Command{
	Use:   "list [REGISTRY]",
	Short: "List remote repositories and tagged images",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		registry := args[0]

		// Remove any existing protocol prefix
		registry = strings.TrimPrefix(registry, "http://")
		registry = strings.TrimPrefix(registry, "https://")

		// Create a registry reference
		reg, err := name.NewRegistry(registry, name.Insecure)
		if err != nil {
			return fmt.Errorf("invalid registry: %w", err)
		}

		// Get authentication options
		auth, err := authn.DefaultKeychain.Resolve(reg)
		if err != nil {
			return fmt.Errorf("failed to resolve authentication: %w", err)
		}

		// Configure remote options
		opts := []remote.Option{remote.WithAuth(auth)}
		if insecure {
			opts = append(opts, remote.WithTransport(remote.DefaultTransport))
		}

		// List repositories with authentication
		repos, err := remote.Catalog(context.Background(), reg, opts...)
		if err != nil {
			return fmt.Errorf("failed to list repositories: %w", err)
		}

		// Create tabwriter for formatted output
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "REPOSITORY\tTAG\tCREATED\tKERNEL VERSION\tKERNEL\tINITRD\tBUILD DATE\tDESCRIPTION")

		for _, repo := range repos {
			repoRef, err := name.NewRepository(fmt.Sprintf("%s/%s", registry, repo), name.Insecure)
			if err != nil {
				fmt.Printf("  Error parsing repository reference: %v\n", err)
				continue
			}

			// List tags for this repository with authentication
			tags, err := remote.List(repoRef, opts...)
			if err != nil {
				fmt.Printf("  Error listing tags: %v\n", err)
				continue
			}

			for _, tag := range tags {
				imgRef, err := name.ParseReference(fmt.Sprintf("%s/%s:%s", registry, repo, tag), name.Insecure)
				if err != nil {
					fmt.Printf("    Error parsing image reference: %v\n", err)
					continue
				}

				img, err := remote.Image(imgRef, opts...)
				if err != nil {
					fmt.Printf("    Error fetching image: %v\n", err)
					continue
				}

				config, err := img.ConfigFile()
				if err != nil {
					fmt.Printf("    Error fetching config: %v\n", err)
					continue
				}

				// Extract image information
				info := ImageInfo{
					Repository: repo,
					Tag:        tag,
					Created:    config.Created.Time,
				}

				// Check for kernel and initrd layers
				manifest, err := img.Manifest()
				if err == nil {
					for _, layer := range manifest.Layers {
						if layer.Annotations != nil {
							if layer.Annotations["org.opencontainers.image.type"] == "kernel" {
								info.HasKernel = true
								info.KernelVer = layer.Annotations["org.opencontainers.image.kernel.version"]
							} else if layer.Annotations["org.opencontainers.image.type"] == "initrd" {
								info.HasInitrd = true
							}
						}
					}
				}

				// Get build date and description from labels
				if config.Config.Labels != nil {
					info.BuildDate = config.Config.Labels["org.opencontainers.image.build-date"]
					info.Description = config.Config.Labels["org.opencontainers.image.description"]
				}

				// Print in tabular format
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%v\t%v\t%s\t%s\n",
					info.Repository,
					info.Tag,
					info.Created.Format("2006-01-02 15:04:05"),
					info.KernelVer,
					info.HasKernel,
					info.HasInitrd,
					info.BuildDate,
					info.Description,
				)
			}
		}
		w.Flush()
		return nil
	},
}

func init() {
	listCmd.Flags().BoolVar(&insecure, "insecure", false, "Allow insecure HTTP connections")
	rootCmd.AddCommand(listCmd)
}
