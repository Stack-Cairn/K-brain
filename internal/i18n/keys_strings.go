package i18n

// Strings of the key bindings: /help rows and the hints that name a key.
var keysChinese = map[string]string{
	"Alt+O — expand or collapse the last tool result": "Alt+O — 展开或折叠最近的工具结果",
	"Ctrl+W / Ctrl+U / Ctrl+K / Alt+D — cut word, to line start, to line end (also Alt+K), next word · Ctrl+Y paste it back (Alt+Y older)": "Ctrl+W / Ctrl+U / Ctrl+K / Alt+D — 剪切前一词、到行首、到行尾（也可 Alt+K）、后一词 · Ctrl+Y 粘回（Alt+Y 更早的）",
	"Ctrl+A / Ctrl+E — start / end of the line":                  "Ctrl+A / Ctrl+E — 行首 / 行尾",
	"Esc (empty composer) / Ctrl+C Ctrl+C — interrupt the agent": "Esc（输入框为空时）/ Ctrl+C Ctrl+C — 中断任务",
	"Ctrl+C again to interrupt":                                  "再按 Ctrl+C 中断",
	"Ctrl+C Ctrl+C to interrupt":                                 "Ctrl+C Ctrl+C 中断",
}

var keysTraditionalChinese = map[string]string{
	"Alt+O — expand or collapse the last tool result": "Alt+O — 展開或摺疊最近的工具結果",
	"Ctrl+W / Ctrl+U / Ctrl+K / Alt+D — cut word, to line start, to line end (also Alt+K), next word · Ctrl+Y paste it back (Alt+Y older)": "Ctrl+W / Ctrl+U / Ctrl+K / Alt+D — 剪下前一詞、到行首、到行尾（也可 Alt+K）、後一詞 · Ctrl+Y 貼回（Alt+Y 更早的）",
	"Ctrl+A / Ctrl+E — start / end of the line":                  "Ctrl+A / Ctrl+E — 行首 / 行尾",
	"Esc (empty composer) / Ctrl+C Ctrl+C — interrupt the agent": "Esc（輸入框為空時）/ Ctrl+C Ctrl+C — 中斷任務",
	"Ctrl+C again to interrupt":                                  "再按 Ctrl+C 中斷",
	"Ctrl+C Ctrl+C to interrupt":                                 "Ctrl+C Ctrl+C 中斷",
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
