import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time

folder = Path(__file__).parent
namespace = {"__name__": "desktop_test"}
exec((folder / "desktop_common.py").read_text(encoding="utf-8-sig"), namespace)
exec((folder / "desktop_linux.py").read_text(encoding="utf-8-sig"), namespace)
backend = namespace["LinuxDesktop"]()
dispatch = namespace["dispatch"]
fixture = '''import gi
gi.require_version("Gtk", "3.0")
from gi.repository import Gtk, GLib
window = Gtk.Window(title="K-brain desktop fixture")
window.set_default_size(460, 280)
box = Gtk.Box(orientation=Gtk.Orientation.VERTICAL)
entry = Gtk.Entry()
entry.set_text("initial text")
entry.get_accessible().set_name("Test entry")
button = Gtk.Button(label="Apply")
button.connect("clicked", lambda _: entry.set_text("clicked"))
box.pack_start(entry, False, False, 0)
box.pack_start(button, False, False, 0)
window.add(box)
window.connect("destroy", Gtk.main_quit)
window.show_all()
window.present()
Gtk.main()
'''
proc = subprocess.Popen([sys.executable, "-c", fixture], env={**os.environ, "GTK_MODULES": "gail:atk-bridge", "NO_AT_BRIDGE":"0"})
try:
    app = str(proc.pid)
    for _ in range(60):
        time.sleep(.2)
        try:
            state = dispatch(backend, "state", {"app": app})
            break
        except Exception:
            pass
    else:
        raise RuntimeError("GTK fixture did not appear on AT-SPI bus")
    assert state["elements"], state
    entry = next(e for e in state["elements"] if e.get("title") == "Test entry")
    button = next(e for e in state["elements"] if e["title"] == "Apply")
    assert entry["value"] == "initial text", entry
    if state.get("screenshot", {}).get("error"):
        subprocess.run(["xdotool", "search", "--pid", app, "windowactivate", "--sync"], check=True)
        time.sleep(.3)
        state = dispatch(backend, "state", {"app":app})
    assert state["screenshot"].get("bytes", 0) > 100, state["screenshot"]
    def act(method, **params):
        snapshot = dispatch(backend, "ax", {"app":app})
        return dispatch(backend, method, {"app":app,"expectedRevision":snapshot["revision"], **params})
    act("set", index=entry["index"], value="中文 hello world")
    act("select", index=entry["index"], target="hello")
    act("press", key="End")
    act("type", text=" typed")
    act("click", index=button["index"])
    time.sleep(.2)
    after = dispatch(backend, "ax", {"app":app})
    assert next(e for e in after["elements"] if e.get("title") == "Test entry")["value"] == "clicked", after
    try:
        dispatch(backend, "click", {"app":app,"expectedRevision":state["revision"],"index":button["index"]})
    except namespace["Fault"] as e:
        assert e.code == 4, e
    else:
        raise AssertionError("stale revision accepted")
    helper_path = os.environ.get("K_BRAIN_TEST_COMPUTER_BIN")
    if helper_path:
        helper = subprocess.Popen([helper_path], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                  text=True, env={**os.environ,"K_BRAIN_COMPUTER_TOKEN":"integration-token"})
        try:
            assert helper.stdout.readline().strip() == "k-brain-computer/1"
            sequence = 0
            def rpc(method, **params):
                global sequence
                sequence += 1
                helper.stdin.write(json.dumps({"jsonrpc":"2.0","id":sequence,"method":method,
                    "params":{"token":"integration-token",**params}}) + "\n")
                helper.stdin.flush()
                response = json.loads(helper.stdout.readline())
                assert "error" not in response, response
                return response["result"]
            assert any(a["pid"] == proc.pid for a in rpc("apps"))
            snap = rpc("state",app=app)
            assert snap["screenshot"]["bytes"] > 100
            idx = next(e["index"] for e in snap["elements"] if e.get("title") == "Test entry")
            changed = rpc("set",app=app,gen=snap["generation"],index=idx,value="Go RPC verified")
            assert next(e["value"] for e in changed["elements"] if e.get("title") == "Test entry") == "Go RPC verified"
            assert changed["generation"] > snap["generation"]
            rpc("shutdown")
            helper.wait(timeout=5)
        finally:
            if helper.poll() is None:
                helper.kill()
                helper.wait(timeout=5)
        print("Go helper RPC integration passed: handshake, apps, state, screenshot, set, refreshed generation, shutdown")
    print("Linux desktop integration passed: apps, AX tree, screenshot, set, select, key, type, click, stale revision")
finally:
    proc.terminate()
    proc.wait(timeout=5)
