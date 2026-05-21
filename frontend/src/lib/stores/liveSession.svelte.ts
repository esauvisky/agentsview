import * as api from "../api/client.js";
import type {
  LiveAssistantDeltaEvent,
  LiveBlockedEvent,
  LivePendingRequest,
  LiveProcessExitEvent,
  LiveRecreatedEvent,
  LiveRequestResolvedEvent,
  LiveState,
  LiveToolCallEvent,
  LiveTurnCompletedEvent,
} from "../api/client.js";
import type { Message, ToolCall } from "../api/types.js";

export interface ProvisionalToolCall {
  itemId: string;
  toolCall: ToolCall;
  status: string; // "inProgress" | "completed"
  startedAt: number;
}

export interface ProvisionalTurn {
  /** Client-side ID, used to correlate state updates. */
  id: string;
  userContent: string;
  imageCount: number;
  /** Date.now() at the moment send() was called. */
  sentAt: number;
  /** Accumulates via assistant_delta events. */
  assistantText: string;
  /** In-progress tool calls for this turn. */
  toolCalls: ProvisionalToolCall[];
  /**
   * True from when the user message is sent until turn_completed arrives.
   * False after the turn is done streaming (but the turn may still be
   * visible until reconciled with the persisted transcript).
   */
  isStreaming: boolean;
}

class LiveSessionStore {
  sessionId: string | null = $state(null);
  state: LiveState | null = $state(null);
  loading: boolean = $state(false);
  sending: boolean = $state(false);
  stopping: boolean = $state(false);
  actingRequestId: string | null = $state(null);
  pendingRequests: LivePendingRequest[] = $state([]);
  error: string | null = $state(null);
  lastExitError: string | null = $state(null);
  reconnectNotice: string | null = $state(null);

  /**
   * Provisional turns: one per user message sent and not yet reconciled
   * with the persisted transcript. Normally 0–2 elements.
   */
  provisionalTurns: ProvisionalTurn[] = $state([]);

  /**
   * Prompt history for the current session, persisted to localStorage.
   * Most recent entry is at the end. Max 50 entries per session.
   */
  promptHistory: string[] = $state([]);

  /**
   * True when the most recent turn is streaming and has produced no
   * assistant text and no tool calls yet — i.e. the model is "thinking".
   */
  get awaitingResponse(): boolean {
    const last = this.provisionalTurns[this.provisionalTurns.length - 1];
    if (!last || !last.isStreaming) return false;
    return last.assistantText.length === 0 && last.toolCalls.length === 0;
  }

  private es: EventSource | null = null;
  private noticeTimer: ReturnType<typeof setTimeout> | null = null;

  /** Start watching a session for live updates. */
  async watch(sessionId: string) {
    if (this.sessionId === sessionId && this.es) {
      return;
    }
    this.clear();
    this.sessionId = sessionId;
    this.loading = true;
    this.error = null;
    this.loadHistory(sessionId);

    try {
      this.state = await api.getLiveState(sessionId);
      this.pendingRequests = this.state.pending_requests ?? [];
      if (!this.state.enabled) {
        return;
      }
      this.es = api.watchLiveSession(sessionId, {
        onStateChanged: (s) => {
          if (this.sessionId !== sessionId) return;
          this.state = s;
          this.pendingRequests = s.pending_requests ?? [];
          if (!s.turn_active) {
            this.stopping = false;
          }
          if (s.state !== "dead") {
            this.lastExitError = null;
          }
        },
        onAssistantDelta: (ev: LiveAssistantDeltaEvent) => {
          if (this.sessionId !== sessionId) return;
          this.appendDelta(ev.delta ?? "");
        },
        onToolCall: (ev: LiveToolCallEvent) => {
          if (this.sessionId !== sessionId) return;
          this.upsertToolCall(ev);
        },
        onTurnCompleted: (ev: LiveTurnCompletedEvent) => {
          if (this.sessionId !== sessionId) return;
          this.stopping = false;
          this.markTurnCompleted(ev.turn_id);
        },
        onBlocked: (ev: LiveBlockedEvent) => {
          if (this.sessionId !== sessionId) return;
          this.error = ev.reason ?? "Live session is blocked";
        },
        onPendingRequest: (ev: LivePendingRequest) => {
          if (this.sessionId !== sessionId) return;
          this.upsertPendingRequest(ev);
        },
        onPendingRequestResolved: (ev: LiveRequestResolvedEvent) => {
          if (this.sessionId !== sessionId || !ev.id) return;
          this.pendingRequests = this.pendingRequests.filter((r) => r.id !== ev.id);
        },
        onControllerRecreated: (ev: LiveRecreatedEvent) => {
          if (this.sessionId !== sessionId) return;
          this.showReconnectNotice(
            `Live controller restarted${ev.count ? ` (${ev.count})` : ""}`,
          );
        },
        onProcessExit: (ev: LiveProcessExitEvent) => {
          if (this.sessionId !== sessionId) return;
          this.lastExitError = ev.error ?? null;
        },
      });
    } catch (err) {
      if (this.sessionId === sessionId) {
        this.error = err instanceof Error ? err.message : "Failed to open live chat";
      }
    } finally {
      if (this.sessionId === sessionId) {
        this.loading = false;
      }
    }
  }

  /** Send a plain text message. */
  async send(content: string): Promise<void> {
    return this.sendWithImages(content, []);
  }

  /** Send a message optionally with attached images. */
  async sendWithImages(content: string, images: File[]): Promise<void> {
    const sessionId = this.sessionId;
    const trimmed = content.trim();
    if (!sessionId || (!trimmed && images.length === 0)) return;

    const turn = this.beginTurn(trimmed, images.length);
    this.sending = true;
    this.error = null;
    this.lastExitError = null;

    try {
      this.state = await api.sendLiveMessage(sessionId, trimmed, images.length > 0 ? images : undefined);
      if (trimmed) {
        this.appendToHistory(trimmed);
      }
    } catch (err) {
      this.dropTurn(turn.id);
      this.error = err instanceof Error ? err.message : "Failed to send message";
    } finally {
      this.sending = false;
    }
  }

  /** Approve or decline a pending request. */
  async approveRequest(
    requestId: string,
    decision: "accept" | "acceptForSession" | "decline" | "cancel",
  ): Promise<void> {
    const sessionId = this.sessionId;
    if (!sessionId) return;
    this.actingRequestId = requestId;
    this.error = null;
    try {
      this.state = await api.approveLiveRequest(sessionId, requestId, decision);
      this.pendingRequests = this.state.pending_requests ?? [];
    } catch (err) {
      this.error = err instanceof Error ? err.message : "Failed to resolve request";
    } finally {
      this.actingRequestId = null;
    }
  }

  /** Reply to a user-input request. */
  async replyToRequest(
    requestId: string,
    answers: Record<string, string[]>,
  ): Promise<void> {
    const sessionId = this.sessionId;
    if (!sessionId) return;
    this.actingRequestId = requestId;
    this.error = null;
    try {
      this.state = await api.replyLiveRequest(sessionId, requestId, answers);
      this.pendingRequests = this.state.pending_requests ?? [];
    } catch (err) {
      this.error = err instanceof Error ? err.message : "Failed to reply to request";
    } finally {
      this.actingRequestId = null;
    }
  }

  /** Interrupt the current agent turn. */
  async stopCurrentTurn(): Promise<void> {
    const sessionId = this.sessionId;
    if (!sessionId || !this.state?.turn_active) return;
    this.stopping = true;
    this.error = null;
    try {
      this.state = await api.stopLiveTurn(sessionId);
      this.pendingRequests = this.state.pending_requests ?? [];
      if (!this.state.turn_active) {
        this.stopping = false;
      }
    } catch (err) {
      this.stopping = false;
      this.error = err instanceof Error ? err.message : "Failed to stop turn";
    }
  }

  /**
   * Called by MessageList after each transcript update.
   * Removes provisional turns that have been persisted.
   */
  reconcileWithMessages(messages: Message[]) {
    if (this.provisionalTurns.length === 0) return;

    const transcript = messages.filter((m) => !m.is_system);
    let cursor = 0;
    const remaining: ProvisionalTurn[] = [];

    for (const turn of this.provisionalTurns) {
      const userIdx = findMessageAfter(transcript, cursor, "user", turn.sentAt);
      if (userIdx < 0) {
        // Not yet persisted — keep this turn and all subsequent ones.
        remaining.push(turn, ...this.provisionalTurns.slice(this.provisionalTurns.indexOf(turn) + 1));
        break;
      }
      cursor = userIdx + 1;

      const assistantIdx = findMessageAfter(transcript, cursor, "assistant");
      if (assistantIdx < 0 && turn.isStreaming) {
        // User message persisted but assistant response not yet — keep streaming.
        remaining.push(turn, ...this.provisionalTurns.slice(this.provisionalTurns.indexOf(turn) + 1));
        break;
      }
      if (assistantIdx >= 0) {
        cursor = assistantIdx + 1;
      }
      // Turn is fully reconciled: drop it.
    }

    this.provisionalTurns = remaining;
  }

  /** Clear all transient live state (called on session change). */
  clear() {
    this.es?.close();
    this.es = null;
    if (this.noticeTimer) {
      clearTimeout(this.noticeTimer);
      this.noticeTimer = null;
    }
    this.sessionId = null;
    this.state = null;
    this.loading = false;
    this.sending = false;
    this.stopping = false;
    this.actingRequestId = null;
    this.pendingRequests = [];
    this.provisionalTurns = [];
    this.promptHistory = [];
    this.error = null;
    this.lastExitError = null;
    this.reconnectNotice = null;
  }

  // --- private helpers ---

  private beginTurn(content: string, imageCount: number): ProvisionalTurn {
    const turn: ProvisionalTurn = {
      id: crypto.randomUUID(),
      userContent: content,
      imageCount,
      sentAt: Date.now(),
      assistantText: "",
      toolCalls: [],
      isStreaming: true,
    };
    this.provisionalTurns = [...this.provisionalTurns, turn];
    return turn;
  }

  private dropTurn(id: string) {
    this.provisionalTurns = this.provisionalTurns.filter((t) => t.id !== id);
  }

  private appendDelta(delta: string) {
    if (!delta) return;
    const last = this.lastStreamingTurn();
    if (!last) return;
    this.provisionalTurns = this.provisionalTurns.map((t) =>
      t.id === last.id ? { ...t, assistantText: t.assistantText + delta } : t,
    );
  }

  private upsertToolCall(ev: LiveToolCallEvent) {
    const turn = this.lastStreamingTurn();
    if (!turn) return;

    const itemId = ev.item_id ?? ev.tool_call.tool_use_id;
    if (!itemId) return;

    const existing = turn.toolCalls.find((tc) => tc.itemId === itemId);
    const updated: ProvisionalToolCall = {
      itemId,
      toolCall: ev.tool_call as ToolCall,
      status: ev.status ?? "inProgress",
      startedAt: existing?.startedAt ?? Date.now(),
    };

    const nextCalls = turn.toolCalls
      .filter((tc) => tc.itemId !== itemId)
      .concat(updated);

    this.provisionalTurns = this.provisionalTurns.map((t) =>
      t.id === turn.id ? { ...t, toolCalls: nextCalls } : t,
    );
  }

  private markTurnCompleted(turnId?: string) {
    // Mark the streaming turn (matched by server turn_id if provided,
    // otherwise the last streaming turn) as no longer streaming.
    const turn = turnId
      ? this.provisionalTurns.find((t) => {
          // We don't store the server turn_id, so fall back to last streaming.
          return t.isStreaming;
        })
      : this.lastStreamingTurn();
    if (!turn) return;
    this.provisionalTurns = this.provisionalTurns.map((t) =>
      t.id === turn.id ? { ...t, isStreaming: false } : t,
    );
  }

  private lastStreamingTurn(): ProvisionalTurn | undefined {
    for (let i = this.provisionalTurns.length - 1; i >= 0; i--) {
      if (this.provisionalTurns[i]!.isStreaming) {
        return this.provisionalTurns[i];
      }
    }
    return undefined;
  }

  private upsertPendingRequest(req: LivePendingRequest) {
    const next = this.pendingRequests.filter((r) => r.id !== req.id);
    next.push(req);
    this.pendingRequests = next;
  }

  private showReconnectNotice(msg: string) {
    this.reconnectNotice = msg;
    if (this.noticeTimer) clearTimeout(this.noticeTimer);
    this.noticeTimer = setTimeout(() => {
      this.reconnectNotice = null;
      this.noticeTimer = null;
    }, 4000);
  }

  private appendToHistory(prompt: string) {
    const last = this.promptHistory[this.promptHistory.length - 1];
    if (last === prompt) return;
    const next = [...this.promptHistory, prompt];
    if (next.length > 50) next.splice(0, next.length - 50);
    this.promptHistory = next;
    this.saveHistory();
  }

  private loadHistory(sessionId: string) {
    try {
      const raw = localStorage.getItem(`livechat-history:${sessionId}`);
      if (raw) this.promptHistory = JSON.parse(raw);
    } catch {
      // Corrupt data — ignore
    }
  }

  private saveHistory() {
    if (!this.sessionId) return;
    try {
      localStorage.setItem(
        `livechat-history:${this.sessionId}`,
        JSON.stringify(this.promptHistory),
      );
    } catch {
      // Storage full — ignore
    }
  }
}

/** Find the first message of the given role at or after startIdx.
 *  If sentAt is provided, skip messages with timestamps more than 15s before it. */
function findMessageAfter(
  messages: Message[],
  startIdx: number,
  role: "user" | "assistant",
  sentAt?: number,
): number {
  for (let i = startIdx; i < messages.length; i++) {
    const m = messages[i]!;
    if (m.role !== role || m.is_system) continue;
    if (sentAt != null) {
      const ts = Date.parse(m.timestamp);
      if (!Number.isNaN(ts) && ts < sentAt - 15_000) continue;
    }
    return i;
  }
  return -1;
}

export const liveSession = new LiveSessionStore();
