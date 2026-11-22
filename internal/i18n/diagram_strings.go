package i18n

// Strings of Mermaid diagrams in the transcript.
var diagramChinese = map[string]string{
	"m open diagram":                                         "m 打开图",
	"no mermaid diagram in this block":                       "此块中没有 Mermaid 图",
	"mermaid: could not write the diagram: ":                 "mermaid：无法写入图源码：",
	"mermaid: could not open the diagram: ":                  "mermaid：无法打开图：",
	"opened %d mermaid diagram(s) from %s":                   "已打开 %d 个 Mermaid 图（%s）",
	"open the reply's mermaid diagrams in the system viewer": "用系统默认程序打开回复中的 Mermaid 图",
}

var diagramTraditionalChinese = map[string]string{
	"m open diagram":                                         "m 開啟圖",
	"no mermaid diagram in this block":                       "此區塊中沒有 Mermaid 圖",
	"mermaid: could not write the diagram: ":                 "mermaid：無法寫入圖原始碼：",
	"mermaid: could not open the diagram: ":                  "mermaid：無法開啟圖：",
	"opened %d mermaid diagram(s) from %s":                   "已開啟 %d 個 Mermaid 圖（%s）",
	"open the reply's mermaid diagrams in the system viewer": "用系統預設程式開啟回覆中的 Mermaid 圖",
}

func init() {
	for k, v := range diagramChinese {
		if _, ok := chinese[k]; !ok {
			chinese[k] = v
		}
	}
	for k, v := range diagramTraditionalChinese {
		if _, ok := traditionalChinese[k]; !ok {
			traditionalChinese[k] = v
		}
	}
}
