package usecase

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"legos-factory/backend/internal/model"
)

const (
	defaultChatTitle = "New chat"
	chatTitleRunes   = 60
	maxChatMessage   = 32 << 10
	maxChatFiles     = 5
	maxChatFile      = 100 << 10
	maxChatFilesAll  = 300 << 10
	maxChatQuote     = 8 << 10
	maxChatDoc       = 60 << 10
	historyMessages  = 20
	historyRunes     = 24000
	chatTurnTimeout  = 10 * time.Minute
	modelListTTL     = 5 * time.Minute
)

// ErrChatBusy means the conversation is already answering a message.
var ErrChatBusy = errors.New("this chat is already answering a message")

// Chat runs general conversations with a read-only agent, outside the
// work-order flow. A conversation keeps one harness session while the harness
// can; otherwise (or once the session is gone) each turn replays recent history.
type Chat struct {
	Store ChatStore
	Docs  interface {
		Detail(ctx context.Context, u *model.User, id string) (*KBDetail, error)
	}
	Projects interface {
		Authorize(ctx context.Context, u *model.User, projectID string) error
	}
	Settings SettingsReader
	Usage    Budget
	// Harness is nil when no harness can run chats read-only.
	Harness Harness
	// Models lists what the harness can run; Default is the "provider/model" auto resolves to.
	Models  func(ctx context.Context) ([]model.ModelInfo, error)
	Default func(ctx context.Context) string
	// Dir is the empty directory chat agents run in.
	Dir string

	mu     sync.Mutex
	busy   map[string]bool
	ses    map[string]chatSession
	listed []model.ModelInfo
	listAt time.Time
}

type chatSession struct{ id string }

type ChatModel struct {
	ID         string `json:"id"` // "provider/model"; empty is auto
	Name       string `json:"name"`
	ProviderID string `json:"provider_id"`
	ModelID    string `json:"model_id"`
	Harness    string `json:"harness"`
}

type ChatModels struct {
	Default string      `json:"default"`
	Models  []ChatModel `json:"models"`
}

type ChatFile struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

type ChatKBRef struct {
	ID    string `json:"id"`
	Quote string `json:"quote,omitempty"`
}

type ChatSend struct {
	Content string     `json:"content"`
	Files   []ChatFile `json:"files"`
	KB      *ChatKBRef `json:"kb"`
}

// ChatEvent is one step of a streamed turn: "user_message", "delta", then
// exactly one of "done" or "error".
type ChatEvent struct {
	Kind    string
	Text    string
	Message *model.ChatMessage
	Error   string
}

func (c *Chat) Create(ctx context.Context, u *model.User, title, projectID, modelRef string) (*model.Chat, error) {
	if projectID != "" {
		if err := c.Projects.Authorize(ctx, u, projectID); err != nil {
			return nil, err
		}
	}
	if err := c.checkModel(ctx, modelRef); err != nil {
		return nil, err
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = defaultChatTitle
	}
	idb := make([]byte, 8)
	if _, err := rand.Read(idb); err != nil {
		return nil, err
	}
	now := time.Now()
	ch := &model.Chat{ID: "chat_" + hex.EncodeToString(idb), UserID: u.ID, ProjectID: projectID, Title: truncateRunes(title, 200), Model: modelRef, CreatedAt: now, UpdatedAt: now}
	if err := c.Store.Create(ctx, ch); err != nil {
		return nil, err
	}
	return ch, nil
}

func (c *Chat) List(ctx context.Context, u *model.User) ([]model.Chat, error) {
	return c.Store.List(ctx, u.ID)
}

// own loads a chat, answering not-found for another user's chat.
func (c *Chat) own(ctx context.Context, u *model.User, id string) (*model.Chat, error) {
	ch, err := c.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if ch.UserID != u.ID {
		return nil, model.ErrChatNotFound
	}
	return ch, nil
}

func (c *Chat) Get(ctx context.Context, u *model.User, id string) (*model.Chat, error) {
	return c.own(ctx, u, id)
}

// Update renames a chat and/or changes its model; nil leaves a field alone.
func (c *Chat) Update(ctx context.Context, u *model.User, id string, title, modelRef *string) (*model.Chat, error) {
	ch, err := c.own(ctx, u, id)
	if err != nil {
		return nil, err
	}
	if title != nil {
		t := strings.TrimSpace(*title)
		if t == "" {
			return nil, fmt.Errorf("%w: title is required", ErrInvalid)
		}
		ch.Title = truncateRunes(t, 200)
	}
	if modelRef != nil && *modelRef != ch.Model {
		if err := c.checkModel(ctx, *modelRef); err != nil {
			return nil, err
		}
		ch.Model = *modelRef
		c.forget(id) // the harness keeps its model for a session's lifetime
	}
	if err := c.Store.Update(ctx, id, ch.Title, ch.Model); err != nil {
		return nil, err
	}
	return c.Store.Get(ctx, id)
}

func (c *Chat) Delete(ctx context.Context, u *model.User, id string) error {
	if _, err := c.own(ctx, u, id); err != nil {
		return err
	}
	if !c.lock(id) {
		return ErrChatBusy
	}
	defer c.unlock(id)
	c.forget(id)
	return c.Store.Delete(ctx, id)
}

func (c *Chat) Messages(ctx context.Context, u *model.User, id string) ([]model.ChatMessage, error) {
	if _, err := c.own(ctx, u, id); err != nil {
		return nil, err
	}
	return c.Store.Messages(ctx, id)
}

// ModelList is the picker: an auto entry, then the models the harness runs,
// narrowed to the workspace's allowed list when one is set.
func (c *Chat) ModelList(ctx context.Context) (*ChatModels, error) {
	out := &ChatModels{Models: []ChatModel{{Name: "Auto (default)"}}}
	if c.Default != nil {
		out.Default = c.Default(ctx)
	}
	if c.Models == nil {
		return out, nil
	}
	infos, err := c.listModels(ctx)
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{}
	if c.Settings != nil {
		if s, err := c.Settings.Get(ctx); err == nil {
			for _, a := range s.AllowedModels {
				if a = strings.TrimSpace(a); a != "" {
					allowed[a] = true
				}
			}
		}
	}
	for _, m := range infos {
		id := m.ProviderID + "/" + m.ModelID
		if len(allowed) > 0 && !allowed[id] {
			continue
		}
		out.Models = append(out.Models, ChatModel{ID: id, Name: m.Name, ProviderID: m.ProviderID, ModelID: m.ModelID, Harness: m.Harness})
	}
	return out, nil
}

func (c *Chat) listModels(ctx context.Context) ([]model.ModelInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.listed != nil && time.Since(c.listAt) < modelListTTL {
		return c.listed, nil
	}
	infos, err := c.Models(ctx)
	if err != nil {
		return nil, err
	}
	c.listed, c.listAt = infos, time.Now()
	return infos, nil
}

func (c *Chat) checkModel(ctx context.Context, ref string) error {
	if ref == "" {
		return nil
	}
	list, err := c.ModelList(ctx)
	if err != nil {
		return fmt.Errorf("%w: the model list is unavailable, so %q cannot be checked", ErrInvalid, ref)
	}
	for _, m := range list.Models {
		if m.ID == ref {
			return nil
		}
	}
	return fmt.Errorf("%w: model %q is not available; pick one from /chat/models", ErrInvalid, ref)
}

func (c *Chat) lock(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.busy[id] {
		return false
	}
	if c.busy == nil {
		c.busy = map[string]bool{}
	}
	c.busy[id] = true
	return true
}

func (c *Chat) unlock(id string) {
	c.mu.Lock()
	delete(c.busy, id)
	c.mu.Unlock()
}

// forget ends the chat's kept harness session, if any.
func (c *Chat) forget(id string) {
	c.mu.Lock()
	s, ok := c.ses[id]
	delete(c.ses, id)
	c.mu.Unlock()
	if sh, can := c.Harness.(SessionHarness); ok && can {
		sh.Close(s.id)
	}
}

// Send validates and stores the user's message, then answers it, reporting
// progress through emit. It returns an error only before anything is stored;
// from then on every outcome, including a failed reply, ends in a "done" or
// "error" event. The turn runs to completion even if the caller goes away.
func (c *Chat) Send(ctx context.Context, u *model.User, id string, in ChatSend, emit func(ChatEvent)) error {
	ch, err := c.own(ctx, u, id)
	if err != nil {
		return err
	}
	if c.Harness == nil {
		return fmt.Errorf("%w: no agent runtime can run chats read-only; use the ACP harness", ErrInvalid)
	}
	prompt, atts, err := c.compose(ctx, u, in)
	if err != nil {
		return err
	}
	if !c.lock(id) {
		return ErrChatBusy
	}
	defer c.unlock(id)

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), chatTurnTimeout)
	defer cancel()
	history, err := c.Store.Messages(ctx, id)
	if err != nil {
		return err
	}
	um := &model.ChatMessage{ChatID: id, Role: "user", Content: strings.TrimSpace(in.Content), Attachments: atts, CreatedAt: time.Now()}
	if err := c.Store.AddMessage(ctx, um); err != nil {
		return err
	}
	if len(history) == 0 && ch.Title == defaultChatTitle {
		_ = c.Store.Update(ctx, id, titleFrom(um.Content), ch.Model)
	}
	emit(ChatEvent{Kind: "user_message", Message: um})

	var streamed strings.Builder
	res, err := c.answer(ctx, ch, history, prompt, func(s string) {
		streamed.WriteString(s)
		emit(ChatEvent{Kind: "delta", Text: s})
	})
	_ = c.Store.Touch(ctx, id)
	am := &model.ChatMessage{ChatID: id, Role: "assistant", Attachments: []model.ChatAttachment{}, CreatedAt: time.Now()}
	if err != nil {
		am.Content, am.Error = streamed.String(), err.Error()
		if serr := c.Store.AddMessage(ctx, am); serr != nil {
			am.ID = 0
		}
		emit(ChatEvent{Kind: "error", Message: am, Error: am.Error})
		return nil
	}
	am.Content = res.Text
	am.Usage = &model.ChatUsage{Model: c.resolve(ctx, ch), TokensIn: res.TokensIn, TokensOut: res.TokensOut, CostMicros: res.CostMicros}
	if err := c.Store.AddMessage(ctx, am); err != nil {
		am.Error = "reply could not be saved: " + err.Error()
		emit(ChatEvent{Kind: "error", Message: am, Error: am.Error})
		return nil
	}
	if c.Usage != nil {
		provider, modelID := SplitModelRef(am.Usage.Model)
		_ = c.Usage.Record(ctx, model.UsageRecord{
			UserID: u.ID, Role: string(model.AgentChat), Provider: provider, Model: modelID,
			TokensIn: res.TokensIn, TokensOut: res.TokensOut, CostMicros: res.CostMicros, Source: res.CostSource,
		})
	}
	emit(ChatEvent{Kind: "done", Message: am})
	return nil
}

func (c *Chat) resolve(ctx context.Context, ch *model.Chat) string {
	if ch.Model != "" || c.Default == nil {
		return ch.Model
	}
	return c.Default(ctx)
}

func (c *Chat) answer(ctx context.Context, ch *model.Chat, history []model.ChatMessage, turn string, onText func(string)) (model.AgentResult, error) {
	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
		return model.AgentResult{}, err
	}
	provider, modelID := SplitModelRef(c.resolve(ctx, ch))
	req := model.AgentRequest{Role: model.AgentChat, ProviderID: provider, ModelID: modelID, WorkspaceDir: c.Dir, OnText: onText}
	sh, canKeep := c.Harness.(SessionHarness)

	var ses model.AgentSession
	if canKeep {
		c.mu.Lock()
		cur, ok := c.ses[ch.ID]
		c.mu.Unlock()
		if ok {
			req.Prompt = turn
			var err error
			ses, err = sh.Continue(ctx, cur.id, req)
			if errors.Is(err, model.ErrAgentSessionNotFound) {
				c.forget(ch.ID)
				ses = nil
			} else if err != nil {
				c.forget(ch.ID)
				return model.AgentResult{}, err
			}
		}
	}
	if ses == nil {
		req.Prompt = chatPreamble + historyText(history) + turn
		req.Keep = canKeep
		var err error
		if ses, err = c.Harness.Start(ctx, req); err != nil {
			return model.AgentResult{}, err
		}
	}
	res, err := c.Harness.Result(ctx, ses.ID())
	if err != nil {
		c.forget(ch.ID)
		return model.AgentResult{}, err
	}
	if canKeep {
		c.mu.Lock()
		if c.ses == nil {
			c.ses = map[string]chatSession{}
		}
		c.ses[ch.ID] = chatSession{id: ses.ID()}
		c.mu.Unlock()
	}
	return res, nil
}

const chatPreamble = "You are an assistant in the Legos factory app, in a general conversation. You cannot run commands or change files; answer from what you know and the context you are given.\n\n"

// historyText replays the recent turns for an agent that has no memory of them.
func historyText(history []model.ChatMessage) string {
	if len(history) > historyMessages {
		history = history[len(history)-historyMessages:]
	}
	var lines []string
	total := 0
	for i := len(history) - 1; i >= 0; i-- {
		m := history[i]
		if m.Content == "" {
			continue
		}
		who := "User"
		if m.Role == "assistant" {
			who = "Assistant"
		}
		line := who + ": " + truncateRunes(m.Content, 4000)
		for _, a := range m.Attachments {
			line += fmt.Sprintf("\n(attached %s: %s)", a.Kind, a.Name)
		}
		if total += utf8.RuneCountInString(line); total > historyRunes {
			break
		}
		lines = append([]string{line}, lines...)
	}
	if len(lines) == 0 {
		return ""
	}
	return "Earlier in this conversation:\n" + strings.Join(lines, "\n") + "\n\n"
}

// compose validates the message and builds this turn's prompt: knowledge
// context, attached files, then the question.
func (c *Chat) compose(ctx context.Context, u *model.User, in ChatSend) (string, []model.ChatAttachment, error) {
	content := strings.TrimSpace(in.Content)
	if content == "" {
		return "", nil, fmt.Errorf("%w: content is required", ErrInvalid)
	}
	if len(content) > maxChatMessage {
		return "", nil, fmt.Errorf("%w: message is over %d KB", ErrInvalid, maxChatMessage>>10)
	}
	if len(in.Files) > maxChatFiles {
		return "", nil, fmt.Errorf("%w: at most %d files per message", ErrInvalid, maxChatFiles)
	}
	var b strings.Builder
	atts := []model.ChatAttachment{}
	if in.KB != nil && in.KB.ID != "" {
		if c.Docs == nil {
			return "", nil, fmt.Errorf("%w: knowledge context is not available", ErrInvalid)
		}
		if len(in.KB.Quote) > maxChatQuote {
			return "", nil, fmt.Errorf("%w: quoted selection is over %d KB", ErrInvalid, maxChatQuote>>10)
		}
		det, err := c.Docs.Detail(ctx, u, in.KB.ID)
		if err != nil {
			return "", nil, err
		}
		body := det.Body
		if len(body) > maxChatDoc {
			body = truncateBytes(body, maxChatDoc) + "\n[document truncated]"
		}
		fmt.Fprintf(&b, "Knowledge document %q (%s):\n<document>\n%s\n</document>\n\n", det.Doc.Title, det.Doc.ID, body)
		if q := strings.TrimSpace(in.KB.Quote); q != "" {
			fmt.Fprintf(&b, "The user selected this passage of it:\n<selection>\n%s\n</selection>\n\n", q)
		}
		atts = append(atts, model.ChatAttachment{Kind: "kb", Name: det.Doc.Title, DocID: det.Doc.ID, Quote: strings.TrimSpace(in.KB.Quote)})
	}
	total := 0
	for _, f := range in.Files {
		name := filepath.Base(strings.ReplaceAll(strings.TrimSpace(f.Name), "\\", "/"))
		if name == "" || name == "." || name == "/" {
			return "", nil, fmt.Errorf("%w: every file needs a name", ErrInvalid)
		}
		if len(f.Content) > maxChatFile {
			return "", nil, fmt.Errorf("%w: %s is over %d KB", ErrInvalid, name, maxChatFile>>10)
		}
		if total += len(f.Content); total > maxChatFilesAll {
			return "", nil, fmt.Errorf("%w: files total over %d KB", ErrInvalid, maxChatFilesAll>>10)
		}
		if !utf8.ValidString(f.Content) || strings.ContainsRune(f.Content, 0) {
			return "", nil, fmt.Errorf("%w: %s looks binary; only text files can be attached", ErrInvalid, name)
		}
		fmt.Fprintf(&b, "Attached file %s:\n<file>\n%s\n</file>\n\n", name, f.Content)
		atts = append(atts, model.ChatAttachment{Kind: "file", Name: name, Size: len(f.Content)})
	}
	b.WriteString(content)
	return b.String(), atts, nil
}

func titleFrom(s string) string {
	return truncateRunes(strings.Join(strings.Fields(s), " "), chatTitleRunes)
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
