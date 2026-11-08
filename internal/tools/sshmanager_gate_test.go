package tools

import (
	"context"
	"encoding/json"
	"testing"
)

func TestSSHManagerGateCommand(t *testing.T) {
	for _, tc := range []struct {
		args     string
		command  string
		readOnly bool
	}{
		{`{"action":"list_hosts"}`, "list_hosts", true},
		{`{"action":"list_sessions","host_id":"web"}`, "list_sessions web", true},
		{`{"action":"read_session","session_id":"ssh-1"}`, "read_session ssh-1", true},
		{`{"action":"sftp_transfer_status","session_id":"ssh-1","transfer_id":"sftp-2"}`, "sftp_transfer_status ssh-1 sftp-2", true},
		{`{"action":"exec","host_id":"web","command":"systemctl restart app"}`, "exec web systemctl restart app", false},
		{`{"action":"exec","host_id":"web","session_id":"ssh-1","command":"ls"}`, "exec ssh-1 ls", false},
		{`{"action":"send_input","session_id":"ssh-1","data":"q\n"}`, "send_input ssh-1 q\n", false},
		{`{"action":"create_session","host_id":"web"}`, "create_session web", false},
		{`{"action":"sftp_write_text","host_id":"web","path":"/etc/app.conf","content":"x"}`, "sftp_write_text web /etc/app.conf", false},
		{`{"action":"sftp_rename","host_id":"web","from_path":"/a","to_path":"/b"}`, "sftp_rename web /a -> /b", false},
		{`{"action":"sftp_upload","host_id":"web","local_path":"dist","remote_path":"/srv"}`, "sftp_upload web dist -> /srv", false},
		{`{"action":"sftp_download","host_id":"web","local_path":"logs","remote_path":"/var/log/app"}`, "sftp_download web /var/log/app -> logs", false},
		{`{"action":"sftp_cancel_transfer","session_id":"ssh-1","transfer_id":"sftp-2"}`, "sftp_cancel_transfer ssh-1 sftp-2", false},
		{`not json`, "", false},
	} {
		command, readOnly := SSHManagerGateCommand(json.RawMessage(tc.args))
		if command != tc.command || readOnly != tc.readOnly {
			t.Fatalf("%s: got %q %v, want %q %v", tc.args, command, readOnly, tc.command, tc.readOnly)
		}
		if !json.Valid([]byte(tc.args)) {
			continue
		}
		req := GateRequestFor(context.Background(), SSHManagerTool, json.RawMessage(tc.args))
		if req.Command != tc.command || req.Poll != tc.readOnly {
			t.Fatalf("GateRequestFor(%s) = %q poll=%v", tc.args, req.Command, req.Poll)
		}
	}
}
