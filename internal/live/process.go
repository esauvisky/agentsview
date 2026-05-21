package live

import "context"

// Process is the minimal contract for a live agent process.
// All adapters must implement this.
type Process interface {
	// Alive reports whether the process is still running.
	Alive() bool
	// Events returns the channel of incoming events from the process.
	Events() <-chan Event
	// Close tears down the process and releases its resources.
	Close() error
}

// TurnProcess extends Process for structured agents that support
// explicit turn management (e.g. the Codex app-server).
// Agents that implement this get structured send/stop/resolve;
// agents that only implement Process get raw string writes.
type TurnProcess interface {
	Process
	// SendTurn delivers a user turn to the agent.
	SendTurn(ctx context.Context, input TurnInput) error
	// InterruptTurn asks the agent to stop the current turn early.
	InterruptTurn(ctx context.Context) error
	// ResolveRequest responds to a pending structured request.
	ResolveRequest(ctx context.Context, requestID string, result any) error
}

// Adapter starts a live process for one session.
type Adapter interface {
	Start(ctx context.Context) (Process, error)
}
