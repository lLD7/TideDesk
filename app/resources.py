"""Process resources shared by Streamlit sessions and background collection."""
import os
import threading

# Preserve the same lock even when Streamlit's resource cache is cleared.
_WRITE_LOCK = threading.RLock()
_COLLECTION_LOCK = threading.Lock()


def get_write_lock():
    return _WRITE_LOCK


# The native calculation adapter has no Streamlit sessions. Importing Streamlit
# there needlessly loads the entire web runtime.
if os.environ.get("GEX_NATIVE_BACKEND") != "1":
    import streamlit as st
    get_write_lock = st.cache_resource(show_spinner=False)(get_write_lock)


def get_collection_lock():
    return _COLLECTION_LOCK


def shared_write_lock():
    """Use the initialized process lock without invoking Streamlit on a worker thread."""
    return _WRITE_LOCK
