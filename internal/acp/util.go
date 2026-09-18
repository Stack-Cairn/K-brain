package acp

import (
	"crypto/rand"
	"encoding/hex"
	"log"

	acp "github.com/coder/acp-go-sdk"
)

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "acp_" + hex.EncodeToString(b[:])
}

func config_logf(format string, args ...any) {
	log.Printf("kn acp: "+format, args...)
	if eventLogf != nil {
		eventLogf(format, args...)
	}
}

var eventLogf func(format string, args ...any)

func SetEventLog(f func(format string, args ...any)) { eventLogf = f }

func updateThoughtText(delta string) acp.SessionUpdate {
	return acp.SessionUpdate{AgentThoughtChunk: &acp.SessionUpdateAgentThoughtChunk{
		SessionUpdate: "agent_thought_chunk",
		Content:       acp.TextBlock(delta),
	}}
}
