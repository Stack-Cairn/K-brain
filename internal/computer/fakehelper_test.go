package computer

import (
	"bytes"
	"context"
	"os"
	"os/exec"
)

func runCmd(dir, name string, args ...string) (string, error) {
	c := exec.CommandContext(context.Background(), name, args...)
	c.Dir = dir
	var b bytes.Buffer
	c.Stdout, c.Stderr = &b, &b
	return b.String(), c.Run()
}

const fakeHelperSource = `

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

var versionLine = "k-brain-computer/1"

type rpcErr struct {
	Code    int
	Message string
}

var handle = func(req map[string]any) (any, *rpcErr) {
	return map[string]any{"ok": true}, nil
}

func init() {}

func main() {
	fmt.Println(versionLine)
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 4<<20), 4<<20)
	enc := json.NewEncoder(os.Stdout)
	for sc.Scan() {
		var req map[string]any
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": nil, "error": map[string]any{"code": -32700, "message": err.Error()}})
			continue
		}
		if req["method"] == "handshake" {
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req["id"], "result": map[string]any{"version": versionLine}})
			continue
		}
		res, rerr := handle(req)
		if rerr != nil {
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req["id"], "error": map[string]any{"code": rerr.Code, "message": rerr.Message}})
			continue
		}
		_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req["id"], "result": res})
	}
}
`

var _ = os.Getenv
