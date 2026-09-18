import json
from pathlib import Path
import unittest

folder = Path(__file__).parent
ns = {"__name__": "desktop_tests"}
exec((folder / "desktop_common.py").read_text(encoding="utf-8-sig"), ns)
Fault = ns["Fault"]
dispatch = ns["dispatch"]


class FakeDesktop:
    def __init__(self):
        self.calls = []
        self.rows = [{"index":0,"title":"中文","enabled":True,"position":[10,20],"size":[100,200]}]

    def permissions(self, request):
        return {"accessibility":True,"screenRecording":False,"pending":request}

    def apps(self):
        return [{"name":"Fixture","pid":42,"bundleId":"fixture","active":True}]

    def resolve(self, app):
        if app != "fixture":
            raise Fault(1,"Unknown app")
        return app

    def tree(self, target):
        return 42, ["node"], self.rows

    def screenshot(self, target):
        raise Fault(3,"Screenshot permission missing")

    def act(self, *args):
        self.calls.append(args)


class DesktopTests(unittest.TestCase):
    def test_scripts_compile(self):
        common = (folder / "desktop_common.py").read_text(encoding="utf-8-sig")
        for name in ("desktop_linux.py", "desktop_macos.py"):
            source = common + "\n" + (folder / name).read_text(encoding="utf-8-sig")
            compile(source, name, "exec")

    def test_state_retains_ax_when_capture_denied(self):
        state = dispatch(FakeDesktop(), "state", {"app":"fixture"})
        self.assertTrue(state["revision"])
        self.assertEqual(state["elements"][0]["title"],"中文")
        self.assertIn("permission", state["screenshot"]["error"])

    def test_stale_before_mutation(self):
        backend = FakeDesktop()
        for method in ("click","type","press","set","select","menu","scroll"):
            with self.assertRaises(Fault) as raised:
                dispatch(backend, method, {"app":"fixture","expectedRevision":"old","index":0})
            self.assertEqual(raised.exception.code,4)
        self.assertEqual(backend.calls,[])

    def test_revision_and_indexes(self):
        backend = FakeDesktop()
        state = dispatch(backend,"ax",{"app":"fixture"})
        p = {"app":"fixture","expectedRevision":state["revision"],"index":0}
        self.assertEqual(dispatch(backend,"click",p),{"ok":True})
        self.assertEqual(len(backend.calls),1)
        for index in (-1,1,True,"0",None):
            with self.assertRaises(Fault):
                dispatch(backend,"click",{**p,"index":index})
        backend.rows[0]["title"]="changed"
        with self.assertRaises(Fault) as raised:
            dispatch(backend,"click",p)
        self.assertEqual(raised.exception.code,4)

    def test_required_index_and_disabled(self):
        backend = FakeDesktop()
        p = {"app":"fixture","expectedRevision":dispatch(backend,"ax",{"app":"fixture"})["revision"]}
        for method in ("set","select","menu","scroll"):
            with self.assertRaises(Fault) as raised:
                dispatch(backend,method,p)
            self.assertEqual(raised.exception.code,-32602)
        backend.rows[0]["enabled"]=False
        p["expectedRevision"]=dispatch(backend,"ax",{"app":"fixture"})["revision"]
        with self.assertRaises(Fault) as raised:
            dispatch(backend,"click",{**p,"index":0})
        self.assertEqual(raised.exception.code,6)

    def test_coordinates(self):
        rows=FakeDesktop().rows
        self.assertEqual(ns["point"]({"x":10,"y":20}, rows),(10,20))
        for x,y in ((9,20),(110,20),(10,220),(True,20),(10,"20")):
            with self.assertRaises(Fault):
                ns["point"]({"x":x,"y":y},rows)

    def test_scroll_validation(self):
        for direction in ("up","down","left","right"):
            self.assertEqual(ns["scroll_args"]({"dir":direction}),(direction,1))
        for p in ({"dir":"other"},{"dir":"up","clicks":0},{"dir":"up","clicks":101},{"dir":"up","clicks":True}):
            with self.assertRaises(Fault):
                ns["scroll_args"](p)

    def test_wayland_never_uses_xdotool(self):
        local = dict(ns)
        exec((folder / "desktop_linux.py").read_text(encoding="utf-8-sig"),local)
        backend = object.__new__(local["LinuxDesktop"])
        backend.wayland=True
        with self.assertRaises(Fault) as raised:
            backend.xdo("key","Return")
        self.assertIn("Wayland",str(raised.exception))

    def test_unknown_method(self):
        with self.assertRaises(Fault) as raised:
            dispatch(FakeDesktop(),"launch",{})
        self.assertEqual(raised.exception.code,-32601)

    def test_jpeg(self):
        with self.assertRaises(Fault):
            ns["jpeg"](b"not a jpeg")
        self.assertEqual(ns["jpeg"](b"\xff\xd8test")["bytes"],6)


if __name__ == "__main__":
    unittest.main()
