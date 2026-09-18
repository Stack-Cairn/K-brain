class LinuxDesktop:
    def __init__(self):
        try:
            import pyatspi
            self.ax = pyatspi
        except ImportError:
            raise Fault(2, "Install python3-pyatspi (AT-SPI2); run inside the user's graphical D-Bus session")
        self.wayland = os.environ.get("XDG_SESSION_TYPE") == "wayland" or bool(os.environ.get("WAYLAND_DISPLAY"))
        self.desktop = self.ax.Registry.getDesktop(0)

    def permissions(self, request):
        ready = self.desktop is not None and self.desktop.childCount > 0
        hint = "AT-SPI2 accessibility bus; applications must expose accessibility. "
        hint += ("Wayland: semantic actions are available; global keys/pointer need a compositor-approved remote-desktop session. Screenshots require grim on a compatible compositor."
                 if self.wayland else "X11: install xdotool for keys/pointer, python3-pil and python3-pil.imagetk for screenshots.")
        return {"accessibility": ready, "screenRecording": False, "hint": hint + " Screenshot permission is checked per capture."}

    def children(self, node):
        for i in range(min(node.childCount, 1500)):
            child = node.getChildAtIndex(i)
            if child is not None:
                yield child

    def applications(self):
        return list(self.children(self.desktop))

    def apps(self):
        result = []
        for app in self.applications():
            active = any(w.getState().contains(self.ax.STATE_ACTIVE) for w in self.children(app))
            result.append({"name": app.name or str(app.get_process_id()), "bundleId": app.name or "",
                           "pid": app.get_process_id(), "active": active})
        return result

    def resolve(self, name):
        matches = [app for app in self.applications() if name.casefold() in
                   ((app.name or "").casefold(), str(app.get_process_id()))]
        if len(matches) != 1:
            raise Fault(1, "App not found or ambiguous; select a PID from apps()")
        app = matches[0]
        windows = [w for w in self.children(app) if w.getState().contains(self.ax.STATE_SHOWING)]
        active = [w for w in windows if w.getState().contains(self.ax.STATE_ACTIVE)]
        if len(active) == 1:
            return app, active[0]
        if len(windows) != 1:
            raise Fault(1, "Window is ambiguous or not visible; activate the requested window and re-read state")
        return app, windows[0]

    def tree(self, target):
        app, window = target
        nodes, rows, stack = [], [], [window]
        while stack and len(nodes) < 1500:
            node = stack.pop()
            state = node.getState()
            if state.contains(self.ax.STATE_DEFUNCT):
                raise Fault(4, "Accessibility tree changed while reading")
            row = {"index": len(nodes), "role": node.getRoleName(), "title": node.name or "",
                   "desc": node.description or "", "enabled": state.contains(self.ax.STATE_ENABLED),
                   "focused": state.contains(self.ax.STATE_FOCUSED), "actions": []}
            try:
                rect = node.queryComponent().getExtents(self.ax.DESKTOP_COORDS)
                row.update(position=[rect.x, rect.y], size=[rect.width, rect.height])
            except NotImplementedError:
                pass
            try:
                action = node.queryAction()
                row["actions"] = [action.getName(i) for i in range(action.nActions)]
            except NotImplementedError:
                pass
            if node.getRole() != self.ax.ROLE_PASSWORD_TEXT:
                try:
                    text = node.queryText()
                    row["value"] = text.getText(0, min(text.characterCount, 16000))
                except NotImplementedError:
                    pass
            try:
                node.queryEditableText()
                row["actions"].append("set")
            except NotImplementedError:
                pass
            nodes.append(node)
            rows.append(row)
            stack.extend(reversed(list(self.children(node))))
        identity = [app.get_process_id(), window.getIndexInParent(), window.name,
                    [n.getIndexInParent() for n in nodes]]
        return identity, nodes, rows

    def foreground(self, target):
        app, window = target
        if not window.getState().contains(self.ax.STATE_ACTIVE):
            raise Fault(6, "Requested window is not active; activate it and re-read state before sending input")

    def screenshot(self, target):
        self.foreground(target)
        try:
            from PIL import Image, ImageGrab
        except ImportError:
            raise Fault(3, "Install python3-pil and python3-pil.imagetk for screenshots")
        import io
        rect = target[1].queryComponent().getExtents(self.ax.DESKTOP_COORDS)
        if rect.width <= 0 or rect.height <= 0 or rect.width * rect.height > 40000000:
            raise Fault(3, "Window bounds are invalid or too large")
        if self.wayland:
            if not shutil.which("grim"):
                raise Fault(3, "This Wayland compositor needs a screenshot portal; automatic capture is unavailable (grim not installed)")
            geometry = f"{rect.x},{rect.y} {rect.width}x{rect.height}"
            image = Image.open(io.BytesIO(run(["grim", "-g", geometry, "-"])))
        else:
            if not os.environ.get("DISPLAY"):
                raise Fault(3, "No X11 DISPLAY in this session")
            image = ImageGrab.grab(bbox=(rect.x, rect.y, rect.x + rect.width, rect.y + rect.height),
                                   xdisplay=os.environ["DISPLAY"])
        output = io.BytesIO()
        image.convert("RGB").save(output, format="JPEG", quality=80)
        return jpeg(output.getvalue())

    def xdo(self, *args):
        if self.wayland:
            raise Fault(6, "Global key/pointer injection is unavailable on Wayland without a user-approved remote-desktop portal; use semantic controls")
        if not shutil.which("xdotool"):
            raise Fault(6, "Install xdotool for X11 keyboard and pointer input")
        return run(["xdotool", *args])

    def action(self, node, names):
        try:
            action = node.queryAction()
            for i in range(action.nActions):
                if action.getName(i).casefold() in names:
                    if not action.doAction(i):
                        raise Fault(6, "Accessibility action was rejected")
                    return
        except NotImplementedError:
            pass
        raise Fault(6, "Element does not expose the requested accessibility action")

    def act(self, target, method, p, node, rows):
        self.foreground(target)
        if method == "click":
            if node is not None:
                self.action(node, ("click", "press", "activate", "jump"))
            else:
                x, y = point(p, rows)
                pointer_pid = self.xdo("mousemove", "--sync", str(x), str(y), "getmouselocation", "--shell").decode()
                window = next((line[7:] for line in pointer_pid.splitlines() if line.startswith("WINDOW=")), "")
                if not window or self.xdo("getwindowpid", window).decode().strip() != str(target[0].get_process_id()):
                    raise Fault(6, "Requested point is obscured by another application")
                self.xdo("click", "1")
        elif method == "type":
            value = required(p, "text")
            if self.wayland:
                focused = [n for n in self.tree(target)[1] if n.getState().contains(self.ax.STATE_FOCUSED)]
                if len(focused) != 1:
                    raise Fault(6, "No unique focused editable control")
                edit = focused[0].queryEditableText()
                text = focused[0].queryText()
                if text.getNSelections():
                    raise Fault(6, "Selected text requires an explicit set operation")
                if not edit.insertText(text.caretOffset, value, len(value.encode('utf-8'))):
                    raise Fault(6, "Text insertion rejected")
            else:
                self.xdo("type", "--clearmodifiers", "--delay", "1", "--", value)
        elif method == "press":
            key = required(p, "key")
            aliases = {"ctrl": "ctrl", "control": "ctrl", "alt": "alt", "shift": "shift",
                       "super": "super", "meta": "super", "cmd": "super", "enter": "Return",
                       "return": "Return", "escape": "Escape", "esc": "Escape", "tab": "Tab",
                       "backspace": "BackSpace", "delete": "Delete", "space": "space", "up": "Up",
                       "down": "Down", "left": "Left", "right": "Right", "home": "Home", "end": "End",
                       "pageup": "Prior", "pagedown": "Next"}
            parts = key.lower().split("+")
            for part in parts:
                if part not in aliases and not (len(part) == 1 and part.isascii() and part.isalnum()) and not (part.startswith("f") and part[1:].isdigit() and 1 <= int(part[1:]) <= 12):
                    raise Fault(-32602, "Unsupported key: " + part)
            combo = "+".join(aliases.get(x, x.upper() if x.startswith("f") and len(x)>1 else x) for x in parts)
            self.xdo("key", "--clearmodifiers", combo)
        elif method == "set":
            if not node.queryEditableText().setTextContents(required(p, "value")):
                raise Fault(6, "Element rejected text update")
        elif method == "select":
            value = required(p, "target")
            if not value:
                raise Fault(-32602, "target cannot be empty")
            text = node.queryText()
            content = text.getText(0, text.characterCount)
            start = content.find(value)
            if start < 0:
                raise Fault(6, "Text not found")
            accepted = text.setSelection(0, start, start + len(value)) if text.getNSelections() else text.addSelection(start, start + len(value))
            if not accepted:
                raise Fault(6, "Text selection rejected")
        elif method == "menu":
            action = required(p, "action").casefold()
            mapping = {"expand": ("expand", "show menu"), "collapse": ("collapse",), "press": ("click", "press", "activate")}
            if action not in mapping:
                raise Fault(-32602, "Supported menu actions: expand, collapse, press")
            self.action(node, mapping[action])
        elif method == "scroll":
            direction, clicks = scroll_args(p)
            action_names = ("scroll " + direction, direction)
            if self.wayland:
                for _ in range(clicks):
                    self.action(node, action_names)
            else:
                rect = node.queryComponent().getExtents(self.ax.DESKTOP_COORDS)
                x, y = rect.x + rect.width//2, rect.y + rect.height//2
                point({"x":x,"y":y}, rows)
                self.xdo("mousemove", "--sync", str(x), str(y))
                raw = self.xdo("getmouselocation", "--shell").decode()
                window = next((line[7:] for line in raw.splitlines() if line.startswith("WINDOW=")), "")
                if not window or self.xdo("getwindowpid", window).decode().strip() != str(target[0].get_process_id()):
                    raise Fault(6, "Scroll target is obscured by another app")
                self.xdo("click", "--repeat", str(clicks), "--delay", "30", str({"up":4,"down":5,"left":6,"right":7}[direction]))
