class MacDesktop:
    def __init__(self):
        try:
            import AppKit
            import Quartz
            import ApplicationServices
            import CoreFoundation
            self.appkit, self.q, self.ax, self.cf = AppKit, Quartz, ApplicationServices, CoreFoundation
        except ImportError:
            raise Fault(2, "Install pyobjc-framework-Cocoa, pyobjc-framework-Quartz and pyobjc-framework-ApplicationServices into K_BRAIN_COMPUTER_PYTHON")

    def permissions(self, request):
        if request:
            accessibility = bool(self.ax.AXIsProcessTrustedWithOptions({self.ax.kAXTrustedCheckOptionPrompt: True}))
            screen = bool(self.q.CGRequestScreenCaptureAccess())
        else:
            accessibility = bool(self.ax.AXIsProcessTrusted())
            screen = bool(self.q.CGPreflightScreenCaptureAccess())
        return {"accessibility": accessibility, "screenRecording": screen,
                "pending": request and not (accessibility and screen),
                "hint": "Grant Accessibility and Screen Recording to the interpreter/terminal in System Settings > Privacy & Security, then restart kn. Coordinates use desktop points."}

    def attr(self, node, name, default=None):
        code, value = self.ax.AXUIElementCopyAttributeValue(node, name, None)
        if code == 0:
            return value
        if code in (-25205, -25212):
            return default
        raise Fault(2, "Accessibility read failed (%s); grant permission or re-read state" % code)

    def apps(self):
        return [{"name": a.localizedName() or str(a.processIdentifier()), "bundleId": a.bundleIdentifier() or "",
                 "pid": a.processIdentifier(), "active": bool(a.isActive())}
                for a in self.appkit.NSWorkspace.sharedWorkspace().runningApplications()
                if a.activationPolicy() == self.appkit.NSApplicationActivationPolicyRegular]

    def resolve(self, name):
        if not self.ax.AXIsProcessTrusted():
            raise Fault(2, "Accessibility permission is required; use permissions.request")
        matches = [a for a in self.appkit.NSWorkspace.sharedWorkspace().runningApplications()
                   if name.casefold() in ((a.localizedName() or "").casefold(), (a.bundleIdentifier() or "").casefold(), str(a.processIdentifier()))]
        if len(matches) != 1:
            raise Fault(1, "App not found or ambiguous; select its bundleId or PID from apps()")
        app = matches[0]
        root = self.ax.AXUIElementCreateApplication(app.processIdentifier())
        window = self.attr(root, "AXFocusedWindow") or self.attr(root, "AXMainWindow")
        if window is None:
            windows = self.attr(root, "AXWindows", [])
            if len(windows) != 1:
                raise Fault(1, "No unique window; activate the requested window and re-read state")
            window = windows[0]
        return app, root, window

    def geometry(self, node):
        pos, size = self.attr(node, "AXPosition"), self.attr(node, "AXSize")
        if pos is None or size is None:
            return [], []
        ok1, point_value = self.ax.AXValueGetValue(pos, self.ax.kAXValueCGPointType, None)
        ok2, size_value = self.ax.AXValueGetValue(size, self.ax.kAXValueCGSizeType, None)
        if not ok1 or not ok2:
            return [], []
        return [point_value.x, point_value.y], [size_value.width, size_value.height]

    def tree(self, target):
        app, root, window = target
        nodes, rows, stack = [], [], [window]
        while stack and len(nodes) < 1500:
            node = stack.pop()
            position, size = self.geometry(node)
            code, actions = self.ax.AXUIElementCopyActionNames(node, None)
            subrole = str(self.attr(node, "AXSubrole", ""))
            value = "" if subrole == "AXSecureTextField" else self.attr(node, "AXValue", "")
            if not isinstance(value, (str, int, float, bool)):
                value = ""
            row = {"index": len(nodes), "role": str(self.attr(node, "AXRole", "")), "subrole": subrole,
                   "title": str(self.attr(node, "AXTitle", "")), "desc": str(self.attr(node, "AXDescription", "")),
                   "value": str(value)[:16000], "position": position, "size": size,
                   "enabled": bool(self.attr(node, "AXEnabled", True)), "focused": bool(self.attr(node, "AXFocused", False)),
                   "actions": list(actions or []) if code == 0 else []}
            nodes.append(node)
            rows.append(row)
            stack.extend(reversed(list(self.attr(node, "AXChildren", []))[:1500]))
        return [app.processIdentifier(), str(self.attr(window, "AXIdentifier", ""))], nodes, rows

    def foreground(self, target):
        if not target[0].isActive():
            raise Fault(6, "Requested app is not foreground; activate it and re-read state before input")
        locked = self.q.CGSessionCopyCurrentDictionary() or {}
        if locked.get("CGSSessionScreenIsLocked", False):
            raise Fault(7, "Screen is locked")

    def screenshot(self, target):
        self.foreground(target)
        if not self.q.CGPreflightScreenCaptureAccess():
            raise Fault(3, "Screen Recording permission is required")
        position, size = self.geometry(target[2])
        if not position or min(size) <= 0 or size[0]*size[1] > 40000000:
            raise Fault(3, "Window is minimized or has invalid bounds")
        geometry = ",".join(str(int(v)) for v in position + size)
        with tempfile.TemporaryDirectory(prefix="k-brain-capture-") as folder:
            path = os.path.join(folder, "capture.jpg")
            run(["/usr/sbin/screencapture", "-x", "-t", "jpg", "-R" + geometry, path])
            with open(path, "rb") as f:
                return jpeg(f.read(24 * 1024 * 1024 + 1))

    def check(self, code):
        if code != 0:
            raise Fault(6, "Accessibility action rejected (%s)" % code)

    def set_attr(self, node, name, value):
        code, writable = self.ax.AXUIElementIsAttributeSettable(node, name, None)
        if code != 0 or not writable:
            raise Fault(6, name + " is read-only or unsupported")
        self.check(self.ax.AXUIElementSetAttributeValue(node, name, value))

    def post(self, event):
        if event is None:
            raise Fault(6, "Cannot create input event")
        self.q.CGEventPost(self.q.kCGHIDEventTap, event)

    def key(self, key):
        parts = key.casefold().split("+")
        modifiers = {"cmd":self.q.kCGEventFlagMaskCommand,"command":self.q.kCGEventFlagMaskCommand,
                     "meta":self.q.kCGEventFlagMaskCommand,"ctrl":self.q.kCGEventFlagMaskControl,
                     "control":self.q.kCGEventFlagMaskControl,"alt":self.q.kCGEventFlagMaskAlternate,
                     "option":self.q.kCGEventFlagMaskAlternate,"shift":self.q.kCGEventFlagMaskShift}
        codes = {"a":0,"s":1,"d":2,"f":3,"h":4,"g":5,"z":6,"x":7,"c":8,"v":9,"b":11,
                 "q":12,"w":13,"e":14,"r":15,"y":16,"t":17,"1":18,"2":19,"3":20,"4":21,
                 "6":22,"5":23,"9":25,"7":26,"8":28,"0":29,"o":31,"u":32,"i":34,"p":35,
                 "l":37,"j":38,"k":40,"n":45,"m":46,"enter":36,"return":36,"tab":48,"space":49,
                 "backspace":51,"escape":53,"esc":53,"delete":117,"home":115,"end":119,
                 "pageup":116,"pagedown":121,"left":123,"right":124,"down":125,"up":126,
                 "f1":122,"f2":120,"f3":99,"f4":118,"f5":96,"f6":97,"f7":98,"f8":100,
                 "f9":101,"f10":109,"f11":103,"f12":111}
        flags = 0
        for part in parts[:-1]:
            if part not in modifiers:
                raise Fault(-32602, "Unknown key modifier: " + part)
            flags |= modifiers[part]
        if parts[-1] not in codes:
            raise Fault(-32602, "Unsupported key: " + parts[-1])
        for down in (True, False):
            event = self.q.CGEventCreateKeyboardEvent(None, codes[parts[-1]], down)
            self.q.CGEventSetFlags(event, flags)
            self.post(event)

    def hit(self, target, x, y):
        system = self.ax.AXUIElementCreateSystemWide()
        code, node = self.ax.AXUIElementCopyElementAtPosition(system, x, y, None)
        if code != 0:
            raise Fault(6, "Cannot identify the element under the pointer")
        code, pid = self.ax.AXUIElementGetPid(node, None)
        if code != 0 or pid != target[0].processIdentifier():
            raise Fault(6, "Requested point is obscured by another application")

    def act(self, target, method, p, node, rows):
        self.foreground(target)
        if method == "click":
            if node is not None:
                self.check(self.ax.AXUIElementPerformAction(node, "AXPress"))
            else:
                x, y = point(p, rows)
                self.hit(target, x, y)
                for kind in (self.q.kCGEventLeftMouseDown, self.q.kCGEventLeftMouseUp):
                    self.post(self.q.CGEventCreateMouseEvent(None, kind, (x,y), self.q.kCGMouseButtonLeft))
        elif method == "type":
            value = required(p, "text")
            for start in range(0, len(value), 64):
                chunk = value[start:start+64]
                count = len(chunk.encode("utf-16-le"))//2
                for down in (True, False):
                    event = self.q.CGEventCreateKeyboardEvent(None, 0, down)
                    self.q.CGEventKeyboardSetUnicodeString(event, count, chunk)
                    self.post(event)
        elif method == "press":
            self.key(required(p, "key"))
        elif method == "set":
            self.set_attr(node, "AXValue", required(p, "value"))
        elif method == "select":
            value = required(p, "target")
            if not value:
                raise Fault(-32602, "target cannot be empty")
            content = str(self.attr(node, "AXValue", ""))
            start = content.find(value)
            if start < 0:
                raise Fault(6, "Text not found")
            offset, length = len(content[:start].encode("utf-16-le"))//2, len(value.encode("utf-16-le"))//2
            span = self.ax.AXValueCreate(self.ax.kAXValueCFRangeType, self.cf.CFRangeMake(offset, length))
            self.set_attr(node, "AXSelectedTextRange", span)
        elif method == "menu":
            action = required(p, "action").casefold()
            if action in ("expand", "collapse"):
                code, writable = self.ax.AXUIElementIsAttributeSettable(node, "AXExpanded", None)
                if code == 0 and writable:
                    self.set_attr(node, "AXExpanded", action == "expand")
                elif action == "expand":
                    self.check(self.ax.AXUIElementPerformAction(node, "AXShowMenu"))
                else:
                    raise Fault(6, "Element does not support collapsing")
            elif action == "press":
                self.check(self.ax.AXUIElementPerformAction(node, "AXPress"))
            else:
                raise Fault(-32602, "Supported menu actions: expand, collapse, press")
        elif method == "scroll":
            direction, clicks = scroll_args(p)
            position, size = self.geometry(node)
            if not position or min(size) <= 0:
                raise Fault(6, "Element has no visible scroll bounds")
            x, y = int(position[0]+size[0]/2), int(position[1]+size[1]/2)
            point({"x":x,"y":y}, rows)
            self.hit(target, x, y)
            self.post(self.q.CGEventCreateMouseEvent(None, self.q.kCGEventMouseMoved, (x,y), self.q.kCGMouseButtonLeft))
            vertical = clicks * (1 if direction == "up" else -1) if direction in ("up", "down") else 0
            horizontal = clicks * (1 if direction == "left" else -1) if direction in ("left", "right") else 0
            self.post(self.q.CGEventCreateScrollWheelEvent(None, self.q.kCGScrollEventUnitLine, 2, vertical, horizontal))
