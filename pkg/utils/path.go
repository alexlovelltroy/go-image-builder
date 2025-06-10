package utils

import (
	"strings"
)

// SanitizeRegistryURL removes any protocol prefix and trailing slashes from a registry URL
func SanitizeRegistryURL(registry string) string {
	// Remove protocol prefix if present
	registry = strings.TrimPrefix(registry, "http://")
	registry = strings.TrimPrefix(registry, "https://")

	// Remove trailing slashes
	registry = strings.TrimRight(registry, "/")

	return registry
}

// SanitizeImagePath removes any double slashes and trailing slashes from an image path
func SanitizeImagePath(path string) string {
	// Remove any double slashes
	path = strings.ReplaceAll(path, "//", "/")

	// Remove trailing slash
	path = strings.TrimRight(path, "/")

	return path
}

// BuildImageReference combines registry and image name into a properly formatted reference
func BuildImageReference(registry, imageName string) string {
	registry = SanitizeRegistryURL(registry)
	imageName = SanitizeImagePath(imageName)

	if registry == "" {
		return imageName
	}

	return registry + "/" + imageName
}
