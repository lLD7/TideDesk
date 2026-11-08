# TideDesk 升级与数据

已安装应用的数据位于 `~/Library/Application Support/GammaGrid/`。源码版默认使用 `data/options.db`。替换应用时，数据库和备份使用各自原有目录。

先退出应用，再按需创建一致性备份：

```sh
./scripts/maintenance.sh --database "$HOME/Library/Application Support/GammaGrid/options.db" backup data/backups/before-upgrade.db
```

Go Store 负责 SQLite 迁移。已有迁移文件保持原样，新增变更使用新编号。旧 SQLite 快照可通过 `./scripts/import-sqlite.sh --target data/imported.db path/to/old.db` 导入新文件。

第 2 版采用 MyGo 原生入口。旧 Streamlit、Python HTTP 和 PostgreSQL 相关源码位于 `legacy/python/`。自动更新的安装及发布流程见 [macOS 安装与打包](docs/macOS安装与打包.md)。
