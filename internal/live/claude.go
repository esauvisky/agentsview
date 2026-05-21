package live

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// ClaudeSupported returns true when the claude CLI is on PATH.
func ClaudeSupported() bool {
	_, err := exec.LookPath("claude")
	return err == nil
}

// NewClaudeAdapter creates a live adapter for a Claude Code session.
func NewClaudeAdapter(sessionID, cwd string) (Adapter, error) {
	if sessionID == "" {
		return nil, errors.New("claude session id is empty")
	}
	return &claudeAdapter{sessionID: sessionID, cwd: cwd}, nil
}

type claudeAdapter struct {
	sessionID string
	cwd       string
}

func (a *claudeAdapter) Start(ctx context.Context) (Process, error) {
	return startClaudeProcess(ctx, a.sessionID, a.cwd)
}

// claudeProcess manages a single claude CLI subprocess.
type claudeProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	events chan Event
	mu     sync.Mutex
	alive  bool
	done   chan struct{}
}

func startClaudeProcess(_ context.Context, sessionID, cwd string) (*claudeProcess, error) {
	args := []string{
		"-p",
		"--resume", sessionID,
		"--output-format=stream-json",
		"--verbose",
		"--input-format=stream-json",
		"--replay-user-messages",
	}

	// Use background context — the process must outlive the HTTP request
	// that triggered Start. Shutdown is managed via Close().
	cmd := exec.Command("claude", args...)
	if cwd != "" {
		cmd.Dir = cwd
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("claude stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, fmt.Errorf("claude stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		stdin.Close()
		return nil, fmt.Errorf("start claude: %w", err)
	}

	p := &claudeProcess{
		cmd:    cmd,
		stdin:  stdin,
		events: make(chan Event, 128),
		alive:  true,
		done:   make(chan struct{}),
	}

	go p.readLoop(stdout)
	go p.waitLoop()

	return p, nil
}

func (p *claudeProcess) Alive() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.alive
}

func (p *claudeProcess) Events() <-chan Event {
	return p.events
}

func (p *claudeProcess) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.alive {
		return nil
	}
	p.alive = false
	p.stdin.Close()
	return p.cmd.Process.Kill()
}

// SendTurn writes a user message to the claude process stdin.
func (p *claudeProcess) SendTurn(ctx context.Context, input TurnInput) error {
	p.mu.Lock()
	if !p.alive {
		p.mu.Unlock()
		return errors.New("claude process is not alive")
	}
	p.mu.Unlock()

	msg := map[string]any{
		"type":    "user",
		"content": input.Text,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}
	data = append(data, '\n')

	_, err = p.stdin.Write(data)
	return err
}

// InterruptTurn sends SIGINT to the claude process to stop the current turn.
func (p *claudeProcess) InterruptTurn(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.alive || p.cmd.Process == nil {
		return nil
	}
	return p.cmd.Process.Signal(os.Interrupt)
}

// ResolveRequest is not directly supported — Claude Code handles permissions
// via its own permission mode. We log and ignore.
func (p *claudeProcess) ResolveRequest(ctx context.Context, requestID string, result any) error {
	log.Printf("claude: ResolveRequest called (id=%s) — not supported in print mode", requestID)
	return nil
}

func (p *claudeProcess) readLoop(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var raw map[string]any
		if err := json.Unmarshal(line, &raw); err != nil {
			continue
		}

		evType, _ := raw["type"].(string)
		subtype, _ := raw["subtype"].(string)

		switch evType {
		case "assistant":
			p.handleAssistantEvent(raw)
		case "content_block_start":
			p.handleContentBlockStart(raw)
		case "content_block_delta":
			p.handleContentBlockDelta(raw)
		case "content_block_stop":
			// no action needed
		case "tool_result":
			p.handleToolResult(raw)
		case "result":
			p.handleResult(raw, subtype)
		case "system":
			if subtype == "init" {
				// Session started — emit state
				p.emit(Event{Type: EventStateChanged, Data: StateView{
					Enabled:   true,
					Available: true,
					State:     StateLive,
				}})
			}
		default:
			if evType != "" {
				log.Printf("claude: unrecognized event type=%s subtype=%s", evType, subtype)
			}
		}
	}
}

func (p *claudeProcess) handleAssistantEvent(raw map[string]any) {
	msg, ok := raw["message"].(map[string]any)
	if !ok {
		return
	}
	content, ok := msg["content"].([]any)
	if !ok {
		return
	}
	for _, block := range content {
		b, ok := block.(map[string]any)
		if !ok {
			continue
		}
		blockType, _ := b["type"].(string)
		switch blockType {
		case "text":
			text, _ := b["text"].(string)
			if text != "" {
				p.emit(Event{Type: EventAssistantDelta, Data: AssistantDeltaData{Delta: text}})
			}
		case "tool_use":
			p.handleToolUseBlock(b, "completed")
		}
	}
}

func (p *claudeProcess) handleContentBlockStart(raw map[string]any) {
	block, ok := raw["content_block"].(map[string]any)
	if !ok {
		return
	}
	blockType, _ := block["type"].(string)
	if blockType == "tool_use" {
		p.handleToolUseBlock(block, "inProgress")
	}
}

func (p *claudeProcess) handleContentBlockDelta(raw map[string]any) {
	delta, ok := raw["delta"].(map[string]any)
	if !ok {
		return
	}
	deltaType, _ := delta["type"].(string)
	switch deltaType {
	case "text_delta":
		text, _ := delta["text"].(string)
		if text != "" {
			p.emit(Event{Type: EventAssistantDelta, Data: AssistantDeltaData{Delta: text}})
		}
	case "input_json_delta":
		// Tool input streaming — we accumulate on the frontend via tool_call updates
	case "thinking_delta":
		text, _ := delta["thinking"].(string)
		if text != "" {
			p.emit(Event{Type: EventAssistantDelta, Data: AssistantDeltaData{Delta: text}})
		}
	}
}

func (p *claudeProcess) handleToolUseBlock(block map[string]any, status string) {
	toolID, _ := block["id"].(string)
	toolName, _ := block["name"].(string)
	if toolID == "" || toolName == "" {
		return
	}

	var inputJSON string
	if inp, ok := block["input"]; ok {
		if b, err := json.Marshal(inp); err == nil {
			inputJSON = string(b)
		}
	}

	category := claudeToolCategory(toolName)

	p.emit(Event{
		Type: EventToolCall,
		Data: ToolCallData{
			ItemID: toolID,
			Status: status,
			ToolCall: ToolCall{
				ToolName:  toolName,
				ToolUseID: toolID,
				Category:  category,
				InputJSON: inputJSON,
			},
		},
	})
}

func (p *claudeProcess) handleToolResult(raw map[string]any) {
	toolUseID, _ := raw["tool_use_id"].(string)
	if toolUseID == "" {
		return
	}
	// Extract result content
	var resultContent string
	if content, ok := raw["content"].([]any); ok {
		var parts []string
		for _, c := range content {
			if block, ok := c.(map[string]any); ok {
				if text, ok := block["text"].(string); ok && text != "" {
					parts = append(parts, text)
				}
			}
		}
		resultContent = strings.Join(parts, "\n")
	} else if content, ok := raw["content"].(string); ok {
		resultContent = content
	}

	p.emit(Event{
		Type: EventToolCall,
		Data: ToolCallData{
			ItemID: toolUseID,
			Status: "completed",
			ToolCall: ToolCall{
				ToolUseID:     toolUseID,
				ResultContent: resultContent,
			},
		},
	})
}

func (p *claudeProcess) handleResult(raw map[string]any, subtype string) {
	p.emit(Event{Type: EventTurnCompleted, Data: TurnCompletedData{}})

	if subtype == "error" {
		errMsg, _ := raw["error"].(string)
		if errMsg == "" {
			errMsg, _ = raw["result"].(string)
		}
		p.emit(Event{Type: EventProcessExit, Data: ProcessExitData{Error: errMsg}})
	}
}

func (p *claudeProcess) waitLoop() {
	err := p.cmd.Wait()
	p.mu.Lock()
	p.alive = false
	p.mu.Unlock()

	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}
	p.emit(Event{Type: EventProcessExit, Data: ProcessExitData{Error: errMsg}})
	close(p.done)
}

func (p *claudeProcess) emit(ev Event) {
	select {
	case p.events <- ev:
	default:
		// Drop if subscriber is too slow
	}
}

// claudeToolCategory maps Claude Code tool names to normalized categories.
func claudeToolCategory(name string) string {
	switch name {
	case "Read":
		return "Read"
	case "Edit":
		return "Edit"
	case "Write", "NotebookEdit":
		return "Write"
	case "Bash":
		return "Bash"
	case "Grep":
		return "Grep"
	case "Glob":
		return "Glob"
	case "Task", "Agent":
		return "Task"
	case "WebFetch", "WebSearch":
		return "Web"
	default:
		if strings.HasPrefix(name, "mcp__") {
			return "Tool"
		}
		return "Tool"
	}
}
