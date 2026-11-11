package i18n

// Strings of the top panel (goal, plan and subagents above the transcript).
var panelChinese = map[string]string{
	"Alt+T — expand or collapse the top panel (goal, plan, subagents); a click on its header does the same": "Alt+T — 展开或收起顶部面板（目标、计划、子代理）；点击面板标题行效果相同",
	"Alt+T expand":    "Alt+T 展开",
	"Alt+T collapse":  "Alt+T 收起",
	"Executing":       "执行中",
	"Idle":            "空闲",
	"Paused":          "已暂停",
	"Waiting for you": "等待你的回复",
	"round %d/%d":     "第 %d/%d 轮",
	"goal check":      "目标检查",
}

var panelTraditionalChinese = map[string]string{
	"Alt+T — expand or collapse the top panel (goal, plan, subagents); a click on its header does the same": "Alt+T — 展開或收合頂部面板（目標、計畫、子代理）；點擊面板標題列效果相同",
	"Alt+T expand":    "Alt+T 展開",
	"Alt+T collapse":  "Alt+T 收合",
	"Executing":       "執行中",
	"Idle":            "閒置",
	"Paused":          "已暫停",
	"Waiting for you": "等待你的回覆",
	"round %d/%d":     "第 %d/%d 輪",
	"goal check":      "目標檢查",
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
