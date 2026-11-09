package main

import (
	_ "embed"
	"encoding/json"
	"strconv"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/updater"
	"github.com/egoist/mygo/plugins/updater/native"
	"github.com/egoist/mygo/ui"
)

//go:embed mygo.json
var releaseConfiguration []byte

//go:embed assets/TideDesk-1024.png
var updateIcon []byte

func releaseVersion() string {
	var config struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(releaseConfiguration, &config); err != nil {
		panic(err)
	}
	return config.Version
}

func configureUpdates() {
	mygo.Use(native.New(updater.Options{
		Interval: 24 * time.Hour,
		Language: "zh-Hans",
		Icon:     updateIcon,
	}))
}

func configureUpdateMenu(app *DesktopApp) {
	viewMenu := []*mygo.MenuItem{
		{Label: "刷新本地数据", Accelerator: "CmdOrCtrl+R", Click: func(_ *mygo.MenuItem, _ *mygo.Window) {
			app.refreshAll()
		}},
		{Label: "显示或隐藏侧栏", Accelerator: "CmdOrCtrl+Alt+S", Click: func(_ *mygo.MenuItem, _ *mygo.Window) {
			app.update(func() { app.collapsed = !app.collapsed })
		}},
		mygo.Separator(),
	}
	for i, title := range workspaceLabels {
		viewMenu = append(viewMenu, &mygo.MenuItem{
			Label: title, Accelerator: "CmdOrCtrl+" + strconv.Itoa(i+1),
			Click: func(_ *mygo.MenuItem, _ *mygo.Window) {
				app.update(func() { app.selectWorkspace(i) })
			},
		})
	}

	mygo.App.SetMenu(mygo.NewMenu([]*mygo.MenuItem{
		{Label: "TideDesk", Submenu: []*mygo.MenuItem{
			{Role: mygo.RoleAbout}, updater.MenuItem(), mygo.Separator(), {Role: mygo.RoleQuit},
		}},
		{Role: mygo.RoleEditMenu}, {Label: "显示", Submenu: viewMenu}, {Role: mygo.RoleWindowMenu},
	}))
	off := updater.OnChange(func() { app.update(func() {}) })
	mygo.App.OnQuit(off)
}

func (a *DesktopApp) renderUpdateSettings(c *ui.Context) {
	sectionCard(c, "TideDesk 软件更新", "v"+releaseVersion(), func() {
		ui.Row(c).Gap(12).Wrap().AlignItems(ui.Center).Children(func() {
			if ui.Button(c, "检查更新").Clicked() {
				updater.CheckForUpdates()
			}
			if mygo.IsDev() {
				ui.Text(c, "源码开发版 · 请使用已安装版本接收更新").FontSize(11).TextColor(colorMuted)
				return
			}
			automatic := updater.AutomaticChecks()
			if ui.Checkbox(c.Key("automatic_updates"), &automatic, "每天自动检查").Changed() {
				updater.SetAutomaticChecks(automatic)
			}
			if version := updater.AvailableVersion(); version != "" {
				ui.Badge(c, "新版本 "+version).Background(colorPos.Alpha(0.16)).TextColor(colorPos)
			} else if checked := updater.LastCheck(); !checked.IsZero() {
				ui.Text(c, "上次检查 "+checked.Local().Format("01-02 15:04")).FontSize(11).TextColor(colorMuted)
			}
		})
		ui.Text(c, "发现新版本后提示；安装并重启后生效，行情与设置保留。").FontSize(11).TextColor(colorMuted)
	})
}
