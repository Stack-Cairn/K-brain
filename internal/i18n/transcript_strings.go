package i18n

// Strings of the transcript: its focus bar, the block viewer and /find.
var transcriptChinese = map[string]string{
	"Alt+↑ — focus the transcript: j/k select a block, h/l fold / open, e toggle, E fold all, v view, / find, Esc back": "Alt+↑ — 聚焦对话记录：j/k 选择块，h/l 折叠 / 展开，e 切换，E 全部折叠，v 查看，/ 查找，Esc 返回",
	"in the block viewer: j/k scroll, Space/PgDn page, g/G top / end, / find, n/N next / previous, Esc back":            "在块查看器中：j/k 滚动，Space/PgDn 翻页，g/G 顶部 / 末尾，/ 查找，n/N 下一个 / 上一个，Esc 返回",
	"search the transcript (n/N step through matches)":                                                                  "搜索对话记录（n/N 逐个跳转匹配）",
	"transcript":                "对话记录",
	"prompt":                    "提问",
	"reply":                     "回复",
	"thinking":                  "思考",
	"tool call":                 "工具调用",
	"tool output":               "工具输出",
	"note":                      "提示",
	"no match":                  "无匹配",
	"all":                       "全部",
	"top":                       "顶部",
	"end":                       "末尾",
	"nothing to search yet":     "还没有可搜索的内容",
	"Enter search · Esc cancel": "Enter 搜索 · Esc 取消",
	"j/k move · e fold · E all · v view · / find · Esc back":         "j/k 移动 · e 折叠 · E 全部 · v 查看 · / 查找 · Esc 返回",
	"n/N older/newer · / new search · Esc back":                      "n/N 更早/更新 · / 新搜索 · Esc 返回",
	"n/N next/previous · j/k scroll · Esc back":                      "n/N 下一个/上一个 · j/k 滚动 · Esc 返回",
	"j/k scroll · Space/PgDn page · g/G top/end · / find · Esc back": "j/k 滚动 · Space/PgDn 翻页 · g/G 顶部/末尾 · / 查找 · Esc 返回",
}

var transcriptTraditionalChinese = map[string]string{
	"Alt+↑ — focus the transcript: j/k select a block, h/l fold / open, e toggle, E fold all, v view, / find, Esc back": "Alt+↑ — 聚焦對話記錄：j/k 選擇區塊，h/l 摺疊 / 展開，e 切換，E 全部摺疊，v 檢視，/ 尋找，Esc 返回",
	"in the block viewer: j/k scroll, Space/PgDn page, g/G top / end, / find, n/N next / previous, Esc back":            "在區塊檢視器中：j/k 捲動，Space/PgDn 翻頁，g/G 頂部 / 末尾，/ 尋找，n/N 下一個 / 上一個，Esc 返回",
	"search the transcript (n/N step through matches)":                                                                  "搜尋對話記錄（n/N 逐個跳轉相符項）",
	"transcript":                "對話記錄",
	"prompt":                    "提問",
	"reply":                     "回覆",
	"thinking":                  "思考",
	"tool call":                 "工具呼叫",
	"tool output":               "工具輸出",
	"note":                      "提示",
	"no match":                  "無相符項",
	"all":                       "全部",
	"top":                       "頂部",
	"end":                       "末尾",
	"nothing to search yet":     "還沒有可搜尋的內容",
	"Enter search · Esc cancel": "Enter 搜尋 · Esc 取消",
	"j/k move · e fold · E all · v view · / find · Esc back":         "j/k 移動 · e 摺疊 · E 全部 · v 檢視 · / 尋找 · Esc 返回",
	"n/N older/newer · / new search · Esc back":                      "n/N 更早/更新 · / 新搜尋 · Esc 返回",
	"n/N next/previous · j/k scroll · Esc back":                      "n/N 下一個/上一個 · j/k 捲動 · Esc 返回",
	"j/k scroll · Space/PgDn page · g/G top/end · / find · Esc back": "j/k 捲動 · Space/PgDn 翻頁 · g/G 頂部/末尾 · / 尋找 · Esc 返回",
}

func init() {
	for k, v := range transcriptChinese {
		if _, ok := chinese[k]; !ok {
			chinese[k] = v
		}
	}
	for k, v := range transcriptTraditionalChinese {
		if _, ok := traditionalChinese[k]; !ok {
			traditionalChinese[k] = v
		}
	}
}
