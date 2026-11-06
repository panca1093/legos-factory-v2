package usecase

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"legos-factory/backend/internal/model"
)

const (
	fallbackTitleRunes = 40
	titleWords         = 6
	draftTranscript    = 24000 // runes of conversation sent to the draft call, newest kept
	relatedMessages    = 4
	relatedLimit       = 5
	draftTitleRunes    = 120
	draftBodyRunes     = 8000
	titleTimeout       = 2 * time.Minute
)

// WorkOrderDraft is a work order proposed from a conversation.
type WorkOrderDraft struct {
	Title      string `json:"title"`
	Objective  string `json:"objective"`
	Acceptance string `json:"acceptance"` // newline-separated criteria
}

type RelatedDoc struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Kind    string `json:"kind"`
	Snippet string `json:"snippet"`
}

// cheapModel prefers a haiku model the workspace allows, else the chat's own;
// ok is false when that fallback is itself no longer allowed.
func (c *Chat) cheapModel(ctx context.Context, ch *model.Chat) (ref string, ok bool) {
	allowed := c.allowedModels(ctx)
	if c.Models != nil {
		if infos, err := c.listModels(ctx); err == nil {
			for _, m := range infos {
				id := m.ProviderID + "/" + m.ModelID
				if strings.Contains(strings.ToLower(m.ModelID), "haiku") && (len(allowed) == 0 || allowed[id]) {
					return id, true
				}
			}
		}
	}
	ref = c.resolve(ctx, ch)
	return ref, ref == "" || len(allowed) == 0 || allowed[ref]
}

func (c *Chat) gen(ctx context.Context, modelRef, prompt string) (model.AgentResult, error) {
	if c.Gen != nil {
		return c.Gen(ctx, modelRef, prompt)
	}
	if c.Harness == nil {
		return model.AgentResult{}, ErrChatUnavailable
	}
	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
		return model.AgentResult{}, err
	}
	provider, modelID := SplitModelRef(modelRef)
	ses, err := c.Harness.Start(ctx, model.AgentRequest{Role: model.AgentChat, ProviderID: provider, ModelID: modelID, WorkspaceDir: c.Dir, Prompt: prompt, OnText: func(string) {}})
	if err != nil {
		return model.AgentResult{}, err
	}
	return c.Harness.Result(ctx, ses.ID())
}

// genRecorded is gen plus its cost in usage; the record outlives a cancelled ctx.
func (c *Chat) genRecorded(ctx context.Context, u *model.User, modelRef, prompt string) (model.AgentResult, error) {
	res, err := c.gen(ctx, modelRef, prompt)
	if c.Usage != nil && (res.TokensIn != 0 || res.TokensOut != 0 || res.CostMicros != 0) {
		provider, modelID := SplitModelRef(modelRef)
		_ = c.Usage.Record(context.WithoutCancel(ctx), model.UsageRecord{
			UserID: u.ID, Role: string(model.AgentChat), Provider: provider, Model: modelID,
			TokensIn: res.TokensIn, TokensOut: res.TokensOut, CostMicros: res.CostMicros, Source: res.CostSource,
		})
	}
	return res, err
}

// generateTitle titles the chat from its first message in the background and
// outlives the turn so its cost is always recorded. The channel yields the
// fallback when generation fails, or "" when the stored title is no longer the
// fallback (the user renamed the chat meanwhile).
func (c *Chat) generateTitle(ctx context.Context, u *model.User, ch *model.Chat, text, fallback string) <-chan string {
	out := make(chan string, 1)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), titleTimeout)
	go func() {
		defer cancel()
		modelRef, ok := c.cheapModel(ctx, ch)
		if !ok {
			out <- fallback
			return
		}
		prompt := "Write a title of at most 6 words for a conversation that starts with the message below. " +
			"Use the same language as the message. Reply with the title only: no quotes, no trailing punctuation.\n\n<message>\n" +
			strings.ReplaceAll(truncateRunes(text, 2000), "</message>", "<\\/message>") + "\n</message>"
		res, err := c.genRecorded(ctx, u, modelRef, prompt)
		t := cleanTitle(res.Text)
		if err != nil || t == "" {
			out <- fallback
			return
		}
		if renamed, err := c.Store.RenameIf(ctx, ch.ID, fallback, t); err != nil || !renamed {
			t = ""
		}
		out <- t
	}()
	return out
}

func cleanTitle(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.Trim(s, " \t\"'`*#“”‘’")
	w := strings.Fields(s)
	if len(w) > titleWords {
		w = w[:titleWords]
	}
	return strings.TrimRight(truncateRunes(strings.Join(w, " "), 200), " .,;:!?-…")
}

// DraftWorkOrder proposes a work order from the conversation with one model call.
func (c *Chat) DraftWorkOrder(ctx context.Context, u *model.User, id string) (*WorkOrderDraft, error) {
	if _, err := c.own(ctx, u, id); err != nil {
		return nil, err
	}
	if c.Harness == nil && c.Gen == nil {
		return nil, ErrChatUnavailable
	}
	if !c.lock(id) {
		return nil, ErrChatBusy
	}
	defer c.unlock(id)
	ch, err := c.own(ctx, u, id)
	if err != nil {
		return nil, err
	}
	if !c.startTurn() {
		return nil, ErrChatSaturated
	}
	defer c.endTurn()
	modelRef, ok := c.cheapModel(ctx, ch)
	if !ok {
		return nil, fmt.Errorf("%w: model %q is no longer allowed; pick another one for this chat", ErrInvalid, modelRef)
	}
	if c.Usage != nil {
		release, ok := c.Usage.Reserve(chatTurnEstimate)
		if !ok {
			return nil, ErrChatBudget
		}
		defer release(0)
	}
	msgs, err := c.Store.Messages(ctx, id)
	if err != nil {
		return nil, err
	}
	transcript := boundedTranscript(msgs)
	if transcript == "" {
		return nil, fmt.Errorf("%w: the chat has no messages yet", ErrInvalid)
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), chatTurnTimeout)
	defer cancel()
	prompt := "Turn the conversation below into one software work order. Reply with a single JSON object and nothing else, with exactly these string keys: " +
		`"title" (short, imperative), "objective" (what to build and why, in a few sentences) and "acceptance" (verifiable acceptance criteria, one per line). ` +
		"Write in the language the user used.\n\n<conversation>\n" + strings.ReplaceAll(transcript, "</conversation>", "<\\/conversation>") + "</conversation>"
	var lastErr error
	for range 2 {
		res, err := c.genRecorded(ctx, u, modelRef, prompt)
		if err != nil {
			lastErr = err
			continue
		}
		d, err := parseDraft(res.Text)
		if err == nil {
			return d, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("%w: %v", ErrDraftFailed, lastErr)
}

// boundedTranscript keeps the newest messages that fit draftTranscript runes.
func boundedTranscript(msgs []model.ChatMessage) string {
	var lines []string
	total := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Content == "" {
			continue
		}
		who := "User"
		if m.Role == "assistant" {
			who = "Assistant"
		}
		line := who + ": " + truncateRunes(m.Content, 4000) + "\n"
		if total += utf8.RuneCountInString(line); total > draftTranscript {
			break
		}
		lines = append([]string{line}, lines...)
	}
	return strings.Join(lines, "")
}

func parseDraft(text string) (*WorkOrderDraft, error) {
	i := strings.IndexByte(text, '{')
	if i < 0 {
		return nil, fmt.Errorf("reply has no JSON object")
	}
	var raw struct {
		Title      string `json:"title"`
		Objective  string `json:"objective"`
		Acceptance any    `json:"acceptance"`
	}
	if err := json.NewDecoder(strings.NewReader(text[i:])).Decode(&raw); err != nil {
		return nil, fmt.Errorf("reply is not valid JSON: %v", err)
	}
	d := &WorkOrderDraft{Title: truncateRunes(strings.TrimSpace(raw.Title), draftTitleRunes), Objective: truncateRunes(strings.TrimSpace(raw.Objective), draftBodyRunes)}
	switch a := raw.Acceptance.(type) {
	case string:
		d.Acceptance = strings.TrimSpace(a)
	case []any:
		var lines []string
		for _, x := range a {
			if s, ok := x.(string); ok && strings.TrimSpace(s) != "" {
				lines = append(lines, strings.TrimSpace(s))
			}
		}
		d.Acceptance = strings.Join(lines, "\n")
	}
	d.Acceptance = truncateRunes(d.Acceptance, draftBodyRunes)
	if d.Title == "" || d.Objective == "" {
		return nil, fmt.Errorf("reply lacks a title or objective")
	}
	return d, nil
}

var stopwords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`the and for are but not you your with this that from have has had was were will would can could should what when where which who how why about into over then than them they their there here also just only some any all more most very make need want use using yang dan untuk dengan atau ini itu dari pada ada tidak bisa akan saya kamu kita mereka dalam agar juga sudah belum apa bagaimana kenapa mau buat`) {
		m[w] = true
	}
	return m
}()

// keywords drops short tokens and stopwords; the store ORs what is left.
func keywords(text string, max int) string {
	seen := map[string]bool{}
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if utf8.RuneCountInString(w) < 3 || stopwords[w] || seen[w] {
			continue
		}
		seen[w] = true
		if out = append(out, w); len(out) == max {
			break
		}
	}
	return strings.Join(out, " ")
}

// RelatedKnowledge finds documents matching the recent conversation, within
// the factory-wide docs and the chat's project when u may reach it.
func (c *Chat) RelatedKnowledge(ctx context.Context, u *model.User, id string) ([]RelatedDoc, error) {
	ch, err := c.own(ctx, u, id)
	if err != nil {
		return nil, err
	}
	out := []RelatedDoc{}
	if c.Related == nil {
		return out, nil
	}
	msgs, err := c.Store.Messages(ctx, id)
	if err != nil {
		return nil, err
	}
	if len(msgs) > relatedMessages {
		msgs = msgs[len(msgs)-relatedMessages:]
	}
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(truncateRunes(m.Content, 1000))
		b.WriteByte(' ')
	}
	text := keywords(b.String(), 24)
	if text == "" {
		return out, nil
	}
	project := ch.ProjectID
	if project != "" && c.Projects.Authorize(ctx, u, project) != nil {
		project = ""
	}
	docs, err := c.Related.Related(ctx, project, text, relatedLimit)
	if err != nil {
		return nil, err
	}
	for _, d := range docs {
		kind := d.Type
		if kind == "" {
			kind = d.Source
		}
		out = append(out, RelatedDoc{ID: d.ID, Title: d.Title, Kind: kind, Snippet: truncateRunes(strings.TrimSpace(d.Description), 200)})
	}
	return out, nil
}
