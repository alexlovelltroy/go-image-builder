package builder

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"go-image-builder/internal/pkgmgr"
	"go-image-builder/pkg/image"
	"go-image-builder/pkg/imageconfig"
	"go-image-builder/pkg/oci"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	log "github.com/sirupsen/logrus"
)

// Builder handles the image building process
type Builder struct {
	config               *imageconfig.Config
	workDir              string
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
		workDir:              workDir,
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

	// 1. Setup the container, either from a parent or from scratch
	log.Info("--> Setting up container")
	containerName, mountPoint, err := b.setupContainer()
	if err != nil {
		return err
	}
	defer b.oci.Cleanup(containerName)
	log.Infof("Container %s mounted at %s", containerName, mountPoint)

	// 2. Customize the container's rootfs
	log.Info("--> Customizing container")
	if err := b.customizeContainer(containerName, mountPoint); err != nil {
		return err
	}

	// 3. Package the final image and artifacts
	log.Info("--> Packaging final image")
	img, err := b.packageImage(containerName, mountPoint)
	if err != nil {
		return err
	}

	// 4. Push the image to a registry if specified
	if b.config.Options.PublishRegistry != "" {
		log.Info("--> Pushing image to registry")
		if err := img.Push(); err != nil {
			return fmt.Errorf("failed to push image: %w", err)
		}
	}

	// 5. Final cleanup
	log.Info("--> Cleaning up build artifacts")
	img.Cleanup()
	if err := b.pm.Cleanup(mountPoint); err != nil {
		return fmt.Errorf("failed to cleanup rootfs: %w", err)
	}

	log.Info("Image build completed successfully")
	return nil
}

// setupContainer prepares the base container for the build, either by pulling a parent image
// or creating a new one from scratch. It returns the container name and mount point.
func (b *Builder) setupContainer() (containerName, mountPoint string, err error) {
	if b.config.Options.Parent != "" && b.config.Options.Parent != "scratch" {
		log.Infof("Pulling parent image: %s", b.config.Options.Parent)
		if err = b.oci.PullParentImage(); err != nil {
			return "", "", fmt.Errorf("failed to pull parent image: %w", err)
		}
		log.Debug("Parent image pulled successfully")

		log.Info("Mounting parent image")
		if err = b.oci.MountParent(); err != nil {
			return "", "", fmt.Errorf("failed to mount parent image: %w", err)
		}
		// Defer unmount until the end of the build process
		defer b.oci.UnmountParent()

		mountPoint = b.oci.GetParentMountPoint()
		containerName = b.oci.GetParentContainer()
		if mountPoint == "" || containerName == "" {
			return "", "", fmt.Errorf("got empty mount point or container name from parent image")
		}
	} else {
		log.Info("Starting from scratch")
		containerName, err = b.oci.CreateContainer()
		if err != nil {
			return "", "", fmt.Errorf("failed to create container: %w", err)
		}

		mountPoint, err = b.oci.MountContainer(containerName)
		if err != nil {
			return "", "", fmt.Errorf("failed to mount container: %w", err)
		}
		if mountPoint == "" {
			return "", "", fmt.Errorf("got empty mount point from container %s", containerName)
		}
	}
	return containerName, mountPoint, nil
}

// customizeContainer runs through all the steps to configure the rootfs, including
// package installation, repository configuration, file copying, and running commands.
func (b *Builder) customizeContainer(containerName, mountPoint string) error {
	// Only initialize package manager if there are packages to install.
	if len(b.config.Packages) > 0 || len(b.config.PackageGroups) > 0 {
		log.Info("Initializing rootfs with package manager")
		if err := b.pm.InitRootfs(mountPoint, *b.config); err != nil {
			return fmt.Errorf("failed to initialize rootfs: %w", err)
		}

		log.Info("Adding repositories")
		if err := b.pm.AddRepos(mountPoint, b.config.Repositories); err != nil {
			return fmt.Errorf("failed to add repositories: %w", err)
		}

		log.Info("Installing packages and groups")
		if err := b.pm.InstallPackages(mountPoint, b.config.Packages, b.config.PackageGroups); err != nil {
			return fmt.Errorf("failed to install packages: %w", err)
		}
	} else {
		log.Info("Skipping package manager setup as no packages are defined.")
	}

	if len(b.config.CopyFiles) > 0 {
		log.Info("Copying files into rootfs")
		if err := b.pm.CopyFiles(mountPoint, b.config.CopyFiles); err != nil {
			return fmt.Errorf("failed to copy files: %w", err)
		}
	}

	if len(b.config.Cmds) > 0 {
		log.Info("Running post-install commands")
		for _, cmd := range b.config.Cmds {
			log.Infof("Running command: %s", cmd.Cmd)
			if err := b.pm.RunCommand(b.oci, containerName, cmd.Cmd); err != nil {
				return fmt.Errorf("failed to run command '%s': %w", cmd.Cmd, err)
			}
		}
	}
	return nil
}

// packageImage creates the final image artifacts, including the initrd, kernel,
// squashfs, and the final layered OCI image.
func (b *Builder) packageImage(containerName, mountPoint string) (*image.Image, error) {
	var kernelVersion string
	var err error

	if b.shouldCreateInitrd {
		kernelVersion, err = b.getKernelVersion(containerName)
		if err != nil {
			return nil, fmt.Errorf("failed to get kernel version: %w", err)
		}
	}

	// Load parent image from local storage if it exists.
	var parentImage v1.Image
	var parentArchivePath string
	if b.config.Options.Parent != "" && b.config.Options.Parent != "scratch" {
		log.Infof("Loading parent image '%s' from local storage.", b.config.Options.Parent)

		// Create a temporary file in the working directory to ensure adequate space.
		tempArchive, err := os.CreateTemp(b.workDir, "parent-image-*.tar")
		if err != nil {
			return nil, fmt.Errorf("failed to create temporary archive file: %w", err)
		}
		// This file must persist until the push is complete. It will be removed
		// by the image.Cleanup() method.
		parentArchivePath = tempArchive.Name()
		tempArchive.Close() // Close the file so buildah can write to it.

		// Save the image from buildah's storage to the archive.
		if err := b.oci.SaveImage(b.config.Options.Parent, parentArchivePath); err != nil {
			os.Remove(parentArchivePath) // Clean up on failure.
			return nil, fmt.Errorf("failed to save parent image to archive: %w", err)
		}

		// Load the image into a v1.Image object.
		parentImage, err = tarball.ImageFromPath(parentArchivePath, nil)
		if err != nil {
			os.Remove(parentArchivePath) // Clean up on failure.
			return nil, fmt.Errorf("failed to load parent image from archive: %w", err)
		}
		log.Debug("Successfully loaded parent image.")
	}

	log.Info("Creating OCI image with layers")
	img, err := image.NewImage(b.config.Options.PublishRegistry, b.config.Options.Name, b.config, parentImage, parentArchivePath)
	if err != nil {
		return nil, fmt.Errorf("failed to create image: %w", err)
	}

	if err = img.AddBaseLayer(mountPoint); err != nil {
		return nil, fmt.Errorf("failed to add base layer: %w", err)
	}

	if err := img.AddConfigLayer(); err != nil {
		return nil, fmt.Errorf("failed to add config layer: %w", err)
	}

	if b.shouldCreateInitrd {
		// Check if the parent already has an initrd layer.
		hasInitrd, err := img.HasLayerWithComment("Initrd Layer")
		if err != nil {
			return nil, fmt.Errorf("failed to check for initrd layer in parent: %w", err)
		}

		var initrdPath string
		if !hasInitrd {
			log.Info("Parent does not have an initrd layer. Generating a new one.")
			if err := b.generateInitrd(containerName, kernelVersion); err != nil {
				return nil, fmt.Errorf("failed to generate initrd: %w", err)
			}

			// After generating it, find its path for the layer creation.
			initrdPath = filepath.Join(mountPoint, "boot", fmt.Sprintf("initramfs-%s.img", kernelVersion))
			if _, err := os.Stat(initrdPath); os.IsNotExist(err) {
				initrdPath = filepath.Join(mountPoint, "boot", "initrd.img")
				if _, err := os.Stat(initrdPath); os.IsNotExist(err) {
					return nil, fmt.Errorf("failed to find initrd file after generating it: %w", err)
				}
			}
		} else {
			log.Info("Found initrd layer in parent image. Skipping generation.")
			// initrdPath remains an empty string, which is handled correctly by AddInitrdLayer.
		}

		// Only extract the kernel if we are building from scratch.
		if b.config.Options.Parent == "" || b.config.Options.Parent == "scratch" {
			log.Info("Extracting kernel for scratch build")
			if err := b.extractKernel(containerName, kernelVersion); err != nil {
				return nil, fmt.Errorf("failed to extract kernel: %w", err)
			}
		}

		kernelPath := filepath.Join(b.rootfs, "..", "kernel")
		if err := img.AddKernelLayer(kernelPath, kernelVersion); err != nil {
			return nil, fmt.Errorf("failed to add kernel layer: %w", err)
		}
		if err := img.AddInitrdLayer(initrdPath); err != nil {
			return nil, fmt.Errorf("failed to add initrd layer: %w", err)
		}
	}

	if b.shouldCreateSquashfs {
		log.Info("Creating squashfs image")
		if err := b.createSquashfs(mountPoint); err != nil {
			return nil, fmt.Errorf("failed to create squashfs: %w", err)
		}
	} else {
		log.Debug("Skipping squashfs creation as per configuration")
	}
	return img, nil
}

func (b *Builder) generateInitrd(containerName, kernelVersion string) error {
	// Run dracut to generate initrd
	dracutCmd := fmt.Sprintf("dracut --add \"dmsquash-live livenet network-manager\" --kver %s -N -f --logfile /tmp/dracut.log 2>/dev/null", kernelVersion)
	if err := b.pm.RunCommand(b.oci, containerName, dracutCmd); err != nil {
		return fmt.Errorf("failed to run dracut: %w", err)
	}

	// Show dracut log
	logCmd := "echo DRACUT LOG:; cat /tmp/dracut.log"
	if err := b.pm.RunCommand(b.oci, containerName, logCmd); err != nil {
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

func (b *Builder) extractKernel(containerName, kernelVersion string) error {
	// Define potential paths for the kernel inside the container.
	potentialPaths := []string{
		fmt.Sprintf("/boot/vmlinuz-%s", kernelVersion),
		fmt.Sprintf("/lib/modules/%s/vmlinuz", kernelVersion),
	}

	var kernelPathInContainer string
	for _, path := range potentialPaths {
		log.Debugf("Checking for kernel in container at: %s", path)
		if err := b.oci.Stat(containerName, path); err == nil {
			kernelPathInContainer = path
			log.Debugf("Found kernel in container at: %s", kernelPathInContainer)
			break
		}
	}

	if kernelPathInContainer == "" {
		return fmt.Errorf("could not find kernel in container for version '%s' in paths: %v", kernelVersion, potentialPaths)
	}

	// Create output directory on the host.
	outputDir := filepath.Join(b.rootfs, "..")
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory for kernel: %w", err)
	}

	// Copy the kernel from the container to the host.
	kernelDestPathOnHost := filepath.Join(outputDir, "kernel")
	log.Debugf("Copying kernel from %s:%s to %s", containerName, kernelPathInContainer, kernelDestPathOnHost)
	if err := b.oci.CopyFromContainerWithCat(containerName, kernelPathInContainer, kernelDestPathOnHost); err != nil {
		return fmt.Errorf("failed to copy kernel from container: %w", err)
	}

	return nil
}

func (b *Builder) getKernelVersion(containerName string) (string, error) {
	// Execute 'ls /lib/modules' inside the container to find kernel versions.
	// This is more robust than reading from the host's view of the mount point.
	log.Debugf("Querying kernel version from container %s", containerName)
	output, err := b.oci.RunCommandWithOutput(containerName, "ls /lib/modules")
	if err != nil {
		return "", fmt.Errorf("failed to list /lib/modules in container: %w", err)
	}

	// The output might contain multiple lines if multiple kernels are installed.
	// We'll take the first non-empty one.
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		kernelVersion := strings.TrimSpace(line)
		if kernelVersion != "" {
			log.Debugf("Found kernel version: %s", kernelVersion)
			return kernelVersion, nil
		}
	}

	return "", fmt.Errorf("could not determine kernel version: /lib/modules is empty or does not exist in container")
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
