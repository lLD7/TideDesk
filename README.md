# TideDesk

macOS 原生期权敞口与压力分布终端，使用 Go / MyGo、Cboe 延迟行情和本地 SQLite。基于 [GammaGrid](https://github.com/gammagrid/gammagrid) 改造，遵循原项目 AGPL-3.0 许可。

[下载最新版](https://github.com/lLD7/TideDesk/releases/latest) · [安装与发布说明](docs/macOS安装与打包.md) · [更新记录](desktop/CHANGELOG.md)

支持 Apple Silicon（M 系列芯片）和 macOS 14+。安装包包含所需环境，无需单独安装 Go、Python、Docker 或 PostgreSQL。当前为本地签名版本，尚未 Apple Developer ID 签名或公证。

## 功能

- Cboe 一次请求采集完整期权链，保留原生 Gamma、Delta、IV、OI；Yahoo 备用源按需启动 Python。
- GEX / Vanna / Charm 热力图、按行权价汇总的压力分布、期权链、资金流与本地采集日志。
- 压力范围以快照现价为基准，默认 ±10%，可调 ±1%～±50%；区间内全部可用行权价纳入图表、明细和合力汇总。
- 区分 OI Wall 与 GEX Wall，支持历史快照回放。
- 默认美东交易日 10:00、12:00、14:00、16:15 低频采集，可按标的配置频率；查询手动刷新。
- SQLite/WAL、本地缓存、单写入口、归档与备份。应用退出后停止采集。
- 原生中文自动更新窗口：启动后检查、运行期间每日检查，可手动检查、稍后提醒或跳过版本；更新包校验 Ed25519 签名。

## 安装与更新

1. 从 Releases 下载 `TideDesk-<版本>-macOS-AppleSilicon.dmg`。
2. 将 TideDesk.app 拖入 Applications，弹出磁盘映像后启动。
3. 后续从 **TideDesk 菜单 → 检查更新**，或 **日志 → TideDesk 软件更新** 更新。

旧版 GammaGrid 0.8.x 需要手动安装一次 TideDesk 0.9.0。改名后继续使用 `~/Library/Application Support/GammaGrid/` 中的行情、设置和备份；无需迁移或复制旧数据库。不要同时运行两个应用。

## 本地开发

准备 Go（版本见 desktop/go.mod）、uv，以及 Python 3.12 环境：

```sh
uv venv --python 3.12 .venv
uv pip install --python .venv/bin/python -r requirements-local.lock
./scripts/start-desktop.sh
```

开发版使用源码目录的 `data/options.db`，不接收在线更新。原 Python / Streamlit 入口保留于 `./scripts/start-local.sh`。

`GEX_DATABASE_PATH` 可指定数据库；`GEX_FETCH_RV=1` 可启用额外的 Yahoo 日线历史波动率数据。

## 构建与发布

```sh
./scripts/package-macos.sh
./scripts/publish-release.sh
```

构建配置与版本号见 `desktop/mygo.json`，更新说明见 `desktop/CHANGELOG.md`。发布脚本仅导出明确列出的源文件，先上传草稿 Release 的所有文件，再公开版本。数据库、开发环境、私钥和本机工作记录均不进入发布仓库。

构建更新包需要持有该项目的更新签名私钥；它不在仓库里。详细要求见 [安装与发布说明](docs/macOS安装与打包.md)。构建脚本不运行应用、回归测试或实盘采集。

## 数据与指标边界

行情为免费延迟数据，可能限流或缺失。历史从本机开始采集时积累。标准 GEX 显示尺度为美元 / 标的变动 1%；Vanna 为美元 / IV 增加 1 个百分点；Charm 为美元 / 经过 1 个日历日。

Call+/Put−、100 合约乘数、固定利率和股息参数是模型假设，不代表实际做市商仓位。模型未完整区分所有指数 AM/PM 结算系列，调整合约和临近到期敞口需要结合这些边界理解。

## 来源与许可

源自 `gammagrid/gammagrid` v0.8.0（`2d4e6f6`），保留其版权与许可声明。该分支增加原生 macOS 界面、Go 数据服务、SQLite、Cboe 主源、压力分布和应用内更新。[LICENSE](LICENSE) · [上游 README](docs/README-upstream.md)
