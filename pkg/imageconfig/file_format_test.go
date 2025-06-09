package imageconfig

import (
	"testing"
)

func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name    string
		config  Config
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid base layer config",
			config: Config{
				Options: struct {
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
				}{
					LayerType:  "base",
					Name:       "test-image",
					PkgManager: "dnf",
				},
			},
			wantErr: false,
		},
		{
			name: "missing layer type",
			config: Config{
				Options: struct {
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
				}{
					Name:       "test-image",
					PkgManager: "dnf",
				},
			},
			wantErr: true,
			errMsg:  "options.layer_type: is required",
		},
		{
			name: "invalid layer type",
			config: Config{
				Options: struct {
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
				}{
					LayerType: "invalid",
					Name:      "test-image",
				},
			},
			wantErr: true,
			errMsg:  "options.layer_type: must be 'base' or 'ansible'",
		},
		{
			name: "missing name",
			config: Config{
				Options: struct {
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
				}{
					LayerType:  "base",
					PkgManager: "dnf",
				},
			},
			wantErr: true,
			errMsg:  "options.name: is required",
		},
		{
			name: "base layer missing pkg_manager",
			config: Config{
				Options: struct {
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
				}{
					LayerType: "base",
					Name:      "test-image",
				},
			},
			wantErr: true,
			errMsg:  "options.pkg_manager: is required for base layer",
		},
		{
			name: "invalid repository config",
			config: Config{
				Options: struct {
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
				}{
					LayerType:  "base",
					Name:       "test-image",
					PkgManager: "dnf",
				},
				Repositories: []Repository{
					{
						Alias: "test-repo",
						// Missing URL
					},
					{
						Alias: "test-repo",
						Url:   "https://test.repo",
						GPG:   "https://test.gpg",
					},
				},
			},
			wantErr: true,
			errMsg:  "repositories[0].url: is required",
		},
		{
			name: "invalid command config",
			config: Config{
				Options: struct {
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
				}{
					LayerType:  "base",
					Name:       "test-image",
					PkgManager: "dnf",
				},
				Cmds: []struct {
					Cmd      string `yaml:"cmd"`
					LogLevel string `yaml:"loglevel"`
				}{
					{
						Cmd:      "echo test",
						LogLevel: "INVALID",
					},
				},
			},
			wantErr: true,
			errMsg:  "cmds[0].loglevel: must be one of: INFO, DEBUG, WARNING, ERROR",
		},
		{
			name: "invalid copyfiles config",
			config: Config{
				Options: struct {
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
				}{
					LayerType:  "base",
					Name:       "test-image",
					PkgManager: "dnf",
				},
				CopyFiles: []CopyFile{
					{
						Src:  "source.txt",
						Dest: "dest.txt",
						Opts: []string{"--recursive"},
						Mode: 0644,
					},
				},
			},
			wantErr: true,
			errMsg:  "copyfiles[0].dest: is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Config.Validate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil && err.Error() != tt.errMsg {
				t.Errorf("Config.Validate() error message = %v, want %v", err.Error(), tt.errMsg)
			}
		})
	}
}
