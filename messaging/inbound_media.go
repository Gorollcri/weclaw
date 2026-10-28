package messaging

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/ilink"
)

type inboundMedia struct {
	name         string
	kind         string
	declaredSize int64
	url          string
	media        *ilink.MediaInfo
}

func (h *Handler) queueInboundMedia(ctx context.Context, msg ilink.WeixinMessage) ([]InboxAttachment, []error) {
	if h.inbox == nil {
		return nil, nil
	}
	var queued []InboxAttachment
	var failures []error
	for index, item := range msg.ItemList {
		spec, ok := inboundMediaFromItem(msg.MessageID, index, item)
		if !ok {
			continue
		}
		if spec.declaredSize > h.inbox.MaxFileBytes() {
			failures = append(failures, fmt.Errorf("%s: %w", spec.name, ErrInboxFileTooBig))
			continue
		}

		data, contentType, err := downloadInboundMedia(ctx, spec, h.inbox.MaxFileBytes())
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", spec.name, err))
			continue
		}
		contentType = detectInboundContentType(spec.name, data, contentType)
		spec.name = ensureUsefulExtension(spec.name, spec.kind, contentType)
		attachment, count, err := h.inbox.Queue(msg.FromUserID, msg.MessageID, spec.name, spec.kind, contentType, data)
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", spec.name, err))
			continue
		}
		queued = append(queued, *attachment)
		logQueuedAttachment(msg.FromUserID, attachment, count)
	}
	return queued, failures
}

func inboundMediaFromItem(messageID int64, index int, item ilink.MessageItem) (inboundMedia, bool) {
	base := fmt.Sprintf("%d-%d", messageID, index+1)
	switch item.Type {
	case ilink.ItemTypeImage:
		if item.ImageItem == nil {
			return inboundMedia{}, false
		}
		return inboundMedia{
			name:         "image-" + base,
			kind:         "image",
			declaredSize: int64(item.ImageItem.MidSize),
			url:          item.ImageItem.URL,
			media:        item.ImageItem.Media,
		}, true
	case ilink.ItemTypeVoice:
		if item.VoiceItem == nil {
			return inboundMedia{}, false
		}
		return inboundMedia{
			name:         "voice-" + base + voiceExtension(item.VoiceItem.EncodeType),
			kind:         "audio",
			declaredSize: int64(item.VoiceItem.VoiceSize),
			media:        item.VoiceItem.Media,
		}, true
	case ilink.ItemTypeVideo:
		if item.VideoItem == nil {
			return inboundMedia{}, false
		}
		return inboundMedia{
			name:         "video-" + base + ".mp4",
			kind:         "video",
			declaredSize: int64(item.VideoItem.VideoSize),
			media:        item.VideoItem.Media,
		}, true
	case ilink.ItemTypeFile:
		if item.FileItem == nil {
			return inboundMedia{}, false
		}
		size, _ := strconv.ParseInt(item.FileItem.Len, 10, 64)
		name := item.FileItem.FileName
		if strings.TrimSpace(name) == "" {
			name = "file-" + base + ".bin"
		}
		return inboundMedia{
			name:         name,
			kind:         "file",
			declaredSize: size,
			media:        item.FileItem.Media,
		}, true
	default:
		return inboundMedia{}, false
	}
}

func downloadInboundMedia(ctx context.Context, spec inboundMedia, maxBytes int64) ([]byte, string, error) {
	if spec.url != "" {
		return downloadFileLimited(ctx, spec.url, maxBytes)
	}
	if spec.media == nil || spec.media.EncryptQueryParam == "" || spec.media.AESKey == "" {
		return nil, "", errors.New("media download credentials are missing")
	}
	data, err := DownloadFileFromCDNWithLimit(ctx, spec.media.EncryptQueryParam, spec.media.AESKey, maxBytes)
	return data, "", err
}

func downloadFileLimited(ctx context.Context, rawURL string, maxBytes int64) ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	reader := io.Reader(resp.Body)
	if maxBytes > 0 {
		reader = io.LimitReader(resp.Body, maxBytes+1)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, "", err
	}
	if maxBytes > 0 && int64(len(data)) > maxBytes {
		return nil, "", ErrInboxFileTooBig
	}
	return data, resp.Header.Get("Content-Type"), nil
}

func detectInboundContentType(name string, data []byte, header string) string {
	if header != "" {
		if mediaType, _, err := mime.ParseMediaType(header); err == nil && mediaType != "application/octet-stream" {
			return mediaType
		}
	}
	detected := http.DetectContentType(data)
	if detected != "application/octet-stream" {
		return detected
	}
	if byExtension := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); byExtension != "" {
		if mediaType, _, err := mime.ParseMediaType(byExtension); err == nil {
			return mediaType
		}
	}
	return "application/octet-stream"
}

func ensureUsefulExtension(name, kind, contentType string) string {
	if filepath.Ext(name) != "" {
		return name
	}
	extensions := map[string]string{
		"image/jpeg": ".jpg", "image/png": ".png", "image/gif": ".gif", "image/webp": ".webp",
		"application/pdf": ".pdf", "audio/mpeg": ".mp3", "audio/wav": ".wav", "video/mp4": ".mp4",
	}
	if extension := extensions[contentType]; extension != "" {
		return name + extension
	}
	switch kind {
	case "image":
		return name + ".img"
	case "audio":
		return name + ".audio"
	case "video":
		return name + ".video"
	default:
		return name + ".bin"
	}
}

func voiceExtension(encodeType int) string {
	switch encodeType {
	case 1:
		return ".pcm"
	case 5:
		return ".amr"
	case 6:
		return ".silk"
	case 7:
		return ".mp3"
	default:
		return ".audio"
	}
}

func logQueuedAttachment(userID string, attachment *InboxAttachment, count int) {
	userHash := sha256Short(userID)
	log.Printf("[handler] queued attachment (conversation=%s, name=%q, kind=%s, mime=%s, bytes=%d, count=%d)",
		userHash, attachment.Name, attachment.Kind, attachment.MIMEType, attachment.Size, count)
}

func sha256Short(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", sum[:6])
}
