"""One-shot local maintenance, only while the desktop app is closed."""
import argparse
import json

from app import collector, db, providers
from app.scheduler import DesktopRuntime


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    collect = commands.add_parser("collect")
    collect.add_argument("symbols", nargs="+")
    collect.add_argument("--provider", choices=providers.known_providers(), default="auto")
    backup = commands.add_parser("backup")
    backup.add_argument("destination")
    args = parser.parse_args()
    runtime = DesktopRuntime(start_scheduler=False)
    conn = None
    try:
        conn = db.get_connection()
        if args.command == "collect":
            symbols = [symbol.upper() for symbol in args.symbols]
            for symbol in symbols:
                db.add_ticker(conn, symbol)
            result = collector.collect_watchlist(conn, symbols, providers.get_provider(args.provider))
            print(json.dumps(result, indent=2))
            for symbol, status in result.items():
                if status == "success":
                    rows = db.get_latest_snapshot(conn, symbol)
                    print(f"{symbol}: {len(rows):,} contracts; source={db.active_source(conn, symbol)}")
            return 0 if all(status == "success" for status in result.values()) else 1
        db.backup_database(conn, args.destination)
        print(f"Backup saved: {args.destination}")
        return 0
    finally:
        if conn is not None:
            conn.close()
        runtime.stop()


if __name__ == "__main__":
    raise SystemExit(main())
