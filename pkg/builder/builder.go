package builder

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"go-image-builder/internal/pkgmgr"
	"go-image-builder/pkg/image"
	"go-image-builder/pkg/imageconfig"
	"go-image-builder/pkg/oci"

	log "github.com/sirupsen/logrus"
)

// Builder handles the image building process
type Builder struct {
	config               *imageconfig.Config
	rootfs               string
	pm                   pkgmgr.PackageManager
	oci                  *oci.OCI
	shouldCreateSquashfs bool
	shouldCreateInitrd   bool
}

// NewBuilder creates a new Builder instance
func NewBuilder(config *imageconfig.Config, workDir string, createSquashfs, createInitrd bool) (*Builder, error) {
	var pm pkgmgr.PackageManager
	switch config.Options.PkgManager {
	case "dnf":
		pm = &pkgmgr.DNF{}
	case "zypper":
		// TODO: implement zypper
		return nil, fmt.Errorf("zypper support not implemented yet")
	case "apt":
		// TODO: implement apt
		return nil, fmt.Errorf("apt support not implemented yet")
	default:
		return nil, fmt.Errorf("unsupported package manager: %s", config.Options.PkgManager)
	}

	return &Builder{
		config:               config,
		rootfs:               filepath.Join(workDir, "rootfs"),
		pm:                   pm,
		oci:                  oci.NewOCI(config, workDir),
		shouldCreateSquashfs: createSquashfs,
		shouldCreateInitrd:   createInitrd,
	}, nil
}

// Build executes the image building pipeline
func (b *Builder) Build() error {
	log.Info("Starting image build process")
	var mountPoint string
	var containerName string

	// 1. Handle parent image or create new container
	if b.config.Options.Parent != "" && b.config.Options.Parent != "scratch" {
		log.Infof("Pulling parent image: %s", b.config.Options.Parent)
		if err := b.oci.PullParentImage(); err != nil {
			return fmt.Errorf("failed to pull parent image: %w", err)
		}
		log.Info("Parent image pulled successfully")
		log.Info("Mounting parent image")
		if err := b.oci.MountParent(); err != nil {
			return fmt.Errorf("failed to mount parent image: %w", err)
		}
		defer b.oci.UnmountParent()
		mountPoint = b.oci.GetParentMountPoint()
		if mountPoint == "" {
			return fmt.Errorf("got empty mount point from parent image")
		}
		log.Debugf("Parent image mounted at: %s", mountPoint)
		containerName = b.oci.GetParentContainer()
	} else {
		log.Info("Starting from scratch")
		// Create and mount new container
		var err error
		containerName, err = b.oci.CreateContainer()
		if err != nil {
			return fmt.Errorf("failed to create container: %w", err)
		}
		defer b.oci.Cleanup(containerName)

		mountPoint, err = b.oci.MountContainer(containerName)
		if err != nil {
			return fmt.Errorf("failed to mount container: %w", err)
		}
		if mountPoint == "" {
			return fmt.Errorf("got empty mount point from container %s", containerName)
		}
		log.Debugf("Container mounted at: %s", mountPoint)
	}

	// 3. Initialize rootfs with package manager
	log.Info("Initializing rootfs")
	log.Debugf("Using package manager: %s", b.config.Options.PkgManager)
	log.Debugf("Rootfs path: %s", mountPoint)
	if err := b.pm.InitRootfs(mountPoint, *b.config); err != nil {
		return fmt.Errorf("failed to initialize rootfs: %w", err)
	}

	// 4. Add repositories
	log.Info("Adding repositories")
	log.Debugf("Adding %d repositories", len(b.config.Repositories))
	if err := b.pm.AddRepos(mountPoint, b.config.Repositories); err != nil {
		return fmt.Errorf("failed to add repositories: %w", err)
	}

	// 5. Install packages and groups
	log.Info("Installing packages and groups")
	log.Debugf("Installing %d packages and %d groups", len(b.config.Packages), len(b.config.PackageGroups))
	if err := b.pm.InstallPackages(mountPoint, b.config.Packages, b.config.PackageGroups); err != nil {
		return fmt.Errorf("failed to install packages: %w", err)
	}

	// 6. Copy files into the rootfs
	if len(b.config.CopyFiles) > 0 {
		log.Info("Copying files into rootfs")
		log.Debugf("Copying %d files", len(b.config.CopyFiles))
		if err := b.pm.CopyFiles(mountPoint, b.config.CopyFiles); err != nil {
			return fmt.Errorf("failed to copy files: %w", err)
		}
	}

	// 7. Run post-install commands
	log.Info("Running post-install commands")
	log.Debugf("Running %d post-install commands", len(b.config.Cmds))
	for _, cmd := range b.config.Cmds {
		log.Infof("Running command: %s", cmd.Cmd)
		log.Debugf("Command log level: %s", cmd.LogLevel)
		if err := b.pm.RunCommand(mountPoint, cmd.Cmd); err != nil {
			return fmt.Errorf("failed to run command '%s': %w", cmd.Cmd, err)
		}
	}

	// 8. Generate initrd if enabled
	if b.shouldCreateInitrd {
		log.Info("Generating initrd")
		if err := b.generateInitrd(mountPoint); err != nil {
			return fmt.Errorf("failed to generate initrd: %w", err)
		}

		// Extract kernel only if initrd is being created
		log.Info("Extracting kernel")
		if err := b.extractKernel(mountPoint); err != nil {
			return fmt.Errorf("failed to extract kernel: %w", err)
		}
	} else {
		log.Info("Skipping initrd generation and kernel extraction")
	}

	// 9. Create squashfs image if enabled
	if b.shouldCreateSquashfs {
		log.Info("Creating squashfs image")
		if err := b.createSquashfs(mountPoint); err != nil {
			return fmt.Errorf("failed to create squashfs: %w", err)
		}
	} else {
		log.Info("Skipping squashfs creation")
	}

	// 10. Create image with layers
	log.Info("Creating image with layers")
	img, err := image.NewImage(b.config.Options.PublishRegistry, b.config.Options.Name, b.config)
	if err != nil {
		return fmt.Errorf("failed to create image: %w", err)
	}

	// Add base layer
	err = img.AddBaseLayer(mountPoint)
	if err != nil {
		return fmt.Errorf("failed to add base layer: %w", err)
	}

	// Add config layer
	if err := img.AddConfigLayer(); err != nil {
		return fmt.Errorf("failed to add config layer: %w", err)
	}

	// Add kernel and initrd layers if they were created
	if b.shouldCreateInitrd {
		// Get kernel version
		kernelVersion, err := b.getKernelVersion(mountPoint)
		if err != nil {
			return fmt.Errorf("failed to get kernel version: %w", err)
		}

		// Copy kernel from container
		kernelPath := filepath.Join(b.rootfs, "..", "kernel")
		if err := copyFile(filepath.Join(mountPoint, "lib/modules", kernelVersion, "vmlinuz"), kernelPath); err != nil {
			return fmt.Errorf("failed to copy kernel: %w", err)
		}

		// Find initrd file
		initrdPath := filepath.Join(mountPoint, "boot", fmt.Sprintf("initramfs-%s.img", kernelVersion))
		if _, err := os.Stat(initrdPath); os.IsNotExist(err) {
			// Try alternative name
			initrdPath = filepath.Join(mountPoint, "boot", "initrd.img")
			if _, err := os.Stat(initrdPath); os.IsNotExist(err) {
				return fmt.Errorf("failed to find initrd file: %w", err)
			}
		}

		// Add kernel and initrd layers
		if err := img.AddKernelLayer(kernelPath, kernelVersion); err != nil {
			return fmt.Errorf("failed to add kernel layer: %w", err)
		}
		if err := img.AddInitrdLayer(initrdPath); err != nil {
			return fmt.Errorf("failed to add initrd layer: %w", err)
		}
	}

	// 11. Push image to registry if specified
	if b.config.Options.PublishRegistry != "" {
		log.Info("Pushing image to registry")
		log.Debugf("Pushing to registry: %s", b.config.Options.PublishRegistry)
		if err := img.Push(); err != nil {
			return fmt.Errorf("failed to push image: %w", err)
		}
	}

	// Clean up temporary directories
	img.Cleanup()

	// 12. Cleanup
	log.Info("Cleaning up")
	if err := b.pm.Cleanup(mountPoint); err != nil {
		return fmt.Errorf("failed to cleanup: %w", err)
	}

	log.Info("Image build completed successfully")
	return nil
}

func (b *Builder) generateInitrd(rootfs string) error {
	// Find the kernel version
	kernelVer, err := b.getKernelVersion(rootfs)
	if err != nil {
		return fmt.Errorf("failed to get kernel version: %w", err)
	}

	// Run dracut to generate initrd
	dracutCmd := fmt.Sprintf("dracut --add \"dmsquash-live livenet network-manager\" --kver %s -N -f --logfile /tmp/dracut.log 2>/dev/null", kernelVer)
	if err := b.pm.RunCommand(rootfs, dracutCmd); err != nil {
		return fmt.Errorf("failed to run dracut: %w", err)
	}

	// Show dracut log
	logCmd := "echo DRACUT LOG:; cat /tmp/dracut.log"
	if err := b.pm.RunCommand(rootfs, logCmd); err != nil {
		return fmt.Errorf("failed to show dracut log: %w", err)
	}

	return nil
}

func (b *Builder) createSquashfs(rootfs string) error {
	outputPath := filepath.Join(b.rootfs, "..", "image.squashfs")
	cmd := exec.Command("mksquashfs",
		rootfs,
		outputPath,
		"-comp", "xz",
		"-no-progress",
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("mksquashfs failed: %w\nOutput: %s", err, string(output))
	}

	return nil
}

func (b *Builder) extractKernel(rootfs string) error {
	// Find the kernel file
	kernelPath := filepath.Join(rootfs, "boot", "vmlinuz-*")
	log.Debugf("Looking for kernel at: %s", kernelPath)

	matches, err := filepath.Glob(kernelPath)
	if err != nil {
		return fmt.Errorf("failed to find kernel: %w", err)
	}
	if len(matches) == 0 {
		// Try alternative path
		kernelPath = filepath.Join(rootfs, "lib", "modules", "*", "vmlinuz")
		log.Debugf("Trying alternative kernel path: %s", kernelPath)
		matches, err = filepath.Glob(kernelPath)
		if err != nil {
			return fmt.Errorf("failed to find kernel in alternative location: %w", err)
		}
		if len(matches) == 0 {
			return fmt.Errorf("no kernel found at %s or %s", filepath.Join(rootfs, "boot", "vmlinuz-*"), kernelPath)
		}
	}

	// Create output directory if it doesn't exist
	outputDir := filepath.Join(b.rootfs, "..")
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// Copy the kernel to the output directory
	outputPath := filepath.Join(outputDir, "kernel")
	log.Debugf("Copying kernel from %s to %s", matches[0], outputPath)

	src, err := os.Open(matches[0])
	if err != nil {
		return fmt.Errorf("failed to open source kernel: %w", err)
	}
	defer src.Close()

	dst, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create destination kernel: %w", err)
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		return fmt.Errorf("failed to copy kernel: %w", err)
	}

	return nil
}

func (b *Builder) getKernelVersion(rootfs string) (string, error) {
	// List kernel modules directory to get the kernel version
	modulesDir := filepath.Join(rootfs, "lib", "modules")
	entries, err := os.ReadDir(modulesDir)
	if err != nil {
		return "", fmt.Errorf("failed to read modules directory: %w", err)
	}
	if len(entries) == 0 {
		return "", fmt.Errorf("no kernel modules found")
	}

	// Use the first kernel version found
	return entries[0].Name(), nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("failed to open source file: %w", err)
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("failed to create destination file: %w", err)
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("failed to copy file contents: %w", err)
	}

	return out.Sync()
}
