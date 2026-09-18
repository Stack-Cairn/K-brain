package tools

import (
	"encoding/json"
	"testing"
)

func TestBuiltinToolSchemasParse(t *testing.T) {
	for _, tool := range append(append(All(), BrowserExec()), ComputerExec()) {
		var v any
		if err := json.Unmarshal(tool.Def.Function.Parameters, &v); err != nil {
			t.Errorf("%s: schema does not parse: %v", tool.Def.Function.Name, err)
		}
	}
}
