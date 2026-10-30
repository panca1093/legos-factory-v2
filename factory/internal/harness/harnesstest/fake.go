// Package harnesstest is a scripted ACP agent: a test binary calls RunIfFakeAgent from TestMain and is then
// spawned as the agent command with ACP_FAKE_AGENT=<scenario> in its environment.
package harnesstest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// RunIfFakeAgent serves the scenario named by ACP_FAKE_AGENT and reports whether it did; callers then exit.
func RunIfFakeAgent() bool {
	scenario := os.Getenv("ACP_FAKE_AGENT")
	if scenario == "" {
		return false
	}
	fakeAgent(scenario)
	return true
}

func fakeAgent(scenario string) {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), 1<<20)
	out := json.NewEncoder(os.Stdout)
	send := func(v map[string]any) { v["jsonrpc"] = "2.0"; _ = out.Encode(v) }
	reply := func(id any, result any) { send(map[string]any{"id": id, "result": result}) }
	chunk := func(text string) {
		send(map[string]any{"method": "session/update", "params": map[string]any{"update": map[string]any{
			"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": text}}}})
	}
	var log []string // requests seen, echoed back in the "echo" scenario
	var promptID any
	for in.Scan() {
		var m struct {
			ID     any             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.Unmarshal(in.Bytes(), &m)
		if m.Method != "initialize" {
			log = append(log, m.Method+" "+string(m.Params))
		}
		switch m.Method {
		case "initialize":
			reply(m.ID, map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{"loadSession": os.Getenv("ACP_FAKE_NOLOAD") == ""}})
		case "session/new":
			if f := os.Getenv("ACP_FAKE_NEWFIXTURE"); f != "" {
				send(fixtureReply(f, "session/new", m.ID))
				continue
			}
			res := map[string]any{"sessionId": "ses_1"}
			if os.Getenv("ACP_FAKE_MODELS") != "" {
				res["models"] = map[string]any{"availableModels": []any{map[string]any{"modelId": "sonnet"}, map[string]any{"modelId": "haiku"}}}
			} else {
				res["configOptions"] = []any{
					map[string]any{"id": "mode", "category": "mode", "options": []any{map[string]any{"value": "build"}}},
					map[string]any{"id": "model", "category": "model", "options": []any{map[string]any{"value": "opencode/free-a"}, map[string]any{"value": "opencode-go/kimi"}}},
				}
			}
			reply(m.ID, res)
			if pidFile := os.Getenv("ACP_FAKE_PIDFILE"); pidFile != "" {
				child := exec.Command("sleep", "60")
				_ = child.Start()
				_ = os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())+" "+strconv.Itoa(child.Process.Pid)), 0o644)
			}
		case "session/load":
			// history replay must not surface as events
			send(map[string]any{"method": "session/update", "params": map[string]any{"update": map[string]any{
				"sessionUpdate": "tool_call", "toolCallId": "old", "title": "old-tool"}}})
			if os.Getenv("ACP_FAKE_LOADFAIL") != "" {
				send(map[string]any{"id": m.ID, "error": map[string]any{"code": -32602, "message": "session not found"}})
				continue
			}
			if f := os.Getenv("ACP_FAKE_NEWFIXTURE"); f != "" {
				send(fixtureReply(f, "session/load", m.ID))
				continue
			}
			reply(m.ID, map[string]any{"configOptions": []any{
				map[string]any{"id": "model", "category": "model", "options": []any{map[string]any{"value": "opencode-go/kimi"}}},
			}})
		case "session/set_config_option", "session/set_model", "authenticate":
			reply(m.ID, map[string]any{})
		case "session/cancel":
			if promptID != nil {
				reply(promptID, map[string]any{"stopReason": "cancelled"})
			}
		case "session/prompt":
			promptID = m.ID
			switch scenario {
			case "echo":
				b, _ := json.Marshal(map[string]any{"log": log, "env": os.Getenv("OPENCODE_CONFIG_CONTENT"), "extra": os.Getenv("EXTRA"), "home": os.Getenv("CODEX_HOME"), "xdg": os.Getenv("XDG_DATA_HOME"),
					"claude": []string{os.Getenv("CLAUDE_CONFIG_DIR"), os.Getenv("IS_SANDBOX"), os.Getenv("ANTHROPIC_DEFAULT_HAIKU_MODEL")}})
				chunk(string(b))
				reply(m.ID, map[string]any{"stopReason": "end_turn"})
			case "replay":
				f, err := os.Open(os.Getenv("ACP_FAKE_FIXTURE"))
				if err != nil {
					panic(err)
				}
				sc := bufio.NewScanner(f)
				sc.Buffer(make([]byte, 1<<20), 1<<20)
				started := false // captures begin with the handshake; replay only what follows the prompt
				for sc.Scan() {
					// fixtures are either bare agent lines or {"dir":..,"msg":..} captures
					var line struct {
						Dir string          `json:"dir"`
						Msg json.RawMessage `json:"msg"`
					}
					raw := json.RawMessage(sc.Bytes())
					if json.Unmarshal(sc.Bytes(), &line) != nil || line.Dir == "" {
						started = true
					} else {
						if strings.HasPrefix(line.Dir, "client") && strings.Contains(string(line.Msg), "session/prompt") {
							started = true
						}
						if !started || !strings.HasPrefix(line.Dir, "agent") {
							continue
						}
						raw = line.Msg
					}
					var msg map[string]any
					_ = json.Unmarshal(raw, &msg)
					_, isCall := msg["method"]
					if _, hasID := msg["id"]; hasID && !isCall {
						msg["id"] = m.ID
					}
					_ = out.Encode(msg)
					if isCall && msg["id"] != nil {
						in.Scan()
						if p := os.Getenv("ACP_FAKE_REPLYLOG"); p != "" {
							f, _ := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
							fmt.Fprintln(f, in.Text())
							f.Close()
						}
					}
				}
			case "build":
				// edits a file in its working directory, like a builder would
				_ = os.WriteFile("built.txt", []byte("built by fake agent\n"), 0o644)
				send(map[string]any{"method": "session/update", "params": map[string]any{"update": map[string]any{
					"sessionUpdate": "tool_call", "toolCallId": "t1", "title": "write built.txt", "status": "pending"}}})
				reply(m.ID, map[string]any{"stopReason": "end_turn", "usage": map[string]any{"inputTokens": 11, "outputTokens": 7}})
			case "permission":
				send(map[string]any{"id": "perm-1", "method": "session/request_permission", "params": map[string]any{
					"toolCall": map[string]any{"toolCallId": "t9", "title": "x", "kind": os.Getenv("ACP_FAKE_KIND"), "locations": []any{map[string]any{"path": os.Getenv("ACP_FAKE_PATH")}}},
					"options": []any{
						map[string]any{"optionId": "no", "name": "Reject", "kind": "reject_once"},
						map[string]any{"optionId": "yes", "name": "Allow", "kind": "allow_once"},
					},
				}})
				in.Scan()
				var r struct {
					Result struct {
						Outcome struct{ OptionID string } `json:"outcome"`
					} `json:"result"`
				}
				_ = json.Unmarshal(in.Bytes(), &r)
				chunk(r.Result.Outcome.OptionID)
				reply(m.ID, map[string]any{"stopReason": "end_turn"})
			case "quota":
				send(map[string]any{"id": m.ID, "error": map[string]any{"code": -32603, "message": "Go usage limit exceeded"}})
			case "funds":
				send(map[string]any{"id": m.ID, "error": map[string]any{"code": -32603, "message": "Internal error: Upstream request failed: Insufficient account funds"}})
			case "overloaded":
				send(map[string]any{"id": m.ID, "error": map[string]any{"code": -32603, "message": "upstream returned 503 overloaded"}})
			case "refusal":
				reply(m.ID, map[string]any{"stopReason": "refusal"})
			case "crash":
				fmt.Fprintln(os.Stderr, "fatal: provider exploded")
				os.Exit(3)
			case "hang":
			}
		}
	}
}

// fixtureReply returns the agent's response to the first client request for method in a capture, addressed to id.
func fixtureReply(path, method string, id any) map[string]any {
	f, err := os.Open(path)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	var reqID string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var line struct {
			Dir string          `json:"dir"`
			Msg json.RawMessage `json:"msg"`
		}
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(sc.Bytes(), &line) != nil || json.Unmarshal(line.Msg, &msg) != nil {
			continue
		}
		switch {
		case strings.HasPrefix(line.Dir, "client") && msg.Method == method && reqID == "":
			reqID = string(msg.ID)
		case strings.HasPrefix(line.Dir, "agent") && msg.Method == "" && reqID != "" && string(msg.ID) == reqID:
			var out map[string]any
			_ = json.Unmarshal(line.Msg, &out)
			out["id"] = id
			return out
		}
	}
	panic("no " + method + " exchange in " + path)
}
