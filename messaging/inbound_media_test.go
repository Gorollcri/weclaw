package messaging

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/ilink"
)

func TestQueueInboundDirectImage(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(png)
	}))
	defer server.Close()

	inbox, err := NewInbox(InboxOptions{Root: t.TempDir(), TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	handler := newTestHandler()
	handler.SetInbox(inbox)
	msg := ilink.WeixinMessage{
		MessageID: 7, FromUserID: "user-a",
		ItemList: []ilink.MessageItem{{
			Type:      ilink.ItemTypeImage,
			ImageItem: &ilink.ImageItem{URL: server.URL, MidSize: len(png)},
		}},
	}
	queued, failures := handler.queueInboundMedia(context.Background(), msg)
	if len(failures) != 0 || len(queued) != 1 {
		t.Fatalf("queued=%+v failures=%+v", queued, failures)
	}
	if queued[0].MIMEType != "image/png" || queued[0].Name != "image-7-1.png" {
		t.Fatalf("unexpected queued image: %+v", queued[0])
	}
}

func TestInboundFileMetadata(t *testing.T) {
	spec, ok := inboundMediaFromItem(9, 0, ilink.MessageItem{
		Type:     ilink.ItemTypeFile,
		FileItem: &ilink.FileItem{FileName: "notes.pdf", Len: "123", Media: &ilink.MediaInfo{}},
	})
	if !ok || spec.name != "notes.pdf" || spec.declaredSize != 123 || spec.kind != "file" {
		t.Fatalf("unexpected file spec: %+v, ok=%v", spec, ok)
	}
}
