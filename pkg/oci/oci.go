package oci

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"go-image-builder/pkg/imageconfig"
	"go-image-builder/pkg/utils"

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

// executeBuildah runs a buildah command with the given arguments, handling root/rootless execution.
func (o *OCI) executeBuildah(args ...string) ([]byte, error) {
	var cmd *exec.Cmd
	var cmdStr string

	if os.Geteuid() == 0 {
		cmd = exec.Command("buildah", args...)
		cmdStr = "buildah " + strings.Join(args, " ")
	} else {
		// Prepend "unshare" for rootless execution
		cmdArgs := append([]string{"buildah"}, args...)
		cmd = exec.Command("unshare", cmdArgs...)
		cmdStr = "unshare " + strings.Join(cmdArgs, " ")
	}

	log.Debugf("Executing: %s", cmdStr)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("buildah command failed: %s\nOutput: %s\nError: %w", cmdStr, string(output), err)
	}
	return output, nil
}

// PullParentImage pulls the parent image if specified
func (o *OCI) PullParentImage() error {
	// If no parent specified or parent is "scratch", skip pulling
	if o.config.Options.Parent == "" || o.config.Options.Parent == "scratch" {
		log.Info("No parent image specified, starting from scratch")
		return nil
	}

	log.Infof("Pulling parent image: %s", o.config.Options.Parent)

	// Clean up any existing containers first
	cleanupArgs := []string{"containers", "--format", "{{.ContainerID}}"}
	output, err := o.executeBuildah(cleanupArgs...)
	if err == nil {
		containers := strings.Split(strings.TrimSpace(string(output)), "\n")
		for _, container := range containers {
			if container == "" {
				continue
			}
			log.Debugf("Cleaning up stale container: %s", container)
			o.executeBuildah("rm", container) // Ignore errors during cleanup
		}
	}

	// Remove the image if it exists
	rmiArgs := []string{"rmi", o.config.Options.Parent}
	o.executeBuildah(rmiArgs...) // Ignore errors, as the image might not exist

	// Clean up any dangling images
	pruneArgs := []string{"prune", "-f"}
	o.executeBuildah(pruneArgs...) // Ignore errors during cleanup

	// Build pull command
	args := []string{"pull"}
	if o.config.Options.PublishRegistry != "" {
		args = append(args, o.config.Options.RegistryOptsPull...)
	}
	args = append(args, o.config.Options.Parent)

	if _, err := o.executeBuildah(args...); err != nil {
		return err
	}

	// Verify the image exists and is accessible
	verifyArgs := []string{"images", "--format", "{{.Name}}:{{.Tag}}"}
	output, err = o.executeBuildah(verifyArgs...)
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
	if _, err := o.executeBuildah(inspectArgs...); err != nil {
		return fmt.Errorf("failed to inspect parent image: %w", err)
	}

	log.Debugf("Successfully verified parent image: %s", o.config.Options.Parent)
	return nil
}

// MountParent mounts the parent image
func (o *OCI) MountParent() error {
	log.Infof("Mounting parent image: %s", o.config.Options.Parent)

	// Clean up any existing containers first
	cleanupArgs := []string{"containers", "--format", "{{.ContainerID}}"}
	output, err := o.executeBuildah(cleanupArgs...)
	if err == nil {
		containers := strings.Split(strings.TrimSpace(string(output)), "\n")
		for _, container := range containers {
			if container == "" {
				continue
			}
			log.Debugf("Cleaning up stale container: %s", container)
			o.executeBuildah("rm", container) // Ignore errors during cleanup
		}
	}

	// Create a new container from the parent image
	fromArgs := []string{"from", "--pull=never", o.config.Options.Parent}
	output, err = o.executeBuildah(fromArgs...)
	if err != nil {
		return fmt.Errorf("failed to create container from parent image: %w", err)
	}
	containerName := strings.TrimSpace(string(output))
	log.Debugf("Created container from parent image: %s", containerName)

	// Mount the container
	mountArgs := []string{"mount", containerName}
	output, err = o.executeBuildah(mountArgs...)
	if err != nil {
		// Clean up the container if mount fails
		o.executeBuildah("rm", containerName) // Ignore errors during cleanup
		return fmt.Errorf("failed to mount parent image: %w", err)
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

	log.Infof("Unmounting parent image: %s", o.config.Options.Parent)

	args := []string{"umount", o.parentContainer}
	if _, err := o.executeBuildah(args...); err != nil {
		return fmt.Errorf("failed to unmount parent image: %w", err)
	}

	return nil
}

// CreateContainer creates a new container
func (o *OCI) CreateContainer() (string, error) {
	containerName := fmt.Sprintf("go-image-builder-%d", time.Now().Unix())
	log.Debugf("Creating container: %s", containerName)

	args := []string{"from", "--log-level=error", "scratch"}

	output, err := o.executeBuildah(args...)
	if err != nil {
		return "", fmt.Errorf("failed to create container: %w", err)
	}

	// Get the container ID from the output
	containerID := strings.TrimSpace(string(output))
	log.Debugf("Created container with ID: %s", containerID)
	return containerID, nil
}

// MountContainer mounts a container and returns its mount point
func (o *OCI) MountContainer(containerName string) (string, error) {
	log.Debugf("Mounting container: %s", containerName)

	args := []string{"mount", containerName}
	output, err := o.executeBuildah(args...)
	if err != nil {
		// Clean up the container if mount fails
		o.executeBuildah("rm", containerName) // Ignore errors during cleanup
		return "", fmt.Errorf("failed to mount parent image: %w", err)
	}
	mountPoint := strings.TrimSpace(string(output))
	log.Debugf("Parent image mounted at: %s", mountPoint)
	return mountPoint, nil
}

// UnmountContainer unmounts the container
func (o *OCI) UnmountContainer(containerName string) error {
	log.Debugf("Unmounting container: %s", containerName)
	args := []string{"umount", containerName}
	_, err := o.executeBuildah(args...)
	return err
}

// PushImage pushes the image to the registry
func (o *OCI) PushImage() error {
	log.Infof("Pushing image: %s to registry: %s", o.config.Options.Name, o.config.Options.PublishRegistry)

	// Clean registry URL and image path
	registry := utils.SanitizeRegistryURL(o.config.Options.PublishRegistry)
	imagePath := utils.SanitizeImagePath(o.config.Options.Name)

	// Combine to get the full image reference
	imageRef := fmt.Sprintf("%s/%s", registry, imagePath)
	log.Debugf("Pushing to image reference: %s", imageRef)

	// Build the push command
	args := []string{"push"}
	if len(o.config.Options.RegistryOptsPush) > 0 {
		args = append(args, o.config.Options.RegistryOptsPush...)
	}
	args = append(args, o.parentContainer, imageRef)

	// Cannot use the helper here because we need to set environment variables.
	var cmd *exec.Cmd
	var cmdStr string

	if os.Geteuid() == 0 {
		cmd = exec.Command("buildah", args...)
		cmdStr = "buildah " + strings.Join(args, " ")
	} else {
		cmdArgs := append([]string{"buildah"}, args...)
		cmd = exec.Command("unshare", cmdArgs...)
		cmdStr = "unshare " + strings.Join(cmdArgs, " ")
	}
	// Auth is handled by podman/buildah config files, no need to set env var explicitly
	// cmd.Env = append(os.Environ(),
	// 	fmt.Sprintf("REGISTRY_AUTH_FILE=%s", o.config.Options.Authfile),
	// )

	// Execute the push command
	log.Debugf("Executing: %s", cmdStr)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to push image: %w\nOutput: %s", err, string(output))
	}

	log.Infof("Successfully pushed image to %s", imageRef)
	return nil
}

// CommitContainer commits the changes to the container
func (o *OCI) CommitContainer(containerName, name string) error {
	log.Debugf("Committing container: %s", containerName)
	args := []string{"commit", containerName, name}
	_, err := o.executeBuildah(args...)
	if err != nil {
		return fmt.Errorf("failed to commit container %s: %w", containerName, err)
	}
	log.Debugf("Successfully committed container: %s", containerName)
	return nil
}

// ContainerInfo holds information about the container
type ContainerInfo struct {
	OS           string
	OSVersion    string
	Architecture string
	PackageCount int
}

// Cleanup removes the container
func (o *OCI) Cleanup(containerName string) error {
	log.Debugf("Cleaning up container: %s", containerName)

	// First, unmount the container
	if err := o.UnmountContainer(containerName); err != nil {
		log.Warnf("Failed to unmount container during cleanup (might already be unmounted): %v", err)
	}

	// Then, remove the container
	args := []string{"rm", containerName}
	if _, err := o.executeBuildah(args...); err != nil {
		return fmt.Errorf("failed to remove container %s: %w", containerName, err)
	}

	log.Debugf("Successfully cleaned up container: %s", containerName)
	return nil
}

// GetParentMountPoint returns the mount point of the parent container
func (o *OCI) GetParentMountPoint() string {
	return o.parentMountPoint
}

// GetParentContainer returns the name of the parent container
func (o *OCI) GetParentContainer() string {
	return o.parentContainer
}
