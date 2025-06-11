package image

import (
	"fmt"
	"go-image-builder/pkg/imageconfig"
	"go-image-builder/pkg/utils"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"archive/tar"

	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/klauspost/compress/gzip"
	log "github.com/sirupsen/logrus"
)

// Image represents a container image
type Image struct {
	img           v1.Image
	registry      string
	name          string
	config        *imageconfig.Config
	tempDirs      []string // Track temporary directories for cleanup
	parentArchive string   // Path to temporary parent archive file, if any.
}

// NewImage creates a new image with the given registry and name.
// If a parentImage is provided, it will be used as the base. Otherwise, an empty image is created.
func NewImage(registry, imgname string, cfg *imageconfig.Config, parentImage v1.Image, parentArchivePath string) (*Image, error) {
	log.Debugf("Creating new image for registry: %s, name: %s", registry, imgname)

	// Sanitize registry and image name
	registry = utils.SanitizeRegistryURL(registry)
	imgname = utils.SanitizeImagePath(imgname)

	// Build the full image name
	fullName := utils.BuildImageReference(registry, imgname)
	log.Debugf("Full image name: %s", fullName)

	var img v1.Image
	var err error

	if parentImage != nil {
		log.Debug("Using provided parent image as base.")
		img = parentImage
	} else {
		log.Debug("No parent image provided, creating new empty image.")
		// Create empty image
		img, err = mutate.ConfigFile(empty.Image, &v1.ConfigFile{
			Architecture: "amd64",
			OS:           "linux",
			Created:      v1.Time{Time: time.Now().UTC()},
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create empty image: %w", err)
		}
	}

	return &Image{
		img:           img,
		registry:      registry,
		name:          fullName,
		config:        cfg,
		parentArchive: parentArchivePath,
	}, nil
}

// AddBaseLayer adds a base layer to the image
func (i *Image) AddBaseLayer(path string) error {
	log.Debugf("Adding base layer from path: %s", path)

	// Create a temporary directory for the layer
	tempDir, err := os.MkdirTemp("", "base-layer-*")
	if err != nil {
		return fmt.Errorf("failed to create temporary directory: %w", err)
	}

	// Only clean up on failure
	success := false
	defer func() {
		if !success {
			os.RemoveAll(tempDir)
		}
	}()

	// Create the tar archive
	tarPath := filepath.Join(tempDir, "layer.tar")
	log.Debugf("Creating tar archive at: %s", tarPath)
	cmd := exec.Command("tar", "-cf", tarPath, "-C", path, ".")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to create tar archive: %w\nOutput: %s", err, string(output))
	}
	log.Debug("Tar archive created successfully")

	// Create the layer
	log.Debug("Creating layer from tar file")
	layer, err := tarball.LayerFromFile(tarPath, tarball.WithCompressionLevel(gzip.BestCompression))
	if err != nil {
		return fmt.Errorf("failed to create layer: %w", err)
	}
	log.Debug("Layer created successfully")

	// Get current config
	config, err := i.img.ConfigFile()
	if err != nil {
		return fmt.Errorf("failed to get image config: %w", err)
	}

	// Read OS information from /etc/os-release
	osReleasePath := filepath.Join(path, "etc", "os-release")
	osReleaseData, err := os.ReadFile(osReleasePath)
	if err == nil {
		// If os-release exists in the new layer, parse it and set the labels.
		log.Debug("Found /etc/os-release in new layer, parsing for OS info.")
		osInfo := make(map[string]string)
		for _, line := range strings.Split(string(osReleaseData), "\n") {
			if line == "" {
				continue
			}
			parts := strings.SplitN(line, "=", 2)
			if len(parts) != 2 {
				continue
			}
			key := parts[0]
			value := strings.Trim(parts[1], "\"")
			osInfo[key] = value
		}
		if config.Config.Labels == nil {
			config.Config.Labels = make(map[string]string)
		}
		config.Config.Labels["com.openchami.image.os.name"] = osInfo["NAME"]
		config.Config.Labels["com.openchami.image.os.version"] = osInfo["VERSION"]
		config.Config.Labels["com.openchami.image.os.id"] = osInfo["ID"]
		config.Config.Labels["com.openchami.image.os.id_like"] = osInfo["ID_LIKE"]
	} else if os.IsNotExist(err) {
		// If it doesn't exist, we just log it. The labels from the parent (if any)
		// are already in the config and will be preserved.
		log.Warn("'/etc/os-release' not found in new layer. OS labels will be inherited from parent if available.")
	} else {
		// For any other error, we fail.
		return fmt.Errorf("failed to read /etc/os-release: %w", err)
	}

	// Get build information
	hostname, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("failed to get hostname: %w", err)
	}
	username := os.Getenv("USER")

	// Add base layer annotations
	if config.Config.Labels == nil {
		config.Config.Labels = make(map[string]string)
	}

	// Add parent image information
	if i.config.Options.Parent != "" && i.config.Options.Parent != "scratch" {
		config.Config.Labels["com.openchami.image.parent"] = i.config.Options.Parent
		// Get parent image layers
		parentLayers, err := i.img.Layers()
		if err != nil {
			return fmt.Errorf("failed to get parent layers: %w", err)
		}
		log.Debugf("Parent image has %d layers", len(parentLayers))
	}

	// Add build information
	config.Config.Labels["com.openchami.image.build.host"] = hostname
	config.Config.Labels["com.openchami.image.build.user"] = username

	// Update the image creation time
	now := time.Now().UTC()
	config.Created = v1.Time{Time: now}

	// Update history
	config.History = append(config.History, v1.History{
		Created:   v1.Time{Time: now},
		CreatedBy: "go-image-builder",
		Comment:   "Base OS Layer",
	})

	// Update image config
	i.img, err = mutate.ConfigFile(i.img, config)
	if err != nil {
		return fmt.Errorf("failed to update image config: %w", err)
	}

	// Add the layer to the image
	log.Debug("Appending layer to image")
	i.img, err = mutate.AppendLayers(i.img, layer)
	if err != nil {
		return fmt.Errorf("failed to append layer: %w", err)
	}
	log.Debug("Layer appended successfully")

	// Mark success
	success = true

	// Store the temporary directory path
	i.tempDirs = append(i.tempDirs, tempDir)

	return nil
}

// HasLayerWithComment checks if the parent image contains a layer with the specified comment.
func (i *Image) HasLayerWithComment(comment string) (bool, error) {
	if i.config.Options.Parent == "" || i.config.Options.Parent == "scratch" {
		return false, nil // No parent to search.
	}

	parentConfig, err := i.img.ConfigFile()
	if err != nil {
		return false, fmt.Errorf("failed to get parent image config: %w", err)
	}

	for _, h := range parentConfig.History {
		if h.Comment == comment {
			return true, nil
		}
	}

	return false, nil
}

// findAndCopyLayerFromParent searches the parent image for a layer with a specific
// comment in its history. If found, it appends the layer and its configuration
// to the new image, returning true.
func (i *Image) findAndCopyLayerFromParent(layerComment string) (bool, error) {
	if i.config.Options.Parent == "" || i.config.Options.Parent == "scratch" {
		return false, nil // No parent to search.
	}

	parentConfig, err := i.img.ConfigFile()
	if err != nil {
		return false, fmt.Errorf("failed to get parent image config: %w", err)
	}

	// Find the history entry matching the comment.
	historyIndex := -1
	for idx, h := range parentConfig.History {
		if h.Comment == layerComment {
			historyIndex = idx
			break
		}
	}

	if historyIndex == -1 {
		log.Debugf("Did not find history comment '%s' in parent image. Will create new layer.", layerComment)
		return false, nil
	}

	// The history list corresponds to the list of diff_ids in the config, which
	// also corresponds to the layers.
	parentLayers, err := i.img.Layers()
	if err != nil {
		return false, fmt.Errorf("failed to get parent layers: %w", err)
	}

	if historyIndex >= len(parentLayers) {
		return false, fmt.Errorf("history index %d is out of bounds for parent layers (count: %d)", historyIndex, len(parentLayers))
	}

	log.Infof("Found existing '%s' in parent image, appending it to the new image.", layerComment)

	// Get the target layer and its history entry.
	layerToAdd := parentLayers[historyIndex]
	historyToAdd := parentConfig.History[historyIndex]

	// Get the current config of our *new* image.
	newConfig, err := i.img.ConfigFile()
	if err != nil {
		return false, fmt.Errorf("failed to get new image config: %w", err)
	}

	// Copy relevant labels from the parent.
	if newConfig.Config.Labels == nil {
		newConfig.Config.Labels = make(map[string]string)
	}
	for key, value := range parentConfig.Config.Labels {
		if strings.Contains(key, "kernel") && strings.Contains(layerComment, "Kernel") {
			newConfig.Config.Labels[key] = value
		}
		if strings.Contains(key, "initrd") && strings.Contains(layerComment, "Initrd") {
			newConfig.Config.Labels[key] = value
		}
	}

	// Append the layer and its history to the new image.
	i.img, err = mutate.Append(i.img, mutate.Addendum{
		Layer:   layerToAdd,
		History: historyToAdd,
	})
	if err != nil {
		return false, fmt.Errorf("failed to append parent layer '%s': %w", layerComment, err)
	}

	// Update the config with the copied labels.
	i.img, err = mutate.ConfigFile(i.img, newConfig)
	if err != nil {
		return false, fmt.Errorf("failed to update config with labels from parent layer '%s': %w", layerComment, err)
	}

	return true, nil
}

// AddKernelLayer adds a kernel layer to the image
func (i *Image) AddKernelLayer(kernelPath, kernelVersion string) error {
	layerComment := "Kernel Layer"
	copied, err := i.findAndCopyLayerFromParent(layerComment)
	if err != nil {
		return err
	}
	if copied {
		return nil // Success, layer was copied from parent.
	}

	// Fallback: If not copied, create the layer from scratch.
	log.Debugf("Adding kernel layer from path: %s", kernelPath)

	// Create a temporary directory for the layer
	tempDir, err := os.MkdirTemp("", "kernel-layer-*")
	if err != nil {
		return fmt.Errorf("failed to create temporary directory: %w", err)
	}

	// Only clean up on failure
	success := false
	defer func() {
		if !success {
			os.RemoveAll(tempDir)
		}
	}()

	// Create the layer structure
	layerPath := filepath.Join(tempDir, "layer")
	if err := os.MkdirAll(layerPath, 0755); err != nil {
		return fmt.Errorf("failed to create layer directory: %w", err)
	}

	// Copy the kernel to the layer
	kernelDest := filepath.Join(layerPath, "boot", "vmlinuz")
	if err := os.MkdirAll(filepath.Dir(kernelDest), 0755); err != nil {
		return fmt.Errorf("failed to create boot directory: %w", err)
	}

	kernelData, err := os.ReadFile(kernelPath)
	if err != nil {
		return fmt.Errorf("failed to read kernel: %w", err)
	}

	if err := os.WriteFile(kernelDest, kernelData, 0644); err != nil {
		return fmt.Errorf("failed to write kernel: %w", err)
	}

	// Create the tar archive
	tarPath := filepath.Join(tempDir, "layer.tar")
	cmd := exec.Command("tar", "-cf", tarPath, "-C", layerPath, ".")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to create tar archive: %w\nOutput: %s", err, string(output))
	}

	// Create the layer
	layer, err := tarball.LayerFromFile(tarPath)
	if err != nil {
		return fmt.Errorf("failed to create layer: %w", err)
	}

	// Get current config
	config, err := i.img.ConfigFile()
	if err != nil {
		return fmt.Errorf("failed to get image config: %w", err)
	}

	// Add kernel version to labels
	if config.Config.Labels == nil {
		config.Config.Labels = make(map[string]string)
	}
	config.Config.Labels["com.openchami.image.kernel-version"] = kernelVersion

	// Update the image creation time
	now := time.Now().UTC()
	config.Created = v1.Time{Time: now}

	// Update history
	config.History = append(config.History, v1.History{
		Created:   v1.Time{Time: now},
		CreatedBy: "go-image-builder",
		Comment:   layerComment,
	})

	// Update image config
	i.img, err = mutate.ConfigFile(i.img, config)
	if err != nil {
		return fmt.Errorf("failed to update image config: %w", err)
	}

	// Add the layer to the image
	i.img, err = mutate.AppendLayers(i.img, layer)
	if err != nil {
		return fmt.Errorf("failed to add layer: %w", err)
	}

	// Mark success
	success = true

	// Store the temporary directory path
	i.tempDirs = append(i.tempDirs, tempDir)

	return nil
}

// AddInitrdLayer adds an initrd layer to the image
func (i *Image) AddInitrdLayer(initrdPath string) error {
	layerComment := "Initrd Layer"
	copied, err := i.findAndCopyLayerFromParent(layerComment)
	if err != nil {
		return err
	}
	if copied {
		return nil // Success, layer was copied from parent.
	}

	// Fallback: If not copied, create the layer from scratch.
	log.Debugf("Adding initrd layer from path: %s", initrdPath)

	// Create a temporary directory for the layer
	tempDir, err := os.MkdirTemp("", "initrd-layer-*")
	if err != nil {
		return fmt.Errorf("failed to create temporary directory: %w", err)
	}

	// Only clean up on failure
	success := false
	defer func() {
		if !success {
			os.RemoveAll(tempDir)
		}
	}()

	// Create the layer structure
	layerPath := filepath.Join(tempDir, "layer")
	if err := os.MkdirAll(layerPath, 0755); err != nil {
		return fmt.Errorf("failed to create layer directory: %w", err)
	}

	// Copy the initrd to the layer
	initrdDest := filepath.Join(layerPath, "boot", "initrd.img")
	if err := os.MkdirAll(filepath.Dir(initrdDest), 0755); err != nil {
		return fmt.Errorf("failed to create boot directory: %w", err)
	}

	initrdData, err := os.ReadFile(initrdPath)
	if err != nil {
		return fmt.Errorf("failed to read initrd: %w", err)
	}

	if err := os.WriteFile(initrdDest, initrdData, 0644); err != nil {
		return fmt.Errorf("failed to write initrd: %w", err)
	}

	// Create the tar archive
	tarPath := filepath.Join(tempDir, "layer.tar")
	cmd := exec.Command("tar", "-cf", tarPath, "-C", layerPath, ".")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to create tar archive: %w\nOutput: %s", err, string(output))
	}

	// Create the layer
	layer, err := tarball.LayerFromFile(tarPath)
	if err != nil {
		return fmt.Errorf("failed to create layer: %w", err)
	}

	// Get current config
	config, err := i.img.ConfigFile()
	if err != nil {
		return fmt.Errorf("failed to get image config: %w", err)
	}

	// Update the image creation time
	now := time.Now().UTC()
	config.Created = v1.Time{Time: now}

	// Update history
	config.History = append(config.History, v1.History{
		Created:   v1.Time{Time: now},
		CreatedBy: "go-image-builder",
		Comment:   layerComment,
	})

	// Update image config
	i.img, err = mutate.ConfigFile(i.img, config)
	if err != nil {
		return fmt.Errorf("failed to update image config: %w", err)
	}

	// Add the layer to the image
	i.img, err = mutate.AppendLayers(i.img, layer)
	if err != nil {
		return fmt.Errorf("failed to add layer: %w", err)
	}

	// Mark success
	success = true

	// Store the temporary directory path
	i.tempDirs = append(i.tempDirs, tempDir)

	return nil
}

// AddConfigLayer adds the configuration as a separate layer
func (i *Image) AddConfigLayer() error {
	log.Debug("Adding config layer")

	// Create a temporary directory for the layer
	tempDir, err := os.MkdirTemp("", "config-layer-*")
	if err != nil {
		return fmt.Errorf("failed to create temporary directory: %w", err)
	}

	// Only clean up on failure
	success := false
	defer func() {
		if !success {
			os.RemoveAll(tempDir)
		}
	}()

	// Create the layer structure
	layerPath := filepath.Join(tempDir, "layer")
	if err := os.MkdirAll(layerPath, 0755); err != nil {
		return fmt.Errorf("failed to create layer directory: %w", err)
	}

	// Write the config to the layer
	configPath := filepath.Join(layerPath, "etc", "image-config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		return fmt.Errorf("failed to create etc directory: %w", err)
	}

	if err := imageconfig.WriteConfig(i.config, configPath); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	// Create the tar archive
	tarPath := filepath.Join(tempDir, "layer.tar")
	cmd := exec.Command("tar", "-cf", tarPath, "-C", layerPath, ".")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to create tar archive: %w\nOutput: %s", err, string(output))
	}

	// Create the layer
	layer, err := tarball.LayerFromFile(tarPath)
	if err != nil {
		return fmt.Errorf("failed to create layer: %w", err)
	}

	// Get current config
	config, err := i.img.ConfigFile()
	if err != nil {
		return fmt.Errorf("failed to get image config: %w", err)
	}

	// Update the image creation time
	now := time.Now().UTC()
	config.Created = v1.Time{Time: now}

	// Update history
	config.History = append(config.History, v1.History{
		Created:   v1.Time{Time: now},
		CreatedBy: "go-image-builder",
		Comment:   "Configuration Layer",
	})

	// Update image config
	i.img, err = mutate.ConfigFile(i.img, config)
	if err != nil {
		return fmt.Errorf("failed to update image config: %w", err)
	}

	// Add the layer to the image
	i.img, err = mutate.AppendLayers(i.img, layer)
	if err != nil {
		return fmt.Errorf("failed to add layer: %w", err)
	}

	// Mark success
	success = true

	// Store the temporary directory path
	i.tempDirs = append(i.tempDirs, tempDir)

	return nil
}

// Push pushes the image to the registry, handling multiple tags and retries.
func (i *Image) Push() error {
	log.Debugf("Starting image push to registry: %s", i.name)
	baseRef, err := name.ParseReference(i.name, name.Insecure)
	if err != nil {
		return fmt.Errorf("failed to parse image reference: %w", err)
	}

	// 1. Ensure the parent image exists in the registry first.
	if err := i.ensureParentImage(); err != nil {
		// Log as a warning because this might not be a fatal error if the
		// parent already exists and is accessible.
		log.Warnf("Could not ensure parent image exists (this may be safe to ignore): %v", err)
	}

	// 2. Get the list of tags to publish.
	tags := strings.Split(i.config.Options.PublishTags, ",")
	if len(tags) == 0 || (len(tags) == 1 && tags[0] == "") {
		tags = []string{baseRef.Identifier()} // Default to the base reference's identifier (e.g., 'latest')
	}
	log.Debugf("Publishing with tags: %v", tags)

	// 3. Push the image with each tag.
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if err := i.pushTagWithRetries(baseRef, tag); err != nil {
			return err // Return on the first failed tag push
		}
	}

	log.Infof("Successfully pushed all tags for image: %s", i.name)
	return nil
}

// ensureParentImage checks for the parent image in the remote registry and pushes it
// if it's not available. This is important for ensuring layers can be found.
func (i *Image) ensureParentImage() error {
	if i.config.Options.Parent == "" || i.config.Options.Parent == "scratch" {
		return nil // No parent to ensure.
	}

	log.Debugf("Ensuring parent image is pushed: %s", i.config.Options.Parent)
	parentRefStr := utils.SanitizeRegistryURL(i.config.Options.Parent)
	parentRef, err := name.ParseReference(parentRefStr, name.Insecure)
	if err != nil {
		return fmt.Errorf("failed to parse parent image reference '%s': %w", parentRefStr, err)
	}

	// Attempting to pull the parent's manifest is a lightweight way to check if it exists.
	if _, err := crane.Manifest(parentRef.String(), crane.Insecure); err == nil {
		log.Debugf("Parent image manifest found in registry: %s", parentRef.String())
		return nil // Parent already exists.
	}

	// If the parent doesn't exist, we need to push it. This assumes the current
	// image `i.img` was built from this parent and contains all its layers.
	log.Infof("Parent image not found in registry, attempting to push it: %s", parentRef.String())
	if err := crane.Push(i.img, parentRef.String(), crane.Insecure); err != nil {
		return fmt.Errorf("failed to push parent image: %w", err)
	}
	log.Debugf("Successfully pushed parent image: %s", parentRef.String())
	return nil
}

// pushTagWithRetries handles the logic of pushing a single tag, including retries
// with exponential backoff for specific, recoverable errors.
func (i *Image) pushTagWithRetries(baseRef name.Reference, tag string) error {
	taggedRef, err := name.NewTag(fmt.Sprintf("%s:%s", baseRef.Context().String(), tag), name.Insecure)
	if err != nil {
		return fmt.Errorf("failed to create tag reference for tag '%s': %w", tag, err)
	}

	maxRetries := 3
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Second * time.Duration(2*attempt) // Exponential backoff
			log.Debugf("Retrying push for tag %s in %v (attempt %d/%d)", tag, backoff, attempt+1, maxRetries)
			time.Sleep(backoff)
		}

		log.Infof("Pushing image with tag: %s", taggedRef.String())
		err = crane.Push(i.img, taggedRef.String(), crane.Insecure)
		if err == nil {
			log.Infof("Successfully pushed tag: %s", taggedRef.String())
			return nil // Success
		}

		lastErr = err
		log.Warnf("Push attempt %d for tag %s failed: %v", attempt+1, tag, err)

		// Only retry on "BLOB_UPLOAD_UNKNOWN", which can be a transient registry issue.
		if !strings.Contains(err.Error(), "BLOB_UPLOAD_UNKNOWN") {
			log.Errorf("Unrecoverable error while pushing tag %s, stopping retries.", tag)
			break
		}
	}

	return fmt.Errorf("failed to push tag %s after %d attempts: %w", tag, maxRetries, lastErr)
}

// extractFile is a helper to extract a single file from the image layers.
func (i *Image) extractFile(pathInImage, destPath string) error {
	layers, err := i.img.Layers()
	if err != nil {
		return fmt.Errorf("could not get layers: %w", err)
	}

	// Loop through layers in reverse to find the last version of the file.
	for j := len(layers) - 1; j >= 0; j-- {
		layer := layers[j]
		rc, err := layer.Uncompressed()
		if err != nil {
			return fmt.Errorf("could not uncompress layer %d: %w", j, err)
		}
		defer rc.Close()

		tr := tar.NewReader(rc)
		for {
			header, err := tr.Next()
			if err == io.EOF {
				break // End of layer
			}
			if err != nil {
				return fmt.Errorf("error reading layer tar %d: %w", j, err)
			}

			// The path in the tarball is relative, so we add a leading /
			if filepath.Clean("/"+header.Name) == pathInImage {
				if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
					return fmt.Errorf("failed to create destination directory for '%s': %w", destPath, err)
				}
				outFile, err := os.Create(destPath)
				if err != nil {
					return fmt.Errorf("failed to create destination file '%s': %w", destPath, err)
				}
				defer outFile.Close()

				if _, err := io.Copy(outFile, tr); err != nil {
					return fmt.Errorf("failed to copy file content for '%s': %w", pathInImage, err)
				}
				log.Debugf("Extracted '%s' to '%s'", pathInImage, destPath)
				return nil // File found and extracted.
			}
		}
	}

	return fmt.Errorf("file '%s' not found in any layer of the image", pathInImage)
}

// ExtractKernel extracts the kernel from the image and saves it to the destination path.
// The kernel is expected to be located at /boot/vmlinuz in the image.
func (i *Image) ExtractKernel(destPath string) error {
	return i.extractFile("/boot/vmlinuz", destPath)
}

// ExtractInitrd extracts the initrd from the image
func (i *Image) ExtractInitrd(destPath string) error {
	return i.extractFile("/boot/initrd.img", destPath)
}

// Cleanup removes all temporary directories and files created during the image build.
func (i *Image) Cleanup() {
	log.Debugf("Cleaning up temporary build artifacts")
	for _, dir := range i.tempDirs {
		log.Debugf("Removing temporary directory: %s", dir)
		os.RemoveAll(dir)
	}

	if i.parentArchive != "" {
		log.Debugf("Removing temporary parent archive: %s", i.parentArchive)
		os.Remove(i.parentArchive)
	}
}
