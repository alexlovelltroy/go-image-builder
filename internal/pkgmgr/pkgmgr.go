package pkgmgr

import (
	"go-image-builder/pkg/imageconfig"
)

// PackageManager defines the interface for package management operations
type PackageManager interface {
	InitRootfs(rootfs string, config imageconfig.Config) error
	AddRepos(rootfs string, repos []imageconfig.Repository) error
	InstallPackages(rootfs string, packages []string, groups []string) error
	RunCommand(rootfs string, cmd string) error
	Cleanup(rootfs string) error
	CopyFiles(rootfs string, files []imageconfig.CopyFile) error
}
