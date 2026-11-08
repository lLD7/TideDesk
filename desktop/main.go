// TideDesk's native GPU desktop application built with MyGo.
// Go owns collection, storage, scheduling, NYSE calendar, and option analytics natively.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

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
			if _, err := os.Stat(filepath.Join(defaultRoot, "app", "migrations")); err == nil {
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
	webFlag := flag.Bool("web", false, "Use embedded Streamlit WebView instead of native MyGo GPU UI")
	flag.Parse()

	root, err := filepath.Abs(*rootFlag)
	if err != nil {
		return err
	}
	dataDir := filepath.Join(root, "data")
	if root == filepath.Clean(bundleRoot) {
		if *webFlag {
			return errors.New("此安装包使用原生界面；Streamlit 模式请从源码目录启动")
		}
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

	if !*webFlag {
		path := os.Getenv("GEX_DATABASE_PATH")
		if path == "" {
			path = filepath.Join(dataDir, "options.db")
		}
		path, err = filepath.Abs(path)
		if err != nil {
			return err
		}
		store, err := openStore(root, path)
		if err != nil {
			return err
		}
		service := newDesktopService(store, root, "")
		service.Start()
		var shutdown sync.Once
		cleanup := func() { shutdown.Do(func() { service.Close(); store.Close() }) }
		defer cleanup()
		// Relaunch fires OnQuit before App.Run returns. Release SQLite ownership
		// before the replacement app is started by the updater.
		mygo.App.OnQuit(cleanup)
		configureUpdates()

		mygo.App.WhenReady(func() {
			app := NewDesktopApp(newLocalAPIClient(service))
			app.win = mygo.NewWindow(mygo.WindowOptions{
				Title:                "TideDesk",
				Width:                1480,
				Height:               940,
				MinWidth:             1080,
				MinHeight:            680,
				TitleBarStyle:        mygo.TitleBarHidden,
				TrafficLightPosition: &mygo.Point{X: 15, Y: 15},
				Transparent:          true,
				Vibrancy:             mygo.VibrancySidebar,
				BackgroundColor:      "#090B0F",
				StateKey:             "main",
				Content:              ui.View(app.view),
			})
			configureUpdateMenu(app)
			app.refreshAll()
		})
		return mygo.App.Run()
	}

	// Legacy -web mode (embedded Streamlit WebView).
	python := filepath.Join(root, ".venv", "bin", "python")
	if _, err := os.Stat(python); err != nil {
		return fmt.Errorf("local Python environment missing: %w", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()

	output, err := os.OpenFile(filepath.Join(root, "data", "desktop.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer output.Close()

	cmd := exec.Command(python, "-m", "app.launcher", "--server.port="+strconv.Itoa(port))
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "PYTHONPATH="+root, "GEX_DESKTOP_PARENT="+strconv.Itoa(os.Getpid()))
	cmd.Stdout, cmd.Stderr = output, output
	if err := cmd.Start(); err != nil {
		return err
	}

	done := make(chan struct{})
	var childErr error
	go func() { childErr = cmd.Wait(); close(done) }()

	var once sync.Once
	stop := func() {
		once.Do(func() {
			select {
			case <-done:
				return
			default:
			}
			_ = cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				_ = cmd.Process.Kill()
				<-done
			}
		})
	}
	defer stop()

	baseURL := "http://127.0.0.1:" + strconv.Itoa(port)
	client := &http.Client{Timeout: time.Second}
	ready := false
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
		select {
		case <-done:
			return fmt.Errorf("Python service stopped (%v); see data/desktop.log", childErr)
		default:
		}
		response, err := client.Get(baseURL + "/_stcore/health")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				ready = true
				break
			}
		}
		time.Sleep(120 * time.Millisecond)
	}
	if !ready {
		return errors.New("Python startup timed out; see data/desktop.log")
	}

	mygo.App.WhenReady(func() {
		mygo.NewWindow(mygo.WindowOptions{
			Title:           "TideDesk Terminal",
			URL:             baseURL,
			Width:           1480,
			Height:          940,
			MinWidth:        1080,
			MinHeight:       680,
			BackgroundColor: "#090B0F",
		})
		go func() { <-done; mygo.App.Quit() }()
	})
	mygo.App.OnQuit(stop)
	return mygo.App.Run()
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
