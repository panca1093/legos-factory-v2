package usecase

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"legos-factory/backend/internal/model"
)

const (
	// the provider measures images on their base64 form and takes 32 MB of it per request
	maxChatImageFile    = 3_932_160
	maxChatPDFFile      = 18 << 20
	maxChatTextFile     = 1 << 20
	maxChatMessageFiles = 10
	maxChatMessageBytes = 18 << 20
	chatReplayBudget    = 10 << 20
	maxChatPending      = 20
	maxChatFileName     = 200
	unattachedFileTTL   = 24 * time.Hour
)

const (
	errOfficeFile = "Word, Excel and PowerPoint files are not supported yet. Export as PDF or text."
	errOtherFile  = "Only images (PNG, JPEG, GIF, WebP), PDF and text files can be attached."
)

var fileIDPattern = regexp.MustCompile(`^cf_[0-9a-f]{16}$`)

// sniffFile names a file's kind and media type from its bytes alone; the client's name and MIME never count.
func sniffFile(ra io.ReaderAt, size int64) (kind, mediaType string, err error) {
	if size == 0 {
		return "", "", fmt.Errorf("%w: the file is empty.", ErrInvalid)
	}
	head := make([]byte, min(size, 16))
	if _, err := ra.ReadAt(head, 0); err != nil {
		return "", "", err
	}
	switch {
	case imageType(head) != "":
		return "image", imageType(head), nil
	case bytes.HasPrefix(head, []byte("%PDF-")):
		return "pdf", "application/pdf", nil
	case bytes.HasPrefix(head, []byte("PK\x03\x04")):
		if zr, zerr := zip.NewReader(ra, size); zerr == nil {
			for _, f := range zr.File {
				if f.Name == "[Content_Types].xml" {
					return "", "", fmt.Errorf("%w: %s", ErrInvalid, errOfficeFile)
				}
			}
		}
	default:
		b := make([]byte, size)
		if _, err := ra.ReadAt(b, 0); err != nil && err != io.EOF {
			return "", "", err
		}
		if utf8.Valid(b) && !bytes.ContainsRune(b, 0) {
			return "text", "text/plain; charset=utf-8", nil
		}
	}
	return "", "", fmt.Errorf("%w: %s", ErrInvalid, errOtherFile)
}

// headLimit is the size cap for a file that starts with head; a zip gets the largest so an Office file is named as one.
func headLimit(head []byte) int {
	switch {
	case imageType(head) != "":
		return maxChatImageFile
	case bytes.HasPrefix(head, []byte("%PDF-")), bytes.HasPrefix(head, []byte("PK\x03\x04")):
		return maxChatPDFFile
	}
	return maxChatTextFile
}

func sizeLabel(limit int) string {
	if limit == maxChatImageFile {
		return "3.75 MB"
	}
	return fmt.Sprintf("%d MB", limit>>20)
}

func cleanFileName(name string) string {
	name = filepath.Base(strings.ReplaceAll(strings.TrimSpace(name), "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if name == "." || name == ".." || name == "/" || name == "" {
		return "file"
	}
	if utf8.RuneCountInString(name) > maxChatFileName {
		name = string([]rune(name)[:maxChatFileName])
	}
	return name
}

func (c *Chat) filePath(chatID, fileID string) string {
	return filepath.Join(c.DataDir, "chat-files", chatID, fileID)
}

// agentMediaType is the media type without parameters, as agents expect it.
func agentMediaType(mt string) string {
	mt, _, _ = strings.Cut(mt, ";")
	return mt
}

// Upload streams one file of the user's chat to disk, deciding its kind from its bytes and enforcing
// the kind's size cap while copying. It is not attached to anything yet.
func (c *Chat) Upload(ctx context.Context, u *model.User, chatID, name string, r io.Reader) (*model.ChatFileRec, error) {
	if _, err := c.own(ctx, u, chatID); err != nil {
		return nil, err
	}
	if c.DataDir == "" {
		return nil, errors.New("file storage is not configured")
	}
	name = cleanFileName(name)
	dir := filepath.Join(c.DataDir, "chat-files", chatID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	head := make([]byte, 16)
	n, err := io.ReadFull(r, head)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, fmt.Errorf("%w: the upload could not be read: %w", ErrInvalid, err)
	}
	head = head[:n]
	limit := headLimit(head)
	sum := sha256.New()
	size, err := io.Copy(io.MultiWriter(tmp, sum), io.MultiReader(bytes.NewReader(head), io.LimitReader(r, int64(limit)+1-int64(n))))
	if err != nil {
		return nil, fmt.Errorf("%w: the upload could not be read: %w", ErrInvalid, err)
	}
	if size > int64(limit) {
		return nil, c.overLimit(tmp, name, limit)
	}
	kind, mediaType, err := sniffFile(tmp, size)
	if err != nil {
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		return nil, err
	}
	idb := make([]byte, 8)
	if _, err := rand.Read(idb); err != nil {
		return nil, err
	}
	rec := &model.ChatFileRec{
		ID: "cf_" + hex.EncodeToString(idb), ChatID: chatID, UserID: u.ID, Name: name,
		MediaType: mediaType, Kind: kind, Size: int(size), SHA256: hex.EncodeToString(sum.Sum(nil)), CreatedAt: time.Now(),
	}
	// the row goes first: a crash before the rename leaves a row the orphan sweep removes, never bytes nobody tracks
	if err := c.Store.AddFile(ctx, rec, maxChatPending); err != nil {
		if errors.Is(err, model.ErrChatTooManyFiles) {
			return nil, fmt.Errorf("%w: Too many files waiting to be sent in this chat. Send or remove some first.", ErrInvalid)
		}
		if errors.Is(err, model.ErrChatNotFound) { // deleted while this upload ran
			c.removeChatFiles(chatID)
		}
		return nil, err
	}
	if err := os.Rename(tmp.Name(), c.filePath(chatID, rec.ID)); err != nil {
		_, _ = c.Store.DeleteFile(ctx, chatID, rec.ID)
		return nil, err
	}
	if _, err := c.Store.Get(ctx, chatID); errors.Is(err, model.ErrChatNotFound) { // deleted while this upload ran
		c.removeChatFiles(chatID)
		return nil, model.ErrChatNotFound
	}
	return rec, nil
}

// overLimit explains a file that passed limit: too big when it is what its first bytes say, otherwise not a supported type.
func (c *Chat) overLimit(f *os.File, name string, limit int) error {
	if limit == maxChatTextFile {
		// the cut can fall inside a multi-byte character
		for trim := 0; trim <= 3; trim++ {
			if kind, _, err := sniffFile(io.NewSectionReader(f, 0, int64(limit-trim)), int64(limit-trim)); err == nil && kind == "text" {
				return fmt.Errorf("%w: %s is over %s.", ErrInvalid, name, sizeLabel(limit))
			}
		}
		return fmt.Errorf("%w: %s", ErrInvalid, errOtherFile)
	}
	return fmt.Errorf("%w: %s is over %s.", ErrInvalid, name, sizeLabel(limit))
}

// ownFile loads a file of the user's own chat; anyone else's is not found.
func (c *Chat) ownFile(ctx context.Context, u *model.User, chatID, fileID string) (*model.ChatFileRec, error) {
	if _, err := c.own(ctx, u, chatID); err != nil {
		return nil, err
	}
	f, err := c.Store.File(ctx, chatID, fileID)
	if err != nil {
		return nil, err
	}
	if f.UserID != u.ID {
		return nil, model.ErrChatFileNotFound
	}
	return f, nil
}

// OpenFile opens an uploaded file's bytes; the caller closes them.
func (c *Chat) OpenFile(ctx context.Context, u *model.User, chatID, fileID string) (*model.ChatFileRec, *os.File, error) {
	rec, err := c.ownFile(ctx, u, chatID, fileID)
	if err != nil {
		return nil, nil, err
	}
	f, err := os.Open(c.filePath(chatID, rec.ID))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, model.ErrChatFileNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	return rec, f, nil
}

// RemoveFile deletes an uploaded file that no message carries yet.
func (c *Chat) RemoveFile(ctx context.Context, u *model.User, chatID, fileID string) error {
	rec, err := c.ownFile(ctx, u, chatID, fileID)
	if err != nil {
		return err
	}
	if rec.MessageID != nil {
		return fmt.Errorf("%w: this file is part of a message and cannot be removed", model.ErrStateConflict)
	}
	ok, err := c.Store.DeleteFile(ctx, chatID, fileID)
	if err != nil {
		return err
	}
	if !ok { // a message took it between the read and the delete
		return fmt.Errorf("%w: this file is part of a message and cannot be removed", model.ErrStateConflict)
	}
	_ = os.Remove(c.filePath(chatID, rec.ID))
	return nil
}

// sendFiles resolves a message's file ids against the user's chat and reads their bytes; each id must be
// the chat's own, unattached and distinct. Call it with the chat locked, so no other send can claim them.
func (c *Chat) sendFiles(ctx context.Context, u *model.User, chatID string, ids []string) ([]model.ChatFileRec, []model.AgentAttachment, error) {
	if len(ids) == 0 {
		return nil, nil, nil
	}
	if len(ids) > maxChatMessageFiles {
		return nil, nil, fmt.Errorf("%w: Up to %d files per message.", ErrInvalid, maxChatMessageFiles)
	}
	var recs []model.ChatFileRec
	var atts []model.AgentAttachment
	seen, total := map[string]bool{}, 0
	for _, id := range ids {
		f, err := c.Store.File(ctx, chatID, id)
		if err != nil || f.UserID != u.ID || f.MessageID != nil || seen[id] {
			if err != nil && !errors.Is(err, model.ErrChatFileNotFound) {
				return nil, nil, err
			}
			return nil, nil, fmt.Errorf("%w: an attached file is unknown or already sent; upload it again", ErrInvalid)
		}
		seen[id] = true
		if total += f.Size; total > maxChatMessageBytes {
			return nil, nil, fmt.Errorf("%w: Attachments can total %d MB at most.", ErrInvalid, maxChatMessageBytes>>20)
		}
		data, err := os.ReadFile(c.filePath(chatID, f.ID))
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, fmt.Errorf("%w: %s is no longer available; upload it again", ErrInvalid, f.Name)
		}
		if err != nil {
			return nil, nil, err
		}
		recs = append(recs, *f)
		atts = append(atts, model.AgentAttachment{Name: f.Name, MediaType: agentMediaType(f.MediaType), Data: data})
	}
	return recs, atts, nil
}

// replayFiles re-reads the files of the recent user messages for a session that has no memory of them:
// newest first, skipping any that would pass budget, are gone from disk, or belong to a turn whose reply failed.
func (c *Chat) replayFiles(chatID string, history []model.ChatMessage, budget int) []model.AgentAttachment {
	if len(history) > historyMessages {
		history = history[len(history)-historyMessages:]
	}
	var out []model.AgentAttachment
	spent := 0
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role != "user" || (i+1 < len(history) && history[i+1].Error != "") {
			continue
		}
		for _, a := range history[i].Attachments {
			if !fileIDPattern.MatchString(a.FileID) || spent+a.Size > budget {
				continue
			}
			data, err := os.ReadFile(c.filePath(chatID, a.FileID))
			if err != nil {
				continue
			}
			spent += a.Size
			out = append(out, model.AgentAttachment{Name: a.Name, MediaType: agentMediaType(a.MediaType), Data: data})
		}
	}
	return out
}

func replayNote(atts []model.AgentAttachment) string {
	if len(atts) == 0 {
		return ""
	}
	names := make([]string, len(atts))
	for i, a := range atts {
		names[i] = a.Name
	}
	return "Files attached earlier in this conversation, newest first, are included with this message: " + strings.Join(names, ", ") + ".\n\n"
}

// removeChatFiles drops a chat's file directory once the chat itself is gone.
func (c *Chat) removeChatFiles(chatID string) {
	if c.DataDir != "" {
		_ = os.RemoveAll(filepath.Join(c.DataDir, "chat-files", chatID))
	}
}

// CollectFiles removes uploads nobody sent within a day and directories of chats that no longer exist; run it at startup.
func (c *Chat) CollectFiles(ctx context.Context) error {
	if c.DataDir == "" {
		return nil
	}
	var first error
	stale, err := c.Store.StaleFiles(ctx, time.Now().Add(-unattachedFileTTL))
	if err != nil {
		first = err
	}
	for _, f := range stale {
		if ok, err := c.Store.DeleteFile(ctx, f.ChatID, f.ID); err != nil {
			first = err
		} else if ok {
			_ = os.Remove(c.filePath(f.ChatID, f.ID))
		}
	}
	root := filepath.Join(c.DataDir, "chat-files")
	dirs, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return first
		}
		return err
	}
	for _, d := range dirs {
		if _, err := c.Store.Get(ctx, d.Name()); errors.Is(err, model.ErrChatNotFound) {
			_ = os.RemoveAll(filepath.Join(root, d.Name()))
		} else if err != nil {
			first = err
		} else {
			c.removeStaleTemps(filepath.Join(root, d.Name()))
		}
	}
	return first
}

// removeStaleTemps deletes half-written uploads a crash left behind.
func (c *Chat) removeStaleTemps(dir string) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if info, err := e.Info(); err == nil && strings.HasPrefix(e.Name(), ".upload-") && time.Since(info.ModTime()) > unattachedFileTTL {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
