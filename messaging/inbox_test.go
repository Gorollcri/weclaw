package messaging

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInboxQueueClaimComplete(t *testing.T) {
	inbox, err := NewInbox(InboxOptions{Root: t.TempDir(), TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	attachment, count, err := inbox.Queue("user-a", 42, "report.pdf", "file", "application/pdf", []byte("%PDF-test"))
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || attachment.Name != "report.pdf" {
		t.Fatalf("unexpected queue result: count=%d attachment=%+v", count, attachment)
	}
	if _, err := os.Stat(attachment.Path); err != nil {
		t.Fatalf("queued file missing: %v", err)
	}
	if _, count, err := inbox.Queue("user-a", 43, "notes.txt", "file", "text/plain", []byte("notes")); err != nil || count != 2 {
		t.Fatalf("second queue failed: count=%d err=%v", count, err)
	}

	batch, err := inbox.Claim("user-a")
	if err != nil {
		t.Fatal(err)
	}
	if batch == nil || len(batch.Attachments) != 2 {
		t.Fatalf("unexpected claimed batch: %+v", batch)
	}
	if !strings.Contains(batch.Attachments[0].Path, "claimed-") {
		t.Fatalf("claimed path not rewritten: %s", batch.Attachments[0].Path)
	}
	if err := inbox.Complete(batch); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(batch.Dir); !os.IsNotExist(err) {
		t.Fatalf("consumed batch still exists: %v", err)
	}
}

func TestInboxRetryIsClaimedBeforeNewPending(t *testing.T) {
	inbox, err := NewInbox(InboxOptions{Root: t.TempDir(), TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := inbox.Queue("user-a", 1, "old.txt", "file", "text/plain", []byte("old")); err != nil {
		t.Fatal(err)
	}
	batch, err := inbox.Claim("user-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := inbox.Retry(batch); err != nil {
		t.Fatal(err)
	}
	if _, _, err := inbox.Queue("user-a", 2, "new.txt", "file", "text/plain", []byte("new")); err != nil {
		t.Fatal(err)
	}
	retried, err := inbox.Claim("user-a")
	if err != nil {
		t.Fatal(err)
	}
	if got := retried.Attachments[0].Name; got != "old.txt" {
		t.Fatalf("claimed %q, want oldest retry", got)
	}
}

func TestInboxRecoversClaimedBatchAfterRestart(t *testing.T) {
	root := t.TempDir()
	inbox, err := NewInbox(InboxOptions{Root: root, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := inbox.Queue("user-a", 1, "recover.txt", "file", "text/plain", []byte("recover")); err != nil {
		t.Fatal(err)
	}
	claimed, err := inbox.Claim("user-a")
	if err != nil || claimed == nil {
		t.Fatalf("claim failed: batch=%+v err=%v", claimed, err)
	}

	restarted, err := NewInbox(InboxOptions{Root: root, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := restarted.Claim("user-a")
	if err != nil || recovered == nil || recovered.Attachments[0].Name != "recover.txt" {
		t.Fatalf("restart recovery failed: batch=%+v err=%v", recovered, err)
	}
}

func TestInboxLimitsAndSanitizedName(t *testing.T) {
	inbox, err := NewInbox(InboxOptions{
		Root: t.TempDir(), MaxFiles: 1, MaxFileBytes: 4, MaxBatchBytes: 4, TTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	attachment, _, err := inbox.Queue("user-a", 1, "../bad?.txt", "file", "text/plain", []byte("1234"))
	if err != nil {
		t.Fatal(err)
	}
	if attachment.Name != "bad_.txt" || filepath.Base(attachment.Path) != "bad_.txt" {
		t.Fatalf("unsafe filename was not sanitized: %+v", attachment)
	}
	if _, _, err := inbox.Queue("user-a", 2, "second.txt", "file", "text/plain", []byte("x")); !errors.Is(err, ErrInboxFileLimit) {
		t.Fatalf("got %v, want file limit", err)
	}
	if _, _, err := inbox.Queue("user-b", 3, "large.txt", "file", "text/plain", []byte("12345")); !errors.Is(err, ErrInboxFileTooBig) {
		t.Fatalf("got %v, want file-size limit", err)
	}
}

func TestInboxExpiresPendingBatch(t *testing.T) {
	inbox, err := NewInbox(InboxOptions{Root: t.TempDir(), TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	inbox.now = func() time.Time { return now }
	if _, _, err := inbox.Queue("user-a", 1, "old.txt", "file", "text/plain", []byte("old")); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	attachments, err := inbox.List("user-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(attachments) != 0 {
		t.Fatalf("expired attachments still listed: %+v", attachments)
	}
}

func TestAttachPendingFilesIncludesSafeInstructions(t *testing.T) {
	inbox, err := NewInbox(InboxOptions{Root: t.TempDir(), TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := inbox.Queue("user-a", 1, "sample.txt", "file", "text/plain", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	handler := newTestHandler()
	handler.SetInbox(inbox)
	prompt, batch, err := handler.attachPendingFiles("user-a", "summarize it")
	if err != nil {
		t.Fatal(err)
	}
	if batch == nil || !strings.Contains(prompt, "summarize it") || !strings.Contains(prompt, "do not execute") || !strings.Contains(prompt, "sample.txt") {
		t.Fatalf("unexpected attachment prompt: %s", prompt)
	}
}
