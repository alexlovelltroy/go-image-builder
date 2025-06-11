package oci

import (
	"bytes"
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
	if o.config.Options.Parent == "" || o.config.Options.Parent == "scratch" {
		log.Info("No parent image specified, starting from scratch")
		return nil
	}

	parentImage := o.config.Options.Parent
	log.Infof("Checking for local parent image: %s", parentImage)

	// 1. Check if image exists locally using 'buildah inspect'.
	inspectArgs := []string{"inspect", "--type=image", parentImage}
	if _, err := o.executeBuildah(inspectArgs...); err == nil {
		log.Infof("Parent image '%s' found locally, using it.", parentImage)
		log.Debug("Note: To force a refresh, remove the local image manually before running.")
		return nil
	}

	log.Infof("Parent image '%s' not found locally. Pulling from registry...", parentImage)

	// Clean up any existing containers first. This is good practice.
	cleanupArgs := []string{"containers", "--format", "{{.ContainerID}}"}
	if output, err := o.executeBuildah(cleanupArgs...); err == nil {
		containers := strings.Split(strings.TrimSpace(string(output)), "\n")
		for _, container := range containers {
			if container == "" {
				continue
			}
			log.Debugf("Cleaning up stale container: %s", container)
			o.executeBuildah("rm", container) // Ignore errors during cleanup
		}
	}

	// Clean up any dangling images to save space.
	o.executeBuildah("prune", "-f") // Ignore errors during cleanup

	// 2. If not local, pull it.
	pullArgs := []string{"pull"}
	if o.config.Options.PublishRegistry != "" {
		pullArgs = append(pullArgs, o.config.Options.RegistryOptsPull...)
	}
	pullArgs = append(pullArgs, parentImage)

	if _, err := o.executeBuildah(pullArgs...); err != nil {
		return err // executeBuildah will provide detailed error
	}

	// 3. Verify the image exists locally after pull.
	log.Debugf("Verifying parent image '%s' exists locally after pull", parentImage)
	if _, err := o.executeBuildah(inspectArgs...); err != nil {
		return fmt.Errorf("failed to inspect parent image '%s' after pulling: %w", parentImage, err)
	}

	log.Infof("Successfully pulled parent image: %s", parentImage)
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

// SaveImage saves a locally stored image to a Docker v2.2 archive tarball at the destination path.
func (o *OCI) SaveImage(imageName, destinationPath string) error {
	log.Debugf("Saving image '%s' to Docker archive at '%s'", imageName, destinationPath)
	pushArgs := []string{
		"push",
		imageName,
		fmt.Sprintf("docker-archive:%s", destinationPath),
	}
	if _, err := o.executeBuildah(pushArgs...); err != nil {
		return fmt.Errorf("failed to save image '%s' to archive: %w", imageName, err)
	}
	return nil
}

// RunCommand executes a command inside the specified container.
func (o *OCI) RunCommand(containerName, command string) error {
	log.Debugf("Running command '%s' in container '%s'", command, containerName)
	args := []string{
		"run",
		containerName,
		"--",
		"sh", "-c", command,
	}
	if _, err := o.executeBuildah(args...); err != nil {
		return fmt.Errorf("failed to run command '%s': %w", command, err)
	}
	return nil
}

// RunCommandWithOutput executes a command inside the container and returns its output.
func (o *OCI) RunCommandWithOutput(containerName, command string) ([]byte, error) {
	log.Debugf("Running command '%s' in container '%s' and capturing output", command, containerName)
	args := []string{
		"run",
		containerName,
		"--",
		"sh", "-c", command,
	}
	output, err := o.executeBuildah(args...)
	if err != nil {
		return nil, fmt.Errorf("failed to run command '%s' with output: %w", command, err)
	}
	return output, nil
}

// Stat checks for the existence of a file or directory inside a container.
func (o *OCI) Stat(containerName, path string) error {
	log.Debugf("Checking for existence of '%s' in container '%s'", path, containerName)
	args := []string{"run", containerName, "--", "stat", path}
	// We discard the output, we only care about the exit code.
	_, err := o.executeBuildah(args...)
	return err
}

// CopyFromContainerWithCat copies a file from the container to a destination path on the
// host by running 'cat' inside the container and redirecting the output. This is
// more reliable than 'buildah copy' for single files in some environments.
func (o *OCI) CopyFromContainerWithCat(containerName, fromPath, toPath string) error {
	log.Debugf("Copying from container '%s:%s' to host '%s' using 'cat'", containerName, fromPath, toPath)

	// Create the destination file on the host.
	hostFile, err := os.Create(toPath)
	if err != nil {
		return fmt.Errorf("failed to create destination file '%s' on host: %w", toPath, err)
	}
	defer hostFile.Close()

	// Prepare the 'buildah run' command.
	var cmd *exec.Cmd
	var cmdStr string
	args := []string{"run", containerName, "--", "cat", fromPath}

	if os.Geteuid() == 0 {
		cmd = exec.Command("buildah", args...)
		cmdStr = "buildah " + strings.Join(args, " ")
	} else {
		cmdArgs := append([]string{"buildah"}, args...)
		cmd = exec.Command("unshare", cmdArgs...)
		cmdStr = "unshare " + strings.Join(cmdArgs, " ")
	}

	cmd.Stdout = hostFile // Redirect stdout directly to the host file.

	// Capture stderr for better error messages.
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	log.Debugf("Executing: %s", cmdStr)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to run 'cat' in container for '%s': %w\nStderr: %s", fromPath, err, stderr.String())
	}

	return nil
}
