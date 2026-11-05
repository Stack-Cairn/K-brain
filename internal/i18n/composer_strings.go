package i18n

// Strings of the composer: editing keys, paste chips, the history search,
// the draft stash and the queue pane.
var composerChinese = map[string]string{
	"Ctrl+Z / Alt+Z — undo / redo the draft": "Ctrl+Z / Alt+Z — 撤销 / 重做草稿编辑",
	"Ctrl+W / Ctrl+U / Alt+K / Alt+D — cut word, to line start, to line end, next word · Ctrl+Y paste it back (Alt+Y older)": "Ctrl+W / Ctrl+U / Alt+K / Alt+D — 剪切前一词、到行首、到行尾、后一词 · Ctrl+Y 粘回（Alt+Y 更早的）",
	"Alt+M — multiline mode: Enter adds a line, Ctrl+J sends":                                                                "Alt+M — 多行模式：Enter 换行，Ctrl+J 发送",
	"Ctrl+R — search prompt history (Tab: all directories)":                                                                  "Ctrl+R — 搜索输入历史（Tab：所有目录）",
	"Ctrl+S — stash the draft, or restore it on an empty composer":                                                           "Ctrl+S — 暂存草稿；输入框为空时恢复",
	"with a queued message selected: Enter sends it now, Ctrl+E edits, Alt+↑/↓ moves":                                        "选中队列消息时：Enter 立即发送，Ctrl+E 编辑，Alt+↑/↓ 移动",
	"  (editing)": "  （编辑中）",
	"↑ select":    "↑ 选择",
	"↑↓ select":   "↑↓ 选择",
	"↑/↓ select · Enter use · Esc cancel": "↑/↓ 选择 · Enter 使用 · Esc 取消",
	"all directories":               "所有目录",
	"this directory":                "当前目录",
	"Tab all directories":           "Tab 所有目录",
	"Tab this directory":            "Tab 当前目录",
	"Alt+↑↓ move":                   "Alt+↑↓ 移动",
	"at most %d images per message": "每条消息最多 %d 张图片",
	"Ctrl+E edit":                   "Ctrl+E 编辑",
	"Del remove":                    "Del 删除",
	"draft restored":                "已恢复草稿",
	"draft stashed · Ctrl+S on an empty composer restores it": "草稿已暂存 · 输入框为空时按 Ctrl+S 恢复",
	"draft stashed — Ctrl+S restores it":                      "草稿已暂存 — Ctrl+S 恢复",
	"Enter on empty input sends now":                          "空输入按 Enter 立即发送",
	"Enter saves":                                             "Enter 保存",
	"Enter send now":                                          "Enter 立即发送",
	"Esc back":                                                "Esc 返回",
	"Esc puts it back":                                        "Esc 放回原处",
	"history":                                                 "历史",
	"loading…":                                                "加载中…",
	"multiline off · Enter sends":                             "多行模式关闭 · Enter 发送",
	"multiline on · Enter adds a line · Ctrl+J / Shift+Enter sends · Alt+M ends": "多行模式开启 · Enter 换行 · Ctrl+J / Shift+Enter 发送 · Alt+M 退出",
	"no matching prompts":                   "没有匹配的输入",
	"no stashed draft":                      "没有暂存的草稿",
	"queued (%d) · editing #%d":             "队列 (%d) · 正在编辑第 %d 条",
	"queued (%d) · sent when the turn ends": "队列 (%d) · 本轮结束后发送",
	"queued (%d)":                           "队列 (%d)",
	"search history…":                       "搜索历史…",
}

var composerTraditionalChinese = map[string]string{
	"Ctrl+Z / Alt+Z — undo / redo the draft": "Ctrl+Z / Alt+Z — 復原 / 重做草稿編輯",
	"Ctrl+W / Ctrl+U / Alt+K / Alt+D — cut word, to line start, to line end, next word · Ctrl+Y paste it back (Alt+Y older)": "Ctrl+W / Ctrl+U / Alt+K / Alt+D — 剪下前一詞、到行首、到行尾、後一詞 · Ctrl+Y 貼回（Alt+Y 更早的）",
	"Alt+M — multiline mode: Enter adds a line, Ctrl+J sends":                                                                "Alt+M — 多行模式：Enter 換行，Ctrl+J 傳送",
	"Ctrl+R — search prompt history (Tab: all directories)":                                                                  "Ctrl+R — 搜尋輸入歷史（Tab：所有目錄）",
	"Ctrl+S — stash the draft, or restore it on an empty composer":                                                           "Ctrl+S — 暫存草稿；輸入框為空時還原",
	"with a queued message selected: Enter sends it now, Ctrl+E edits, Alt+↑/↓ moves":                                        "選取佇列訊息時：Enter 立即傳送，Ctrl+E 編輯，Alt+↑/↓ 移動",
	"  (editing)": "  （編輯中）",
	"↑ select":    "↑ 選擇",
	"↑↓ select":   "↑↓ 選擇",
	"↑/↓ select · Enter use · Esc cancel": "↑/↓ 選擇 · Enter 使用 · Esc 取消",
	"all directories":               "所有目錄",
	"this directory":                "目前目錄",
	"Tab all directories":           "Tab 所有目錄",
	"Tab this directory":            "Tab 目前目錄",
	"Alt+↑↓ move":                   "Alt+↑↓ 移動",
	"at most %d images per message": "每則訊息最多 %d 張圖片",
	"Ctrl+E edit":                   "Ctrl+E 編輯",
	"Del remove":                    "Del 刪除",
	"draft restored":                "已還原草稿",
	"draft stashed · Ctrl+S on an empty composer restores it": "草稿已暫存 · 輸入框為空時按 Ctrl+S 還原",
	"draft stashed — Ctrl+S restores it":                      "草稿已暫存 — Ctrl+S 還原",
	"Enter on empty input sends now":                          "空輸入按 Enter 立即傳送",
	"Enter saves":                                             "Enter 儲存",
	"Enter send now":                                          "Enter 立即傳送",
	"Esc back":                                                "Esc 返回",
	"Esc puts it back":                                        "Esc 放回原處",
	"history":                                                 "歷史",
	"loading…":                                                "載入中…",
	"multiline off · Enter sends":                             "多行模式關閉 · Enter 傳送",
	"multiline on · Enter adds a line · Ctrl+J / Shift+Enter sends · Alt+M ends": "多行模式開啟 · Enter 換行 · Ctrl+J / Shift+Enter 傳送 · Alt+M 結束",
	"no matching prompts":                   "沒有相符的輸入",
	"no stashed draft":                      "沒有暫存的草稿",
	"queued (%d) · editing #%d":             "佇列 (%d) · 正在編輯第 %d 則",
	"queued (%d) · sent when the turn ends": "佇列 (%d) · 本輪結束後傳送",
	"queued (%d)":                           "佇列 (%d)",
	"search history…":                       "搜尋歷史…",
}

func init() {
	for k, v := range composerChinese {
		if _, ok := chinese[k]; !ok {
			chinese[k] = v
		}
	}
	for k, v := range composerTraditionalChinese {
		if _, ok := traditionalChinese[k]; !ok {
			traditionalChinese[k] = v
		}
	}
}
