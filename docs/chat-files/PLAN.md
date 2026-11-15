# Chat attachments — plan

Status: approved 2026-11-16 (user accepted the defaults).

## Why

Today text files are pasted into the prompt and images ride as base64 inside the message JSON. Nothing is kept: after the agent session ends (20 min idle, restart, project or MCP change) the agent only sees "(attached file: x)", and after a reload the UI shows no thumbnail. PDFs and other binaries are refused. Two code paths, one 12 MB request, no progress.

## Decisions

- **Upload first.** Each file is uploaded on its own, stored on disk, and a message refers to files by id.
- **Kinds:** `image` (PNG, JPEG, GIF, WebP), `pdf`, `text` (any UTF-8 text without NUL bytes: code, md, csv, json, log, …). Type is decided **server-side from the bytes**, never from the name or the client's MIME. Word/Excel/PowerPoint (zip with `[Content_Types].xml`) get their own message: "Word, Excel and PowerPoint files are not supported yet. Export as PDF or text." Anything else: "Only images (PNG, JPEG, GIF, WebP), PDF and text files can be attached." HEIC counts as anything else.
- **Limits:** image 3.75 MB (3,932,160 bytes), PDF 18 MB, text 1 MB; per message at most 10 files and 18 MB in total; at most 20 uploaded files per chat waiting to be sent. Why: Anthropic measures images on their base64 form and a request may carry 32 MB of base64, so a turn's raw files stay under 18 MB (about 24 MB encoded, leaving room for the prompt text).
- **To the agent:** one `AgentRequest.Attachments []AgentAttachment{Name, MediaType, Data}` replaces `Images`.
  - ACP: images keep the `image` block (proven path). PDF and text go as ACP `resource` blocks `{"type":"resource","resource":{"uri":"file:///attachments/<escaped name>","mimeType":"…","blob":"<base64>"}}` — claude-agent-acp turns a PDF blob into a `document` block and a text blob into `<context>`. Only when the agent's `initialize` result advertises `agentCapabilities.promptCapabilities.embeddedContext`; otherwise text goes inline as a text block (`Attached file <name>:\n<file>…</file>`) and a PDF fails the turn with ErrPermanent "This agent cannot read PDF files."
  - OpenCode HTTP harness (chat is off there): keep images as file parts, text inline, PDF error. Mock: accept and ignore.
- **Replay on a new session.** When a turn starts a new harness session (Start, not Continue), it also re-sends files attached to user messages inside the history window (`historyMessages`, last 20), newest first, until a replay budget of min(10 MB, 18 MB minus this turn's files) is spent (files of turns whose reply failed are not replayed), before the history text, introduced by "Files attached earlier in this conversation:". Files over budget stay as the existing "(attached …)" line in the history. A Continue sends only this turn's files.
- **Rail:** `ChatUsage.AttachBytes` = bytes of every file sent this turn, replays included.

## Backend

- Migration `023_chat_files.sql`: `chat_files(id TEXT PRIMARY KEY, chat_id TEXT NOT NULL REFERENCES chats(id) ON DELETE CASCADE, user_id TEXT NOT NULL, message_id INTEGER NULL, name TEXT NOT NULL, media_type TEXT NOT NULL, kind TEXT NOT NULL, size INTEGER NOT NULL, sha256 TEXT NOT NULL, created_at INTEGER NOT NULL)` + index on `chat_id`.
- Storage: `<DataDir>/chat-files/<chatID>/<fileID>` (NOT under the chat agent's working dir `<DataDir>/chat`). File id generated server-side (`cf_` + random hex); names never appear in paths. Write to a temp file in the same dir, fsync, rename.
- `POST /api/v1/chats/:id/files` (multipart, field `file`, one file per request, streamed to disk; route body limit 19 MiB, excluded from the global 1 MB limit the same way chat messages are). Owner only. Returns `{"id","name","media_type","kind","size"}`. Name: base name, trimmed, control chars removed, max 200 runes.
- `GET /api/v1/chats/:id/files/:fileId` owner only; `Content-Type` = stored media type (`text/plain; charset=utf-8` for every text file), `X-Content-Type-Options: nosniff`, `Content-Security-Policy: sandbox`, `Content-Disposition: inline; filename*=UTF-8''…`, `Cache-Control: private, max-age=3600`.
- `DELETE /api/v1/chats/:id/files/:fileId` owner only, only while not attached to a message (409 otherwise); removes row and file.
- `ChatSend` gets `file_ids []string`; the inline `files` field and its base64 path are removed (the UI PR switches at the same time). Send validates under the chat lock: ids belong to this chat and user, are not yet attached, count/total limits; then stores the user message and sets `message_id` on those files in one transaction-ish step (if the message insert fails, files stay unattached).
- `model.ChatAttachment` gains `file_id` and `media_type` (omitempty); kinds `image|pdf|text|kb`; old rows with kind `file` read as text and have no file_id.
- Chat delete (and every other path that deletes chats, e.g. project delete — find them) removes `<DataDir>/chat-files/<chatID>` after the DB delete. Startup: delete unattached files older than 24 h (rows and bytes).
- Tests: sniffing table (each kind, office zip, random binary, UTF-8 with BOM, NUL in text, empty file), limits, ownership on all three endpoints (other user 404), path safety, send with foreign/attached/unknown ids, replay order and budget, Continue sends only new files, ACP block shapes with and without embeddedContext, delete cleanup, orphan GC.

## UI

- Composer: attach button (multi-select), drag and drop onto the thread or composer, paste images from the clipboard. Each file uploads at once with `XMLHttpRequest` progress; a chip shows name, size, progress, error and a remove button (DELETE if already uploaded). Images show a local object-URL thumbnail (revoke on remove/unmount). Client pre-checks the same kinds/limits by extension and MIME for quick feedback; the server's answer wins. Send is disabled while an upload is in flight or failed; it sends `file_ids`.
- Messages: image attachments render as thumbnails from the GET url (click opens full size in a new tab); PDF and text as chips that open the file in a new tab. Old attachments without `file_id` render as today. KB attachments unchanged.
- The rail's "From this conversation" attachments list links to the files.
- Remove the now-unused base64/inline helpers in `chatUtils.ts`; update their tests.

## Known limits

- Office files are refused until a conversion step exists.
- A file is attached to exactly one message; re-sending means uploading again.
