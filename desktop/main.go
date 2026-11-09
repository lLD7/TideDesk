// TideDesk's native GPU desktop application built with MyGo.
// Go owns collection, storage, scheduling, NYSE calendar, and option analytics natively.
package main

import (
	"errors"
	"flag"
	"log"
	"os"
	"path/filepath"
	"sync"

	"gammagrid.local/desktop/internal/store"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func run() error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	defaultRoot := filepath.Dir(executable)
	bundleRoot := filepath.Join(filepath.Dir(executable), "..", "Resources", "gex")
	if _, err := os.Stat(filepath.Join(bundleRoot, "app", "sqlite_migrations")); err == nil {
		defaultRoot = filepath.Clean(bundleRoot)
	} else {
		for {
			if _, err := os.Stat(filepath.Join(defaultRoot, ".venv", "bin", "python")); err == nil {
				break
			}
			if _, err := os.Stat(filepath.Join(defaultRoot, "app", "sqlite_migrations")); err == nil {
				break
			}
			parent := filepath.Dir(defaultRoot)
			if parent == defaultRoot {
				break
			}
			defaultRoot = parent
		}
	}
	rootFlag := flag.String("root", defaultRoot, "TideDesk checkout root")
	flag.Parse()

	root, err := filepath.Abs(*rootFlag)
	if err != nil {
		return err
	}
	dataDir := filepath.Join(root, "data")
	if root == filepath.Clean(bundleRoot) {
		configDir, err := os.UserConfigDir()
		if err != nil {
			return err
		}
		// Keep the installed GammaGrid database and backups across the rename.
		dataDir = filepath.Join(configDir, "GammaGrid")
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return err
	}

	path := os.Getenv("GEX_DATABASE_PATH")
	if path == "" {
		path = filepath.Join(dataDir, "options.db")
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return err
	}
	database, err := store.Open(root, path)
	if err != nil {
		return err
	}
	service := newDesktopService(database, root)
	service.Start()

	var shutdown sync.Once
	cleanup := func() { shutdown.Do(func() { service.Close(); database.Close() }) }
	defer cleanup()
	// Relaunch fires OnQuit before App.Run returns. Release SQLite ownership
	// before the replacement app is started by the updater.
	mygo.App.OnQuit(cleanup)
	offWake := func() {}
	defer func() { offWake() }()
	configureUpdates()

	mygo.App.WhenReady(func() {
		offWake = observeSystemWake(service.wakeScheduler)
		app := NewDesktopApp(service)
		app.win = mygo.NewWindow(mygo.WindowOptions{
			Title:           "TideDesk",
			Width:           1480,
			Height:          940,
			MinWidth:        1080,
			MinHeight:       680,
			TitleBarStyle:   mygo.TitleBarHiddenInset,
			Transparent:     true,
			Vibrancy:        mygo.VibrancySidebar,
			BackgroundColor: "#080A0E",
			StateKey:        "main",
			Content:         ui.View(app.view),
		})
		configureUpdateMenu(app)
		app.refreshAll()
	})
	return mygo.App.Run()

}

func main() {
	if len(os.Args) > 1 {
		var command func([]string) error
		switch os.Args[1] {
		case "import-sqlite":
			command = store.RunImport
		case "maintenance":
			command = runMaintenance
		}
		if command != nil {
			if err := command(os.Args[2:]); err != nil && !errors.Is(err, flag.ErrHelp) {
				log.Fatal(err)
			}
			return
		}
	}
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
