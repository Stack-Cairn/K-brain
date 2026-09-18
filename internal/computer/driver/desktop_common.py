import base64
import hashlib
import json
import os
import shutil
import subprocess
import sys
import tempfile
import time


class Fault(Exception):
    def __init__(self, code, message):
        super().__init__(message)
        self.code = code


def required(p, name, kind=str):
    value = p.get(name)
    if type(value) is not kind:
        raise Fault(-32602, name + " must be " + kind.__name__)
    return value


def run(args, data=None):
    try:
        result = subprocess.run(args, input=data, stdout=subprocess.PIPE,
                                stderr=subprocess.PIPE, timeout=8, check=False)
    except (OSError, subprocess.TimeoutExpired) as e:
        raise Fault(6, str(e))
    if result.returncode:
        raise Fault(6, result.stderr.decode("utf-8", "replace").strip() or "Desktop command failed")
    return result.stdout


def revision(identity, rows):
    raw = json.dumps([identity, rows], sort_keys=True, ensure_ascii=True).encode()
    return hashlib.sha256(raw).hexdigest()


def jpeg(data):
    if not data.startswith(b"\xff\xd8"):
        raise Fault(3, "Screenshot did not return JPEG data")
    if len(data) > 24 * 1024 * 1024:
        raise Fault(3, "Screenshot exceeds 24 MiB")
    return {"jpegBase64": base64.b64encode(data).decode("ascii"), "bytes": len(data)}


def element_at(p, nodes, rows, mandatory=False):
    if "index" not in p:
        if mandatory:
            raise Fault(-32602, "index is required")
        return None
    index = required(p, "index", int)
    if index < 0 or index >= len(nodes):
        raise Fault(5, "Element index out of range")
    if not rows[index]["enabled"]:
        raise Fault(6, "Element is disabled")
    return nodes[index]


def scroll_args(p):
    direction = required(p, "dir")
    clicks = p.get("clicks", 1)
    if direction not in ("up", "down", "left", "right"):
        raise Fault(-32602, "Unknown scroll direction")
    if type(clicks) is not int or not 1 <= clicks <= 100:
        raise Fault(-32602, "clicks must be 1..100")
    return direction, clicks


def point(p, rows):
    x, y = required(p, "x", int), required(p, "y", int)
    bounds = rows[0]
    pos, size = bounds.get("position", []), bounds.get("size", [])
    if len(pos) != 2 or len(size) != 2 or not (pos[0] <= x < pos[0] + size[0] and pos[1] <= y < pos[1] + size[1]):
        raise Fault(-32602, "Coordinates are outside the requested window (absolute desktop coordinates required)")
    return x, y


def dispatch(backend, method, p):
    if method in ("permissions.status", "permissions.request"):
        return backend.permissions(method.endswith("request"))
    if method == "apps":
        return backend.apps()
    if method not in ("state", "ax", "screenshot", "click", "type", "press", "scroll", "set", "select", "menu"):
        raise Fault(-32601, "Unknown method " + method)
    app = required(p, "app")
    if not app.strip():
        raise Fault(-32602, "app is required")
    target = backend.resolve(app)
    if method == "screenshot":
        return backend.screenshot(target)
    identity, nodes, rows = backend.tree(target)
    rev = revision(identity, rows)
    if method in ("state", "ax"):
        result = {"app": app, "elements": rows, "revision": rev}
        if method == "state":
            try:
                result["screenshot"] = backend.screenshot(target)
            except Exception as e:
                result["screenshot"] = {"error": str(e)}
        return result
    if required(p, "expectedRevision") != rev:
        raise Fault(4, "state changed — re-read state(app)")
    element = element_at(p, nodes, rows, method in ("set", "select", "menu", "scroll"))
    backend.act(target, method, p, element, rows)
    return {"ok": True}


def serve(factory):
    try:
        request = json.load(sys.stdin)
        method = request["method"]
        try:
            backend = factory()
        except Exception as e:
            if method in ("permissions.status", "permissions.request"):
                print(json.dumps({"result": {"accessibility": False, "screenRecording": False, "hint": str(e)}}))
                return
            raise
        result = dispatch(backend, method, request.get("params", {}))
        print(json.dumps({"result": result}, ensure_ascii=True))
    except Exception as e:
        print(json.dumps({"error": {"code": getattr(e, "code", 6), "message": str(e)}}, ensure_ascii=True))
