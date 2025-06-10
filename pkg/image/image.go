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
	img      v1.Image
	registry string
	name     string
	config   *imageconfig.Config
	tempDirs []string // Track temporary directories for cleanup
}

// detectOSFromRootfs reads /etc/os-release from the rootfs to determine the OS
func detectOSFromRootfs(rootfs string) (string, error) {
	osReleasePath := filepath.Join(rootfs, "etc", "os-release")
	if _, err := os.Stat(osReleasePath); os.IsNotExist(err) {
		return "linux", nil // Default to linux if os-release doesn't exist
	}

	content, err := os.ReadFile(osReleasePath)
	if err != nil {
		return "linux", fmt.Errorf("failed to read os-release: %w", err)
	}

	// Parse os-release content
	lines := strings.Split(string(content), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "ID=") {
			os := strings.Trim(strings.TrimPrefix(line, "ID="), "\"")
			return os, nil
		}
	}

	return "linux", nil // Default to linux if ID not found
}

// NewImage creates a new image with the given registry and name
func NewImage(registry, imgname string, cfg *imageconfig.Config) (*Image, error) {
	log.Debugf("Creating new image for registry: %s, name: %s", registry, imgname)

	// Sanitize registry and image name
	registry = utils.SanitizeRegistryURL(registry)
	imgname = utils.SanitizeImagePath(imgname)

	// Build the full image name
	fullName := utils.BuildImageReference(registry, imgname)
	log.Debugf("Full image name: %s", fullName)

	var img v1.Image
	var err error

	if cfg.Options.Parent != "" && cfg.Options.Parent != "scratch" {
		log.Debugf("Using parent image: %s", cfg.Options.Parent)
		// Pull and use parent image
		ref, err := name.ParseReference(cfg.Options.Parent, name.Insecure)
		if err != nil {
			return nil, fmt.Errorf("failed to parse parent image reference: %w", err)
		}
		img, err = crane.Pull(ref.String(), crane.Insecure)
		if err != nil {
			return nil, fmt.Errorf("failed to pull parent image: %w", err)
		}
		log.Debugf("Successfully pulled parent image")
	} else {
		log.Debug("Creating new empty image")
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
		img:      img,
		registry: registry,
		name:     fullName,
		config:   cfg,
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
	if err != nil {
		return fmt.Errorf("failed to read /etc/os-release: %w", err)
	}

	// Parse OS information
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

	// Add OS information
	config.Config.Labels["com.openchami.image.os.name"] = osInfo["NAME"]
	config.Config.Labels["com.openchami.image.os.version"] = osInfo["VERSION"]
	config.Config.Labels["com.openchami.image.os.id"] = osInfo["ID"]
	config.Config.Labels["com.openchami.image.os.id_like"] = osInfo["ID_LIKE"]

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

// AddKernelLayer adds a kernel layer to the image
func (i *Image) AddKernelLayer(kernelPath, kernelVersion string) error {
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
		Comment:   "Kernel Layer",
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
		Comment:   "Initrd Layer",
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

// Push pushes the image to the registry
func (i *Image) Push() error {
	log.Debugf("Starting image push to registry: %s", i.name)

	// Configure registry options
	log.Debug("Configuring registry options")
	opts := []crane.Option{
		crane.Insecure,
	}

	// Add registry options from config if specified
	if i.config.Options.RegistryOptsPush != nil {
		log.Debugf("Adding registry options: %v", i.config.Options.RegistryOptsPush)
		for _, opt := range i.config.Options.RegistryOptsPush {
			if opt == "--tls-verify=false" {
				opts = append(opts, crane.Insecure)
				log.Debug("Added insecure option for TLS verification")
			}
		}
	}

	// Get the base image reference
	log.Debug("Parsing image reference")
	baseRef, err := name.ParseReference(i.name, name.Insecure)
	if err != nil {
		return fmt.Errorf("failed to parse image reference: %w", err)
	}
	log.Debugf("Parsed image reference: %s", baseRef.String())

	// Verify the image exists and get its manifest
	log.Debug("Verifying image exists and getting manifest")
	manifest, err := i.img.Manifest()
	if err != nil {
		return fmt.Errorf("failed to get image manifest: %w", err)
	}
	log.Debugf("Image manifest verified with %d layers", len(manifest.Layers))
	for i, layer := range manifest.Layers {
		log.Debugf("Layer %d: Size=%d, Digest=%s", i, layer.Size, layer.Digest)
	}

	// Parse publish tags
	tags := strings.Split(i.config.Options.PublishTags, ",")
	if len(tags) == 0 {
		// If no tags specified, use the default tag
		tags = []string{baseRef.Identifier()}
	}
	log.Debugf("Publishing with tags: %v", tags)

	// Push the image with each tag
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}

		// Create tag reference
		taggedRef, err := name.NewTag(fmt.Sprintf("%s:%s", baseRef.Context().String(), tag), name.Insecure)
		if err != nil {
			return fmt.Errorf("failed to create tag reference: %w", err)
		}
		log.Debugf("Created tag reference: %s", taggedRef.String())

		// If we have a parent image, ensure it's pushed first
		if i.config.Options.Parent != "" && i.config.Options.Parent != "scratch" {
			parentRefStr := utils.SanitizeRegistryURL(i.config.Options.Parent)
			log.Debugf("Ensuring parent image is pushed: %s", parentRefStr)
			parentRef, err := name.ParseReference(parentRefStr, name.Insecure)
			if err != nil {
				return fmt.Errorf("failed to parse parent image reference: %w", err)
			}

			// Try to pull the parent image first to ensure it exists
			log.Debugf("Attempting to pull parent image: %s", parentRef.String())
			_, err = crane.Pull(parentRef.String(), opts...)
			if err != nil {
				log.Warnf("Failed to pull parent image (this is expected if it doesn't exist): %v", err)
			} else {
				log.Debugf("Successfully pulled parent image: %s", parentRef.String())
			}

			// Try to push the parent image first
			log.Debugf("Attempting to push parent image: %s", parentRef.String())
			if err := crane.Push(i.img, parentRef.String(), opts...); err != nil {
				log.Warnf("Failed to push parent image (this is expected if it already exists): %v", err)
			} else {
				log.Debugf("Successfully pushed parent image: %s", parentRef.String())
			}
		}

		// Push the image with retries
		maxRetries := 3
		var lastErr error
		for attempt := 0; attempt < maxRetries; attempt++ {
			if attempt > 0 {
				log.Debugf("Retrying push (attempt %d/%d)", attempt+1, maxRetries)
				time.Sleep(time.Second * time.Duration(attempt+1)) // Exponential backoff
			}

			log.Debugf("Pushing image with tag: %s (attempt %d)", tag, attempt+1)
			if err := crane.Push(i.img, taggedRef.String(), opts...); err != nil {
				lastErr = err
				log.Warnf("Push attempt %d failed: %v", attempt+1, err)

				// Check if we should retry based on the error
				if strings.Contains(err.Error(), "BLOB_UPLOAD_UNKNOWN") {
					log.Debug("BLOB_UPLOAD_UNKNOWN error detected, will retry")
					continue
				}

				// For other errors, don't retry
				break
			}

			log.Debugf("Successfully pushed image with tag: %s", tag)
			lastErr = nil
			break
		}

		if lastErr != nil {
			return fmt.Errorf("failed to push image with tag %s after %d attempts: %w", tag, maxRetries, lastErr)
		}
	}

	return nil
}

// ExtractKernel extracts the kernel from the image
func (i *Image) ExtractKernel(destPath string) error {
	// Find the kernel layer
	var kernelLayer v1.Layer
	layers, err := i.img.Layers()
	if err != nil {
		return fmt.Errorf("failed to get image layers: %w", err)
	}

	for _, layer := range layers {
		config, err := i.img.ConfigFile()
		if err != nil {
			return fmt.Errorf("failed to get image config: %w", err)
		}
		if config.Config.Labels["org.opencontainers.image.type"] == "kernel" {
			kernelLayer = layer
			break
		}
	}

	if kernelLayer == nil {
		return fmt.Errorf("kernel layer not found in image")
	}

	// Extract the kernel
	rc, err := kernelLayer.Uncompressed()
	if err != nil {
		return fmt.Errorf("failed to uncompress kernel layer: %w", err)
	}
	defer rc.Close()

	// Create destination directory
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return fmt.Errorf("failed to create destination directory: %w", err)
	}

	// Create destination file
	dst, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("failed to create destination file: %w", err)
	}
	defer dst.Close()

	// Copy kernel to destination
	if _, err := io.Copy(dst, rc); err != nil {
		return fmt.Errorf("failed to copy kernel: %w", err)
	}

	return nil
}

// ExtractInitrd extracts the initrd from the image
func (i *Image) ExtractInitrd(destPath string) error {
	// Find the initrd layer
	var initrdLayer v1.Layer
	layers, err := i.img.Layers()
	if err != nil {
		return fmt.Errorf("failed to get image layers: %w", err)
	}

	for _, layer := range layers {
		config, err := i.img.ConfigFile()
		if err != nil {
			return fmt.Errorf("failed to get image config: %w", err)
		}
		if config.Config.Labels["org.opencontainers.image.type"] == "initrd" {
			initrdLayer = layer
			break
		}
	}

	if initrdLayer == nil {
		return fmt.Errorf("initrd layer not found in image")
	}

	// Extract the initrd
	rc, err := initrdLayer.Uncompressed()
	if err != nil {
		return fmt.Errorf("failed to uncompress initrd layer: %w", err)
	}
	defer rc.Close()

	// Create destination directory
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return fmt.Errorf("failed to create destination directory: %w", err)
	}

	// Create destination file
	dst, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("failed to create destination file: %w", err)
	}
	defer dst.Close()

	// Copy initrd to destination
	if _, err := io.Copy(dst, rc); err != nil {
		return fmt.Errorf("failed to copy initrd: %w", err)
	}

	return nil
}

// Cleanup removes all temporary directories
func (i *Image) Cleanup() {
	for _, dir := range i.tempDirs {
		os.RemoveAll(dir)
	}
	i.tempDirs = nil
}
