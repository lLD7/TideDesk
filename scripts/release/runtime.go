package main

import (
	"encoding/csv"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func bundleRuntime(root, target string) error {
	runtime := filepath.Join(root, "build", "python-cache", runtimeName)
	if err := filepath.WalkDir(runtime, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && entry.Name() == "__pycache__" {
			return filepath.SkipDir
		}
		if filepath.Ext(path) == ".pyc" {
			return nil
		}
		rel, err := filepath.Rel(runtime, path)
		if err != nil {
			return err
		}
		dest := filepath.Join(target, rel)
		switch {
		case entry.IsDir():
			return os.MkdirAll(dest, 0755)
		case entry.Type()&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if filepath.IsAbs(link) || !filepath.IsLocal(filepath.Join(filepath.Dir(rel), link)) {
				return fmt.Errorf("runtime link leaves bundle: %s", rel)
			}
			return os.Symlink(link, dest)
		default:
			return copyFile(path, dest)
		}
	}); err != nil {
		return err
	}
	lock, err := os.ReadFile(filepath.Join(root, "requirements-local.lock"))
	if err != nil {
		return err
	}
	source := filepath.Join(root, ".venv", "lib", "python3.12", "site-packages")
	dest := filepath.Join(target, "lib", "python3.12", "site-packages")
	packages := make(map[string]string)
	for _, line := range strings.Split(string(lock), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, version, ok := strings.Cut(line, "==")
		if !ok || name == "" || version == "" {
			return fmt.Errorf("invalid runtime lock entry: %s", line)
		}
		if err := copyDistribution(source, dest, name, version); err != nil {
			return err
		}
		packages[name] = version
	}
	fmt.Printf("Bundled portable Python and %d locked helper dependencies\n", len(packages))
	return writeJSON(filepath.Join(target, "tidedesk-runtime.json"), map[string]any{
		"python_runtime": runtimeName, "packages": packages, "source": "Locked files copied from checkout .venv",
	})
}

func copyDistribution(source, target, name, version string) error {
	dist := strings.ReplaceAll(name, "-", "_") + "-" + version + ".dist-info"
	file, err := os.Open(filepath.Join(source, dist, "RECORD"))
	if err != nil {
		return fmt.Errorf("%s==%s needs to be installed in the Python 3.12 .venv: %w", name, version, err)
	}
	defer file.Close()
	rows, err := csv.NewReader(file).ReadAll()
	if err != nil {
		return fmt.Errorf("read %s RECORD: %w", name, err)
	}
	for _, row := range rows {
		if len(row) != 3 {
			return fmt.Errorf("invalid %s RECORD entry", name)
		}
		rel := filepath.FromSlash(row[0])
		// Wheels can also own entry-point scripts outside site-packages.
		if !filepath.IsLocal(rel) || strings.Contains("/"+row[0]+"/", "/../") ||
			strings.Contains("/"+row[0]+"/", "/__pycache__/") || filepath.Ext(rel) == ".pyc" {
			continue
		}
		if err := copyFile(filepath.Join(source, rel), filepath.Join(target, rel)); err != nil {
			return fmt.Errorf("copy %s==%s: %w", name, version, err)
		}
	}
	return nil
}
