package i18n

// Strings of the key bindings: /help rows and the hints that name a key.
var keysChinese = map[string]string{
	"Alt+O — expand or collapse the last tool result": "Alt+O — 展开或折叠最近的工具结果",
	"Ctrl+W / Ctrl+U / Ctrl+K / Alt+D — cut word, to line start, to line end (also Alt+K), next word · Ctrl+Y paste it back (Alt+Y older)": "Ctrl+W / Ctrl+U / Ctrl+K / Alt+D — 剪切前一词、到行首、到行尾（也可 Alt+K）、后一词 · Ctrl+Y 粘回（Alt+Y 更早的）",
	"Ctrl+A / Ctrl+E — start / end of the line":                  "Ctrl+A / Ctrl+E — 行首 / 行尾",
	"Esc (empty composer) / Ctrl+C Ctrl+C — interrupt the agent": "Esc（输入框为空时）/ Ctrl+C Ctrl+C — 中断任务",
	"Ctrl+C again to interrupt":                                  "再按 Ctrl+C 中断",
	"Ctrl+C Ctrl+C to interrupt":                                 "Ctrl+C Ctrl+C 中断",
	"Shift+←/→/↑/↓, Shift+Home/End, Alt+Shift+←/→ — select in the composer by character, row, to the line's start / end, by word; typing replaces it, Backspace deletes it, Ctrl+C copies it":          "Shift+←/→/↑/↓、Shift+Home/End、Alt+Shift+←/→ — 在输入框中按字符、按行、到行首 / 行尾、按词选择；输入替换选区，Backspace 删除，Ctrl+C 复制",
	"End (empty composer) / Ctrl+End — back to the newest output after scrolling up":                                                                                                                   "End（输入框为空时）/ Ctrl+End — 向上滚动后回到最新输出",
	"Alt+↑ — focus the transcript: j/k select a block, Ctrl+U/Ctrl+D half a page, h/l fold / open, e toggle, E fold all, v view, / find, Esc back":                                                     "Alt+↑ — 聚焦对话记录：j/k 选择块，Ctrl+U/Ctrl+D 翻半页，h/l 折叠 / 展开，e 切换，E 全部折叠，v 查看，/ 查找，Esc 返回",
	"Alt+↑ — focus the transcript: j/k select a block, [/] previous / next prompt, Ctrl+U/Ctrl+D half a page, h/l fold / open, e toggle, E fold all, y copy, r raw markdown, v view, / find, Esc back": "Alt+↑ — 聚焦对话记录：j/k 选择块，[/] 上一条 / 下一条提问，Ctrl+U/Ctrl+D 翻半页，h/l 折叠 / 展开，e 切换，E 全部折叠，y 复制，r 原始 Markdown，v 查看，/ 查找，Esc 返回",
	"j/k move · [/] turn · e fold · E all · y copy · r raw · v view · / find · Esc back":                                                                                                               "j/k 移动 · [/] 提问 · e 折叠 · E 全部 · y 复制 · r 原文 · v 查看 · / 查找 · Esc 返回",
	"only a reply has a raw view": "只有回复有原始视图",
	"previous prompt":             "上一条提问",
	"next prompt":                 "下一条提问",
	"copy the block (a reply's markdown, or just the code)": "复制该块（回复的 Markdown，或仅代码）",
	"show a reply's raw markdown":                           "显示回复的原始 Markdown",
	"/vim — vim mode: Esc for normal mode · h j k l w b e 0 ^ $ move · x dd dw D delete · cw cc C change · yy p yank / put · u undo · i a I A o O insert": "/vim — vim 模式：Esc 进入普通模式 · h j k l w b e 0 ^ $ 移动 · x dd dw D 删除 · cw cc C 修改 · yy p 复制 / 粘贴 · u 撤销 · i a I A o O 插入",
	"vim mode: on — Esc for normal mode, i to insert": "vim 模式：开 — Esc 进入普通模式，i 回到插入模式",
	"vim mode: off":        "vim 模式：关",
	"Esc Esc to interrupt": "Esc Esc 中断",
	"Vim mode":             "Vim 模式",
	"modal editing in the composer: Esc for normal mode, i to insert": "输入框里的模式编辑：Esc 进入普通模式，i 回到插入模式",
	"Vim normal mode (/vim)":                                       "Vim 普通模式（/vim）",
	"toggle vim mode in the composer":                              "切换输入框的 vim 模式",
	"insert mode → normal mode; in normal mode the usual Esc":      "插入模式 → 普通模式；普通模式下是原来的 Esc",
	"move: left, down, up, right (j/k past the ends walk history)": "移动：左、下、上、右（在首行 / 末行按 j/k 翻历史）",
	"next word, back a word, end of word":                          "下一个词、上一个词、词尾",
	"line start, first non-blank, line end":                        "行首、第一个非空字符、行尾",
	"delete: x a character, dd the line, dw a word, D to line end": "删除：x 一个字符，dd 整行，dw 一个词，D 到行尾",
	"change: cw the word, cc the line, C to line end":              "修改：cw 当前词，cc 整行，C 到行尾",
	"yy yanks the line":                                            "yy 复制整行",
	"put the yank after the cursor (a line below it)":              "把复制的内容粘贴到光标后（整行则粘贴到下一行）",
	"undo": "撤销",
	"insert: before / after the cursor, at line start / end": "插入：光标前 / 后，行首 / 行尾",
	"open a line below / above and insert":                   "在下方 / 上方新开一行并插入",
	"send, back in insert mode":                              "发送，并回到插入模式",
	"%s is what Option+%s types unless the terminal sends Option as Meta; for Alt shortcuts see /doctor": "终端没有把 Option 当作 Meta 键时，Option+%[2]s 输入的是 %[1]s；要用 Alt 快捷键请看 /doctor",
	"Option as Alt": "Option 当作 Alt",
	"read macOS Option+letter characters as Alt shortcuts": "把 macOS 上 Option+字母输入的字符当作 Alt 快捷键",
	"Thinking tokens shown · Ctrl+O hides them":            "已显示思考过程 · Ctrl+O 隐藏",
	"Thinking tokens hidden · Ctrl+O shows them":           "已隐藏思考过程 · Ctrl+O 显示",
}

var keysTraditionalChinese = map[string]string{
	"Alt+O — expand or collapse the last tool result": "Alt+O — 展開或摺疊最近的工具結果",
	"Ctrl+W / Ctrl+U / Ctrl+K / Alt+D — cut word, to line start, to line end (also Alt+K), next word · Ctrl+Y paste it back (Alt+Y older)": "Ctrl+W / Ctrl+U / Ctrl+K / Alt+D — 剪下前一詞、到行首、到行尾（也可 Alt+K）、後一詞 · Ctrl+Y 貼回（Alt+Y 更早的）",
	"Ctrl+A / Ctrl+E — start / end of the line":                  "Ctrl+A / Ctrl+E — 行首 / 行尾",
	"Esc (empty composer) / Ctrl+C Ctrl+C — interrupt the agent": "Esc（輸入框為空時）/ Ctrl+C Ctrl+C — 中斷任務",
	"Ctrl+C again to interrupt":                                  "再按 Ctrl+C 中斷",
	"Ctrl+C Ctrl+C to interrupt":                                 "Ctrl+C Ctrl+C 中斷",
	"Shift+←/→/↑/↓, Shift+Home/End, Alt+Shift+←/→ — select in the composer by character, row, to the line's start / end, by word; typing replaces it, Backspace deletes it, Ctrl+C copies it":          "Shift+←/→/↑/↓、Shift+Home/End、Alt+Shift+←/→ — 在輸入框中按字元、按行、到行首 / 行尾、按詞選取；輸入取代選取範圍，Backspace 刪除，Ctrl+C 複製",
	"End (empty composer) / Ctrl+End — back to the newest output after scrolling up":                                                                                                                   "End（輸入框為空時）/ Ctrl+End — 向上捲動後回到最新輸出",
	"Alt+↑ — focus the transcript: j/k select a block, Ctrl+U/Ctrl+D half a page, h/l fold / open, e toggle, E fold all, v view, / find, Esc back":                                                     "Alt+↑ — 聚焦對話記錄：j/k 選擇區塊，Ctrl+U/Ctrl+D 翻半頁，h/l 摺疊 / 展開，e 切換，E 全部摺疊，v 檢視，/ 尋找，Esc 返回",
	"Alt+↑ — focus the transcript: j/k select a block, [/] previous / next prompt, Ctrl+U/Ctrl+D half a page, h/l fold / open, e toggle, E fold all, y copy, r raw markdown, v view, / find, Esc back": "Alt+↑ — 聚焦對話記錄：j/k 選擇區塊，[/] 上一則 / 下一則提問，Ctrl+U/Ctrl+D 翻半頁，h/l 摺疊 / 展開，e 切換，E 全部摺疊，y 複製，r 原始 Markdown，v 檢視，/ 尋找，Esc 返回",
	"j/k move · [/] turn · e fold · E all · y copy · r raw · v view · / find · Esc back":                                                                                                               "j/k 移動 · [/] 提問 · e 摺疊 · E 全部 · y 複製 · r 原文 · v 檢視 · / 尋找 · Esc 返回",
	"only a reply has a raw view": "只有回覆有原始檢視",
	"previous prompt":             "上一則提問",
	"next prompt":                 "下一則提問",
	"copy the block (a reply's markdown, or just the code)": "複製該區塊（回覆的 Markdown，或僅程式碼）",
	"show a reply's raw markdown":                           "顯示回覆的原始 Markdown",
	"/vim — vim mode: Esc for normal mode · h j k l w b e 0 ^ $ move · x dd dw D delete · cw cc C change · yy p yank / put · u undo · i a I A o O insert": "/vim — vim 模式：Esc 進入一般模式 · h j k l w b e 0 ^ $ 移動 · x dd dw D 刪除 · cw cc C 修改 · yy p 複製 / 貼上 · u 復原 · i a I A o O 插入",
	"vim mode: on — Esc for normal mode, i to insert": "vim 模式：開 — Esc 進入一般模式，i 回到插入模式",
	"vim mode: off":        "vim 模式：關",
	"Esc Esc to interrupt": "Esc Esc 中斷",
	"Vim mode":             "Vim 模式",
	"modal editing in the composer: Esc for normal mode, i to insert": "輸入框裡的模式編輯：Esc 進入一般模式，i 回到插入模式",
	"Vim normal mode (/vim)":                                       "Vim 一般模式（/vim）",
	"toggle vim mode in the composer":                              "切換輸入框的 vim 模式",
	"insert mode → normal mode; in normal mode the usual Esc":      "插入模式 → 一般模式；一般模式下是原來的 Esc",
	"move: left, down, up, right (j/k past the ends walk history)": "移動：左、下、上、右（在首行 / 末行按 j/k 翻歷史）",
	"next word, back a word, end of word":                          "下一個詞、上一個詞、詞尾",
	"line start, first non-blank, line end":                        "行首、第一個非空白字元、行尾",
	"delete: x a character, dd the line, dw a word, D to line end": "刪除：x 一個字元，dd 整行，dw 一個詞，D 到行尾",
	"change: cw the word, cc the line, C to line end":              "修改：cw 目前的詞，cc 整行，C 到行尾",
	"yy yanks the line":                                            "yy 複製整行",
	"put the yank after the cursor (a line below it)":              "把複製的內容貼到游標後（整行則貼到下一行）",
	"undo": "復原",
	"insert: before / after the cursor, at line start / end": "插入：游標前 / 後，行首 / 行尾",
	"open a line below / above and insert":                   "在下方 / 上方新開一行並插入",
	"send, back in insert mode":                              "送出，並回到插入模式",
	"%s is what Option+%s types unless the terminal sends Option as Meta; for Alt shortcuts see /doctor": "終端機沒有把 Option 當作 Meta 鍵時，Option+%[2]s 輸入的是 %[1]s；要用 Alt 快捷鍵請看 /doctor",
	"Option as Alt": "Option 當作 Alt",
	"read macOS Option+letter characters as Alt shortcuts": "把 macOS 上 Option+字母輸入的字元當作 Alt 快捷鍵",
	"Thinking tokens shown · Ctrl+O hides them":            "已顯示思考過程 · Ctrl+O 隱藏",
	"Thinking tokens hidden · Ctrl+O shows them":           "已隱藏思考過程 · Ctrl+O 顯示",
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
