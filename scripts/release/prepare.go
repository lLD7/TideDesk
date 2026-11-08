package main

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const runtimeName = "cpython-3.12.15-macos-aarch64-none"

func prepare(root, resources, archive string, cfg config) error {
	if err := os.MkdirAll(filepath.Dir(resources), 0755); err != nil {
		return err
	}
	if _, err := os.Lstat(resources); err == nil && resources != filepath.Join(root, "build", "tidedesk-resources") {
		return fmt.Errorf("resources directory already exists: %s", resources)
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	stage, err := os.MkdirTemp(filepath.Dir(resources), ".tidedesk-resources-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	gex := filepath.Join(stage, "gex")
	if err := copyFile(filepath.Join(root, "app", "yahoo_cli.py"), filepath.Join(gex, "app", "yahoo_cli.py")); err != nil {
		return err
	}
	if err := walkFiles(filepath.Join(root, "app", "sqlite_migrations"), ".sql", func(path, rel string) error {
		return copyFile(path, filepath.Join(gex, "app", "sqlite_migrations", rel))
	}); err != nil {
		return err
	}
	if err := bundleRuntime(root, filepath.Join(gex, "python")); err != nil {
		return err
	}
	if err := copyLicenses(root, filepath.Join(stage, "ThirdPartyLicenses")); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(gex, "BUILD.json"), map[string]any{
		"name": "TideDesk", "version": cfg.Version, "architecture": "arm64", "minimum_macos": "14.0",
		"built_at": time.Now().UTC().Format(time.RFC3339), "notarized": false,
		"update_repository": cfg.Updates.GitHub, "update_signature": "Ed25519",
		"user_data": "~/Library/Application Support/GammaGrid",
	}); err != nil {
		return err
	}
	if err := writeSourceZIP(root, archive); err != nil {
		return err
	}
	if _, err := os.Lstat(resources); err == nil {
		// Keep the previous staging directory until the prepared one is ready.
		backup := stage + "-previous"
		if err := os.Rename(resources, backup); err != nil {
			return err
		}
		if err := os.Rename(stage, resources); err != nil {
			return fmt.Errorf("replace staging resources: %w", errors.Join(err, os.Rename(backup, resources)))
		}
		return os.RemoveAll(backup)
	}
	return os.Rename(stage, resources)
}

func walkFiles(root, extensions string, use func(path, rel string) error) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := entry.Name()
		if entry.IsDir() && (name == "__pycache__" || strings.HasSuffix(name, ".app") || strings.HasPrefix(name, ".")) && path != root {
			return filepath.SkipDir
		}
		if !entry.Type().IsRegular() || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "mygo_internal") {
			return nil
		}
		if !strings.Contains(" "+extensions+" ", " "+filepath.Ext(name)+" ") || filepath.Ext(name) == "" {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		return use(path, rel)
	})
}

func copyFile(source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("not a regular file: %s", source)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	return errors.Join(err, out.Close())
}

func copyLicenses(root, target string) error {
	if err := os.MkdirAll(target, 0755); err != nil {
		return err
	}
	cmd := exec.Command("go", "list", "-m", "-f", "{{.Path}}|{{.Version}}|{{.Dir}}", "all")
	cmd.Dir, cmd.Stderr = filepath.Join(root, "desktop"), os.Stderr
	output, err := cmd.Output()
	if err != nil {
		return err
	}
	var modules strings.Builder
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n")[1:] {
		fields := strings.SplitN(line, "|", 3)
		if len(fields) != 3 {
			return fmt.Errorf("unexpected module listing: %s", line)
		}
		name, version, dir := fields[0], fields[1], fields[2]
		fmt.Fprintf(&modules, "%s %s\n", name, version)
		if dir == "" {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			upper := strings.ToUpper(entry.Name())
			if entry.Type().IsRegular() && (strings.HasPrefix(upper, "LICENSE") || strings.HasPrefix(upper, "COPYING") || strings.HasPrefix(upper, "NOTICE")) {
				file := strings.ReplaceAll(name, "/", "_") + "-" + entry.Name()
				if err := copyFile(filepath.Join(dir, entry.Name()), filepath.Join(target, file)); err != nil {
					return err
				}
			}
		}
	}
	return os.WriteFile(filepath.Join(target, "go-modules.txt"), []byte(modules.String()), 0644)
}

func writeSourceZIP(root, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(target), ".tidedesk-source-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	writer := zip.NewWriter(file)
	add := func(path, rel string) error {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name, header.Method = "TideDesk/"+filepath.ToSlash(rel), zip.Deflate
		out, err := writer.CreateHeader(header)
		if err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		_, err = io.Copy(out, in)
		return err
	}
	for _, tree := range []struct{ name, extensions string }{
		{"app", ".py .sql .sha256"},
		{"desktop", ".go .mod .sum .plist .json .png .icns .md"},
		{"scripts", ".sh .go .mod .sum"},
	} {
		if err := walkFiles(filepath.Join(root, tree.name), tree.extensions, func(path, rel string) error {
			return add(path, filepath.Join(tree.name, rel))
		}); err != nil {
			return err
		}
	}
	for _, name := range []string{
		"LICENSE", "README.md", "requirements.txt", "requirements-local.lock", "pyproject.toml", ".gitignore",
		"docs/macOS安装与打包.md", "docs/README-upstream.md", "UPGRADING.md",
	} {
		if err := add(filepath.Join(root, name), name); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := errors.Join(writer.Close(), file.Close()); err != nil {
		return err
	}
	return os.Rename(file.Name(), target)
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0644)
}
