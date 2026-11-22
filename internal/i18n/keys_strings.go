package i18n

// Strings of the key bindings: /help rows and the hints that name a key.
var keysChinese = map[string]string{
	"Alt+O — expand or collapse the last tool result": "Alt+O — 展开或折叠最近的工具结果",
	"Ctrl+W / Ctrl+U / Ctrl+K / Alt+D — cut word, to line start, to line end (also Alt+K), next word · Ctrl+Y paste it back (Alt+Y older)": "Ctrl+W / Ctrl+U / Ctrl+K / Alt+D — 剪切前一词、到行首、到行尾（也可 Alt+K）、后一词 · Ctrl+Y 粘回（Alt+Y 更早的）",
	"Ctrl+A / Ctrl+E — start / end of the line":                  "Ctrl+A / Ctrl+E — 行首 / 行尾",
	"Esc (empty composer) / Ctrl+C Ctrl+C — interrupt the agent": "Esc（输入框为空时）/ Ctrl+C Ctrl+C — 中断任务",
	"Ctrl+C again to interrupt":                                  "再按 Ctrl+C 中断",
	"Ctrl+C Ctrl+C to interrupt":                                 "Ctrl+C Ctrl+C 中断",
	"Shift+←/→/↑/↓, Shift+Home/End, Alt+Shift+←/→ — select in the composer by character, row, to the line's start / end, by word; typing replaces it, Backspace deletes it, Ctrl+C copies it": "Shift+←/→/↑/↓、Shift+Home/End、Alt+Shift+←/→ — 在输入框中按字符、按行、到行首 / 行尾、按词选择；输入替换选区，Backspace 删除，Ctrl+C 复制",
	"End (empty composer) / Ctrl+End — back to the newest output after scrolling up":                                                                                                          "End（输入框为空时）/ Ctrl+End — 向上滚动后回到最新输出",
	"Alt+↑ — focus the transcript: j/k select a block, Ctrl+U/Ctrl+D half a page, h/l fold / open, e toggle, E fold all, v view, / find, Esc back":                                            "Alt+↑ — 聚焦对话记录：j/k 选择块，Ctrl+U/Ctrl+D 翻半页，h/l 折叠 / 展开，e 切换，E 全部折叠，v 查看，/ 查找，Esc 返回",
}

var keysTraditionalChinese = map[string]string{
	"Alt+O — expand or collapse the last tool result": "Alt+O — 展開或摺疊最近的工具結果",
	"Ctrl+W / Ctrl+U / Ctrl+K / Alt+D — cut word, to line start, to line end (also Alt+K), next word · Ctrl+Y paste it back (Alt+Y older)": "Ctrl+W / Ctrl+U / Ctrl+K / Alt+D — 剪下前一詞、到行首、到行尾（也可 Alt+K）、後一詞 · Ctrl+Y 貼回（Alt+Y 更早的）",
	"Ctrl+A / Ctrl+E — start / end of the line":                  "Ctrl+A / Ctrl+E — 行首 / 行尾",
	"Esc (empty composer) / Ctrl+C Ctrl+C — interrupt the agent": "Esc（輸入框為空時）/ Ctrl+C Ctrl+C — 中斷任務",
	"Ctrl+C again to interrupt":                                  "再按 Ctrl+C 中斷",
	"Ctrl+C Ctrl+C to interrupt":                                 "Ctrl+C Ctrl+C 中斷",
	"Shift+←/→/↑/↓, Shift+Home/End, Alt+Shift+←/→ — select in the composer by character, row, to the line's start / end, by word; typing replaces it, Backspace deletes it, Ctrl+C copies it": "Shift+←/→/↑/↓、Shift+Home/End、Alt+Shift+←/→ — 在輸入框中按字元、按行、到行首 / 行尾、按詞選取；輸入取代選取範圍，Backspace 刪除，Ctrl+C 複製",
	"End (empty composer) / Ctrl+End — back to the newest output after scrolling up":                                                                                                          "End（輸入框為空時）/ Ctrl+End — 向上捲動後回到最新輸出",
	"Alt+↑ — focus the transcript: j/k select a block, Ctrl+U/Ctrl+D half a page, h/l fold / open, e toggle, E fold all, v view, / find, Esc back":                                            "Alt+↑ — 聚焦對話記錄：j/k 選擇區塊，Ctrl+U/Ctrl+D 翻半頁，h/l 摺疊 / 展開，e 切換，E 全部摺疊，v 檢視，/ 尋找，Esc 返回",
}

func init() {
	for k, v := range keysChinese {
		if _, ok := chinese[k]; !ok {
			chinese[k] = v
		}
	}
	for k, v := range keysTraditionalChinese {
		if _, ok := traditionalChinese[k]; !ok {
			traditionalChinese[k] = v
		}
	}
}
