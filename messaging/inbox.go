package messaging

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	DefaultInboxMaxFiles      = 10
	DefaultInboxMaxFileBytes  = int64(100 << 20)
	DefaultInboxMaxBatchBytes = int64(200 << 20)
	DefaultInboxTTL           = 30 * time.Minute
)

var (
	ErrInboxFileLimit   = errors.New("inbox attachment count limit reached")
	ErrInboxFileTooBig  = errors.New("inbox attachment exceeds size limit")
	ErrInboxBatchTooBig = errors.New("inbox batch exceeds size limit")
)

type InboxOptions struct {
	Root          string
	MaxFiles      int
	MaxFileBytes  int64
	MaxBatchBytes int64
	TTL           time.Duration
}

type InboxAttachment struct {
	ID         string    `json:"id"`
	MessageID  int64     `json:"message_id,omitempty"`
	Name       string    `json:"name"`
	FileName   string    `json:"file_name"`
	Kind       string    `json:"kind"`
	MIMEType   string    `json:"mime_type"`
	Size       int64     `json:"size"`
	SHA256     string    `json:"sha256"`
	ReceivedAt time.Time `json:"received_at"`
	Path       string    `json:"-"`
}

type inboxManifest struct {
	Version     int               `json:"version"`
	BatchID     string            `json:"batch_id"`
	State       string            `json:"state"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
	Attachments []InboxAttachment `json:"attachments"`
}

type InboxBatch struct {
	ID          string
	Dir         string
	Attachments []InboxAttachment
}

type Inbox struct {
	root          string
	maxFiles      int
	maxFileBytes  int64
	maxBatchBytes int64
	ttl           time.Duration
	now           func() time.Time
	locks         sync.Map
}

func NewInbox(options InboxOptions) (*Inbox, error) {
	if options.Root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve home directory: %w", err)
		}
		options.Root = filepath.Join(home, ".weclaw", "inbox")
	}
	if options.MaxFiles <= 0 {
		options.MaxFiles = DefaultInboxMaxFiles
	}
	if options.MaxFileBytes <= 0 {
		options.MaxFileBytes = DefaultInboxMaxFileBytes
	}
	if options.MaxBatchBytes <= 0 {
		options.MaxBatchBytes = DefaultInboxMaxBatchBytes
	}
	if options.TTL <= 0 {
		options.TTL = DefaultInboxTTL
	}

	root, err := filepath.Abs(options.Root)
	if err != nil {
		return nil, fmt.Errorf("resolve inbox directory: %w", err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create inbox directory: %w", err)
	}

	inbox := &Inbox{
		root:          root,
		maxFiles:      options.MaxFiles,
		maxFileBytes:  options.MaxFileBytes,
		maxBatchBytes: options.MaxBatchBytes,
		ttl:           options.TTL,
		now:           time.Now,
	}
	if err := inbox.recoverClaimedBatches(); err != nil {
		return nil, err
	}
	return inbox, nil
}

func (i *Inbox) Root() string        { return i.root }
func (i *Inbox) MaxFileBytes() int64 { return i.maxFileBytes }

func (i *Inbox) Queue(userID string, messageID int64, name, kind, mimeType string, data []byte) (*InboxAttachment, int, error) {
	if int64(len(data)) > i.maxFileBytes {
		return nil, 0, ErrInboxFileTooBig
	}
	unlock := i.lockConversation(userID)
	defer unlock()

	conversationDir := i.conversationDir(userID)
	if err := os.MkdirAll(conversationDir, 0o700); err != nil {
		return nil, 0, fmt.Errorf("create conversation inbox: %w", err)
	}
	i.cleanupExpiredLocked(conversationDir)

	pendingDir := filepath.Join(conversationDir, "pending")
	manifest, err := loadOrCreateManifest(pendingDir, i.now())
	if err != nil {
		return nil, 0, err
	}
	if len(manifest.Attachments) >= i.maxFiles {
		return nil, len(manifest.Attachments), ErrInboxFileLimit
	}
	var total int64
	for _, attachment := range manifest.Attachments {
		total += attachment.Size
	}
	if total+int64(len(data)) > i.maxBatchBytes {
		return nil, len(manifest.Attachments), ErrInboxBatchTooBig
	}

	if err := os.MkdirAll(pendingDir, 0o700); err != nil {
		return nil, len(manifest.Attachments), fmt.Errorf("create pending batch: %w", err)
	}
	originalName := sanitizeInboxFileName(name)
	fileName := uniqueFileName(pendingDir, originalName)
	finalPath := filepath.Join(pendingDir, fileName)
	partPath := finalPath + ".part"
	if err := os.WriteFile(partPath, data, 0o600); err != nil {
		return nil, len(manifest.Attachments), fmt.Errorf("write inbox attachment: %w", err)
	}
	if err := os.Rename(partPath, finalPath); err != nil {
		_ = os.Remove(partPath)
		return nil, len(manifest.Attachments), fmt.Errorf("commit inbox attachment: %w", err)
	}

	sum := sha256.Sum256(data)
	attachment := InboxAttachment{
		ID:         uuid.NewString(),
		MessageID:  messageID,
		Name:       originalName,
		FileName:   fileName,
		Kind:       kind,
		MIMEType:   mimeType,
		Size:       int64(len(data)),
		SHA256:     hex.EncodeToString(sum[:]),
		ReceivedAt: i.now().UTC(),
		Path:       finalPath,
	}
	manifest.Attachments = append(manifest.Attachments, attachment)
	manifest.UpdatedAt = i.now().UTC()
	if err := writeManifest(pendingDir, manifest); err != nil {
		_ = os.Remove(finalPath)
		return nil, len(manifest.Attachments) - 1, err
	}

	return &attachment, len(manifest.Attachments), nil
}

func (i *Inbox) Claim(userID string) (*InboxBatch, error) {
	unlock := i.lockConversation(userID)
	defer unlock()

	conversationDir := i.conversationDir(userID)
	if err := os.MkdirAll(conversationDir, 0o700); err != nil {
		return nil, err
	}
	i.cleanupExpiredLocked(conversationDir)

	candidates, err := filepath.Glob(filepath.Join(conversationDir, "retry-*"))
	if err != nil {
		return nil, err
	}
	sort.Strings(candidates)
	pendingDir := filepath.Join(conversationDir, "pending")
	if _, statErr := os.Stat(filepath.Join(pendingDir, "manifest.json")); statErr == nil {
		candidates = append(candidates, pendingDir)
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	sourceDir := candidates[0]
	manifest, err := readManifest(sourceDir)
	if err != nil {
		return nil, err
	}
	if len(manifest.Attachments) == 0 {
		_ = os.RemoveAll(sourceDir)
		return nil, nil
	}
	claimedDir := filepath.Join(conversationDir, "claimed-"+manifest.BatchID)
	if sourceDir != claimedDir {
		if err := os.Rename(sourceDir, claimedDir); err != nil {
			return nil, fmt.Errorf("claim inbox batch: %w", err)
		}
	}
	manifest.State = "claimed"
	manifest.UpdatedAt = i.now().UTC()
	for idx := range manifest.Attachments {
		manifest.Attachments[idx].Path = filepath.Join(claimedDir, manifest.Attachments[idx].FileName)
	}
	if err := writeManifest(claimedDir, manifest); err != nil {
		return nil, err
	}
	return &InboxBatch{ID: manifest.BatchID, Dir: claimedDir, Attachments: manifest.Attachments}, nil
}

func (i *Inbox) Complete(batch *InboxBatch) error {
	if batch == nil || !i.isWithinRoot(batch.Dir) {
		return nil
	}
	return os.RemoveAll(batch.Dir)
}

func (i *Inbox) Retry(batch *InboxBatch) error {
	if batch == nil || !i.isWithinRoot(batch.Dir) {
		return nil
	}
	manifest, err := readManifest(batch.Dir)
	if err != nil {
		return err
	}
	manifest.State = "retry"
	manifest.UpdatedAt = i.now().UTC()
	retryDir := filepath.Join(filepath.Dir(batch.Dir), "retry-"+batch.ID)
	if batch.Dir != retryDir {
		if err := os.Rename(batch.Dir, retryDir); err != nil {
			return err
		}
		batch.Dir = retryDir
	}
	return writeManifest(retryDir, manifest)
}

func (i *Inbox) List(userID string) ([]InboxAttachment, error) {
	unlock := i.lockConversation(userID)
	defer unlock()
	conversationDir := i.conversationDir(userID)
	i.cleanupExpiredLocked(conversationDir)

	dirs, err := os.ReadDir(conversationDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var attachments []InboxAttachment
	for _, entry := range dirs {
		if !entry.IsDir() || (!strings.HasPrefix(entry.Name(), "retry-") && entry.Name() != "pending") {
			continue
		}
		dir := filepath.Join(conversationDir, entry.Name())
		manifest, manifestErr := readManifest(dir)
		if manifestErr != nil {
			continue
		}
		for _, attachment := range manifest.Attachments {
			attachment.Path = filepath.Join(dir, attachment.FileName)
			attachments = append(attachments, attachment)
		}
	}
	sort.Slice(attachments, func(a, b int) bool { return attachments[a].ReceivedAt.Before(attachments[b].ReceivedAt) })
	return attachments, nil
}

func (i *Inbox) Clear(userID string) (int, error) {
	unlock := i.lockConversation(userID)
	defer unlock()
	conversationDir := i.conversationDir(userID)
	entries, err := os.ReadDir(conversationDir)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, entry := range entries {
		if !entry.IsDir() || (!strings.HasPrefix(entry.Name(), "retry-") && entry.Name() != "pending") {
			continue
		}
		dir := filepath.Join(conversationDir, entry.Name())
		if manifest, manifestErr := readManifest(dir); manifestErr == nil {
			removed += len(manifest.Attachments)
		}
		if err := os.RemoveAll(dir); err != nil {
			return removed, err
		}
	}
	return removed, nil
}

func (i *Inbox) conversationDir(userID string) string {
	sum := sha256.Sum256([]byte(userID))
	return filepath.Join(i.root, hex.EncodeToString(sum[:16]))
}

func (i *Inbox) lockConversation(userID string) func() {
	value, _ := i.locks.LoadOrStore(userID, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func (i *Inbox) cleanupExpiredLocked(conversationDir string) {
	entries, err := os.ReadDir(conversationDir)
	if err != nil {
		return
	}
	cutoff := i.now().Add(-i.ttl)
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), "claimed-") {
			continue
		}
		dir := filepath.Join(conversationDir, entry.Name())
		manifest, manifestErr := readManifest(dir)
		if manifestErr == nil && manifest.UpdatedAt.Before(cutoff) {
			_ = os.RemoveAll(dir)
		}
	}
}

func (i *Inbox) recoverClaimedBatches() error {
	conversations, err := os.ReadDir(i.root)
	if err != nil {
		return fmt.Errorf("scan inbox directory: %w", err)
	}
	for _, conversation := range conversations {
		if !conversation.IsDir() {
			continue
		}
		conversationDir := filepath.Join(i.root, conversation.Name())
		batches, readErr := os.ReadDir(conversationDir)
		if readErr != nil {
			return fmt.Errorf("scan conversation inbox: %w", readErr)
		}
		for _, batch := range batches {
			if !batch.IsDir() || !strings.HasPrefix(batch.Name(), "claimed-") {
				continue
			}
			sourceDir := filepath.Join(conversationDir, batch.Name())
			batchID := strings.TrimPrefix(batch.Name(), "claimed-")
			retryDir := filepath.Join(conversationDir, "retry-"+batchID)
			if _, statErr := os.Stat(retryDir); statErr == nil {
				continue
			}
			if renameErr := os.Rename(sourceDir, retryDir); renameErr != nil {
				return fmt.Errorf("recover claimed inbox batch: %w", renameErr)
			}
			if manifest, manifestErr := readManifest(retryDir); manifestErr == nil {
				manifest.State = "retry"
				manifest.UpdatedAt = i.now().UTC()
				if writeErr := writeManifest(retryDir, manifest); writeErr != nil {
					return writeErr
				}
			}
		}
	}
	return nil
}

func (i *Inbox) isWithinRoot(path string) bool {
	rel, err := filepath.Rel(i.root, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func loadOrCreateManifest(dir string, now time.Time) (*inboxManifest, error) {
	manifest, err := readManifest(dir)
	if err == nil {
		return manifest, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	return &inboxManifest{
		Version:   1,
		BatchID:   uuid.NewString(),
		State:     "pending",
		CreatedAt: now.UTC(),
		UpdatedAt: now.UTC(),
	}, nil
}

func readManifest(dir string) (*inboxManifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, err
	}
	var manifest inboxManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("parse inbox manifest: %w", err)
	}
	return &manifest, nil
}

func writeManifest(dir string, manifest *inboxManifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	partPath := filepath.Join(dir, "manifest.json.part")
	finalPath := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(partPath, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(partPath, finalPath); err != nil {
		// Windows does not replace an existing destination with os.Rename.
		if removeErr := os.Remove(finalPath); removeErr != nil && !os.IsNotExist(removeErr) {
			_ = os.Remove(partPath)
			return removeErr
		}
		if retryErr := os.Rename(partPath, finalPath); retryErr != nil {
			_ = os.Remove(partPath)
			return retryErr
		}
	}
	return nil
}

func sanitizeInboxFileName(name string) string {
	name = strings.TrimSpace(filepath.Base(strings.ReplaceAll(name, "\\", "/")))
	if name == "" || name == "." || name == ".." {
		return "attachment.bin"
	}
	var builder strings.Builder
	for _, r := range name {
		if r < 32 || strings.ContainsRune(`<>:"/\\|?*`, r) {
			builder.WriteRune('_')
		} else {
			builder.WriteRune(r)
		}
	}
	name = strings.Trim(builder.String(), " .")
	if name == "" {
		return "attachment.bin"
	}
	if len([]rune(name)) > 180 {
		runes := []rune(name)
		name = string(runes[:180])
	}
	return name
}

func uniqueFileName(dir, name string) string {
	if _, err := os.Stat(filepath.Join(dir, name)); os.IsNotExist(err) {
		return name
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for index := 2; ; index++ {
		candidate := fmt.Sprintf("%s-%d%s", base, index, ext)
		if _, err := os.Stat(filepath.Join(dir, candidate)); os.IsNotExist(err) {
			return candidate
		}
	}
}
