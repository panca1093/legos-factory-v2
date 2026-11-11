package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"legos-factory/backend/internal/model"
)

const mcpDiscoverTimeout = 20 * time.Second

// ListMCPTools connects to s, runs the MCP handshake and returns every tool it lists, all disabled.
func ListMCPTools(ctx context.Context, s model.MCPServer) ([]model.MCPTool, error) {
	ctx, cancel := context.WithTimeout(ctx, mcpDiscoverTimeout)
	defer cancel()
	var t mcpTransport
	if s.Transport == model.MCPHTTP {
		t = &mcpHTTP{url: s.URL, headers: s.Secret, client: &http.Client{}}
	} else {
		st, err := startMCPStdio(ctx, s)
		if err != nil {
			return nil, err
		}
		defer st.close()
		t = st
	}
	raw, err := t.call(ctx, "initialize", map[string]any{
		"protocolVersion": "2025-03-26", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "legos-factory", "version": "1"},
	})
	if err != nil {
		return nil, err
	}
	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(raw, &init)
	if h, ok := t.(*mcpHTTP); ok {
		h.version = init.ProtocolVersion
	}
	if err := t.notify(ctx, "notifications/initialized"); err != nil {
		return nil, err
	}
	var tools []model.MCPTool
	cursor := ""
	for page := 0; page < 50; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := t.call(ctx, "tools/list", params)
		if err != nil {
			return nil, err
		}
		var res struct {
			Tools []struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				InputSchema json.RawMessage `json:"inputSchema"`
				Annotations struct {
					ReadOnlyHint bool `json:"readOnlyHint"`
				} `json:"annotations"`
			} `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			return nil, fmt.Errorf("mcp: tools/list returned %q: %w", truncate(string(raw), 100), err)
		}
		for _, x := range res.Tools {
			tools = append(tools, model.MCPTool{Name: x.Name, Description: x.Description, ReadOnly: x.Annotations.ReadOnlyHint,
				Size: len(x.Name) + len(x.Description) + len(x.InputSchema)})
		}
		if res.NextCursor == "" {
			return tools, nil
		}
		cursor = res.NextCursor
	}
	return nil, fmt.Errorf("mcp: tools/list has more than 50 pages")
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

type mcpTransport interface {
	call(ctx context.Context, method string, params any) (json.RawMessage, error)
	notify(ctx context.Context, method string) error
}

type mcpReply struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (r mcpReply) result(method string) (json.RawMessage, error) {
	if r.Error != nil {
		return nil, fmt.Errorf("mcp: %s failed: %s (%d)", method, r.Error.Message, r.Error.Code)
	}
	return r.Result, nil
}

type mcpStdio struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	replies chan mcpReply
	stderr  *tailBuffer
	nextID  int
}

func startMCPStdio(ctx context.Context, s model.MCPServer) (*mcpStdio, error) {
	cmd := exec.CommandContext(ctx, s.Command, s.Args...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
	for k, v := range s.Secret {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.WaitDelay = time.Second
	st := &mcpStdio{cmd: cmd, replies: make(chan mcpReply, 16), stderr: &tailBuffer{max: 2000}}
	cmd.Stderr = st.stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mcp: cannot start %s: %w", s.Command, err)
	}
	st.stdin = stdin
	go func() {
		defer close(st.replies)
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 1<<20), 8<<20)
		for sc.Scan() {
			var r mcpReply
			if json.Unmarshal(sc.Bytes(), &r) == nil && len(r.ID) > 0 {
				st.replies <- r
			}
		}
	}()
	return st, nil
}

func (s *mcpStdio) close() {
	_ = s.stdin.Close()
	_ = s.cmd.Process.Kill()
	_ = s.cmd.Wait()
}

func (s *mcpStdio) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.stdin.Write(append(b, '\n'))
	return err
}

func (s *mcpStdio) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	s.nextID++
	id := s.nextID
	if err := s.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return nil, s.fail(method, err)
	}
	for {
		select {
		case r, ok := <-s.replies:
			if !ok {
				return nil, s.fail(method, io.EOF)
			}
			if string(r.ID) == fmt.Sprint(id) {
				return r.result(method)
			}
		case <-ctx.Done():
			return nil, s.fail(method, ctx.Err())
		}
	}
}

func (s *mcpStdio) fail(method string, cause error) error {
	if tail := strings.TrimSpace(s.stderr.String()); tail != "" {
		return fmt.Errorf("mcp: %s: %w; server said: %s", method, cause, tail)
	}
	return fmt.Errorf("mcp: %s: %w", method, cause)
}

func (s *mcpStdio) notify(_ context.Context, method string) error {
	return s.send(map[string]any{"jsonrpc": "2.0", "method": method})
}

type mcpHTTP struct {
	url, session, version string
	headers               map[string]string
	client                *http.Client
	nextID                int
}

func (h *mcpHTTP) post(ctx context.Context, body map[string]any) (*http.Response, error) {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.url, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	for k, v := range h.headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if h.session != "" {
		req.Header.Set("Mcp-Session-Id", h.session)
	}
	if h.version != "" {
		req.Header.Set("MCP-Protocol-Version", h.version)
	}
	return h.client.Do(req)
}

func (h *mcpHTTP) notify(ctx context.Context, method string) error {
	resp, err := h.post(ctx, map[string]any{"jsonrpc": "2.0", "method": method})
	if err != nil {
		return fmt.Errorf("mcp: %s: %w", method, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("mcp: %s: server returned %d", method, resp.StatusCode)
	}
	return nil
}

func (h *mcpHTTP) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	h.nextID++
	id := h.nextID
	resp, err := h.post(ctx, map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return nil, fmt.Errorf("mcp: %s: %w", method, err)
	}
	defer resp.Body.Close()
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		h.session = sid
	}
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return nil, fmt.Errorf("mcp: %s: server returned %d: %s", method, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	want := fmt.Sprint(id)
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 8<<20)
		for sc.Scan() {
			data, ok := strings.CutPrefix(sc.Text(), "data:")
			if !ok {
				continue
			}
			var r mcpReply
			if json.Unmarshal([]byte(strings.TrimSpace(data)), &r) == nil && string(r.ID) == want {
				return r.result(method)
			}
		}
		if err := sc.Err(); err != nil {
			return nil, fmt.Errorf("mcp: %s: %w", method, err)
		}
		return nil, fmt.Errorf("mcp: %s: the event stream ended without a reply", method)
	}
	var r mcpReply
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&r); err != nil {
		return nil, fmt.Errorf("mcp: %s: unreadable reply: %w", method, err)
	}
	return r.result(method)
}
