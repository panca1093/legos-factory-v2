package usecase

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"legos-factory/backend/internal/model"
)

func pdfBytes(n int) []byte { return append([]byte("%PDF-1.7\n"), make([]byte, n)...) }

func zipWith(t *testing.T, names ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, n := range names {
		w, _ := zw.Create(n)
		w.Write([]byte("<x/>"))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func (r *chatRig) upload(t *testing.T, u *model.User, chatID, name string, data []byte) (*model.ChatFileRec, error) {
	t.Helper()
	return r.chat.Upload(context.Background(), u, chatID, name, bytes.NewReader(data))
}

func (r *chatRig) mustUpload(t *testing.T, chatID, name string, data []byte) *model.ChatFileRec {
	t.Helper()
	f, err := r.upload(t, r.ana, chatID, name, data)
	if err != nil {
		t.Fatalf("upload %s: %v", name, err)
	}
	return f
}

func TestSniffFile(t *testing.T) {
	cases := []struct {
		name     string
		data     []byte
		kind, mt string
		errPart  string
	}{
		{"png", pngBytes, "image", "image/png", ""},
		{"jpeg", []byte{0xFF, 0xD8, 0xFF, 0xE0, 1}, "image", "image/jpeg", ""},
		{"gif", []byte("GIF89a.."), "image", "image/gif", ""},
		{"webp", append(append([]byte("RIFF\x00\x00\x00\x00"), "WEBP"...), 'x'), "image", "image/webp", ""},
		{"pdf", pdfBytes(4), "pdf", "application/pdf", ""},
		{"text", []byte("package main\n"), "text", "text/plain; charset=utf-8", ""},
		{"utf8 with bom", append([]byte("\xef\xbb\xbf"), "a,b\n1,2"...), "text", "text/plain; charset=utf-8", ""},
		{"unicode text", []byte("héllo ✓"), "text", "text/plain; charset=utf-8", ""},
		{"docx", zipWith(t, "[Content_Types].xml", "word/document.xml"), "", "", "Word, Excel and PowerPoint files are not supported yet. Export as PDF or text."},
		{"plain zip", zipWith(t, "a.txt"), "", "", "Only images (PNG, JPEG, GIF, WebP), PDF and text files can be attached."},
		{"random binary", []byte{0x00, 0x01, 0x02, 0xFE, 0xFF}, "", "", "Only images"},
		{"nul in text", []byte("ab\x00cd"), "", "", "Only images"},
		{"invalid utf8", []byte("\xff\xfe"), "", "", "Only images"},
		{"heic", append([]byte("\x00\x00\x00\x18ftypheic"), make([]byte, 20)...), "", "", "Only images"},
		{"empty", nil, "", "", "empty"},
	}
	for _, c := range cases {
		kind, mt, err := sniffFile(bytes.NewReader(c.data), int64(len(c.data)))
		if c.errPart != "" {
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), c.errPart) {
				t.Errorf("%s: err=%v", c.name, err)
			}
			continue
		}
		if err != nil || kind != c.kind || mt != c.mt {
			t.Errorf("%s: got %q %q %v", c.name, kind, mt, err)
		}
	}
}

func TestChatUploadStoresByIDAndIgnoresNameAndMime(t *testing.T) {
	ctx := context.Background()
	r := newChatRig(t, &chatPlain{reply: "ok"})
	ch, _ := r.chat.Create(ctx, r.ana, "", "", "")
	f := r.mustUpload(t, ch.ID, "../../etc/pass\x07wd.pdf", pngBytes)
	if f.Name != "pass"+"wd.pdf" || f.Kind != "image" || f.MediaType != "image/png" || f.Size != len(pngBytes) || !strings.HasPrefix(f.ID, "cf_") {
		t.Fatalf("rec: %+v", f)
	}
	ents, _ := os.ReadDir(filepath.Join(r.chat.DataDir, "chat-files", ch.ID))
	if len(ents) != 1 || ents[0].Name() != f.ID {
		t.Fatalf("directory holds %v, want only %s", ents, f.ID)
	}
	if got := r.mustUpload(t, ch.ID, "..", []byte("x")); got.Name != "file" {
		t.Fatalf("dot-dot name: %q", got.Name)
	}
	if got := r.mustUpload(t, ch.ID, strings.Repeat("é", 300), []byte("x")); len([]rune(got.Name)) != maxChatFileName {
		t.Fatalf("long name: %d runes", len([]rune(got.Name)))
	}
}

func TestChatUploadLimits(t *testing.T) {
	ctx := context.Background()
	r := newChatRig(t, &chatPlain{reply: "ok"})
	ch, _ := r.chat.Create(ctx, r.ana, "", "", "")
	over := func(name string, data []byte, want string) {
		t.Helper()
		if _, err := r.upload(t, r.ana, ch.ID, name, data); !errors.Is(err, ErrInvalid) || !strings.HasSuffix(err.Error(), want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	img := func(n int) []byte { return append(append([]byte{}, pngBytes...), make([]byte, n-len(pngBytes))...) }
	over("big.png", img(maxChatImageFile+1), "big.png is over 3.75 MB.")
	over("big.pdf", pdfBytes(maxChatPDFFile), "big.pdf is over 18 MB.")
	over("big.docx", append(zipWith(t, "[Content_Types].xml"), make([]byte, maxChatPDFFile)...), "big.docx is over 18 MB.")
	over("big.bin", bytes.Repeat([]byte{0, 1, 2}, maxChatTextFile), "Only images (PNG, JPEG, GIF, WebP), PDF and text files can be attached.")
	over("cut.txt", bytes.Repeat([]byte("é"), maxChatTextFile), "cut.txt is over 1 MB.")
	over("big.txt", bytes.Repeat([]byte("a"), maxChatTextFile+1), "big.txt is over 1 MB.")
	r.mustUpload(t, ch.ID, "ok.png", img(maxChatImageFile))
	r.mustUpload(t, ch.ID, "ok.txt", bytes.Repeat([]byte("a"), maxChatTextFile))
	r.mustUpload(t, ch.ID, "ok.pdf", pdfBytes(maxChatPDFFile-9))
}

func TestChatUploadCapsPendingFiles(t *testing.T) {
	ctx := context.Background()
	r := newChatRig(t, &chatPlain{reply: "ok"})
	ch, _ := r.chat.Create(ctx, r.ana, "", "", "")
	other, _ := r.chat.Create(ctx, r.ana, "", "", "")
	var first *model.ChatFileRec
	for i := 0; i < maxChatPending; i++ {
		f := r.mustUpload(t, ch.ID, fmt.Sprintf("%d.txt", i), []byte("x"))
		if first == nil {
			first = f
		}
	}
	_, err := r.upload(t, r.ana, ch.ID, "21.txt", []byte("x"))
	if !errors.Is(err, ErrInvalid) || !strings.HasSuffix(err.Error(), "Too many files waiting to be sent in this chat. Send or remove some first.") {
		t.Fatalf("21st: %v", err)
	}
	if ents, _ := os.ReadDir(filepath.Join(r.chat.DataDir, "chat-files", ch.ID)); len(ents) != maxChatPending {
		t.Fatalf("a refused upload must leave no bytes: %d entries", len(ents))
	}
	r.mustUpload(t, other.ID, "o.txt", []byte("x"))
	if _, err := r.send(t, r.ana, ch.ID, ChatSend{Content: "q", FileIDs: []string{first.ID}}); err != nil {
		t.Fatal(err)
	}
	r.mustUpload(t, ch.ID, "after.txt", []byte("x"))
}

type racingStore struct {
	ChatStore
	deleteBefore bool
}

func (s racingStore) AddFile(ctx context.Context, f *model.ChatFileRec, max int) error {
	if s.deleteBefore {
		_ = s.ChatStore.Delete(ctx, f.ChatID)
	}
	err := s.ChatStore.AddFile(ctx, f, max)
	if !s.deleteBefore && err == nil {
		_ = s.ChatStore.Delete(ctx, f.ChatID)
	}
	return err
}

func TestChatUploadRacingChatDelete(t *testing.T) {
	ctx := context.Background()
	r := newChatRig(t, &chatPlain{reply: "ok"})
	real := r.chat.Store
	for _, before := range []bool{true, false} {
		ch, _ := r.chat.Create(ctx, r.ana, "", "", "")
		r.chat.Store = racingStore{real, before}
		if _, err := r.upload(t, r.ana, ch.ID, "a.txt", []byte("x")); !errors.Is(err, model.ErrChatNotFound) {
			t.Errorf("deleteBefore=%v: %v", before, err)
		}
		if _, err := os.Stat(filepath.Join(r.chat.DataDir, "chat-files", ch.ID)); !os.IsNotExist(err) {
			t.Errorf("deleteBefore=%v: directory of a deleted chat must not stay: %v", before, err)
		}
	}
}

func TestChatStoreAddFileMapsMissingChat(t *testing.T) {
	r := newChatRig(t, &chatPlain{reply: "ok"})
	f := &model.ChatFileRec{ID: "cf_1", ChatID: "chat_missing", UserID: "ana", Name: "a", MediaType: "text/plain", Kind: "text", CreatedAt: time.Now()}
	if err := r.chat.Store.AddFile(context.Background(), f, maxChatPending); !errors.Is(err, model.ErrChatNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestChatFileOwnership(t *testing.T) {
	ctx := context.Background()
	r := newChatRig(t, &chatPlain{reply: "ok"})
	ana, _ := r.chat.Create(ctx, r.ana, "", "", "")
	bob, _ := r.chat.Create(ctx, r.bob, "", "", "")
	f := r.mustUpload(t, ana.ID, "a.txt", []byte("secret"))

	if _, err := r.upload(t, r.bob, ana.ID, "b.txt", []byte("x")); !errors.Is(err, model.ErrChatNotFound) {
		t.Errorf("upload into another's chat: %v", err)
	}
	for label, chatID := range map[string]string{"owner's chat": ana.ID, "own chat, foreign id": bob.ID} {
		if _, fh, err := r.chat.OpenFile(ctx, r.bob, chatID, f.ID); !errors.Is(err, model.ErrChatNotFound) {
			fh.Close()
			t.Errorf("open via %s: %v", label, err)
		}
		if err := r.chat.RemoveFile(ctx, r.bob, chatID, f.ID); !errors.Is(err, model.ErrChatNotFound) {
			t.Errorf("remove via %s: %v", label, err)
		}
	}
	if _, err := r.send(t, r.bob, bob.ID, ChatSend{Content: "q", FileIDs: []string{f.ID}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("send with another chat's file: %v", err)
	}
	if _, err := r.send(t, r.bob, ana.ID, ChatSend{Content: "q", FileIDs: []string{f.ID}}); !errors.Is(err, model.ErrChatNotFound) {
		t.Errorf("send into another's chat: %v", err)
	}
	if _, fh, err := r.chat.OpenFile(ctx, r.ana, ana.ID, f.ID); err != nil {
		t.Errorf("owner opens: %v", err)
	} else {
		fh.Close()
	}
}

func TestChatSendAttachesFilesOnce(t *testing.T) {
	ctx := context.Background()
	h := &chatKeeper{chatPlain: &chatPlain{reply: "ok"}}
	r := newChatRig(t, h)
	ch, _ := r.chat.Create(ctx, r.ana, "", "", "")
	img := r.mustUpload(t, ch.ID, "shot.png", pngBytes)
	txt := r.mustUpload(t, ch.ID, "n.txt", []byte("note"))

	if _, err := r.send(t, r.ana, ch.ID, ChatSend{Content: "q", FileIDs: []string{img.ID, img.ID}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate id: %v", err)
	}
	evs, err := r.send(t, r.ana, ch.ID, ChatSend{Content: "look", FileIDs: []string{img.ID, txt.ID}})
	if err != nil {
		t.Fatal(err)
	}
	want := []model.ChatAttachment{
		{Kind: "image", Name: "shot.png", Size: len(pngBytes), FileID: img.ID, MediaType: "image/png"},
		{Kind: "text", Name: "n.txt", Size: 4, FileID: txt.ID, MediaType: "text/plain; charset=utf-8"},
	}
	if got := evs[0].Message.Attachments; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("attachments: %+v", got)
	}
	got := h.starts[0].Attachments
	if len(got) != 2 || got[0].MediaType != "image/png" || got[1].MediaType != "text/plain" || string(got[1].Data) != "note" || got[1].Name != "n.txt" {
		t.Fatalf("agent attachments: %+v", got)
	}
	if strings.Contains(h.starts[0].Prompt, "note") {
		t.Fatalf("file content must not be in the prompt: %q", h.starts[0].Prompt)
	}
	if _, err := r.send(t, r.ana, ch.ID, ChatSend{Content: "again", FileIDs: []string{img.ID}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a file attaches once: %v", err)
	}
	if err := r.chat.RemoveFile(ctx, r.ana, ch.ID, img.ID); !errors.Is(err, model.ErrStateConflict) {
		t.Fatalf("remove attached: %v", err)
	}
	spare := r.mustUpload(t, ch.ID, "s.txt", []byte("x"))
	if err := r.chat.RemoveFile(ctx, r.ana, ch.ID, spare.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(r.chat.filePath(ch.ID, spare.ID)); !os.IsNotExist(err) {
		t.Fatalf("removed file still on disk: %v", err)
	}
	if _, fh, err := r.chat.OpenFile(ctx, r.ana, ch.ID, spare.ID); !errors.Is(err, model.ErrChatNotFound) {
		fh.Close()
		t.Fatalf("open removed: %v", err)
	}
}

type failingAdd struct{ ChatStore }

func (failingAdd) AddMessageFiles(context.Context, *model.ChatMessage, []string) error {
	return errors.New("disk full")
}

func TestChatFailedMessageInsertLeavesFilesUnattached(t *testing.T) {
	ctx := context.Background()
	r := newChatRig(t, &chatPlain{reply: "ok"})
	ch, _ := r.chat.Create(ctx, r.ana, "", "", "")
	f := r.mustUpload(t, ch.ID, "n.txt", []byte("x"))
	real := r.chat.Store
	r.chat.Store = failingAdd{real}
	if _, err := r.send(t, r.ana, ch.ID, ChatSend{Content: "q", FileIDs: []string{f.ID}}); err == nil {
		t.Fatal("send should fail")
	}
	got, err := real.File(ctx, ch.ID, f.ID)
	if err != nil || got.MessageID != nil {
		t.Fatalf("file after failed insert: %+v %v", got, err)
	}
	r.chat.Store = real
	if _, err := r.send(t, r.ana, ch.ID, ChatSend{Content: "q", FileIDs: []string{f.ID}}); err != nil {
		t.Fatalf("retry: %v", err)
	}
}

func TestChatStoreAttachIsAtomic(t *testing.T) {
	ctx := context.Background()
	r := newChatRig(t, &chatPlain{reply: "ok"})
	ch, _ := r.chat.Create(ctx, r.ana, "", "", "")
	f := r.mustUpload(t, ch.ID, "n.txt", []byte("x"))
	m := &model.ChatMessage{ChatID: ch.ID, Role: "user", Content: "q", CreatedAt: time.Now()}
	if err := r.chat.Store.AddMessageFiles(ctx, m, []string{f.ID, "cf_missing"}); !errors.Is(err, model.ErrChatFilesUnavailable) {
		t.Fatalf("missing id: %v", err)
	}
	if msgs, _ := r.chat.Store.Messages(ctx, ch.ID); len(msgs) != 0 {
		t.Fatalf("a failed attach must store no message: %d", len(msgs))
	}
	if got, _ := r.chat.Store.File(ctx, ch.ID, f.ID); got.MessageID != nil {
		t.Fatal("a failed attach must leave the file free")
	}
}

func TestChatReplayOnNewSessionOnly(t *testing.T) {
	ctx := context.Background()
	h := &chatKeeper{chatPlain: &chatPlain{reply: "ok"}}
	r := newChatRig(t, h)
	ch, _ := r.chat.Create(ctx, r.ana, "", "", "")
	a := r.mustUpload(t, ch.ID, "a.png", pngBytes)
	b := r.mustUpload(t, ch.ID, "b.pdf", pdfBytes(100))
	c := r.mustUpload(t, ch.ID, "c.txt", []byte("notes"))
	for _, f := range []*model.ChatFileRec{a, b} {
		if _, err := r.send(t, r.ana, ch.ID, ChatSend{Content: "q", FileIDs: []string{f.ID}}); err != nil {
			t.Fatal(err)
		}
	}
	if len(h.starts) != 1 || len(h.conts) != 1 || len(h.conts[0].Attachments) != 1 || h.conts[0].Attachments[0].Name != "b.pdf" {
		t.Fatalf("a continue sends only this turn's files: starts=%d conts=%+v", len(h.starts), h.conts)
	}
	if strings.Contains(h.conts[0].Prompt, "attached earlier") {
		t.Fatal("no replay note on a continued session")
	}

	h.gone = true
	if _, err := r.send(t, r.ana, ch.ID, ChatSend{Content: "now", FileIDs: []string{c.ID}}); err != nil {
		t.Fatal(err)
	}
	req := h.starts[len(h.starts)-1]
	var names []string
	for _, at := range req.Attachments {
		names = append(names, at.Name)
	}
	if got := strings.Join(names, ","); got != "b.pdf,a.png,c.txt" {
		t.Fatalf("replays newest first, then this turn's: %s", got)
	}
	note := "Files attached earlier in this conversation, newest first, are included with this message: b.pdf, a.png.\n\n"
	if !strings.Contains(req.Prompt, note+"Earlier in this conversation:") || !strings.Contains(req.Prompt, "(attached pdf: b.pdf)") {
		t.Fatalf("prompt: %q", req.Prompt)
	}
	msgs, _ := r.chat.Messages(ctx, r.ana, ch.ID)
	last := msgs[len(msgs)-1].Usage
	if want := len(pngBytes) + 109 + 5; last.AttachBytes != want {
		t.Fatalf("attach bytes count replays: %d, want %d", last.AttachBytes, want)
	}
	if cont := msgs[3].Usage.AttachBytes; cont != 109 {
		t.Fatalf("continue counts its own files only: %d", cont)
	}
}

func TestChatReplayBudgetAndWindow(t *testing.T) {
	ctx := context.Background()
	h := &chatPlain{reply: "ok"}
	r := newChatRig(t, h)
	ch, _ := r.chat.Create(ctx, r.ana, "", "", "")
	old := r.mustUpload(t, ch.ID, "old.pdf", pdfBytes(6<<20))
	mid := r.mustUpload(t, ch.ID, "mid.pdf", pdfBytes(6<<20))
	small := r.mustUpload(t, ch.ID, "new.txt", []byte("hi"))
	for _, f := range []*model.ChatFileRec{old, mid, small} {
		if _, err := r.send(t, r.ana, ch.ID, ChatSend{Content: "q", FileIDs: []string{f.ID}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.send(t, r.ana, ch.ID, ChatSend{Content: "plain"}); err != nil {
		t.Fatal(err)
	}
	req := h.starts[len(h.starts)-1]
	var names []string
	for _, at := range req.Attachments {
		names = append(names, at.Name)
	}
	if got := strings.Join(names, ","); got != "new.txt,mid.pdf" {
		t.Fatalf("budget keeps the newest that fit: %s", got)
	}
	if !strings.Contains(req.Prompt, "(attached pdf: old.pdf)") {
		t.Fatalf("a file over budget stays a history line: %q", req.Prompt)
	}

	for i := 0; i < historyMessages; i++ {
		if _, err := r.send(t, r.ana, ch.ID, ChatSend{Content: fmt.Sprintf("filler %d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if got := h.starts[len(h.starts)-1].Attachments; len(got) != 0 {
		t.Fatalf("files outside the history window are not replayed: %d", len(got))
	}
}

func TestChatReplaySkipsFailedTurns(t *testing.T) {
	ctx := context.Background()
	h := &chatPlain{reply: "ok"}
	r := newChatRig(t, h)
	ch, _ := r.chat.Create(ctx, r.ana, "", "", "")
	good := r.mustUpload(t, ch.ID, "good.txt", []byte("g"))
	bad := r.mustUpload(t, ch.ID, "bad.txt", []byte("b"))
	if _, err := r.send(t, r.ana, ch.ID, ChatSend{Content: "q", FileIDs: []string{good.ID}}); err != nil {
		t.Fatal(err)
	}
	h.err = errors.New("boom")
	if _, err := r.send(t, r.ana, ch.ID, ChatSend{Content: "q", FileIDs: []string{bad.ID}}); err != nil {
		t.Fatal(err)
	}
	h.err = nil
	if _, err := r.send(t, r.ana, ch.ID, ChatSend{Content: "next"}); err != nil {
		t.Fatal(err)
	}
	got := h.starts[len(h.starts)-1].Attachments
	if len(got) != 1 || got[0].Name != "good.txt" {
		t.Fatalf("replays: %+v", got)
	}
}

func TestChatReplayBudgetShrinksWithThisTurn(t *testing.T) {
	ctx := context.Background()
	h := &chatPlain{reply: "ok"}
	r := newChatRig(t, h)
	ch, _ := r.chat.Create(ctx, r.ana, "", "", "")
	old := r.mustUpload(t, ch.ID, "old.pdf", pdfBytes(2<<20))
	if _, err := r.send(t, r.ana, ch.ID, ChatSend{Content: "q", FileIDs: []string{old.ID}}); err != nil {
		t.Fatal(err)
	}
	big := r.mustUpload(t, ch.ID, "big.pdf", pdfBytes(maxChatMessageBytes-(1<<20)))
	if _, err := r.send(t, r.ana, ch.ID, ChatSend{Content: "q", FileIDs: []string{big.ID}}); err != nil {
		t.Fatal(err)
	}
	got := h.starts[len(h.starts)-1].Attachments
	if len(got) != 1 || got[0].Name != "big.pdf" {
		t.Fatalf("2 MB replay must not fit beside a 17 MB turn: %d attachments", len(got))
	}
}

func TestChatOldFileKindReadsAsText(t *testing.T) {
	ctx := context.Background()
	h := &chatPlain{reply: "ok"}
	r := newChatRig(t, h)
	ch, _ := r.chat.Create(ctx, r.ana, "", "", "")
	old := &model.ChatMessage{ChatID: ch.ID, Role: "user", Content: "see file", Attachments: []model.ChatAttachment{{Kind: "file", Name: "old.txt", Size: 3}}, CreatedAt: time.Now()}
	if err := r.chat.Store.AddMessage(ctx, old); err != nil {
		t.Fatal(err)
	}
	msgs, _ := r.chat.Messages(ctx, r.ana, ch.ID)
	if a := msgs[0].Attachments[0]; a.Kind != "text" || a.FileID != "" || a.Name != "old.txt" {
		t.Fatalf("old attachment: %+v", a)
	}
	if _, err := r.send(t, r.ana, ch.ID, ChatSend{Content: "next"}); err != nil {
		t.Fatal(err)
	}
	req := h.starts[0]
	if len(req.Attachments) != 0 || !strings.Contains(req.Prompt, "(attached text: old.txt)") {
		t.Fatalf("old message in history: %+v %q", req.Attachments, req.Prompt)
	}
}

func TestChatDeleteAndCollectFiles(t *testing.T) {
	ctx := context.Background()
	r := newChatRig(t, &chatPlain{reply: "ok"})
	gone, _ := r.chat.Create(ctx, r.ana, "", "", "")
	live, _ := r.chat.Create(ctx, r.ana, "", "", "")
	r.mustUpload(t, gone.ID, "a.txt", []byte("x"))
	sent := r.mustUpload(t, live.ID, "sent.txt", []byte("x"))
	stale := r.mustUpload(t, live.ID, "stale.txt", []byte("x"))
	fresh := r.mustUpload(t, live.ID, "fresh.txt", []byte("x"))
	if _, err := r.send(t, r.ana, live.ID, ChatSend{Content: "q", FileIDs: []string{sent.ID}}); err != nil {
		t.Fatal(err)
	}

	if err := r.chat.Delete(ctx, r.ana, gone.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(r.chat.DataDir, "chat-files", gone.ID)); !os.IsNotExist(err) {
		t.Fatalf("deleting a chat must remove its files: %v", err)
	}

	orphan := filepath.Join(r.chat.DataDir, "chat-files", "chat_deadbeefdeadbeef")
	if err := os.MkdirAll(orphan, 0o700); err != nil {
		t.Fatal(err)
	}
	oldTmp := filepath.Join(r.chat.DataDir, "chat-files", live.ID, ".upload-123")
	if err := os.WriteFile(oldTmp, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-48 * time.Hour)
	os.Chtimes(oldTmp, past, past)
	r.chat.Store = agedStore{r.chat.Store, stale.ID}

	if err := r.chat.CollectFiles(ctx); err != nil {
		t.Fatal(err)
	}
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }
	if exists(orphan) || exists(oldTmp) {
		t.Fatal("orphan directory and stale temp file must go")
	}
	if exists(r.chat.filePath(live.ID, stale.ID)) {
		t.Fatal("a day-old unattached upload must go")
	}
	if !exists(r.chat.filePath(live.ID, fresh.ID)) || !exists(r.chat.filePath(live.ID, sent.ID)) {
		t.Fatal("fresh and attached files must stay")
	}
}

// agedStore reports one upload as older than the cutoff.
type agedStore struct {
	ChatStore
	id string
}

func (a agedStore) StaleFiles(ctx context.Context, _ time.Time) ([]model.ChatFileRec, error) {
	all, err := a.ChatStore.StaleFiles(ctx, time.Now().Add(time.Hour))
	var out []model.ChatFileRec
	for _, f := range all {
		if f.ID == a.id {
			out = append(out, f)
		}
	}
	return out, err
}
