# TideDesk 安装、更新与发布

当前版本：0.9.0 · 2026-11-08。

## 安装

面向 Apple Silicon、macOS 14+。从 [GitHub Releases](https://github.com/lLD7/TideDesk/releases/latest) 下载 DMG，将 TideDesk.app 拖入 Applications，弹出映像后打开。

当前仅本地 ad-hoc 签名，尚未 Apple 公证。首次打开被拦截时，在确认来源后按 [Apple 官方说明](https://support.apple.com/en-us/102445)前往“系统设置 → 隐私与安全”选择“仍要打开”。无需关闭系统安全检查。

旧版 GammaGrid 0.8.x 没有更新器，需要最后手动安装一次 TideDesk。此后使用应用内更新，无需反复下载和拖动 DMG。

## 自动更新

- 菜单 **TideDesk → 检查更新** 和日志页 **TideDesk 软件更新** 均可手动检查。
- 默认启动约 10 秒后检查；应用运行时每 24 小时检查一次，可在日志页关闭。
- 中文窗口显示版本说明、下载进度，支持稍后提醒、跳过版本和安装后重启。
- 默认提醒后由用户决定安装；更新窗口另有自动下载选项，启用后仍在下次启动使用新版。
- 更新包来自 `lLD7/TideDesk`，由 MyGo 对下载内容校验 Ed25519 签名。签名无效的文件不安装。
- macOS 安装到应用目录后才可更新，需要对应用所在目录有写权限；从只读 DMG 直接运行不能原地更新。
- 完整更新包始终提供；构建时如有同一密钥签名的历史版本，可生成最近三个版本的增量包，增量失败会退回完整下载。

更新只替换应用包。数据库、设置和备份仍在：

```text
~/Library/Application Support/GammaGrid/
```

名称保留是为了兼容旧版数据；bundle identifier 也保留为 `local.gammagrid.desktop`。不要同时运行 GammaGrid 和 TideDesk。更新重启时先停止后台任务并释放 SQLite 锁，再启动新版本。

## 构建

依赖 macOS Apple Silicon、Go、uv，以及现有 Python 3.12 `.venv`：

```sh
./scripts/package-macos.sh
```

该脚本准备迁移文件、应用资源、独立 CPython 3.12.15 和 Yahoo/RV 依赖；使用固定版本的 MyGo 官方 builder 编译、签名并产生更新清单，之后生成供首次安装的 DMG。数值算法与既有界面不在发布脚本中修改。

版本和更新源只有一个构建配置：`desktop/mygo.json`。修改版本后，在 `desktop/CHANGELOG.md` 增加同名版本段落。`desktop/Info.plist` 是开发启动入口的元数据，也应保持版本一致。

首次密钥已生成并保存在本机：

```text
~/Library/Application Support/TideDesk/update-keys/mygo-update.key
~/Library/Application Support/TideDesk/update-keys/mygo-update.pub
```

私钥不放进 Git、安装包或源码 ZIP。请单独安全备份，后续版本必须使用同一私钥。构建脚本默认从以上路径读取；也支持 `TIDEDESK_UPDATE_KEY` 指定密钥文件，或 `MYGO_UPDATER_PRIVATE_KEY` 提供 CI secret。不要把私钥写入 `mygo.json`。

输出位于 `dist/`：

- `TideDesk-0.9.0-macOS-AppleSilicon.dmg`：首次安装。
- `tidedesk-0.9.0-darwin-arm64.tar.gz`：已签名的应用内更新归档。
- `update-darwin-arm64.json`：平台版本清单及签名。
- `TideDesk-0.9.0-Source.zip`：对应应用源码、构建脚本和许可。
- `TideDesk-0.9.0-SHA256SUMS.txt`：分发文件指纹。

## 发布

使用 lLD7 的 GitHub 官方 CLI 登录后执行：

```sh
./scripts/publish-release.sh
```

发布脚本创建临时源码导出 checkout，仅提交源代码允许列表，保留原工作区的未提交修改和 Git 历史。默认仓库为 `lLD7/TideDesk`；先推送源码，再创建草稿版本、上传所有附件，最后公开为最新版本。已存在的版本不会覆盖，应提升版本号后再发布。

源代码允许列表不包含行情数据库、自选列表、环境变量文件、私钥、性能测量输出和个人工作记录。项目保留上游 GammaGrid AGPL 许可与来源说明。

## 本轮验证范围

按用户要求未重新执行应用功能、数值回归、性能或实盘采集测试。构建与发布状态另行记录；版本清单可访问不代表已经实际执行跨版本更新。首次 0.9.0 为后续升级建立入口，没有更旧的 TideDesk 自动更新版本。
