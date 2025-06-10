package oci

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"go-image-builder/pkg/imageconfig"

	log "github.com/sirupsen/logrus"
)

// OCIInterface defines the interface for container image operations
type OCIInterface interface {
	PullParentImage() error
	MountParent() error
	UnmountParent() error
	CreateContainer(name string) error
	MountContainer(name string) error
	UnmountContainer(name string) error
	CommitContainer(name string) error
	PushImage() error
	Cleanup() error
}

// OCI implements container image operations
type OCI struct {
	config           *imageconfig.Config
	workDir          string
	parentContainer  string
	parentMountPoint string
}

// NewOCI creates a new OCI instance
func NewOCI(config *imageconfig.Config, workDir string) *OCI {
	return &OCI{
		config:  config,
		workDir: workDir,
	}
}

// PullParentImage pulls the parent image if specified
func (o *OCI) PullParentImage() error {
	// If no parent specified or parent is "scratch", skip pulling
	if o.config.Options.Parent == "" || o.config.Options.Parent == "scratch" {
		log.Printf("No parent image specified, starting from scratch")
		return nil
	}

	log.Printf("Pulling parent image: %s", o.config.Options.Parent)

	// Clean up any existing containers first
	cleanupArgs := []string{"containers", "--format", "{{.ContainerID}}"}
	cmd := exec.Command("buildah", cleanupArgs...)
	output, err := cmd.CombinedOutput()
	if err == nil {
		containers := strings.Split(strings.TrimSpace(string(output)), "\n")
		for _, container := range containers {
			if container == "" {
				continue
			}
			log.Debugf("Cleaning up stale container: %s", container)
			cleanupCmd := exec.Command("buildah", "rm", container)
			cleanupCmd.Run() // Ignore errors during cleanup
		}
	}

	// Remove the image if it exists
	rmiArgs := []string{"rmi", o.config.Options.Parent}
	cmd = exec.Command("buildah", rmiArgs...)
	cmd.Run() // Ignore errors, as the image might not exist

	// Clean up any dangling images
	pruneArgs := []string{"prune", "-f"}
	cmd = exec.Command("buildah", pruneArgs...)
	cmd.Run() // Ignore errors during cleanup

	// Build pull command
	args := []string{"pull"}
	if o.config.Options.PublishRegistry != "" {
		args = append(args, o.config.Options.RegistryOptsPull...)
	}
	args = append(args, o.config.Options.Parent)

	cmd = exec.Command("buildah", args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to pull parent image: %w\nOutput: %s", err, string(output))
	}

	// Verify the image exists and is accessible
	verifyArgs := []string{"images", "--format", "{{.Name}}:{{.Tag}}"}
	cmd = exec.Command("buildah", verifyArgs...)
	output, err = cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to verify image pull: %w", err)
	}

	images := strings.Split(strings.TrimSpace(string(output)), "\n")
	imageFound := false
	for _, image := range images {
		if image == o.config.Options.Parent {
			imageFound = true
			break
		}
	}

	if !imageFound {
		return fmt.Errorf("parent image %s not found after pull", o.config.Options.Parent)
	}

	// Verify the image is accessible by trying to inspect it
	inspectArgs := []string{"inspect", o.config.Options.Parent}
	cmd = exec.Command("buildah", inspectArgs...)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to inspect parent image: %w\nOutput: %s", err, string(output))
	}

	log.Debugf("Successfully verified parent image: %s", o.config.Options.Parent)
	return nil
}

// MountParent mounts the parent image
func (o *OCI) MountParent() error {
	log.Infof("Mounting parent image: %s", o.config.Options.Parent)

	// Check if we're running as root
	isRoot := os.Geteuid() == 0
	log.Debugf("Running as root: %v", isRoot)

	// Clean up any existing containers first
	cleanupArgs := []string{"containers", "--format", "{{.ContainerID}}"}
	cmd := exec.Command("buildah", cleanupArgs...)
	output, err := cmd.CombinedOutput()
	if err == nil {
		containers := strings.Split(strings.TrimSpace(string(output)), "\n")
		for _, container := range containers {
			if container == "" {
				continue
			}
			log.Debugf("Cleaning up stale container: %s", container)
			cleanupCmd := exec.Command("buildah", "rm", container)
			cleanupCmd.Run() // Ignore errors during cleanup
		}
	}

	// Create a new container from the parent image
	var fromArgs []string
	if isRoot {
		fromArgs = []string{"from", "--pull=never", o.config.Options.Parent}
	} else {
		fromArgs = []string{"unshare", "buildah", "from", "--pull=never", o.config.Options.Parent}
	}
	cmd = exec.Command("buildah", fromArgs...)
	output, err = cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to create container from parent image: %w\nOutput: %s", err, string(output))
	}
	containerName := strings.TrimSpace(string(output))
	log.Debugf("Created container from parent image: %s", containerName)

	// Mount the container
	var mountArgs []string
	if isRoot {
		mountArgs = []string{"mount", containerName}
	} else {
		mountArgs = []string{"unshare", "buildah", "mount", containerName}
	}
	cmd = exec.Command("buildah", mountArgs...)
	output, err = cmd.CombinedOutput()
	if err != nil {
		// Clean up the container if mount fails
		cleanupCmd := exec.Command("buildah", "rm", containerName)
		cleanupCmd.Run() // Ignore errors during cleanup
		return fmt.Errorf("failed to mount parent image: %w\nOutput: %s", err, string(output))
	}
	mountPoint := strings.TrimSpace(string(output))
	log.Debugf("Parent image mounted at: %s", mountPoint)

	// Store the container name for cleanup
	o.parentContainer = containerName
	o.parentMountPoint = mountPoint

	return nil
}

// UnmountParent unmounts the parent image if it was mounted
func (o *OCI) UnmountParent() error {
	// If no parent specified or parent is "scratch", skip unmounting
	if o.config.Options.Parent == "" || o.config.Options.Parent == "scratch" {
		return nil
	}

	log.Printf("Unmounting parent image: %s", o.config.Options.Parent)

	// Check if we're running as root
	isRoot := os.Geteuid() == 0
	log.Debugf("Running as root: %v", isRoot)

	// Use buildah unshare for rootless mode
	var args []string
	if isRoot {
		args = []string{"umount", o.parentContainer}
	} else {
		args = []string{"unshare", "buildah", "umount", o.parentContainer}
	}
	cmd := exec.Command("buildah", args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to unmount parent image: %w\nOutput: %s", err, string(output))
	}

	return nil
}

// CreateContainer creates a new container
func (o *OCI) CreateContainer() (string, error) {
	containerName := fmt.Sprintf("go-image-builder-%d", time.Now().Unix())
	log.Debugf("Creating container: %s", containerName)

	// Check if we're running as root
	isRoot := os.Geteuid() == 0

	var args []string
	if isRoot {
		args = []string{"from", "scratch"}
	} else {
		args = []string{"unshare", "buildah", "from", "scratch"}
	}

	// Set environment to reduce buildah verbosity
	cmd := exec.Command("buildah", args...)
	cmd.Env = append(os.Environ(), "BUILDAH_LOGLEVEL=error")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to create container: %w\nOutput: %s", err, string(output))
	}

	// Get the container ID from the output
	containerID := strings.TrimSpace(string(output))
	log.Debugf("Created container with ID: %s", containerID)
	return containerID, nil
}

// MountContainer mounts a container and returns its mount point
func (o *OCI) MountContainer(containerName string) (string, error) {
	log.Debugf("Mounting container: %s", containerName)

	// Check if we're running as root
	isRoot := os.Geteuid() == 0

	var args []string
	if isRoot {
		// If running as root, use buildah directly
		args = []string{"mount", containerName}
	} else {
		// If running as non-root, use buildah unshare
		args = []string{"unshare", "buildah", "mount", containerName}
	}

	// Set environment to reduce buildah verbosity
	cmd := exec.Command("buildah", args...)
	cmd.Env = append(os.Environ(), "BUILDAH_LOGLEVEL=error")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to mount container: %w\nOutput: %s", err, string(output))
	}

	mountPoint := strings.TrimSpace(string(output))
	if mountPoint == "" {
		return "", fmt.Errorf("mount command returned empty mount point for container %s", containerName)
	}

	// Verify the mount point exists
	if _, err := os.Stat(mountPoint); err != nil {
		return "", fmt.Errorf("mount point %s does not exist: %w", mountPoint, err)
	}

	log.Debugf("Container %s mounted at: %s", mountPoint, containerName)
	return mountPoint, nil
}

// UnmountContainer unmounts the container
func (o *OCI) UnmountContainer(containerName string) error {
	log.Infof("Unmounting container: %s", containerName)
	cmd := exec.Command("buildah", "umount", containerName)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to unmount container: %w\nOutput: %s", err, string(output))
	}
	return nil
}

// cleanRegistryURL removes any trailing slashes from the registry URL
func (o *OCI) cleanRegistryURL(registry string) string {
	return strings.TrimRight(registry, "/")
}

// cleanImagePath removes any double slashes and ensures proper path formatting
func (o *OCI) cleanImagePath(path string) string {
	// Remove any double slashes
	path = strings.ReplaceAll(path, "//", "/")
	// Remove trailing slash
	path = strings.TrimRight(path, "/")
	return path
}

// PushImage pushes the image to the registry
func (o *OCI) PushImage() error {
	log.Printf("Pushing image to registry")

	// Clean up registry URL and image path
	registry := o.cleanRegistryURL(o.config.Options.PublishRegistry)
	imageName := o.cleanImagePath(o.config.Options.Name)

	log.Debugf("Pushing to registry: %s", registry)

	// Build the full image name
	imgName := fmt.Sprintf("%s/%s", registry, imageName)
	log.Debugf("Starting image push to registry: %s", imgName)

	// Configure registry options
	var args []string
	if o.config.Options.RegistryOptsPush != nil {
		args = append(args, o.config.Options.RegistryOptsPush...)
	}
	log.Debugf("Configuring registry options with insecure flag")

	// Add tags if specified
	var tags []string
	if o.config.Options.PublishTags != "" {
		tags = strings.Split(o.config.Options.PublishTags, ",")
	} else {
		tags = []string{"latest"}
	}

	// If we have a parent image, try to push it first
	if o.config.Options.Parent != "" && o.config.Options.Parent != "scratch" {
		log.Debugf("Pushing parent image first: %s", o.config.Options.Parent)
		parentArgs := append([]string{"push"}, args...)
		parentArgs = append(parentArgs, o.config.Options.Parent)
		cmd := exec.Command("buildah", parentArgs...)
		if output, err := cmd.CombinedOutput(); err != nil {
			log.Warnf("Failed to push parent image (this is expected if it already exists): %v\nOutput: %s", err, string(output))
		}
	}

	// Push each tag with retries
	maxRetries := 3
	var lastErr error
	for _, tag := range tags {
		taggedRef := fmt.Sprintf("%s:%s", imgName, tag)
		log.Debugf("Pushing image with tag: %s", tag)

		for i := 0; i < maxRetries; i++ {
			if i > 0 {
				log.Debugf("Retrying push (attempt %d/%d)", i+1, maxRetries)
				time.Sleep(time.Second * time.Duration(i+1)) // Exponential backoff
			}

			// Build push command with additional options for parent images
			pushArgs := append([]string{"push"}, args...)
			if o.config.Options.Parent != "" && o.config.Options.Parent != "scratch" {
				// Add options to handle parent image layers
				pushArgs = append(pushArgs, "--format", "oci")
				pushArgs = append(pushArgs, "--disable-compression")
			}
			pushArgs = append(pushArgs, taggedRef)

			cmd := exec.Command("buildah", pushArgs...)
			output, err := cmd.CombinedOutput()
			if err == nil {
				log.Debugf("Successfully pushed image to registry")
				break
			}

			lastErr = fmt.Errorf("failed to push image with tag %s: %w\nOutput: %s", tag, err, string(output))
			log.Warnf("Push attempt %d failed: %v", i+1, lastErr)

			// Check if we should retry based on the error
			if strings.Contains(err.Error(), "BLOB_UPLOAD_UNKNOWN") {
				// Try to clean up any stale uploads
				cleanupArgs := []string{"rmi", taggedRef}
				cleanupCmd := exec.Command("buildah", cleanupArgs...)
				cleanupCmd.Run() // Ignore errors during cleanup

				// Try to clean up the registry's upload directory
				if i == maxRetries-1 {
					log.Warnf("All retry attempts failed. Please check registry configuration and storage.")
					log.Warnf("You may need to clean up the registry's upload directory manually.")
					log.Warnf("If using a parent image, ensure it exists in the registry and is accessible.")
				}
				continue
			}

			// For other errors, don't retry
			break
		}

		if lastErr != nil {
			return fmt.Errorf("failed to push image after %d attempts: %w", maxRetries, lastErr)
		}
	}

	return nil
}

// CommitContainer commits the container as a new image
func (o *OCI) CommitContainer(containerName, name string) error {
	log.Infof("Committing container %s", containerName)

	// Build the image reference
	imageRef := name
	if o.config.Options.PublishRegistry != "" {
		registry := o.cleanRegistryURL(o.config.Options.PublishRegistry)
		imageRef = fmt.Sprintf("%s/%s", registry, name)
	}

	// Add tags if specified
	var tags []string
	if o.config.Options.PublishTags != "" {
		tags = strings.Split(o.config.Options.PublishTags, ",")
	} else {
		tags = []string{"latest"}
	}

	// Commit with each tag
	for _, tag := range tags {
		taggedRef := fmt.Sprintf("%s:%s", imageRef, tag)
		args := []string{"commit", containerName, taggedRef}

		// Set environment to reduce buildah verbosity
		cmd := exec.Command("buildah", args...)
		cmd.Env = append(os.Environ(), "BUILDAH_LOGLEVEL=error")
		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("failed to commit container: %w\nOutput: %s", err, string(output))
		}
		log.Infof("Successfully committed container as %s", taggedRef)
	}

	return nil
}

// ContainerInfo holds information about the container
type ContainerInfo struct {
	OS           string
	OSVersion    string
	Architecture string
	PackageCount int
}

// getContainerInfo gathers information about the container
func (o *OCI) getContainerInfo(containerName string) (*ContainerInfo, error) {
	info := &ContainerInfo{}

	// Get OS and version from /etc/os-release
	cmd := exec.Command("buildah", "run", containerName, "cat", "/etc/os-release")
	output, err := cmd.CombinedOutput()
	if err == nil {
		// Parse os-release file
		lines := strings.Split(string(output), "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "ID=") {
				info.OS = strings.Trim(strings.TrimPrefix(line, "ID="), "\"")
			} else if strings.HasPrefix(line, "VERSION_ID=") {
				info.OSVersion = strings.Trim(strings.TrimPrefix(line, "VERSION_ID="), "\"")
			}
		}
	}

	// Get package count (for RPM-based systems)
	cmd = exec.Command("buildah", "run", containerName, "sh", "-c", "rpm -qa")
	if output, err := cmd.CombinedOutput(); err == nil {
		info.PackageCount = len(strings.Split(strings.TrimSpace(string(output)), "\n"))
	}

	return info, nil
}

// Cleanup removes temporary containers and images
func (o *OCI) Cleanup(containerName string) error {
	log.Infof("Cleaning up container: %s", containerName)

	// Remove container
	cmd := exec.Command("buildah", "rm", containerName)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to remove container: %w\nOutput: %s", err, string(output))
	}

	return nil
}

// GetParentMountPoint returns the mount point of the parent image
func (o *OCI) GetParentMountPoint() string {
	return o.parentMountPoint
}

// GetParentContainer returns the name of the parent container
func (o *OCI) GetParentContainer() string {
	return o.parentContainer
}
