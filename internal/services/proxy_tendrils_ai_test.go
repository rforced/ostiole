package services

import (
	"fmt"
	"strings"
)

// tdAgents are MCP clients, by the name each gives and the User-Agent it
// sends.
var tdAgents = [][2]string{{"Claude Code", "claude-cli/2.1.0 (external, cli)"}, {"OpenCode", "opencode/1.0.0 ai-sdk/5.0.0 runtime/bun/1.3.0"},
	{"LM Studio", "LM Studio/0.3.30 node/22.12.0"}}

// tdAgentText is what an agent writes into a title, a note or a search,
// on top of what people type.
var tdAgentText = []string{
	"Book the ferry -- call Sam; don't forget",
	"Paint 🎨: select the colour before Friday",
	"Draft the reply:\n\n```\nHi Jo, the 14th works. Bring the receipts.\n```\n\n- [ ] confirm the room",
	"Follow-up from the call (2026-10-19): the quote's \"final\" price is €1,200 -- ask whether delivery is in it.",
	"## Steps\n1. Sand the shelf\n2. Paint it\n3. Hang it 🔨",
}

func (g td) agentText() string { return g.pick(append(tdAgentText, tdProse...)...) }

// mcp is one JSON-RPC message to the MCP endpoint, as a client of the
// 2026-07-28 protocol sends it with its request metadata, or as an older
// client sends it, with its token and headers. A notification has no id.
func (g td) mcp(legacy bool, method string, params map[string]any) tdRequest {
	agent := tdAgents[g.r.IntN(len(tdAgents))]
	msg := map[string]any{"jsonrpc": "2.0", "method": method}
	if !strings.HasPrefix(method, "notifications/") {
		msg["id"] = any(1 + g.r.IntN(500))
		if g.r.IntN(2) == 0 {
			msg["id"] = g.id()
		}
	}
	header := map[string]string{"Authorization": "Bearer tdp_" + g.token(), "Accept": "application/json, text/event-stream",
		"User-Agent": agent[1], "Mcp-Method": method}
	if !legacy {
		if params == nil {
			params = map[string]any{}
		}
		meta, _ := params["_meta"].(map[string]any)
		if meta == nil {
			meta = map[string]any{}
		}
		meta["io.modelcontextprotocol/protocolVersion"] = "2026-07-28"
		meta["io.modelcontextprotocol/clientInfo"] = map[string]any{"name": agent[0], "title": agent[0], "version": "2.1.0"}
		meta["io.modelcontextprotocol/clientCapabilities"] = map[string]any{"roots": map[string]any{"listChanged": true}, "elicitation": map[string]any{}}
		params["_meta"] = meta
		header["MCP-Protocol-Version"] = "2026-07-28"
	}
	if params != nil {
		msg["params"] = params
		if name, ok := params["name"].(string); ok {
			header["Mcp-Name"] = name
		}
	}
	return tdRequest{method: "POST", path: "/api/v1/mcp", body: vwJSON(msg), header: header}
}

func (g td) tool(legacy bool, name string, args map[string]any) tdRequest {
	return g.mcp(legacy, "tools/call", map[string]any{"name": name, "arguments": args})
}

func (g td) minute() string { return g.day() + "T" + g.clock() }

// mcpRequests is a session of an agent's: the start, the tool list and
// every tool, with what each takes.
func (g td) mcpRequests(legacy bool) []tdRequest {
	space, task, other := g.id(), g.id(), g.id()
	agent := tdAgents[g.r.IntN(len(tdAgents))]
	rs := []tdRequest{
		g.mcp(legacy, "server/discover", nil),
		g.mcp(legacy, "notifications/initialized", nil),
		g.mcp(legacy, "tools/list", nil),
		g.mcp(legacy, "ping", nil),
		g.tool(legacy, "list_spaces", map[string]any{}),
		g.tool(legacy, "search_tasks", map[string]any{"query": g.pick(tdSearches...), "kinds": []string{"task", "goal", "list"}, "limit": 10}),
		g.tool(legacy, "get_task", map[string]any{"task_id": task}),
		g.tool(legacy, "list_tasks", map[string]any{"space_id": space, "parent_id": other, "include_done": true, "limit": 100}),
		g.tool(legacy, "get_today", map[string]any{"space_id": space}),
		g.tool(legacy, "get_upcoming", map[string]any{"days": 14, "space_id": space}),
		g.tool(legacy, "create_task", map[string]any{"title": g.title(), "space_id": space, "parent_id": other, "notes": g.agentText(),
			"due": g.day(), "start": g.minute(), "priority": g.r.IntN(4)}),
		g.tool(legacy, "update_task", map[string]any{"task_id": task, "version": 1 + g.r.IntN(40), "title": g.title(), "notes_append": g.agentText(),
			"due": g.minute(), "start": nil, "priority": 2}),
		g.tool(legacy, "update_task", map[string]any{"task_id": task, "version": 3, "due": nil}),
		g.tool(legacy, "complete_task", map[string]any{"task_id": task, "occurrence": g.day()}),
		g.tool(legacy, "undo_completion", map[string]any{"task_id": task, "completion_id": g.id()}),
		g.tool(legacy, "move_task", map[string]any{"task_id": task, "space_id": space, "parent_id": other, "position": "last"}),
		g.tool(legacy, "move_task", map[string]any{"task_id": task, "position": "first"}),
		g.tool(legacy, "move_task", map[string]any{"task_id": task, "top_level": true}),
	}
	if legacy {
		rs = append(rs, g.mcp(true, "initialize", map[string]any{"protocolVersion": "2025-06-18",
			"capabilities": map[string]any{"roots": map[string]any{"listChanged": true}, "sampling": map[string]any{}},
			"clientInfo":   map[string]any{"name": agent[0], "title": agent[0], "version": "0.3.30"}}))
	}
	return rs
}

// aiRequests are the web client's AI calls: the settings, a key, the
// captures and reviews, the providers an admin sets up, the tokens agents
// use.
func (g td) aiRequests() []tdRequest {
	const api = "/api/v1"
	space, task, job := g.id(), g.id(), g.id()
	provider := map[string]any{"kind": g.pick("openai", "anthropic", "openai_compatible"), "label": g.pick("Ollama on the NAS", "Sam's key (work)"),
		"base_url": g.baseURL(), "api_key": g.secret(), "models": map[string]any{"fast": g.model(), "smart": g.model()}, "local": true,
		"structured": true, "max_parallel": 2, "ctx_chars": 32000, "enabled": true, "position": 1,
		"notice": "Runs at home; nothing leaves the house.", "extra": map[string]any{"chat_template_kwargs": map[string]any{"enable_thinking": false},
			"temperature": 0.2}}
	return []tdRequest{
		tdGet(api + "/ai/status"),
		tdGet(api + "/ai/usage" + tdQuery("period", g.pick("month", "day"))),
		tdBody("PUT", api+"/ai/settings", map[string]any{"opted_in": true, "consent_version": 2, "preferred": "instance." + g.id(),
			"journal_level": g.pick("none", "summary"), "review_mode": "weekly", "briefing_enabled": true, "value": "medium"}),
		tdBody("PUT", api+"/ai/keys/"+g.pick("anthropic", "openai"), map[string]any{"key": g.secret()}),
		tdBody("POST", api+"/ai/capture/jabber", map[string]any{"text": g.dump(), "space_ids": []string{space, g.id()},
			"default": map[string]any{"space_id": space, "parent_id": task}, "tz": g.pick(tdZones...), "previous": []string{g.agentText(), g.title()}}),
		tdBody("POST", api+"/ai/capture/break-down", map[string]any{"task_id": task, "hint": g.agentText(), "previous": []string{g.title()},
			"tz": g.pick(tdZones...)}),
		tdBody("POST", api+"/ai/review/weekly", map[string]any{"job_id": job}),
		tdBody("POST", api+"/ai/review/briefing", map[string]any{"job_id": job, "tz": g.pick(tdZones...)}),
		tdGet(api + "/ai/jobs/" + job),
		tdBody("POST", api+"/tasks/similar", map[string]any{"items": []any{map[string]any{"key": "k1", "title": g.title(), "space_id": space},
			map[string]any{"key": "k2", "title": g.title(), "space_id": space}}}),
		tdBody("POST", api+"/tasks", map[string]any{"id": g.id(), "space_id": space, "kind": "task", "title": g.title(),
			"via": map[string]any{"kind": "ai", "feature": g.pick("jabber", "break_down")}, "batch": g.id()}),
		tdBody("PUT", api+"/spaces/"+space+"/ai-policy", map[string]any{"policy": g.pick("allow", "deny", "local_only")}),
		tdBody("PATCH", api+"/admin/ai", map[string]any{"enabled": true, "key_mode": "instance", "user_cap_micros": 5000000,
			"instance_cap_micros": 50000000, "features": map[string]any{"jabber": true, "break_down": true, "weekly_review": false, "briefing": true}}),
		tdBody("POST", api+"/admin/ai/providers", provider),
		tdBody("PATCH", api+"/admin/ai/providers/"+g.id(), map[string]any{"base_url": g.baseURL(), "api_key": g.secret(),
			"models": map[string]any{"fast": g.model()}, "extra": map[string]any{"top_p": 0.9}}),
		tdBody("PATCH", api+"/admin/ai/users/"+g.id(), map[string]any{"cap_micros": 2000000}),
		tdBody("POST", api+"/tokens", map[string]any{"name": g.pick("Claude Code on the laptop", "Sam's agent (read only)"),
			"scope": g.pick("read", "write"), "space_mode": "some", "space_ids": []string{space, g.id()}, "expires_in_days": 90, "ack": true}),
		tdBody("PATCH", api+"/admin/agent-access", map[string]any{"enabled": true, "mcp_access": g.pick("off", "lan", "any")}),
	}
}

// dump is a Jabber capture: a few thousand characters of whatever comes
// to mind, in paragraphs.
func (g td) dump() string {
	var b strings.Builder
	for n := 1000 + g.r.IntN(4700); b.Len() < n; {
		b.WriteString(g.agentText() + "\n\n")
	}
	return b.String()
}

// baseURL is a provider's address: a service's, or one at home by name,
// by address or on this machine.
func (g td) baseURL() string {
	return g.pick("https://llm.example.com/v1", "https://api.example.net", "http://192.0.2.10:11434/v1", "http://nas.home.example:8080",
		"http://ollama.home.example:11434", "https://gateway.example.org/openai/v1/")
}

// model is a provider's model name, with the tags and organisations local
// servers use.
func (g td) model() string {
	return g.pick("llama3.1:8b", "org/qwen3.8", "qwen2.5-coder:32b-instruct-q4_K_M", "gpt-5.1-mini", "meta-llama/Llama-3.3-70B-Instruct",
		"hf.co/example/Qwen3-8B-GGUF:Q4_K_M", "claude-sonnet-5-5")
}

// secret is an API key, a service's or a home server's, which holds
// whatever its owner chose.
func (g td) secret() string {
	return g.pick("sk-proj-"+g.b64url(60), "sk-ant-api03-"+g.b64url(70)+"-AA", fmt.Sprintf("%x", g.bytes(24)), `p@ss'w0rd"; --`, "<Tr0ub4dor&3>")
}
