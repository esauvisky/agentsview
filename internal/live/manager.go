package live

import (
	"context"

	"github.com/wesm/agentsview/internal/db"
)

// SessionLoader resolves a session by ID.
type SessionLoader func(context.Context, string) (*db.Session, error)

// AdapterFactory creates an agent-specific adapter for one session.
type AdapterFactory func(*db.Session) (Adapter, error)

// Manager owns the live-controller registry and dispatches to adapters.
type Manager struct {
	enabled   bool
	load      SessionLoader
	factories map[string]AdapterFactory
	registry  *Registry
}

// NewManager constructs a Manager.
func NewManager(
	enabled bool,
	load SessionLoader,
	factories map[string]AdapterFactory,
) *Manager {
	return &Manager{
		enabled:   enabled,
		load:      load,
		factories: factories,
		registry:  NewRegistry(),
	}
}

// State returns the current snapshot for a session without creating a controller.
func (m *Manager) State(ctx context.Context, sessionID string) (StateView, error) {
	if !m.enabled {
		return StateView{Enabled: false, Available: false}, nil
	}
	session, err := m.load(ctx, sessionID)
	if err != nil {
		return StateView{}, err
	}
	if session == nil {
		return StateView{}, ErrNotFound
	}
	if _, ok := m.factories[string(session.Agent)]; !ok {
		return StateView{
			Enabled:         true,
			Available:       false,
			State:           StateOffline,
			UnsupportedNote: "agent does not support live chat",
		}, nil
	}
	key := Key{Agent: string(session.Agent), SessionID: session.ID}
	if ctrl, ok := m.registry.Get(key); ok {
		return ctrl.Snapshot(), nil
	}
	return StateView{Enabled: true, Available: true, State: StateOffline}, nil
}

// Ensure returns the existing controller or creates a new one.
func (m *Manager) Ensure(ctx context.Context, sessionID string) (*Controller, StateView, error) {
	if !m.enabled {
		return nil, StateView{Enabled: false, Available: false}, ErrDisabled
	}
	session, err := m.load(ctx, sessionID)
	if err != nil {
		return nil, StateView{}, err
	}
	if session == nil {
		return nil, StateView{}, ErrNotFound
	}
	factory, ok := m.factories[string(session.Agent)]
	if !ok {
		view := StateView{
			Enabled:         true,
			Available:       false,
			State:           StateOffline,
			UnsupportedNote: "agent does not support live chat",
		}
		return nil, view, ErrUnsupported
	}
	key := Key{Agent: string(session.Agent), SessionID: session.ID}
	ctrl, existed := m.registry.GetOrCreate(key, func() *Controller {
		adapter, adapterErr := factory(session)
		if adapterErr != nil {
			// Return a controller that will error on first send.
			// The error is propagated through the process start failure.
			adapter = &errorAdapter{err: adapterErr}
		}
		return NewController(Config{Key: key, Adapter: adapter})
	})
	_ = existed
	return ctrl, ctrl.Snapshot(), nil
}

// Send ensures a controller exists and sends one text message.
func (m *Manager) Send(ctx context.Context, sessionID, content string) (StateView, error) {
	return m.SendInput(ctx, sessionID, TurnInput{Text: content})
}

// SendInput ensures a controller exists and sends one structured turn.
func (m *Manager) SendInput(ctx context.Context, sessionID string, input TurnInput) (StateView, error) {
	ctrl, _, err := m.Ensure(ctx, sessionID)
	if err != nil {
		return StateView{}, err
	}
	if err := ctrl.SendInput(ctx, input); err != nil {
		return ctrl.Snapshot(), err
	}
	return ctrl.Snapshot(), nil
}

// Stop interrupts the current turn while keeping the controller alive.
func (m *Manager) Stop(ctx context.Context, sessionID string) (StateView, error) {
	ctrl, _, err := m.Ensure(ctx, sessionID)
	if err != nil {
		return StateView{}, err
	}
	if err := ctrl.Stop(ctx); err != nil {
		return ctrl.Snapshot(), err
	}
	return ctrl.Snapshot(), nil
}

// Respond resolves one pending structured request.
func (m *Manager) Respond(ctx context.Context, sessionID, requestID string, result any) (StateView, error) {
	ctrl, _, err := m.Ensure(ctx, sessionID)
	if err != nil {
		return StateView{}, err
	}
	if err := ctrl.Respond(ctx, requestID, result); err != nil {
		return ctrl.Snapshot(), err
	}
	return ctrl.Snapshot(), nil
}

// Subscribe subscribes to a session's live event stream. The subscription
// is cancelled when ctx is done.
func (m *Manager) Subscribe(ctx context.Context, sessionID string) (<-chan Event, func(), error) {
	ctrl, _, err := m.Ensure(ctx, sessionID)
	if err != nil {
		return nil, nil, err
	}
	ch, unsub := ctrl.Subscribe()
	go func() {
		<-ctx.Done()
		unsub()
	}()
	return ch, unsub, nil
}

// errorAdapter is an Adapter that always fails Start with a fixed error.
// Used when the factory returns an error during GetOrCreate.
type errorAdapter struct{ err error }

func (a *errorAdapter) Start(_ context.Context) (Process, error) { return nil, a.err }
