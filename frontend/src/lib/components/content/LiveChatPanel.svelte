<script lang="ts">
  import { liveSession } from "../../stores/liveSession.svelte.js";
  import type { LivePendingRequest } from "../../api/client.js";
  import { connectLiveSession } from "../../api/client.js";
  import { formatDuration } from "../../utils/duration.js";
  import { liveTick } from "../../stores/liveTick.svelte.js";

  interface Props {
    sessionId: string;
  }

  let { sessionId }: Props = $props();

  let draft = $state("");
  let historyIndex: number | null = $state(null);
  let savedDraft = $state("");
  let textareaRef: HTMLTextAreaElement | undefined = $state(undefined);
  let fileInputRef: HTMLInputElement | undefined = $state(undefined);
  let queuedImages: Array<{ id: string; file: File; url: string }> = $state([]);

  // Sending state
  let sending = $derived(liveSession.sending);
  let turnActive = $derived(liveSession.state?.turn_active ?? false);

  function stepHistory(dir: "up" | "down") {
    const h = liveSession.promptHistory;
    if (h.length === 0) return;
    if (dir === "up") {
      if (historyIndex === null) {
        savedDraft = draft;
        historyIndex = h.length - 1;
      } else if (historyIndex > 0) {
        historyIndex--;
      } else {
        return;
      }
    } else {
      if (historyIndex === null) return;
      if (historyIndex < h.length - 1) {
        historyIndex++;
      } else {
        historyIndex = null;
        draft = savedDraft;
        return;
      }
    }
    draft = h[historyIndex] ?? draft;
  }

  function handleKeydown(e: KeyboardEvent) {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      void handleSend();
      return;
    }
    if (e.key === "ArrowUp" && !e.shiftKey) {
      const ta = e.currentTarget as HTMLTextAreaElement;
      // Only navigate history when cursor is at position 0 (or empty)
      if (ta.selectionStart === 0 && ta.selectionEnd === 0) {
        e.preventDefault();
        stepHistory("up");
        return;
      }
    }
    if (e.key === "ArrowDown" && !e.shiftKey && historyIndex !== null) {
      e.preventDefault();
      stepHistory("down");
      return;
    }
    // Any non-history key resets history navigation
    if (e.key !== "ArrowUp" && e.key !== "ArrowDown") {
      historyIndex = null;
    }
  }

  function handlePaste(e: ClipboardEvent) {
    const items = e.clipboardData?.items;
    if (!items) return;
    const imageItems = Array.from(items).filter((i) =>
      i.type.startsWith("image/"),
    );
    if (imageItems.length === 0) return;
    e.preventDefault();
    for (const item of imageItems) {
      const file = item.getAsFile();
      if (file) addImage(file);
    }
  }

  function handleDrop(e: DragEvent) {
    e.preventDefault();
    const files = e.dataTransfer?.files;
    if (!files) return;
    for (const file of Array.from(files)) {
      if (file.type.startsWith("image/")) addImage(file);
    }
  }

  function handleDragover(e: DragEvent) {
    e.preventDefault();
  }

  function addImage(file: File) {
    const id = crypto.randomUUID();
    const reader = new FileReader();
    reader.onload = (ev) => {
      const url = ev.target?.result as string;
      queuedImages = [...queuedImages, { id, file, url }];
    };
    reader.readAsDataURL(file);
  }

  function removeImage(id: string) {
    queuedImages = queuedImages.filter((img) => img.id !== id);
  }

  function handleFileInput(e: Event) {
    const input = e.currentTarget as HTMLInputElement;
    for (const file of Array.from(input.files ?? [])) {
      if (file.type.startsWith("image/")) addImage(file);
    }
    input.value = "";
  }

  async function handleSend() {
    const content = draft.trim();
    if ((!content && queuedImages.length === 0) || sending || sessionId !== liveSession.sessionId) return;

    const images = queuedImages.map((q) => q.file);
    draft = "";
    queuedImages = [];
    historyIndex = null;

    await liveSession.sendWithImages(content, images);
  }

  async function handleStop() {
    await liveSession.stopCurrentTurn();
  }

  async function approveRequest(
    req: LivePendingRequest,
    decision: "accept" | "acceptForSession" | "decline" | "cancel",
  ) {
    if (!req.id) return;
    await liveSession.approveRequest(req.id, decision);
  }

  let replyAnswers: Record<string, string> = $state({});

  async function replyToRequest(req: LivePendingRequest) {
    if (!req.id) return;
    const answers: Record<string, string[]> = {};
    for (const q of req.questions ?? []) {
      const key = q.id ?? q.prompt ?? "";
      if (key) answers[key] = [replyAnswers[key] ?? ""];
    }
    await liveSession.replyToRequest(req.id, answers);
    replyAnswers = {};
  }

  function stateLabel(s: string | undefined): string {
    switch (s) {
      case "live": return "live";
      case "starting": return "starting";
      case "blocked": return "blocked";
      case "degraded": return "degraded";
      case "dead": return "dead";
      default: return "offline";
    }
  }

  let statePillClass = $derived.by(() => {
    const s = liveSession.state?.state;
    if (s === "live") return "state-live";
    if (s === "starting") return "state-starting";
    if (s === "blocked" || s === "degraded") return "state-warn";
    if (s === "dead") return "state-dead";
    return "state-offline";
  });

  // Thinking: streaming with no text and no tool calls yet
  let awaitingResponse = $derived(liveSession.awaitingResponse);

  let isOffline = $derived(
    !liveSession.state?.state || liveSession.state.state === "offline" || liveSession.state.state === "dead",
  );

  let connecting = $state(false);

  async function handleConnect() {
    if (connecting || !sessionId) return;
    connecting = true;
    try {
      const state = await connectLiveSession(sessionId);
      liveSession.state = state;
      // Start watching the SSE stream after connect
      void liveSession.watch(sessionId);
    } catch {
      // Errors will show in the state
    } finally {
      connecting = false;
    }
  }

  // Allow send when live/starting, or when offline (first message auto-starts the process)
  let canSend = $derived(
    sessionId === liveSession.sessionId &&
    !sending &&
    liveSession.state?.state !== "blocked",
  );

  let placeholderText = $derived.by(() => {
    if (awaitingResponse) return "Thinking...";
    if (isOffline) return "Send a message to connect...";
    return "Message...";
  });
</script>

<div class="live-panel">
  <!-- Reconnect notice -->
  {#if liveSession.reconnectNotice}
    <div class="banner banner-info">{liveSession.reconnectNotice}</div>
  {/if}

  <!-- Blocked reason -->
  {#if liveSession.state?.blocked_reason}
    <div class="banner banner-warn">Blocked: {liveSession.state.blocked_reason}</div>
  {/if}

  <!-- Error -->
  {#if liveSession.error}
    <div class="banner banner-error">{liveSession.error}</div>
  {/if}

  <!-- Process exit error -->
  {#if liveSession.lastExitError}
    <div class="banner banner-error">Process exited: {liveSession.lastExitError}</div>
  {/if}

  <!-- Pending requests -->
  {#each liveSession.pendingRequests as req (req.id)}
    <div class="pending-card">
      {#if req.title}
        <div class="pending-title">{req.title}</div>
      {/if}
      {#if req.body}
        <div class="pending-body">{req.body}</div>
      {/if}
      {#if req.command}
        <div class="pending-command">{req.command}</div>
      {/if}

      {#if req.kind === "command_approval" || req.kind === "file_change_approval"}
        <div class="pending-actions">
          <button
            class="pending-btn pending-btn-primary"
            disabled={liveSession.actingRequestId === req.id}
            onclick={() => approveRequest(req, "accept")}
          >
            Allow
          </button>
          <button
            class="pending-btn pending-btn-secondary"
            disabled={liveSession.actingRequestId === req.id}
            onclick={() => approveRequest(req, "acceptForSession")}
          >
            Always allow
          </button>
          <button
            class="pending-btn pending-btn-danger"
            disabled={liveSession.actingRequestId === req.id}
            onclick={() => approveRequest(req, "decline")}
          >
            Deny
          </button>
        </div>
      {:else if req.kind === "user_input"}
        <div class="pending-inputs">
          {#each req.questions ?? [] as q}
            {@const key = q.id ?? q.prompt ?? ""}
            <div class="pending-question">
              {#if q.prompt}
                <label class="pending-q-label">{q.prompt}</label>
              {/if}
              <input
                class="pending-q-input"
                type="text"
                bind:value={replyAnswers[key]}
              />
            </div>
          {/each}
          <div class="pending-actions">
            <button
              class="pending-btn pending-btn-primary"
              disabled={liveSession.actingRequestId === req.id}
              onclick={() => replyToRequest(req)}
            >
              Reply
            </button>
          </div>
        </div>
      {/if}
    </div>
  {/each}

  <!-- Image strip -->
  {#if queuedImages.length > 0}
    <div class="image-strip">
      {#each queuedImages as img (img.id)}
        <div class="image-chip">
          <img src={img.url} alt="queued" class="image-thumb" />
          <button
            class="image-remove"
            title="Remove image"
            onclick={() => removeImage(img.id)}
          >
            &times;
          </button>
        </div>
      {/each}
    </div>
  {/if}

  <!-- Connect button when offline -->
  {#if isOffline && !connecting}
    <button
      class="connect-btn"
      onclick={handleConnect}
    >
      <svg width="12" height="12" viewBox="0 0 16 16" fill="currentColor">
        <path d="M11.534 7h3.932a.25.25 0 01.192.41l-1.966 2.36a.25.25 0 01-.384 0l-1.966-2.36a.25.25 0 01.192-.41zm-11 2h3.932a.25.25 0 00.192-.41L2.692 6.23a.25.25 0 00-.384 0L.342 8.59A.25.25 0 00.534 9z"/>
        <path fill-rule="evenodd" d="M8 3c-1.552 0-2.94.707-3.857 1.818a.5.5 0 11-.771-.636A6.002 6.002 0 0113.917 7H12.9A5.002 5.002 0 008 3zM3.1 9a5.002 5.002 0 008.757 2.182.5.5 0 11.771.636A6.002 6.002 0 012.083 9H3.1z"/>
      </svg>
      Connect Session
    </button>
  {/if}
  {#if connecting}
    <div class="connect-status">
      <span class="provisional-spinner"></span>
      Connecting...
    </div>
  {/if}

  <!-- Composer -->
  <div class="composer">
    <textarea
      bind:this={textareaRef}
      class="composer-input"
      placeholder={placeholderText}
      rows={1}
      bind:value={draft}
      disabled={!canSend}
      onkeydown={handleKeydown}
      onpaste={handlePaste}
      ondrop={handleDrop}
      ondragover={handleDragover}
    ></textarea>

    <div class="composer-actions">
      <div class="composer-left">
        <span class="state-pill {statePillClass}">
          {stateLabel(liveSession.state?.state)}
        </span>
        {#if awaitingResponse}
          <span class="thinking-pulse">thinking</span>
        {/if}
      </div>
      <div class="composer-right">
        <!-- Attach image -->
        <input
          bind:this={fileInputRef}
          type="file"
          accept="image/*"
          multiple
          style="display:none"
          onchange={handleFileInput}
        />
        <button
          class="action-btn"
          title="Attach image"
          disabled={!canSend}
          onclick={() => fileInputRef?.click()}
        >
          <svg width="14" height="14" viewBox="0 0 16 16" fill="currentColor">
            <path d="M4.502 9a1.5 1.5 0 100-3 1.5 1.5 0 000 3z"/>
            <path d="M14.002 13a2 2 0 01-2 2h-10a2 2 0 01-2-2V5A2 2 0 012 3a2 2 0 01-2-2h10.5a.5.5 0 010 1H2a1 1 0 00-1 1v8a1 1 0 001 1h10a1 1 0 001-1v-2a.5.5 0 011 0v2z"/>
            <path d="M10.564 8.27 14 12H2.062l2.5-2.5 1.25 1.25 2.75-3.5 2 1z"/>
          </svg>
        </button>

        <!-- Stop button (when turn active) -->
        {#if turnActive}
          <button
            class="action-btn action-btn-stop"
            title="Stop current turn"
            disabled={liveSession.stopping}
            onclick={handleStop}
          >
            <svg width="10" height="10" viewBox="0 0 10 10" fill="currentColor">
              <rect width="10" height="10" rx="1"/>
            </svg>
          </button>
        {/if}

        <!-- Send button -->
        <button
          class="action-btn action-btn-send"
          title="Send message"
          disabled={!canSend || (!draft.trim() && queuedImages.length === 0)}
          onclick={handleSend}
        >
          <svg width="14" height="14" viewBox="0 0 16 16" fill="currentColor">
            <path d="M15.964.686a.5.5 0 00-.65-.65L.767 5.855H.766l-.452.18a.5.5 0 00-.082.887l.41.26.001.002 4.995 3.178 3.178 4.995.002.002.26.41a.5.5 0 00.886-.083l6-15Zm-1.833 1.89L6.637 10.07l-.215-.338a.5.5 0 00-.154-.154l-.338-.215 7.494-7.494 1.178-.471-.63 1.178z"/>
          </svg>
        </button>
      </div>
    </div>
  </div>
</div>

<style>
  .live-panel {
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding: 8px;
    border-top: 1px solid var(--border-muted);
    background: var(--bg-surface);
  }

  /* ── Banners ── */
  .banner {
    font-size: 12px;
    padding: 6px 10px;
    border-radius: var(--radius-sm, 4px);
    border-left: 3px solid;
  }

  .banner-info {
    background: color-mix(in srgb, var(--accent-blue) 10%, transparent);
    border-color: var(--accent-blue);
    color: var(--text-secondary);
  }

  .banner-warn {
    background: color-mix(in srgb, #f0a72b 10%, transparent);
    border-color: #f0a72b;
    color: var(--text-secondary);
  }

  .banner-error {
    background: color-mix(in srgb, var(--accent-red, #f85149) 10%, transparent);
    border-color: var(--accent-red, #f85149);
    color: var(--text-secondary);
  }

  /* ── Pending request cards ── */
  .pending-card {
    background: var(--bg-inset);
    border: 1px solid var(--border-default);
    border-radius: var(--radius-md, 6px);
    padding: 10px 12px;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }

  .pending-title {
    font-size: 12px;
    font-weight: 600;
    color: var(--text-primary);
  }

  .pending-body {
    font-size: 12px;
    color: var(--text-secondary);
  }

  .pending-command {
    font-size: 12px;
    font-family: var(--font-mono);
    background: var(--bg-default);
    border: 1px solid var(--border-muted);
    border-radius: 4px;
    padding: 4px 8px;
    color: var(--text-primary);
    white-space: pre-wrap;
    word-break: break-all;
  }

  .pending-actions {
    display: flex;
    gap: 6px;
    flex-wrap: wrap;
  }

  .pending-btn {
    font-size: 11px;
    font-weight: 500;
    padding: 4px 10px;
    border-radius: var(--radius-sm, 4px);
    border: 1px solid transparent;
    cursor: pointer;
    transition: opacity 0.1s;
  }

  .pending-btn:disabled {
    opacity: 0.5;
    cursor: default;
  }

  .pending-btn-primary {
    background: var(--accent-blue);
    color: white;
  }

  .pending-btn-primary:hover:not(:disabled) {
    opacity: 0.85;
  }

  .pending-btn-secondary {
    background: var(--bg-surface-hover, var(--bg-tertiary));
    color: var(--text-primary);
    border-color: var(--border-default);
  }

  .pending-btn-secondary:hover:not(:disabled) {
    background: var(--bg-tertiary);
  }

  .pending-btn-danger {
    background: color-mix(in srgb, var(--accent-red, #f85149) 15%, transparent);
    color: var(--accent-red, #f85149);
    border-color: var(--accent-red, #f85149);
  }

  .pending-btn-danger:hover:not(:disabled) {
    background: color-mix(in srgb, var(--accent-red, #f85149) 25%, transparent);
  }

  .pending-inputs {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }

  .pending-question {
    display: flex;
    flex-direction: column;
    gap: 3px;
  }

  .pending-q-label {
    font-size: 11px;
    color: var(--text-muted);
  }

  .pending-q-input {
    font-size: 12px;
    padding: 5px 8px;
    border-radius: 4px;
    border: 1px solid var(--border-default);
    background: var(--bg-default);
    color: var(--text-primary);
    outline: none;
  }

  .pending-q-input:focus {
    border-color: var(--accent-blue);
  }

  /* ── Image strip ── */
  .image-strip {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }

  .image-chip {
    position: relative;
    width: 48px;
    height: 48px;
    border-radius: 4px;
    overflow: hidden;
    border: 1px solid var(--border-muted);
    flex-shrink: 0;
  }

  .image-thumb {
    width: 100%;
    height: 100%;
    object-fit: cover;
  }

  .image-remove {
    position: absolute;
    top: 2px;
    right: 2px;
    width: 16px;
    height: 16px;
    border-radius: 50%;
    background: rgba(0, 0, 0, 0.7);
    color: white;
    border: none;
    cursor: pointer;
    font-size: 12px;
    line-height: 1;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 0;
  }

  /* ── Composer ── */
  .composer {
    display: flex;
    flex-direction: column;
    gap: 6px;
    background: var(--bg-inset);
    border: 1px solid var(--border-default);
    border-radius: var(--radius-md, 6px);
    overflow: hidden;
    transition: border-color 0.15s;
  }

  .composer:focus-within {
    border-color: var(--accent-blue);
  }

  .composer-input {
    resize: none;
    border: none;
    outline: none;
    background: transparent;
    color: var(--text-primary);
    font-size: 13px;
    line-height: 1.5;
    padding: 10px 12px 0;
    font-family: inherit;
    max-height: 168px;
    overflow-y: auto;
    field-sizing: content;
  }

  .composer-input::placeholder {
    color: var(--text-muted);
  }

  .composer-input:disabled {
    opacity: 0.6;
    cursor: default;
  }

  .composer-actions {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 4px 8px 6px;
    gap: 6px;
  }

  .composer-left {
    display: flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
  }

  .composer-right {
    display: flex;
    align-items: center;
    gap: 4px;
    flex-shrink: 0;
  }

  /* ── State pill ── */
  .state-pill {
    font-size: 10px;
    font-weight: 600;
    padding: 2px 7px;
    border-radius: 10px;
    letter-spacing: 0.03em;
    text-transform: uppercase;
    white-space: nowrap;
  }

  .state-live {
    background: color-mix(in srgb, #3fb950 15%, transparent);
    color: #3fb950;
  }

  .state-starting {
    background: color-mix(in srgb, #f0a72b 15%, transparent);
    color: #f0a72b;
  }

  .state-warn {
    background: color-mix(in srgb, #f0a72b 15%, transparent);
    color: #f0a72b;
  }

  .state-dead {
    background: color-mix(in srgb, var(--accent-red, #f85149) 15%, transparent);
    color: var(--accent-red, #f85149);
  }

  .state-offline {
    background: var(--bg-tertiary);
    color: var(--text-muted);
  }

  /* ── Thinking indicator ── */
  @keyframes thinking-pulse {
    0%, 100% { opacity: 0.4; }
    50% { opacity: 1; }
  }

  .thinking-pulse {
    font-size: 11px;
    color: var(--text-muted);
    animation: thinking-pulse 1.4s ease-in-out infinite;
  }

  /* ── Action buttons ── */
  .action-btn {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 28px;
    height: 28px;
    border: none;
    border-radius: var(--radius-sm, 4px);
    background: transparent;
    color: var(--text-muted);
    cursor: pointer;
    transition: background 0.1s, color 0.1s;
    flex-shrink: 0;
  }

  .action-btn:hover:not(:disabled) {
    background: var(--bg-surface-hover, var(--bg-tertiary));
    color: var(--text-primary);
  }

  .action-btn:disabled {
    opacity: 0.4;
    cursor: default;
  }

  .action-btn-stop {
    color: var(--accent-red, #f85149);
  }

  .action-btn-stop:hover:not(:disabled) {
    background: color-mix(in srgb, var(--accent-red, #f85149) 15%, transparent);
    color: var(--accent-red, #f85149);
  }

  .action-btn-send {
    color: var(--accent-blue);
  }

  .action-btn-send:hover:not(:disabled) {
    background: color-mix(in srgb, var(--accent-blue) 15%, transparent);
    color: var(--accent-blue);
  }

  .action-btn-send:disabled {
    opacity: 0.3;
  }

  /* ── Connect button ── */
  .connect-btn {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 6px;
    width: 100%;
    height: 32px;
    border: 1px solid var(--accent-blue);
    border-radius: var(--radius-md, 6px);
    background: color-mix(in srgb, var(--accent-blue) 10%, transparent);
    color: var(--accent-blue);
    font-size: 12px;
    font-weight: 500;
    cursor: pointer;
    transition: background 0.12s;
  }

  .connect-btn:hover {
    background: color-mix(in srgb, var(--accent-blue) 18%, transparent);
  }

  .connect-status {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 8px;
    height: 32px;
    font-size: 12px;
    color: var(--text-muted);
  }

  @keyframes provisional-spin {
    to { transform: rotate(360deg); }
  }

  .provisional-spinner {
    display: inline-block;
    width: 12px;
    height: 12px;
    border: 2px solid var(--border-muted);
    border-top-color: var(--accent-blue);
    border-radius: 50%;
    animation: provisional-spin 0.7s linear infinite;
    flex-shrink: 0;
  }
</style>
