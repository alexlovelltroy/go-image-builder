package imageconfig

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type Repository struct {
	Alias    string `yaml:"alias"`
	Url      string `yaml:"url"`
	GPG      string `yaml:"gpg"`
	Proxy    string `yaml:"proxy"`
	Priority int    `yaml:"priority"`
}

// CopyFile represents a file to be copied into the rootfs
type CopyFile struct {
	Src  string   `yaml:"src"`
	Dest string   `yaml:"dest"`
	Opts []string `yaml:"opts"`
	Mode int      `yaml:"mode"`
}

type Config struct {
	Options struct {
		LayerType        string            `yaml:"layer_type"`
		Name             string            `yaml:"name"`
		PkgManager       string            `yaml:"pkg_manager"`
		Parent           string            `yaml:"parent"`
		PublishTags      string            `yaml:"publish_tags"`
		PublishRegistry  string            `yaml:"publish_registry"`
		PublishLocal     bool              `yaml:"publish_local"`
		PublishS3        string            `yaml:"publish_s3"`
		S3Prefix         string            `yaml:"s3_prefix"`
		S3Bucket         string            `yaml:"s3_bucket"`
		Groups           []string          `yaml:"groups"`
		Playbooks        []string          `yaml:"playbooks"`
		Inventory        []string          `yaml:"inventory"`
		Vars             map[string]any    `yaml:"vars"`
		AnsibleVerbosity int               `yaml:"ansible_verbosity"`
		Labels           map[string]string `yaml:"labels"`
		RegistryOptsPush []string          `yaml:"registry_opts_push"`
		RegistryOptsPull []string          `yaml:"registry_opts_pull"`
	} `yaml:"options"`
	Repositories   []Repository        `yaml:"repos"`
	Packages       []string            `yaml:"packages"`
	PackageGroups  []string            `yaml:"package_groups"`
	RemovePackages []string            `yaml:"remove_packages"`
	Modules        map[string][]string `yaml:"modules"`
	Cmds           []struct {
		Cmd      string `yaml:"cmd"`
		LogLevel string `yaml:"loglevel"`
	} `yaml:"cmds"`
	CopyFiles []CopyFile `yaml:"copyfiles"`
}

// ValidationError represents a configuration validation error
type ValidationError struct {
	Field string
	Msg   string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Field, e.Msg)
}

// Validate performs validation on the Config struct
func (c *Config) Validate() error {
	// Validate Options
	if c.Options.LayerType == "" {
		return &ValidationError{Field: "options.layer_type", Msg: "is required"}
	}
	if c.Options.LayerType != "base" && c.Options.LayerType != "ansible" {
		return &ValidationError{Field: "options.layer_type", Msg: "must be 'base' or 'ansible'"}
	}

	if c.Options.Name == "" {
		return &ValidationError{Field: "options.name", Msg: "is required"}
	}

	if c.Options.LayerType == "base" && c.Options.PkgManager == "" {
		return &ValidationError{Field: "options.pkg_manager", Msg: "is required for base layer"}
	}

	// Validate Repositories
	for i, repo := range c.Repositories {
		if repo.Alias == "" {
			return &ValidationError{Field: fmt.Sprintf("repositories[%d].alias", i), Msg: "is required"}
		}
		if repo.Url == "" {
			return &ValidationError{Field: fmt.Sprintf("repositories[%d].url", i), Msg: "is required"}
		}
	}

	// Validate Commands
	for i, cmd := range c.Cmds {
		if cmd.Cmd == "" {
			return &ValidationError{Field: fmt.Sprintf("cmds[%d].cmd", i), Msg: "is required"}
		}
		if cmd.LogLevel != "" {
			cmd.LogLevel = strings.ToUpper(cmd.LogLevel)
			if cmd.LogLevel != "INFO" && cmd.LogLevel != "DEBUG" && cmd.LogLevel != "WARNING" && cmd.LogLevel != "ERROR" {
				return &ValidationError{Field: fmt.Sprintf("cmds[%d].loglevel", i), Msg: "must be one of: INFO, DEBUG, WARNING, ERROR"}
			}
		}
	}

	// Validate CopyFiles
	for i, cf := range c.CopyFiles {
		if cf.Src == "" {
			return &ValidationError{Field: fmt.Sprintf("copyfiles[%d].src", i), Msg: "is required"}
		}
		if cf.Dest == "" {
			return &ValidationError{Field: fmt.Sprintf("copyfiles[%d].dest", i), Msg: "is required"}
		}
	}

	return nil
}

// LoadConfig loads and validates a configuration file
func LoadConfig(path string) (*Config, error) {
	// Read the configuration file
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// Parse the configuration
	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Validate the configuration
	if err := config.Validate(); err != nil {
		// Check if it's our custom validation error
		if valErr, ok := err.(*ValidationError); ok {
			return nil, fmt.Errorf("configuration validation failed:\n  %s", valErr.Error())
		}
		return nil, fmt.Errorf("configuration validation failed: %w", err)
	}

	return &config, nil
}

// WriteConfig writes a configuration to a YAML file
func WriteConfig(config *Config, path string) error {
	// Marshal the configuration to YAML
	data, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	// Write the configuration to the file
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}
