"""Start Streamlit in this Python process; the optional desktop shell owns it."""
import os
import signal
import sys
import threading
from pathlib import Path

from streamlit.web import cli

from app.scheduler import get_runtime


def _watch_parent(parent):
    while threading.main_thread().is_alive():
        if os.getppid() != parent:
            os.kill(os.getpid(), signal.SIGTERM)
            return
        threading.Event().wait(2)


def main():
    runtime = get_runtime()  # Claim the database before exposing the server.
    parent = os.environ.get("GEX_DESKTOP_PARENT")
    if parent:
        threading.Thread(target=_watch_parent, args=(int(parent),), daemon=True, name="desktop-parent").start()
    dashboard = Path(__file__).with_name("dashboard.py")
    args = sys.argv[1:]
    sys.argv = ["streamlit", "run", str(dashboard), "--server.address=127.0.0.1",
                "--server.headless=true", "--browser.gatherUsageStats=false", *args]
    try:
        cli.main()
    finally:
        runtime.stop()


if __name__ == "__main__":
    main()
