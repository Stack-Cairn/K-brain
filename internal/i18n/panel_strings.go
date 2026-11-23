package i18n

// Strings of the top panel (goal, plan and subagents above the transcript).
var panelChinese = map[string]string{
	"Ctrl+] / Alt+T — expand or collapse the top panel (goal, plan, subagents); a click on its header does the same": "Ctrl+] / Alt+T — 展开或收起顶部面板（目标、计划、子代理）；点击面板标题行效果相同",
	"Ctrl+] expand":             "Ctrl+] 展开",
	"Ctrl+] collapse":           "Ctrl+] 收起",
	"Executing":                 "执行中",
	"Idle":                      "空闲",
	"Paused":                    "已暂停",
	"Waiting for you":           "等待你的回复",
	"round %d/%d":               "第 %d/%d 轮",
	"goal check":                "目标检查",
	"Subagent · %s wants to %s": "子代理 · %s 想要%s",
	"run this command":          "运行此命令",
	"fetch this URL":            "获取此网址",
	"search the web for this":   "在网上搜索这些内容",
	"create this file":          "创建此文件",
	"apply this edit":           "应用此修改",
	"write this file":           "写入此文件",
	"use %s":                    "使用 %s",
	" · +%d pending":            " · 另有 %d 个待批准",
	"waiting for approval":      "等待批准",
}

var panelTraditionalChinese = map[string]string{
	"Ctrl+] / Alt+T — expand or collapse the top panel (goal, plan, subagents); a click on its header does the same": "Ctrl+] / Alt+T — 展開或收合頂部面板（目標、計畫、子代理）；點擊面板標題列效果相同",
	"Ctrl+] expand":             "Ctrl+] 展開",
	"Ctrl+] collapse":           "Ctrl+] 收合",
	"Executing":                 "執行中",
	"Idle":                      "閒置",
	"Paused":                    "已暫停",
	"Waiting for you":           "等待你的回覆",
	"round %d/%d":               "第 %d/%d 輪",
	"goal check":                "目標檢查",
	"Subagent · %s wants to %s": "子代理 · %s 想要%s",
	"run this command":          "執行此命令",
	"fetch this URL":            "擷取此網址",
	"search the web for this":   "在網路上搜尋這些內容",
	"create this file":          "建立此檔案",
	"apply this edit":           "套用此修改",
	"write this file":           "寫入此檔案",
	"use %s":                    "使用 %s",
	" · +%d pending":            " · 另有 %d 個待核准",
	"waiting for approval":      "等待核准",
}

func init() {
	for k, v := range panelChinese {
		if _, ok := chinese[k]; !ok {
			chinese[k] = v
		}
	}
	for k, v := range panelTraditionalChinese {
		if _, ok := traditionalChinese[k]; !ok {
			traditionalChinese[k] = v
		}
	}
}
