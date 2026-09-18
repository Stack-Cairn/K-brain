

let ws = null;
let pinnedTabId = null;

const SESSION_ID = "k-brain-ext";

async function relayEndpoint() {
  const url = chrome.runtime.getURL("relay.json");
  const res = await fetch(url);
  if (!res.ok) throw new Error("relay.json missing — run `kn browser install`");
  const { addr, token, autoAttach } = await res.json();
  if (!addr || !token) throw new Error("relay.json is incomplete — re-run `kn browser install`");
  return {
    ws: `ws://${addr}/ext?token=${encodeURIComponent(token)}`,
    log: `http://${addr}/swlog?token=${encodeURIComponent(token)}`,
    autoAttach: !!autoAttach,
  };
}

async function swlog(step, extra) {
  try {
    const res = await fetch(chrome.runtime.getURL("relay.json"));
    if (!res.ok) { return; }
    const { addr, token } = await res.json();
    if (!addr) { return; }
    await fetch(`http://${addr}/swlog?token=${encodeURIComponent(token || "")}`, {
      method: "POST",
      body: step + (extra !== undefined ? ": " + extra : ""),
    });
  } catch (_) {}
}

async function maybeAutoAttach() {
  let auto = false;
  try {
    auto = (await relayEndpoint()).autoAttach;
  } catch (err) {
    swlog("relay-read-failed", String(err));
    return;
  }
  swlog("autoAttach-flag", String(auto));
  if (!auto) return;
  for (let i = 0; i < 30 && pinnedTabId == null; i++) {
    try {
      const tabs = await chrome.tabs.query({});
      const tab = tabs.find((t) => t.url && !t.url.startsWith("chrome://")) || tabs[0];
      swlog("poll", `i=${i} tabs=${tabs.length} tab=${tab && tab.id} url=${tab && tab.url}`);
      if (tab && tab.id != null) {
        await pin(tab.id);
        swlog("pinned", `tabId=${tab.id}`);
        return;
      }
    } catch (err) {
      swlog("autoAttach-attempt-failed", String(err && err.message || err));
    }
    await new Promise((r) => setTimeout(r, 500));
  }
}

function setBadge(on) {
  const text = on ? "●" : "";
  const color = on ? "#16a34a" : "#000000";
  if (pinnedTabId != null) {
    chrome.action.setBadgeText({ text, tabId: pinnedTabId }).catch(() => {});
    if (on) chrome.action.setBadgeBackgroundColor({ color, tabId: pinnedTabId }).catch(() => {});
  }
}

async function pin(tabId) {

  await swlog("pin-enter", `tabId=${tabId}`);
  await chrome.debugger.attach({ tabId }, "1.3");
  await swlog("debugger-attached", `tabId=${tabId}`);
  pinnedTabId = tabId;
  const { ws: endpoint } = await relayEndpoint();
  ws = new WebSocket(endpoint);
  ws.onopen = async () => {
    await swlog("ws-open", `tabId=${tabId}`);

    let title = "", url = "";
    try {
      const t = await chrome.tabs.get(tabId);
      title = t.title || ""; url = t.url || "";
    } catch (_) {}
    ws.send(JSON.stringify({ method: "k-brain.attached", params: { tabId, title, url } }));
    setBadge(true);
  };
  ws.onerror = () => { swlog("ws-error", `tabId=${tabId}`); };
  ws.onmessage = (ev) => {

    try {
      const msg = JSON.parse(ev.data);
      if (msg.sessionId === SESSION_ID) delete msg.sessionId;
      chrome.debugger
        .sendCommand({ tabId }, msg.method, msg.params || {})
        .then((result) => {
          if (msg.id) send({ id: msg.id, result: result || {} });
        })
        .catch((err) => {
          if (msg.id) send({ id: msg.id, error: { code: -32000, message: String(err && err.message || err) } });
        });
    } catch (_) {}
  };
  ws.onclose = () => unpin();
  ws.onerror = () => {};
}

function send(obj) {
  if (ws && ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify(obj));
}

async function unpin() {
  const tabId = pinnedTabId;
  pinnedTabId = null;
  if (ws) {
    const s = ws;
    ws = null;
    try { s.close(); } catch (_) {}
  }
  if (tabId != null) {
    setBadge(false);
    try { await chrome.debugger.detach({ tabId }); } catch (_) {}
  }
}

chrome.debugger.onEvent.addListener((source, method, params) => {
  if (source.tabId === pinnedTabId) send({ method, params: params || {} });
});

chrome.debugger.onDetach.addListener((source) => {
  if (source.tabId === pinnedTabId) unpin();
});

chrome.action.onClicked.addListener(async (tab) => {
  if (pinnedTabId === tab.id) {
    await unpin();
    return;
  }
  if (pinnedTabId != null) await unpin();
  try {
    await pin(tab.id);
  } catch (err) {
    console.error("k-brain: pin failed:", err);
    pinnedTabId = null;
  }
});

setInterval(() => {
  if (pinnedTabId != null && ws && ws.readyState === WebSocket.OPEN) {
    send({ method: "k-brain.ping", params: {} });
  }
}, 20000);

chrome.runtime.onInstalled.addListener(() => { swlog("onInstalled"); maybeAutoAttach(); });
chrome.runtime.onStartup.addListener(() => { swlog("onStartup"); maybeAutoAttach(); });

swlog("sw-start", "top-level");
maybeAutoAttach();
