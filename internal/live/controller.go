package live

import (
	"context"
	"sync"
)

// Config holds the parameters for a new Controller.
type Config struct {
	Key     Key
	Adapter Adapter
}

type sendReq struct {
	ctx   context.Context
	input TurnInput
	done  chan error
}

// Controller manages the lifecycle of one live process and fans events
// out to all subscribers. Sends are serialised through an internal queue.
type Controller struct {
	mu            sync.Mutex
	key           Key
	state         State
	adapter       Adapter
	proc          Process
	turnProc      TurnProcess // non-nil when proc also implements TurnProcess
	procGen       int
	blockedReason string
	recreateCount int
	activeTurn    bool
	pending       map[string]PendingRequest
	queue         chan sendReq
	subs          map[int]chan Event
	nextSubID     int
}

// NewController constructs a Controller and starts its worker goroutine.
func NewController(cfg Config) *Controller {
	c := &Controller{
		key:     cfg.Key,
		state:   StateOffline,
		adapter: cfg.Adapter,
		queue:   make(chan sendReq),
		pending: map[string]PendingRequest{},
		subs:    map[int]chan Event{},
	}
	go c.run()
	return c
}

// Send queues one user message and blocks until the controller has
// accepted it (i.e. the process has received it).
func (c *Controller) Send(ctx context.Context, msg string) error {
	return c.SendInput(ctx, TurnInput{Text: msg})
}

// SendInput queues one structured user turn and blocks until accepted.
func (c *Controller) SendInput(ctx context.Context, input TurnInput) error {
	c.mu.Lock()
	if c.state == StateBlocked {
		c.mu.Unlock()
		return ErrBlocked
	}
	c.mu.Unlock()

	req := sendReq{ctx: ctx, input: input, done: make(chan error, 1)}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case c.queue <- req:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-req.done:
		return err
	}
}

// Stop asks the agent to interrupt its current turn without tearing down
// the controller. No-op if no turn is active or the process is not a
// TurnProcess.
func (c *Controller) Stop(ctx context.Context) error {
	c.mu.Lock()
	tp := c.turnProc
	active := c.activeTurn
	c.mu.Unlock()
	if !active || tp == nil {
		return nil
	}
	return tp.InterruptTurn(ctx)
}

// Respond resolves one pending structured request on the active process.
func (c *Controller) Respond(ctx context.Context, requestID string, result any) error {
	c.mu.Lock()
	tp := c.turnProc
	c.mu.Unlock()
	if tp == nil {
		return ErrUnsupported
	}
	return tp.ResolveRequest(ctx, requestID, result)
}

// Snapshot returns the current external state without blocking.
func (c *Controller) Snapshot() StateView {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snapshotLocked()
}

// Subscribe adds a subscriber and returns its event channel plus an
// unsubscribe function. The current state is pushed immediately.
func (c *Controller) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 64)
	c.mu.Lock()
	id := c.nextSubID
	c.nextSubID++
	c.subs[id] = ch
	snap := c.snapshotLocked()
	c.mu.Unlock()

	ch <- Event{Type: EventStateChanged, Data: snap}
	return ch, func() {
		c.mu.Lock()
		sub, ok := c.subs[id]
		if ok {
			delete(c.subs, id)
		}
		c.mu.Unlock()
		if ok {
			close(sub)
		}
	}
}

// --- internal ---

func (c *Controller) run() {
	for req := range c.queue {
		req.done <- c.handleSend(req.ctx, req.input)
	}
}

func (c *Controller) handleSend(ctx context.Context, input TurnInput) error {
	c.mu.Lock()
	if c.state == StateBlocked {
		c.mu.Unlock()
		return ErrBlocked
	}
	c.mu.Unlock()

	if err := c.ensureStarted(ctx); err != nil {
		return err
	}

	c.mu.Lock()
	tp := c.turnProc
	proc := c.proc
	c.mu.Unlock()

	if tp != nil {
		if err := tp.SendTurn(ctx, input); err != nil {
			c.transition(StateDead, "")
			return err
		}
		c.setActiveTurn(true)
		c.transition(StateLive, "")
		return nil
	}

	// The process does not support structured turns.
	_ = proc
	return ErrUnsupported
}

func (c *Controller) ensureStarted(ctx context.Context) error {
	c.mu.Lock()
	if c.proc != nil && c.proc.Alive() {
		c.mu.Unlock()
		return nil
	}
	hadPrior := c.proc != nil
	c.state = StateStarting
	c.blockedReason = ""
	c.pending = map[string]PendingRequest{}
	c.mu.Unlock()
	c.publishState()

	proc, err := c.adapter.Start(ctx)
	if err != nil {
		c.transition(StateDead, "")
		return err
	}

	tp, _ := proc.(TurnProcess)

	c.mu.Lock()
	c.proc = proc
	c.turnProc = tp
	c.procGen++
	gen := c.procGen
	if hadPrior {
		c.recreateCount++
	}
	recreateCount := c.recreateCount
	c.state = StateLive
	c.blockedReason = ""
	c.mu.Unlock()
	c.publishState()

	if hadPrior {
		c.publish(Event{Type: EventControllerRecreate, Data: RecreatedData{Count: recreateCount}})
	}
	go c.consumeProcess(gen, proc)
	return nil
}

func (c *Controller) consumeProcess(gen int, proc Process) {
	for ev := range proc.Events() {
		if !c.routeEvent(gen, ev) {
			continue
		}
		c.publish(ev)
	}
	if !proc.Alive() {
		c.setDeadIfCurrent(gen)
	}
}

// routeEvent updates controller state for events that require it.
// Returns false if the event is stale (wrong process generation) and
// should be dropped.
func (c *Controller) routeEvent(gen int, ev Event) bool {
	switch ev.Type {
	case EventBlocked:
		d, ok := ev.Data.(BlockedData)
		if !ok {
			return false
		}
		return c.transitionBlockedIfCurrent(gen, d.Reason)
	case EventPendingRequest:
		req, ok := ev.Data.(PendingRequest)
		if !ok {
			return false
		}
		return c.trackPendingIfCurrent(gen, req)
	case EventRequestResolved:
		d, ok := ev.Data.(RequestResolvedData)
		if !ok {
			return false
		}
		return c.clearPendingIfCurrent(gen, d.ID)
	case EventTurnCompleted:
		return c.setActiveTurnIfCurrent(gen, false)
	case EventProcessExit:
		return c.setDeadIfCurrent(gen)
	}
	// Other events (assistant_delta, tool_call, raw_output, etc.) pass through
	// as long as the generation matches.
	c.mu.Lock()
	current := gen == c.procGen
	c.mu.Unlock()
	return current
}

func (c *Controller) setDeadIfCurrent(gen int) bool {
	c.mu.Lock()
	if gen != c.procGen {
		c.mu.Unlock()
		return false
	}
	c.proc = nil
	c.turnProc = nil
	c.state = StateDead
	c.activeTurn = false
	c.blockedReason = ""
	c.pending = map[string]PendingRequest{}
	c.mu.Unlock()
	c.publishState()
	return true
}

func (c *Controller) trackPendingIfCurrent(gen int, req PendingRequest) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen != c.procGen || req.ID == "" {
		return false
	}
	c.pending[req.ID] = req
	return true
}

func (c *Controller) clearPendingIfCurrent(gen int, requestID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen != c.procGen || requestID == "" {
		return false
	}
	delete(c.pending, requestID)
	return true
}

func (c *Controller) setActiveTurnIfCurrent(gen int, active bool) bool {
	c.mu.Lock()
	if gen != c.procGen {
		c.mu.Unlock()
		return false
	}
	changed := c.activeTurn != active
	c.activeTurn = active
	c.mu.Unlock()
	if changed {
		c.publishState()
	}
	return true
}

func (c *Controller) setActiveTurn(active bool) {
	c.mu.Lock()
	changed := c.activeTurn != active
	c.activeTurn = active
	c.mu.Unlock()
	if changed {
		c.publishState()
	}
}

func (c *Controller) transitionBlockedIfCurrent(gen int, reason string) bool {
	c.mu.Lock()
	if gen != c.procGen {
		c.mu.Unlock()
		return false
	}
	c.state = StateBlocked
	c.blockedReason = reason
	c.mu.Unlock()
	c.publishState()
	return true
}

func (c *Controller) transition(state State, blockedReason string) {
	c.mu.Lock()
	c.state = state
	c.blockedReason = blockedReason
	c.mu.Unlock()
	c.publishState()
}

func (c *Controller) snapshotLocked() StateView {
	reqs := make([]PendingRequest, 0, len(c.pending))
	for _, req := range c.pending {
		reqs = append(reqs, req)
	}
	if len(reqs) == 0 {
		reqs = nil
	}
	return StateView{
		Enabled:         true,
		Available:       true,
		State:           c.state,
		TurnActive:      c.activeTurn,
		BlockedReason:   c.blockedReason,
		RecreatedCount:  c.recreateCount,
		PendingRequests: reqs,
	}
}

func (c *Controller) publishState() {
	c.publish(Event{Type: EventStateChanged, Data: c.Snapshot()})
}

func (c *Controller) publish(ev Event) {
	c.mu.Lock()
	subs := make([]chan Event, 0, len(c.subs))
	for _, ch := range c.subs {
		subs = append(subs, ch)
	}
	c.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- ev:
		default:
		}
	}
}
