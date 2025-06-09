package pkgmgr

import (
	"bufio"
	"fmt"
	"go-image-builder/pkg/imageconfig"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	log "github.com/sirupsen/logrus"
)

type DNF struct{}

func (d *DNF) InitRootfs(root string, config imageconfig.Config) error {
	log.Infof("Installing dnf in %s", root)

	// Create necessary directories
	dirs := []string{
		filepath.Join(root, "etc", "yum.repos.d"),
		filepath.Join(root, "var", "log", "dnf"),
		filepath.Join(root, "var", "cache", "dnf"),
		filepath.Join(root, "etc", "pki", "rpm-gpg"),
		filepath.Join(root, "var", "lib", "rpm"),
		filepath.Join(root, "var", "lib", "dnf"),
		filepath.Join(root, "etc", "dnf"),
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	// Copy resolv.conf for DNS resolution
	resolvConf := "/etc/resolv.conf"
	if _, err := os.Stat(resolvConf); err == nil {
		destResolvConf := filepath.Join(root, "etc", "resolv.conf")
		if err := os.Link(resolvConf, destResolvConf); err != nil {
			// If hard link fails, try copying
			if err := copyFile(resolvConf, destResolvConf); err != nil {
				return fmt.Errorf("failed to copy resolv.conf: %w", err)
			}
		}
	}

	// Add repositories first
	if err := d.AddRepos(root, config.Repositories); err != nil {
		return fmt.Errorf("failed to add repositories: %w", err)
	}

	// Install minimal packages using host's dnf
	cmd := exec.Command("dnf",
		"--installroot", root,
		"--releasever", "9", // TODO: Get from config
		"install",
		"--assumeyes",
		"--setopt=install_weak_deps=False",
		"dnf",
		"yum",
		"systemd",
		"filesystem",
		"setup",
		"shadow-utils",
		"rootfiles",
		"bash",
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to install dnf: %w\nOutput: %s", err, string(output))
	}

	return nil
}

// copyFile copies a file from src to dst
func copyFile(src, dst string) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	destFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destFile.Close()

	_, err = io.Copy(destFile, sourceFile)
	if err != nil {
		return err
	}

	// Copy file mode
	sourceInfo, err := os.Stat(src)
	if err != nil {
		return err
	}

	return os.Chmod(dst, sourceInfo.Mode())
}

func (d *DNF) AddRepos(root string, repos []imageconfig.Repository) error {
	if len(repos) == 0 {
		log.Debug("No repositories to add")
		return nil
	}

	// Create repo directory if it doesn't exist
	repoDir := filepath.Join(root, "etc", "yum.repos.d")
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		return fmt.Errorf("failed to create repo directory: %w", err)
	}

	for _, repo := range repos {
		log.Debugf("Adding repository: %s", repo.Alias)
		repoFile := filepath.Join(repoDir, fmt.Sprintf("%s.repo", repo.Alias))
		content := fmt.Sprintf(`[%s]
name=%s
baseurl=%s
enabled=1
gpgcheck=0
`, repo.Alias, repo.Alias, repo.Url)

		if repo.Priority > 0 {
			content += fmt.Sprintf("priority=%d\n", repo.Priority)
		}

		if err := os.WriteFile(repoFile, []byte(content), 0644); err != nil {
			return fmt.Errorf("failed to write repo file: %w", err)
		}
	}

	return nil
}

func (d *DNF) InstallPackages(root string, packages []string, groups []string) error {
	// Create necessary directories
	cacheDir := filepath.Join(root, "var", "cache", "dnf")
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return fmt.Errorf("failed to create cache directory: %w", err)
	}

	// Install packages
	if len(packages) > 0 {
		log.Infof("Installing %d packages...", len(packages))
		args := []string{root, "dnf", "--assumeyes", "--setopt=install_weak_deps=False", "install"}
		args = append(args, packages...)
		cmd := exec.Command("chroot", args...)

		// Create a pipe to capture output
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return fmt.Errorf("failed to create stdout pipe: %w", err)
		}
		stderr, err := cmd.StderrPipe()
		if err != nil {
			return fmt.Errorf("failed to create stderr pipe: %w", err)
		}

		if err := cmd.Start(); err != nil {
			return fmt.Errorf("failed to start dnf install: %w", err)
		}

		// Create a scanner to read output
		scanner := bufio.NewScanner(io.MultiReader(stdout, stderr))
		scanner.Split(bufio.ScanLines)

		// Buffer to store all output for error reporting
		var outputBuffer strings.Builder

		for scanner.Scan() {
			line := scanner.Text()
			// Store all output
			outputBuffer.WriteString(line + "\n")

			// Show progress for package operations
			if strings.Contains(line, "Installing") ||
				strings.Contains(line, "Downloading") ||
				strings.Contains(line, "Verifying") ||
				strings.Contains(line, "Running") {
				log.Info(line)
			}
		}

		if err := cmd.Wait(); err != nil {
			return fmt.Errorf("failed to install packages: %w\nFull output:\n%s", err, outputBuffer.String())
		}
	}

	// Install groups
	if len(groups) > 0 {
		log.Infof("Installing %d groups...", len(groups))
		args := []string{root, "dnf", "--assumeyes", "--setopt=install_weak_deps=False", "group", "install"}
		args = append(args, groups...)
		cmd := exec.Command("chroot", args...)

		// Create a pipe to capture output
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return fmt.Errorf("failed to create stdout pipe: %w", err)
		}
		stderr, err := cmd.StderrPipe()
		if err != nil {
			return fmt.Errorf("failed to create stderr pipe: %w", err)
		}

		if err := cmd.Start(); err != nil {
			return fmt.Errorf("failed to start dnf group install: %w", err)
		}

		// Create a scanner to read output
		scanner := bufio.NewScanner(io.MultiReader(stdout, stderr))
		scanner.Split(bufio.ScanLines)

		// Buffer to store all output for error reporting
		var outputBuffer strings.Builder

		for scanner.Scan() {
			line := scanner.Text()
			// Store all output
			outputBuffer.WriteString(line + "\n")

			// Show progress for group operations
			if strings.Contains(line, "Installing") ||
				strings.Contains(line, "Downloading") ||
				strings.Contains(line, "Verifying") ||
				strings.Contains(line, "Running") {
				log.Info(line)
			}
		}

		if err := cmd.Wait(); err != nil {
			return fmt.Errorf("failed to install groups: %w\nFull output:\n%s", err, outputBuffer.String())
		}
	}

	return nil
}

func (d *DNF) RunCommand(root string, cmd string) error {
	log.Infof("Running command: %s", cmd)

	// Split the command into args
	args := []string{root, "sh", "-c", cmd}
	execCmd := exec.Command("chroot", args...)

	// Create a pipe to capture output
	stdout, err := execCmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdout pipe: %w", err)
	}
	stderr, err := execCmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	if err := execCmd.Start(); err != nil {
		return fmt.Errorf("failed to start command: %w", err)
	}

	// Create a scanner to read output
	scanner := bufio.NewScanner(io.MultiReader(stdout, stderr))
	scanner.Split(bufio.ScanLines)

	// Buffer to store all output for error reporting
	var outputBuffer strings.Builder

	for scanner.Scan() {
		line := scanner.Text()
		// Store all output
		outputBuffer.WriteString(line + "\n")

		// Show progress for command operations
		if strings.Contains(line, "Progress") ||
			strings.Contains(line, "Installing") ||
			strings.Contains(line, "Downloading") ||
			strings.Contains(line, "Running") {
			log.Info(line)
		}
	}

	if err := execCmd.Wait(); err != nil {
		return fmt.Errorf("failed to run command '%s': %w\nFull output:\n%s", cmd, err, outputBuffer.String())
	}
	return nil
}

func (d *DNF) Cleanup(root string) error {
	// Clean DNF cache
	cmd := exec.Command("dnf", "--installroot", root, "clean", "all")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to clean DNF cache: %w\nOutput: %s", err, string(output))
	}

	// Remove unnecessary files
	dirsToClean := []string{
		"var/cache/dnf",
		"var/log",
		"tmp",
	}

	for _, dir := range dirsToClean {
		cmd := exec.Command("rm", "-rf", filepath.Join(root, dir, "*"))
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to clean directory %s: %w", dir, err)
		}
	}

	return nil
}

func (d *DNF) CopyFiles(root string, files []imageconfig.CopyFile) error {
	for _, file := range files {
		// Ensure source file exists
		if _, err := os.Stat(file.Src); err != nil {
			return fmt.Errorf("source file %s does not exist: %w", file.Src, err)
		}

		// Create destination directory if it doesn't exist
		destDir := filepath.Dir(filepath.Join(root, file.Dest))
		if err := os.MkdirAll(destDir, 0755); err != nil {
			return fmt.Errorf("failed to create destination directory %s: %w", destDir, err)
		}

		// Build cp command with options
		args := []string{"-a"} // -a preserves all file attributes
		args = append(args, file.Opts...)
		args = append(args, file.Src, filepath.Join(root, file.Dest))

		cmd := exec.Command("cp", args...)
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to copy file %s to %s: %w\nOutput: %s",
				file.Src, file.Dest, err, string(output))
		}
	}
	return nil
}
