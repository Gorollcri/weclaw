package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"testing"
)

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

func TestSelectAllowPermissionOption(t *testing.T) {
	tests := []struct {
		name    string
		options []permissionOption
		want    string
		ok      bool
	}{
		{
			name: "prefers one shot grant",
			options: []permissionOption{
				{OptionID: "reject-once", Kind: "reject_once"},
				{OptionID: "always", Kind: "allow_always"},
				{OptionID: "once", Kind: "allow_once"},
			},
			want: "once",
			ok:   true,
		},
		{
			name:    "supports persistent grant",
			options: []permissionOption{{OptionID: "always", Kind: "allow_always"}},
			want:    "always",
			ok:      true,
		},
		{
			name:    "supports legacy allow",
			options: []permissionOption{{OptionID: "legacy", Kind: "allow"}},
			want:    "legacy",
			ok:      true,
		},
		{
			name:    "does not invent an option",
			options: []permissionOption{{OptionID: "reject", Kind: "reject_once"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := selectAllowPermissionOption(tt.options)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("selectAllowPermissionOption() = (%q, %v), want (%q, %v)", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestChatCancellationNotifiesACPServer(t *testing.T) {
	var output bytes.Buffer
	agent := NewACPAgent(ACPAgentConfig{})
	agent.started = true
	agent.cmd = &exec.Cmd{Process: &os.Process{Pid: 123}}
	agent.stdin = nopWriteCloser{Writer: &output}
	agent.sessions["conversation"] = "session-1"
	releasePrompt := make(chan struct{})
	agent.rpcCall = func(ctx context.Context, method string, _ interface{}) (json.RawMessage, error) {
		if method != "session/prompt" {
			t.Fatalf("unexpected RPC method %q", method)
		}
		<-releasePrompt
		return nil, ctx.Err()
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := agent.Chat(ctx, "conversation", "hello")
	close(releasePrompt)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Chat() error = %v, want context.Canceled", err)
	}

	var notification struct {
		Method string `json:"method"`
		Params struct {
			SessionID string `json:"sessionId"`
		} `json:"params"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &notification); err != nil {
		t.Fatalf("decode notification: %v", err)
	}
	if notification.Method != "session/cancel" || notification.Params.SessionID != "session-1" {
		t.Fatalf("unexpected notification: %+v", notification)
	}
}

func TestProcessExitSettlesPendingCalls(t *testing.T) {
	agent := NewACPAgent(ACPAgentConfig{})
	cmd := &exec.Cmd{}
	agent.cmd = cmd
	agent.started = true
	agent.sessions["conversation"] = "session-1"
	pending := make(chan *rpcResponse, 1)
	agent.pending[1] = pending

	agent.handleProcessExit(cmd)

	resp := <-pending
	if resp.Error == nil || resp.Error.Message != "ACP subprocess disconnected" {
		t.Fatalf("unexpected pending response: %+v", resp)
	}
	if agent.started {
		t.Fatal("agent remained started after process exit")
	}
	if len(agent.sessions) != 0 {
		t.Fatalf("sessions were not cleared: %+v", agent.sessions)
	}
}
