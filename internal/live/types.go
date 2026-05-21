package live

import "errors"

// State is the coarse lifecycle state of a live controller.
type State string

const (
	StateOffline  State = "offline"
	StateStarting State = "starting"
	StateLive     State = "live"
	StateBlocked  State = "blocked"
	StateDegraded State = "degraded"
	StateDead     State = "dead"
)

// Event type constants used on both the internal channel and the SSE wire.
const (
	EventStateChanged       = "state_changed"
	EventRawOutput          = "raw_output"
	EventAssistantDelta     = "assistant_delta"
	EventToolCall           = "tool_call"
	EventTurnCompleted      = "turn_completed"
	EventBlocked            = "blocked"
	EventPendingRequest     = "pending_request"
	EventRequestResolved    = "pending_request_resolved"
	EventControllerRecreate = "controller_recreated"
	EventProcessExit        = "process_exit"
)

var (
	ErrBlocked     = errors.New("live controller is blocked")
	ErrDisabled    = errors.New("live chat disabled")
	ErrNotFound    = errors.New("session not found")
	ErrUnsupported = errors.New("live chat unsupported for session")
)

// Key uniquely identifies one live controller.
type Key struct {
	Agent     string
	SessionID string
}

// Event is a live-controller event. Type is one of the EventXxx constants.
// Data is a typed struct (see below) that also marshals cleanly to JSON for SSE.
type Event struct {
	Type string `json:"type"`
	Data any    `json:"data,omitempty"`
}

// --- Typed event payloads ---

// AssistantDeltaData carries an incremental assistant text chunk.
type AssistantDeltaData struct {
	Delta string `json:"delta"`
}

// ToolCallData carries a live tool-call update.
type ToolCallData struct {
	ItemID   string   `json:"item_id"`
	TurnID   string   `json:"turn_id,omitempty"`
	Status   string   `json:"status,omitempty"`
	ToolCall ToolCall `json:"tool_call"`
}

// TurnCompletedData signals that a turn finished.
type TurnCompletedData struct {
	TurnID string `json:"turn_id,omitempty"`
}

// ProcessExitData carries the error message (if any) when a process exits.
type ProcessExitData struct {
	Error string `json:"error,omitempty"`
}

// RecreatedData carries the restart count when a controller is recreated.
type RecreatedData struct {
	Count int `json:"count"`
}

// BlockedData carries the reason a controller became blocked.
type BlockedData struct {
	Reason string `json:"reason"`
}

// RequestResolvedData signals that a pending request was resolved.
type RequestResolvedData struct {
	ID string `json:"id"`
}

// ToolCall carries identity and content for a single tool invocation.
type ToolCall struct {
	ToolName      string `json:"tool_name"`
	Category      string `json:"category,omitempty"`
	ToolUseID     string `json:"tool_use_id,omitempty"`
	InputJSON     string `json:"input_json,omitempty"`
	ResultContent string `json:"result_content,omitempty"`
}

// TurnInput carries what the user sends in one turn.
type TurnInput struct {
	Text            string   `json:"text,omitempty"`
	LocalImagePaths []string `json:"local_image_paths,omitempty"`
}

// --- Pending request types ---

// PendingRequestKind classifies what kind of approval is needed.
type PendingRequestKind string

const (
	PendingRequestCommand     PendingRequestKind = "command_approval"
	PendingRequestFileChange  PendingRequestKind = "file_change_approval"
	PendingRequestUserInput   PendingRequestKind = "user_input"
	PendingRequestPermissions PendingRequestKind = "permissions_approval"
	PendingRequestUnsupported PendingRequestKind = "unsupported"
)

// PendingQuestion is a single question inside a user-input request.
type PendingQuestion struct {
	ID      string   `json:"id"`
	Header  string   `json:"header,omitempty"`
	Prompt  string   `json:"prompt"`
	Secret  bool     `json:"secret,omitempty"`
	Options []string `json:"options,omitempty"`
}

// PendingRequest represents one pending approval or input request from the agent.
type PendingRequest struct {
	ID        string             `json:"id"`
	Kind      PendingRequestKind `json:"kind"`
	Title     string             `json:"title"`
	Body      string             `json:"body,omitempty"`
	Command   string             `json:"command,omitempty"`
	Cwd       string             `json:"cwd,omitempty"`
	GrantRoot string             `json:"grant_root,omitempty"`
	FilePath  string             `json:"file_path,omitempty"`
	Changes   string             `json:"changes,omitempty"`
	Questions []PendingQuestion  `json:"questions,omitempty"`
}

// StateView is the external snapshot returned by HTTP endpoints.
type StateView struct {
	Enabled         bool             `json:"enabled"`
	Available       bool             `json:"available"`
	State           State            `json:"state,omitempty"`
	TurnActive      bool             `json:"turn_active,omitempty"`
	BlockedReason   string           `json:"blocked_reason,omitempty"`
	RecreatedCount  int              `json:"recreated_count,omitempty"`
	UnsupportedNote string           `json:"unsupported_note,omitempty"`
	PendingRequests []PendingRequest `json:"pending_requests,omitempty"`
}
