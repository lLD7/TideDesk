package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type config struct {
	Version string `json:"version"`
	Updates struct {
		GitHub    string `json:"github"`
		Changelog string `json:"changelog"`
	} `json:"updates"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	rootFlag := flag.String("root", "..", "TideDesk source directory")
	resourcesFlag := flag.String("resources", "", "override staging resources directory")
	archiveFlag := flag.String("archive", "", "override source ZIP output")
	flag.Parse()
	root, err := filepath.Abs(*rootFlag)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(root, "desktop", "mygo.json"))
	if err != nil {
		return err
	}
	var cfg config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return err
	}
	if cfg.Version == "" || !filepath.IsLocal(cfg.Version) || strings.Contains(cfg.Version, "/") {
		return fmt.Errorf("invalid version in desktop/mygo.json")
	}
	switch flag.Arg(0) {
	case "version":
		fmt.Println(cfg.Version)
	case "notes":
		notes, err := releaseNotes(root, cfg)
		if err != nil {
			return err
		}
		fmt.Println(notes)
	case "prepare", "build":
		resources := *resourcesFlag
		if resources == "" {
			resources = filepath.Join(root, "build", "tidedesk-resources")
		}
		archive := *archiveFlag
		if archive == "" {
			archive = filepath.Join(root, "dist", "TideDesk-"+cfg.Version+"-Source.zip")
		}
		if flag.Arg(0) == "build" && (*resourcesFlag != "" || *archiveFlag != "") {
			return fmt.Errorf("resource and archive overrides are for prepare only")
		}
		if err := prepare(root, resources, archive, cfg); err != nil {
			return err
		}
		if flag.Arg(0) == "build" {
			return buildApp(root)
		}
	default:
		return fmt.Errorf("usage: release [flags] version|notes|prepare|build")
	}
	return nil
}

func releaseNotes(root string, cfg config) (string, error) {
	name := cfg.Updates.Changelog
	if name == "" {
		name = "CHANGELOG.md"
	}
	raw, err := os.ReadFile(filepath.Join(root, "desktop", name))
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) != "## "+cfg.Version {
			continue
		}
		end := i + 1
		for end < len(lines) && !strings.HasPrefix(lines[end], "## ") {
			end++
		}
		return strings.TrimSpace(strings.Join(lines[i+1:end], "\n")), nil
	}
	return "", fmt.Errorf("version %s missing from %s", cfg.Version, name)
}

func buildApp(root string) error {
	args := []string{"run", "github.com/egoist/mygo/cmd/mygo", "build", "-platform", "darwin/arm64", "-skip-dmg"}
	if identity := os.Getenv("TIDEDESK_SIGN_IDENTITY"); identity != "" {
		args = append(args, "-sign", identity)
	}
	cmd := exec.Command("go", args...)
	cmd.Dir = filepath.Join(root, "desktop")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), "CGO_ENABLED=1", "MACOSX_DEPLOYMENT_TARGET=14.0")
	if os.Getenv("MYGO_UPDATER_PRIVATE_KEY") == "" {
		path := os.Getenv("TIDEDESK_UPDATE_KEY")
		if path == "" {
			dir, err := os.UserConfigDir()
			if err != nil {
				return err
			}
			path = filepath.Join(dir, "TideDesk", "update-keys", "mygo-update.key")
		}
		key, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read update signing key: %w", err)
		}
		cmd.Env = append(cmd.Env, "MYGO_UPDATER_PRIVATE_KEY="+strings.TrimSpace(string(key)))
	}
	return cmd.Run()
}
