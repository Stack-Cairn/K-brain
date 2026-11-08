package tools

import (
	"encoding/json"
	"strings"
)

// SSHManagerTool is the name of the native SSH tool.
const SSHManagerTool = "SSHManager"

// SSHManagerReadOnly reports the SSHManager actions that only read K-brain's
// own state (configured hosts, open sessions, buffered shell output,
// transfer progress): they open no connection and change nothing remote.
func SSHManagerReadOnly(action string) bool {
	switch strings.TrimSpace(action) {
	case "list_hosts", "list_sessions", "read_session", "sftp_transfer_status":
		return true
	}
	return false
}

// SSHManagerGateCommand is the command an SSHManager call is gated with:
// "<action> <host_id or session_id> <detail>", where detail is the remote
// command, input or paths the action works on. Permission rules match it as
// text, for example SSHManager(exec web-1:*). readOnly is true for the
// actions SSHManagerReadOnly lists.
func SSHManagerGateCommand(args json.RawMessage) (command string, readOnly bool) {
	var a struct {
		Action     string `json:"action"`
		HostID     string `json:"host_id"`
		SessionID  string `json:"session_id"`
		Command    string `json:"command"`
		Data       string `json:"data"`
		Path       string `json:"path"`
		FromPath   string `json:"from_path"`
		ToPath     string `json:"to_path"`
		LocalPath  string `json:"local_path"`
		RemotePath string `json:"remote_path"`
		TransferID string `json:"transfer_id"`
	}
	_ = json.Unmarshal(args, &a)
	action := strings.TrimSpace(a.Action)
	target := strings.TrimSpace(a.SessionID)
	if target == "" {
		target = strings.TrimSpace(a.HostID)
	}
	var detail string
	switch action {
	case "exec":
		detail = a.Command
	case "send_input":
		detail = a.Data
	case "sftp_rename":
		detail = strings.TrimSpace(a.FromPath) + " -> " + strings.TrimSpace(a.ToPath)
	case "sftp_upload":
		detail = strings.TrimSpace(a.LocalPath) + " -> " + strings.TrimSpace(a.RemotePath)
	case "sftp_download":
		detail = strings.TrimSpace(a.RemotePath) + " -> " + strings.TrimSpace(a.LocalPath)
	case "sftp_transfer_status", "sftp_cancel_transfer":
		detail = strings.TrimSpace(a.TransferID)
	default:
		detail = strings.TrimSpace(a.Path)
	}
	parts := []string{action}
	if target != "" {
		parts = append(parts, target)
	}
	if detail != "" {
		parts = append(parts, detail)
	}
	return strings.Join(parts, " "), SSHManagerReadOnly(action)
}
