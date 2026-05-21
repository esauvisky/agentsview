package live

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
)

// CodexAppServerSupported returns true when the codex CLI is on PATH.
func CodexAppServerSupported() bool {
	_, err := exec.LookPath("codex")
	return err == nil
}

// CodexAppServer manages a single long-running "codex app-server" process
// and multiplexes multiple thread sessions over it using JSON-RPC 2.0.
// It is injected (not global) and must be constructed with NewCodexAppServer.
type CodexAppServer struct {
	mu         sync.Mutex
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	pending    map[string]chan rpcResult // keyed by string id
	threads    map[string]map[*codexThread]struct{}
	serverReqs map[string]serverReqMeta // pending requests the server sent to us
	nextID     uint64
	alive      bool
	done       chan struct{}
}

type rpcResult struct {
	Result json.RawMessage
	Err    error
}

type serverReqMeta struct {
	ThreadID string
	Method   string
	RawID    any // original id value for responding
}

// NewCodexAppServer creates a new (not yet started) CodexAppServer.
func NewCodexAppServer() *CodexAppServer {
	return &CodexAppServer{}
}

// ResumeThread attaches to an existing Codex thread and returns a Process.
// The server is started lazily on the first call.
func (s *CodexAppServer) ResumeThread(ctx context.Context, threadID, cwd string) (*codexThread, error) {
	if err := s.ensureStarted(ctx); err != nil {
		return nil, err
	}
	t := &codexThread{
		server:   s,
		threadID: threadID,
		events:   make(chan Event, 128),
		alive:    true,
	}
	s.addThread(t)
	params := map[string]any{"threadId": threadID}
	if cwd != "" {
		params["cwd"] = cwd
	}
	if err := s.call(ctx, "thread/resume", params, nil); err != nil {
		s.removeThread(t)
		t.markDead()
		close(t.events)
		return nil, err
	}
	return t, nil
}

// StartTurn sends a new user turn to the Codex thread.
func (s *CodexAppServer) StartTurn(ctx context.Context, threadID string, input TurnInput) (turnID string, err error) {
	items := make([]map[string]any, 0, 1+len(input.LocalImagePaths))
	if strings.TrimSpace(input.Text) != "" {
		items = append(items, map[string]any{
			"type":          "text",
			"text":          input.Text,
			"text_elements": []any{},
		})
	}
	for _, path := range input.LocalImagePaths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		items = append(items, map[string]any{
			"type": "localImage",
			"path": path,
		})
	}
	params := map[string]any{
		"threadId": threadID,
		"input":    items,
	}
	var resp struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := s.call(ctx, "turn/start", params, &resp); err != nil {
		return "", err
	}
	// Propagate the turn ID to all threads watching this threadID.
	s.mu.Lock()
	for t := range s.threads[threadID] {
		t.setTurnID(resp.Turn.ID)
	}
	s.mu.Unlock()
	return resp.Turn.ID, nil
}

// InterruptTurn asks Codex to stop the given turn.
func (s *CodexAppServer) InterruptTurn(ctx context.Context, threadID, turnID string) error {
	if threadID == "" || turnID == "" {
		return nil
	}
	return s.call(ctx, "turn/interrupt", map[string]any{
		"threadId": threadID,
		"turnId":   turnID,
	}, nil)
}

// ResolveRequest sends a response to a pending server-initiated request.
func (s *CodexAppServer) ResolveRequest(ctx context.Context, threadID, requestID string, result any) error {
	s.mu.Lock()
	meta, ok := s.serverReqs[requestID]
	stdin := s.stdin
	alive := s.alive
	s.mu.Unlock()

	if !alive || stdin == nil {
		return errors.New("codex app server is not running")
	}
	if !ok {
		return fmt.Errorf("unknown pending request %q", requestID)
	}
	if meta.ThreadID != "" && meta.ThreadID != threadID {
		return fmt.Errorf("request %q does not belong to thread %q", requestID, threadID)
	}
	return writeRPC(stdin, map[string]any{
		"jsonrpc": "2.0",
		"id":      meta.RawID,
		"result":  result,
	})
}

// ensureStarted starts the codex app-server process if not already running.
func (s *CodexAppServer) ensureStarted(ctx context.Context) error {
	s.mu.Lock()
	if s.alive && s.stdin != nil {
		s.mu.Unlock()
		return nil
	}

	cmd := exec.Command("codex", "app-server", "--listen", "stdio://")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("codex app-server stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("codex app-server stdout: %w", err)
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("starting codex app-server: %w", err)
	}

	s.cmd = cmd
	s.stdin = stdin
	s.pending = map[string]chan rpcResult{}
	s.threads = map[string]map[*codexThread]struct{}{}
	s.serverReqs = map[string]serverReqMeta{}
	s.done = make(chan struct{})
	s.alive = true
	done := s.done
	s.mu.Unlock()

	go s.readLoop(stdout)
	go s.waitLoop(cmd, done)

	if err := s.call(ctx, "initialize", map[string]any{
		"protocolVersion": 2,
		"clientInfo":      map[string]any{"name": "agentsview", "version": "dev"},
		"capabilities":    map[string]any{},
	}, nil); err != nil {
		s.stop()
		return fmt.Errorf("codex initialize: %w", err)
	}
	return s.notify("initialized", map[string]any{})
}

func (s *CodexAppServer) call(ctx context.Context, method string, params any, out any) error {
	s.mu.Lock()
	stdin := s.stdin
	alive := s.alive
	if !alive || stdin == nil {
		s.mu.Unlock()
		return errors.New("codex app server is not running")
	}
	s.nextID++
	id := s.nextID
	idStr := fmt.Sprintf("%d", id)
	ch := make(chan rpcResult, 1)
	s.pending[idStr] = ch
	done := s.done
	s.mu.Unlock()

	if err := writeRPC(stdin, map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}); err != nil {
		s.mu.Lock()
		delete(s.pending, idStr)
		s.mu.Unlock()
		return err
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return errors.New("codex app server stopped")
	case res := <-ch:
		if res.Err != nil {
			return res.Err
		}
		if out != nil && len(res.Result) > 0 {
			return json.Unmarshal(res.Result, out)
		}
		return nil
	}
}

func (s *CodexAppServer) notify(method string, params any) error {
	s.mu.Lock()
	stdin := s.stdin
	alive := s.alive
	s.mu.Unlock()
	if !alive || stdin == nil {
		return errors.New("codex app server is not running")
	}
	return writeRPC(stdin, map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	})
}

func (s *CodexAppServer) readLoop(stdout io.Reader) {
	dec := json.NewDecoder(stdout)
	for {
		var envelope map[string]json.RawMessage
		if err := dec.Decode(&envelope); err != nil {
			s.failAll(err)
			return
		}
		method := jsonString(envelope["method"])
		idRaw := envelope["id"]

		switch {
		case method != "" && len(idRaw) > 0:
			// Server-initiated request (needs a response from us).
			s.handleServerRequest(rawIDStr(idRaw), rawIDVal(idRaw), method, envelope["params"])
		case method != "":
			// Notification (no response needed).
			s.handleNotification(method, envelope["params"])
		case len(idRaw) > 0:
			// Response to one of our calls.
			s.handleResponse(idRaw, envelope["result"], envelope["error"])
		}
	}
}

func (s *CodexAppServer) waitLoop(cmd *exec.Cmd, done chan struct{}) {
	err := cmd.Wait()

	s.mu.Lock()
	threads := s.drainThreadsLocked()
	s.alive = false
	if s.stdin != nil {
		_ = s.stdin.Close()
		s.stdin = nil
	}
	if s.done == done {
		close(s.done)
		s.done = nil
	}
	s.mu.Unlock()

	s.failAll(err)

	errStr := ""
	if err != nil {
		errStr = err.Error()
	}
	for _, t := range threads {
		t.emit(Event{Type: EventProcessExit, Data: ProcessExitData{Error: errStr}})
		t.markDead()
		close(t.events)
	}
}

func (s *CodexAppServer) failAll(err error) {
	s.mu.Lock()
	pending := s.pending
	s.pending = map[string]chan rpcResult{}
	s.mu.Unlock()
	for _, ch := range pending {
		ch <- rpcResult{Err: err}
	}
}

func (s *CodexAppServer) stop() {
	s.mu.Lock()
	cmd := s.cmd
	s.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func (s *CodexAppServer) handleResponse(idRaw, resultRaw, errRaw json.RawMessage) {
	id := rawIDStr(idRaw)
	s.mu.Lock()
	ch, ok := s.pending[id]
	if ok {
		delete(s.pending, id)
	}
	s.mu.Unlock()
	if !ok {
		return
	}
	if len(errRaw) > 0 && string(errRaw) != "null" {
		ch <- rpcResult{Err: fmt.Errorf("codex app-server error: %s", strings.TrimSpace(string(errRaw)))}
		return
	}
	ch <- rpcResult{Result: resultRaw}
}

func (s *CodexAppServer) handleServerRequest(
	requestID string, rawID any, method string, paramsRaw json.RawMessage,
) {
	threadID := paramThreadID(paramsRaw)
	req := buildPendingRequest(requestID, method, paramsRaw)

	s.mu.Lock()
	s.serverReqs[requestID] = serverReqMeta{ThreadID: threadID, Method: method, RawID: rawID}
	targets := s.snapshotThreadTargets(threadID)
	s.mu.Unlock()

	ev := Event{Type: EventPendingRequest, Data: req}
	for _, t := range targets {
		t.emit(ev)
	}
}

func (s *CodexAppServer) handleNotification(method string, paramsRaw json.RawMessage) {
	threadID := paramThreadID(paramsRaw)
	targets := s.threadTargets(threadID)

	switch method {
	case "item/agentMessage/delta":
		var p struct {
			Delta string `json:"delta"`
		}
		_ = json.Unmarshal(paramsRaw, &p)
		ev := Event{Type: EventAssistantDelta, Data: AssistantDeltaData{Delta: p.Delta}}
		for _, t := range targets {
			t.emit(ev)
		}

	case "item/started", "item/completed":
		ev, ok := toolCallEventFromNotification(method, paramsRaw)
		if !ok {
			return
		}
		for _, t := range targets {
			t.emit(Event{Type: EventToolCall, Data: ev})
		}

	case "turn/completed":
		turnID := paramTurnID(paramsRaw)
		for _, t := range targets {
			t.clearTurnID(turnID)
		}
		ev := Event{Type: EventTurnCompleted, Data: TurnCompletedData{TurnID: turnID}}
		for _, t := range targets {
			t.emit(ev)
		}

	case "serverRequest/resolved":
		var p struct {
			RequestID any `json:"requestId"`
		}
		_ = json.Unmarshal(paramsRaw, &p)
		reqID := fmt.Sprint(p.RequestID)
		s.mu.Lock()
		delete(s.serverReqs, reqID)
		s.mu.Unlock()
		ev := Event{Type: EventRequestResolved, Data: RequestResolvedData{ID: reqID}}
		for _, t := range targets {
			t.emit(ev)
		}
	}
}

func (s *CodexAppServer) addThread(t *codexThread) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.threads[t.threadID] == nil {
		s.threads[t.threadID] = map[*codexThread]struct{}{}
	}
	s.threads[t.threadID][t] = struct{}{}
}

func (s *CodexAppServer) removeThread(t *codexThread) {
	s.mu.Lock()
	defer s.mu.Unlock()
	group := s.threads[t.threadID]
	delete(group, t)
	if len(group) == 0 {
		delete(s.threads, t.threadID)
	}
}

func (s *CodexAppServer) threadTargets(threadID string) []*codexThread {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotThreadTargets(threadID)
}

func (s *CodexAppServer) snapshotThreadTargets(threadID string) []*codexThread {
	group := s.threads[threadID]
	out := make([]*codexThread, 0, len(group))
	for t := range group {
		out = append(out, t)
	}
	return out
}

func (s *CodexAppServer) drainThreadsLocked() []*codexThread {
	var out []*codexThread
	for _, group := range s.threads {
		for t := range group {
			out = append(out, t)
		}
	}
	s.threads = map[string]map[*codexThread]struct{}{}
	return out
}

// --- codexThread (implements TurnProcess) ---

type codexThread struct {
	server   *CodexAppServer
	threadID string
	events   chan Event

	mu     sync.Mutex
	alive  bool
	turnID string
}

func (t *codexThread) Alive() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.alive
}

func (t *codexThread) Events() <-chan Event { return t.events }

func (t *codexThread) Close() error {
	t.server.removeThread(t)
	t.markDead()
	return nil
}

func (t *codexThread) SendTurn(ctx context.Context, input TurnInput) error {
	_, err := t.server.StartTurn(ctx, t.threadID, input)
	return err
}

func (t *codexThread) InterruptTurn(ctx context.Context) error {
	t.mu.Lock()
	turnID := t.turnID
	t.mu.Unlock()
	return t.server.InterruptTurn(ctx, t.threadID, turnID)
}

func (t *codexThread) ResolveRequest(ctx context.Context, requestID string, result any) error {
	return t.server.ResolveRequest(ctx, t.threadID, requestID, result)
}

func (t *codexThread) emit(ev Event) {
	t.mu.Lock()
	alive := t.alive
	t.mu.Unlock()
	if !alive {
		return
	}
	select {
	case t.events <- ev:
	default:
	}
}

func (t *codexThread) markDead() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.alive = false
	t.turnID = ""
}

func (t *codexThread) setTurnID(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.turnID = id
}

func (t *codexThread) clearTurnID(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if id == "" || t.turnID == id {
		t.turnID = ""
	}
}

// --- helpers ---

func writeRPC(w io.Writer, v any) error {
	return json.NewEncoder(w).Encode(v)
}

func jsonString(raw json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

func rawIDStr(raw json.RawMessage) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return strings.TrimSpace(string(raw))
	}
	return fmt.Sprint(v)
}

func rawIDVal(raw json.RawMessage) any {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return strings.TrimSpace(string(raw))
	}
	return v
}

func paramThreadID(raw json.RawMessage) string {
	var p struct {
		ThreadID string `json:"threadId"`
	}
	_ = json.Unmarshal(raw, &p)
	return p.ThreadID
}

func paramTurnID(raw json.RawMessage) string {
	var p struct {
		TurnID string `json:"turnId"`
	}
	_ = json.Unmarshal(raw, &p)
	return p.TurnID
}

func buildPendingRequest(requestID, method string, paramsRaw json.RawMessage) PendingRequest {
	req := PendingRequest{ID: requestID, Kind: PendingRequestUnsupported, Title: method}
	switch method {
	case "item/commandExecution/requestApproval":
		var p struct {
			Command string `json:"command"`
			Cwd     string `json:"cwd"`
			Reason  string `json:"reason"`
		}
		_ = json.Unmarshal(paramsRaw, &p)
		req.Kind = PendingRequestCommand
		req.Title = "Command approval"
		req.Body = p.Reason
		req.Command = p.Command
		req.Cwd = p.Cwd
	case "item/fileChange/requestApproval":
		var p struct {
			Reason    string `json:"reason"`
			GrantRoot string `json:"grantRoot"`
			FilePath  string `json:"filePath"`
			Path      string `json:"path"`
			Changes   string `json:"changes"`
		}
		_ = json.Unmarshal(paramsRaw, &p)
		req.Kind = PendingRequestFileChange
		req.Title = "File change approval"
		req.Body = p.Reason
		req.GrantRoot = p.GrantRoot
		req.FilePath = p.FilePath
		if req.FilePath == "" {
			req.FilePath = p.Path
		}
		req.Changes = p.Changes
	case "item/permissions/requestApproval":
		var p struct {
			Cwd    string `json:"cwd"`
			Reason string `json:"reason"`
		}
		_ = json.Unmarshal(paramsRaw, &p)
		req.Kind = PendingRequestPermissions
		req.Title = "Permissions approval"
		req.Body = p.Reason
		req.Cwd = p.Cwd
	case "item/tool/requestUserInput":
		var p struct {
			Questions []struct {
				ID       string `json:"id"`
				Header   string `json:"header"`
				Question string `json:"question"`
				IsSecret bool   `json:"isSecret"`
				Options  []struct {
					Label string `json:"label"`
				} `json:"options"`
			} `json:"questions"`
		}
		_ = json.Unmarshal(paramsRaw, &p)
		req.Kind = PendingRequestUserInput
		req.Title = "Input requested"
		for _, q := range p.Questions {
			pq := PendingQuestion{
				ID:     q.ID,
				Header: q.Header,
				Prompt: q.Question,
				Secret: q.IsSecret,
			}
			for _, opt := range q.Options {
				pq.Options = append(pq.Options, opt.Label)
			}
			req.Questions = append(req.Questions, pq)
		}
	default:
		req.Body = "This request type is not supported yet."
	}
	return req
}

func toolCallEventFromNotification(method string, paramsRaw json.RawMessage) (ToolCallData, bool) {
	var p struct {
		TurnID string         `json:"turnId"`
		Item   map[string]any `json:"item"`
	}
	if err := json.Unmarshal(paramsRaw, &p); err != nil || len(p.Item) == 0 {
		return ToolCallData{}, false
	}
	itemType, _ := p.Item["type"].(string)
	itemID, _ := p.Item["id"].(string)
	if itemType == "" || itemID == "" {
		return ToolCallData{}, false
	}

	toolName, category := toolIdentity(itemType, p.Item)
	status, _ := p.Item["status"].(string)
	if status == "" {
		if method == "item/completed" {
			status = "completed"
		} else {
			status = "inProgress"
		}
	}

	return ToolCallData{
		ItemID: itemID,
		TurnID: p.TurnID,
		Status: status,
		ToolCall: ToolCall{
			ToolName:      toolName,
			Category:      category,
			ToolUseID:     itemID,
			InputJSON:     toolInputJSON(itemType, p.Item),
			ResultContent: toolResultContent(itemType, p.Item),
		},
	}, true
}

func toolIdentity(itemType string, item map[string]any) (name, category string) {
	switch itemType {
	case "commandExecution":
		return "exec_command", "Bash"
	case "fileChange":
		return "apply_patch", "Edit"
	case "webSearch":
		return "web_search", "Web"
	case "mcpToolCall", "toolCall":
		if n, _ := item["toolName"].(string); n != "" {
			return n, "Tool"
		}
		return itemType, "Tool"
	case "task":
		return "Task", "Task"
	default:
		return itemType, "Other"
	}
}

func toolInputJSON(itemType string, item map[string]any) string {
	switch itemType {
	case "commandExecution":
		return marshalMap(map[string]any{"command": item["command"], "cwd": item["cwd"]})
	case "fileChange":
		return marshalMapKeys(item, "filePath", "path", "grantRoot", "changes", "reason")
	default:
		return marshalMapWithout(item, "id", "type", "status", "result", "resultContent",
			"content", "stdout", "output")
	}
}

func toolResultContent(itemType string, item map[string]any) string {
	switch itemType {
	case "commandExecution":
		return firstStr(item["output"], item["stdout"], item["resultContent"], item["result"])
	case "fileChange":
		return firstStr(item["summary"], item["resultContent"], item["result"])
	default:
		return firstStr(item["resultContent"], item["output"], item["stdout"],
			item["content"], item["result"])
	}
}

func marshalMap(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	s := string(b)
	if s == "null" || s == "{}" || s == "[]" {
		return ""
	}
	return s
}

func marshalMapKeys(src map[string]any, keys ...string) string {
	out := map[string]any{}
	for _, k := range keys {
		if v, ok := src[k]; ok && v != nil {
			out[k] = v
		}
	}
	return marshalMap(out)
}

func marshalMapWithout(src map[string]any, excluded ...string) string {
	skip := make(map[string]struct{}, len(excluded))
	for _, k := range excluded {
		skip[k] = struct{}{}
	}
	out := map[string]any{}
	for k, v := range src {
		if v == nil {
			continue
		}
		if _, ex := skip[k]; ex {
			continue
		}
		out[k] = v
	}
	return marshalMap(out)
}

func firstStr(vals ...any) string {
	for _, v := range vals {
		switch s := v.(type) {
		case string:
			if s != "" {
				return s
			}
		case []byte:
			if len(s) > 0 {
				return string(s)
			}
		default:
			if v == nil {
				continue
			}
			if t := marshalMap(v); t != "" {
				return t
			}
		}
	}
	return ""
}
