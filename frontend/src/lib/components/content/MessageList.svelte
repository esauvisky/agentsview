<script lang="ts">
  import { onDestroy } from "svelte";
  import type { Virtualizer } from "@tanstack/virtual-core";
  import { messages } from "../../stores/messages.svelte.js";
  import { ui } from "../../stores/ui.svelte.js";
  import { sessions } from "../../stores/sessions.svelte.js";
  import { createVirtualizer } from "../../virtual/createVirtualizer.svelte.js";
  import MessageContent from "./MessageContent.svelte";
  import CompactBoundaryDivider from "./CompactBoundaryDivider.svelte";
  import SystemBoundaryCard from "../system/SystemBoundaryCard.svelte";
  import ToolCallGroup from "./ToolCallGroup.svelte";
  import type { Message } from "../../api/types.js";
  import {
    buildDisplayItems,
    type DisplayItem,
  } from "../../utils/display-items.js";
  import { filterDisplayItemsByTranscriptMode } from "../../utils/transcript-mode.js";
  import {
    hasVisibleSegments,
  } from "../../utils/content-parser.js";
  import { isSystemMessage } from "../../utils/messages.js";
  import { inSessionSearch } from "../../stores/inSessionSearch.svelte.js";
  import { sessionActivity } from "../../stores/sessionActivity.svelte.js";
  import SessionFindBar from "./SessionFindBar.svelte";
  import { untrack } from "svelte";
  import { liveSession, turnAssistantText, turnToolCalls } from "../../stores/liveSession.svelte.js";
  import type { ProvisionalSegment } from "../../stores/liveSession.svelte.js";
  import { renderMarkdown } from "../../utils/markdown.js";
  import { displayToolName } from "../../utils/toolDisplay.js";
  import { formatDuration } from "../../utils/duration.js";
  import { liveTick } from "../../stores/liveTick.svelte.js";
  import ToolBlock from "./ToolBlock.svelte";

  let containerRef: HTMLDivElement | undefined = $state(undefined);
  let scrollRaf: number | null = $state(null);
  let lastScrollRequest = 0;
  function segmentKey(seg: ProvisionalSegment, index: number): string {
    return seg.kind === "tool" ? seg.tc.itemId : `text-${index}`;
  }

  let baseMessages: Message[] = $derived.by(() =>
    messages.messages.filter((m) => !isSystemMessage(m)),
  );

  let baseDisplayItemsAsc = $derived(
    buildDisplayItems(baseMessages),
  );

  let filteredDisplayItemsAsc = $derived(
    buildDisplayItems(baseMessages, {
      skipToolGrouping: !ui.isBlockVisible("tool"),
    }),
  );

  function isItemVisible(item: DisplayItem): boolean {
    if (item.kind === "tool-group") {
      return true;
    }
    return hasVisibleSegments(item.message, (type) =>
      ui.isBlockVisible(type),
    );
  }

  let normalDisplayItemsAsc = $derived.by(() => {
    if (!ui.hasBlockFilters) return baseDisplayItemsAsc;
    return filteredDisplayItemsAsc.filter(isItemVisible);
  });

  let displayItemsAsc = $derived.by(() => {
    if (ui.transcriptMode === "normal") {
      return normalDisplayItemsAsc;
    }

    if (!ui.hasBlockFilters) {
      return filterDisplayItemsByTranscriptMode(
        baseDisplayItemsAsc,
        "focused",
      );
    }

    return filterDisplayItemsByTranscriptMode(
      filteredDisplayItemsAsc,
      "focused",
      {
        isMessageVisible: (message) =>
          hasVisibleSegments(message, (type) =>
            ui.isBlockVisible(type),
          ),
      },
    ).filter(isItemVisible);
  });

  function itemAt(index: number) {
    if (ui.sortNewestFirst) {
      const mapped = displayItemsAsc.length - 1 - index;
      return displayItemsAsc[mapped];
    }
    return displayItemsAsc[index];
  }

  const virtualizer = createVirtualizer(() => {
    const count = displayItemsAsc.length;
    const el = containerRef ?? null;
    const sid = sessions.activeSessionId ?? "";
    return {
      count,
      getScrollElement: () => el,
      estimateSize: () => 120,
      overscan: 5,
      useAnimationFrameWithResizeObserver: true,
      measureCacheKey: sid,
      getItemKey: (index: number) => {
        const item = itemAt(index);
        if (!item) return `${sid}-${index}`;
        if (item.kind === "tool-group") {
          return `${sid}-tg-${item.ordinals[0]}`;
        }
        return `${sid}-m-${item.message.ordinal}`;
      },
    };
  });

  /** Svelte action: measure element for variable-height virtualizer */
  function measureElement(
    node: HTMLElement,
    virt: Virtualizer<HTMLElement, HTMLElement> | undefined,
  ) {
    virt?.measureElement(node);
    return {
      update(
        nextVirt:
          | Virtualizer<HTMLElement, HTMLElement>
          | undefined,
      ) {
        nextVirt?.measureElement(node);
      },
      destroy() {
        // Cleanup handled by virtualizer
      },
    };
  }

  function publishVisibleTimestamp() {
    const v = virtualizer.instance;
    if (!v) return;
    const items = v.getVirtualItems();
    // Skip overscanned items above the viewport.
    const scrollTop = v.scrollOffset ?? 0;
    for (const vi of items) {
      if (vi.end <= scrollTop) continue;
      const item =
        displayItemsAsc[
          ui.sortNewestFirst
            ? displayItemsAsc.length - 1 - vi.index
            : vi.index
        ];
      if (!item) continue;
      const ts =
        item.kind === "message"
          ? item.message.timestamp
          : item.timestamp;
      if (ts) {
        sessionActivity.firstVisibleTimestamp = ts;
        return;
      }
    }
    sessionActivity.firstVisibleTimestamp = null;
  }

  // Recompute visible timestamp when minimap opens or
  // message content changes (e.g. SSE reload).
  $effect(() => {
    if (ui.vitalsOpen) {
      // Track message array so the effect re-runs after
      // content changes while the minimap is open.
      void messages.messages.length;
      publishVisibleTimestamp();
    }
  });

  function handleScroll() {
    if (!containerRef) return;
    if (scrollRaf !== null) return;
    scrollRaf = requestAnimationFrame(() => {
      scrollRaf = null;
      if (!containerRef) return;
      const items =
        virtualizer.instance?.getVirtualItems() ?? [];
      if (items.length > 0 && messages.hasOlder) {
        const firstVisible = items[0]!.index;
        const lastVisible =
          items[items.length - 1]!.index;
        const threshold = 30;
        if (
          (ui.sortNewestFirst &&
            lastVisible >=
              displayItemsAsc.length - threshold) ||
          (!ui.sortNewestFirst &&
            firstVisible <= threshold)
        ) {
          messages.loadOlder();
        }
      }

      if (ui.vitalsOpen) {
        publishVisibleTimestamp();
      }
    });
  }

  onDestroy(() => {
    if (scrollRaf !== null) {
      cancelAnimationFrame(scrollRaf);
      scrollRaf = null;
    }
  });

  function scrollToDisplayIndex(
    index: number,
    waitFrames: number = 0,
    scrollRetries: number = 0,
    reqId: number = lastScrollRequest,
  ) {
    if (reqId !== lastScrollRequest) return;

    const v = virtualizer.instance;
    if (!v) return;

    // Phase 1: wait up to 5 frames for virtualCount to sync.
    const desiredCount = displayItemsAsc.length;
    const virtualCount = v.options.count;
    if (
      waitFrames < 5 &&
      (virtualCount !== desiredCount || index >= virtualCount)
    ) {
      requestAnimationFrame(() => {
        scrollToDisplayIndex(
          index, waitFrames + 1, 0, reqId,
        );
      });
      return;
    }

    // Phase 2a: item already rendered — use exact measured offset.
    const virtualItems = v.getVirtualItems();
    const isRendered = virtualItems.some(
      (vi) => vi.index === index,
    );
    if (isRendered) {
      const offsetAndAlign =
        v.getOffsetForIndex(index, "start");
      if (offsetAndAlign) {
        const [offset] = offsetAndAlign;
        v.scrollToOffset(
          Math.round(offset),
          { align: "start" },
        );
      }
      return;
    }

    // Phase 2b: item not yet in render window. scrollToIndex
    // scrolls to an estimated position, but TanStack's reconcile
    // loop exits after 1 stable frame — before ResizeObserver
    // measurements (delayed by bumpVersion's setTimeout(0)) have
    // updated the offsets.
    //
    // Retry in 2 frames: by then ResizeObserver + bumpVersion have
    // fired, measurements are updated, and the next attempt either
    // finds the item rendered (for an exact offset scroll) or
    // repeats with a more accurate estimate. Limit to 15 scroll
    // retries (~480 ms) to avoid looping forever.
    v.scrollToIndex(index, { align: "start" });
    if (scrollRetries < 15) {
      requestAnimationFrame(() => {
        requestAnimationFrame(() => {
          scrollToDisplayIndex(
            index, waitFrames, scrollRetries + 1, reqId,
          );
        });
      });
    }
  }

  function raf(): Promise<void> {
    return new Promise((r) => requestAnimationFrame(() => r()));
  }

  async function scrollToOrdinalInternal(ordinal: number) {
    const reqId = ++lastScrollRequest;

    const idxAsc = displayItemsAsc.findIndex((item) =>
      item.ordinals.includes(ordinal),
    );
    if (idxAsc >= 0) {
      const idx = ui.sortNewestFirst
        ? displayItemsAsc.length - 1 - idxAsc
        : idxAsc;
      scrollToDisplayIndex(idx, 0, 0, reqId);
      return;
    }

    await messages.ensureOrdinalLoaded(ordinal);
    if (reqId !== lastScrollRequest) return;

    // Let Svelte re-derive displayItemsAsc and the
    // virtualizer update its count after loading.
    // Two frames: one for Svelte reactivity, one for
    // virtualizer resize observation.
    await raf();
    await raf();
    if (reqId !== lastScrollRequest) return;

    const loadedIdxAsc = displayItemsAsc.findIndex(
      (item) => item.ordinals.includes(ordinal),
    );
    if (loadedIdxAsc < 0) return;
    const loadedIdx = ui.sortNewestFirst
      ? displayItemsAsc.length - 1 - loadedIdxAsc
      : loadedIdxAsc;
    scrollToDisplayIndex(loadedIdx, 0, 0, reqId);
  }

  export function scrollToOrdinal(ordinal: number) {
    void scrollToOrdinalInternal(ordinal);
  }

  export function getDisplayItems(): DisplayItem[] {
    return displayItemsAsc;
  }

  export function getNormalDisplayItems(): DisplayItem[] {
    return normalDisplayItemsAsc;
  }

  let highlightQuery = $derived(
    inSessionSearch.isOpen && inSessionSearch.query.trim().length > 0
      ? inSessionSearch.query
      : "",
  );

  // Show provisional turns only for the active session
  let provisionalTurns = $derived.by(() => {
    if (liveSession.sessionId !== sessions.activeSessionId) return [];
    return liveSession.provisionalTurns;
  });

  // Reconcile provisional turns whenever the persisted transcript changes.
  // untrack() prevents reconcileWithMessages from registering provisionalTurns
  // as a dependency of this effect: it reads provisionalTurns internally, and
  // without untrack() writing to provisionalTurns would re-trigger this effect,
  // causing an infinite update cycle (effect_update_depth_exceeded).
  $effect(() => {
    const msgs = messages.messages;
    untrack(() => liveSession.reconcileWithMessages(msgs));
  });

  // Catch-up reload: when provisional turns drop to zero and a reload was
  // deferred, trigger it now so persisted messages appear.
  $effect(() => {
    if (provisionalTurns.length === 0 && liveSession.deferredReload) {
      liveSession.deferredReload = false;
      messages.reload();
    }
  });

  // Auto-scroll when provisional content changes (new text/tool calls)
  $effect(() => {
    const totalContent = provisionalTurns.reduce(
      (acc, t) => acc + t.segments.length + t.segments.reduce(
        (s, seg) => s + (seg.kind === "text" ? seg.content.length : 1), 0,
      ),
      0,
    );
    void totalContent;
    if (!containerRef || provisionalTurns.length === 0) return;
    const el = containerRef;
    if (ui.sortNewestFirst) {
      // Provisionals at top in newest-first — scroll to top if near top
      if (el.scrollTop < 200) {
        requestAnimationFrame(() => {
          if (containerRef) containerRef.scrollTop = 0;
        });
      }
    } else {
      // Provisionals at bottom in oldest-first — scroll to bottom if near bottom
      const distFromBottom = el.scrollHeight - el.scrollTop - el.clientHeight;
      if (distFromBottom < 200) {
        requestAnimationFrame(() => {
          if (containerRef) containerRef.scrollTop = containerRef.scrollHeight;
        });
      }
    }
  });
</script>

{#if !sessions.activeSessionId}
  <div class="empty-state">
    <div class="empty-icon">
      <svg width="36" height="36" viewBox="0 0 16 16" fill="var(--text-muted)">
        <path d="M14 1a1 1 0 011 1v8a1 1 0 01-1 1h-2.5a2 2 0 00-1.6.8L8 14.333 6.1 11.8a2 2 0 00-1.6-.8H2a1 1 0 01-1-1V2a1 1 0 011-1h12z"/>
      </svg>
    </div>
    <p class="empty-text">Select a session to view messages</p>
  </div>
{:else if messages.loading && messages.messages.length === 0}
  <div class="empty-state">
    <p class="empty-text">Loading messages...</p>
  </div>
{:else}
  <SessionFindBar />
  <div
    class="message-list-scroll layout-{ui.messageLayout}"
    bind:this={containerRef}
    data-session-id={sessions.activeSessionId}
    data-messages-session-id={messages.sessionId}
    data-loaded={!messages.loading}
    onscroll={handleScroll}
  >
    {#if ui.sortNewestFirst && provisionalTurns.length > 0}
      {@render provisionalSection()}
    {/if}
    <div
      style="height: {virtualizer.instance?.getTotalSize() ?? 0}px; width: 100%; position: relative;"
    >
      {#each virtualizer.instance?.getVirtualItems() ?? [] as row (row.key)}
        {@const item = itemAt(row.index)}
        {#if item}
          <!-- svelte-ignore a11y_click_events_have_key_events -->
          <!-- svelte-ignore a11y_no_static_element_interactions -->
          <div
            class="virtual-row"
            class:selected={ui.selectedOrdinal !== null &&
              item.ordinals.includes(ui.selectedOrdinal)}
            data-index={row.index}
            style="position: absolute; top: 0; left: 0; width: 100%; transform: translateY({row.start}px);"
            use:measureElement={virtualizer.instance}
            onclick={() => {
              const sel = window.getSelection();
              if (sel && sel.toString().length > 0) return;
              ui.selectOrdinal(item.ordinals[0]!);
            }}
          >
            {#if item.kind === "tool-group"}
              <ToolCallGroup
                messages={item.messages}
                timestamp={item.timestamp}
                highlightQuery={highlightQuery}
                isCurrentHighlight={item.ordinals.includes(inSessionSearch.currentOrdinal ?? -1)}
              />
            {:else if item.message.is_compact_boundary}
              <CompactBoundaryDivider message={item.message} />
            {:else if item.message.is_system && item.message.source_subtype && item.message.source_subtype !== 'compact_boundary'}
              <SystemBoundaryCard
                subtype={item.message.source_subtype}
                content={item.message.content}
                timestamp={item.message.timestamp}
              />
            {:else}
              <MessageContent
                message={item.message}
                highlightQuery={highlightQuery}
                isCurrentHighlight={inSessionSearch.currentOrdinal === item.message.ordinal}
              />
            {/if}
          </div>
        {/if}
      {/each}
    </div>

    {#if !ui.sortNewestFirst && provisionalTurns.length > 0}
      {@render provisionalSection()}
    {/if}
  </div>
{/if}

{#snippet provisionalSection()}
  <div class="provisional-section">
    {#each provisionalTurns as turn (turn.id)}
      <!-- User message -->
      <div class="provisional-row">
        <div class="prov-message prov-is-user" style:border-left-color="var(--accent-blue)" style:background="var(--user-bg)">
          <div class="prov-message-header">
            <span class="prov-role-icon" style:background="var(--accent-blue)">U</span>
            <span class="prov-role-label" style:color="var(--accent-blue)">User</span>
            {#if turn.imageCount > 0}
              <span class="provisional-meta">{turn.imageCount} image{turn.imageCount > 1 ? "s" : ""}</span>
            {/if}
            <div class="prov-header-meta">
              <span class="live-badge">live</span>
            </div>
          </div>
          {#if turn.userContent}
            <div class="prov-message-body">
              <div class="prov-text-content prov-markdown">{@html renderMarkdown(turn.userContent)}</div>
            </div>
          {/if}
        </div>
      </div>

      <!-- Interleaved assistant text and tool call segments -->
      {#each turn.segments as seg, i (segmentKey(seg, i))}
        {#if seg.kind === "text" && seg.content}
          <div class="provisional-row">
            <div class="prov-message" style:border-left-color="var(--accent-purple)" style:background="var(--assistant-bg)">
              <div class="prov-message-header">
                <span class="prov-role-icon" style:background="var(--accent-purple)">A</span>
                <span class="prov-role-label" style:color="var(--accent-purple)">Assistant</span>
                <div class="prov-header-meta">
                  <span class="live-badge">live</span>
                </div>
              </div>
              <div class="prov-message-body">
                <div class="prov-text-content prov-markdown">{@html renderMarkdown(seg.content)}</div>
                {#if turn.isStreaming && i === turn.segments.length - 1}
                  <span class="stream-cursor"></span>
                {/if}
              </div>
            </div>
          </div>
        {:else if seg.kind === "tool"}
          <div class="provisional-row">
            <ToolBlock
              toolCall={seg.tc.toolCall}
              content=""
              label={displayToolName(seg.tc.toolCall)}
              durationLabel={seg.tc.status === "inProgress"
                ? formatDuration(Math.max(0, liveTick.now - seg.tc.startedAt))
                : undefined}
              isRunning={seg.tc.status === "inProgress"}
            />
          </div>
        {/if}
      {/each}

      <!-- Awaiting response: streaming with no segments yet -->
      {#if turn.isStreaming && turn.segments.length === 0}
        <div class="provisional-row">
          <div class="prov-message" style:border-left-color="var(--accent-purple)" style:background="var(--assistant-bg)">
            <div class="prov-message-header">
              <span class="prov-role-icon" style:background="var(--accent-purple)">A</span>
              <span class="prov-role-label" style:color="var(--accent-purple)">Assistant</span>
              <div class="prov-header-meta">
                <span class="live-badge">live</span>
              </div>
            </div>
            <div class="prov-message-body">
              <span class="stream-cursor"></span>
            </div>
          </div>
        </div>
      {/if}
    {/each}
  </div>
{/snippet}

<style>
  .message-list-scroll {
    flex: 1;
    overflow-y: auto;
    overflow-x: hidden;
    padding: 8px 0;
    overflow-anchor: none;
  }

  .virtual-row {
    padding: 5px 12px;
    overflow-anchor: none;
  }

  .virtual-row.selected > :global(*) {
    outline: 2px solid var(--accent-blue);
    outline-offset: -2px;
    border-radius: var(--radius-md, 6px);
  }

  .empty-state {
    flex: 1;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    color: var(--text-muted);
    gap: 12px;
  }

  .empty-icon {
    opacity: 0.25;
  }

  .empty-text {
    font-size: 14px;
    font-weight: 500;
  }

  /* ── Provisional (live streaming) section ── */
  .provisional-section {
    padding: 0 0 8px;
  }

  .provisional-row {
    padding: 5px 12px;
  }

  /* Provisional message styles (matching MessageContent) */
  .prov-message {
    border-left: 4px solid;
    padding: 14px 20px;
    border-radius: 0 var(--radius-md, 6px) var(--radius-md, 6px) 0;
    opacity: 0.92;
  }

  .prov-message-header {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-bottom: 10px;
  }

  .prov-role-icon {
    width: 22px;
    height: 22px;
    border-radius: 50%;
    display: flex;
    align-items: center;
    justify-content: center;
    font-size: 11px;
    font-weight: 700;
    color: white;
    flex-shrink: 0;
    line-height: 1;
  }

  .prov-role-label {
    font-size: 13px;
    font-weight: 600;
    letter-spacing: 0.01em;
  }

  .prov-header-meta {
    margin-left: auto;
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .prov-message-body {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }

  .prov-text-content {
    font-size: 14px;
    line-height: 1.7;
    color: var(--text-primary);
    word-wrap: break-word;
  }

  /* Markdown prose for provisional messages */
  .prov-markdown :global(p) { margin: 0.5em 0; }
  .prov-markdown :global(p:first-child) { margin-top: 0; }
  .prov-markdown :global(p:last-child) { margin-bottom: 0; }
  .prov-markdown :global(h1),
  .prov-markdown :global(h2),
  .prov-markdown :global(h3),
  .prov-markdown :global(h4),
  .prov-markdown :global(h5),
  .prov-markdown :global(h6) { margin: 0.8em 0 0.4em; line-height: 1.3; font-weight: 600; }
  .prov-markdown :global(h1) { font-size: 1.35em; }
  .prov-markdown :global(h2) { font-size: 1.2em; }
  .prov-markdown :global(h3) { font-size: 1.1em; }
  .prov-markdown :global(a) { color: var(--accent-blue); text-decoration: none; }
  .prov-markdown :global(a:hover) { text-decoration: underline; }
  .prov-markdown :global(code) {
    font-family: var(--font-mono);
    font-size: 0.85em;
    background: var(--bg-inset);
    border: 1px solid var(--border-muted);
    border-radius: 4px;
    padding: 0.15em 0.4em;
  }
  .prov-markdown :global(pre) {
    background: var(--code-bg);
    color: var(--code-text);
    border-radius: var(--radius-md);
    padding: 12px 16px;
    overflow-x: auto;
    margin: 0.5em 0;
  }
  .prov-markdown :global(pre code) { background: none; border: none; padding: 0; font-size: 13px; color: inherit; }
  .prov-markdown :global(blockquote) { border-left: 3px solid var(--border-default); margin: 0.5em 0; padding: 0.3em 1em; color: var(--text-secondary); }
  .prov-markdown :global(ul),
  .prov-markdown :global(ol) { padding-left: 1.6em; margin: 0.5em 0; }
  .prov-markdown :global(li) { margin: 0.2em 0; line-height: 1.65; }
  .prov-markdown :global(hr) { border: none; border-top: 1px solid var(--border-muted); margin: 0.8em 0; }

  /* Live badge */
  @keyframes live-pulse {
    0%, 100% { opacity: 0.7; }
    50% { opacity: 1; }
  }

  .live-badge {
    font-size: 9px;
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--accent-green, #3fb950);
    background: color-mix(in srgb, var(--accent-green, #3fb950) 10%, transparent);
    border: 1px solid color-mix(in srgb, var(--accent-green, #3fb950) 20%, transparent);
    padding: 1px 6px;
    border-radius: 3px;
    animation: live-pulse 2s ease-in-out infinite;
  }

  .provisional-meta {
    font-size: 11px;
    color: var(--text-muted);
  }

  @keyframes stream-blink {
    0%, 100% { opacity: 1; }
    50% { opacity: 0; }
  }

  .stream-cursor {
    display: inline-block;
    width: 8px;
    height: 14px;
    background: var(--accent-purple);
    border-radius: 1px;
    animation: stream-blink 1s ease-in-out infinite;
    vertical-align: text-bottom;
  }

  /* ── Compact layout ── */
  .layout-compact {
    padding: 4px 0;
  }

  .layout-compact .virtual-row {
    padding: 2px 12px;
  }

  .layout-compact :global(.message) {
    padding: 6px 12px;
    border-left-width: 2px;
    border-radius: 0;
  }

  .layout-compact :global(.message-header) {
    margin-bottom: 4px;
    gap: 6px;
  }

  .layout-compact :global(.role-icon) {
    width: 16px;
    height: 16px;
    font-size: 9px;
  }

  .layout-compact :global(.role-label) {
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    font-weight: 700;
  }

  .layout-compact :global(.timestamp),
  .layout-compact :global(.group-timestamp) {
    font-size: 10px;
  }

  .layout-compact :global(.text-content) {
    font-size: 13px;
    line-height: 1.55;
  }

  .layout-compact :global(.message-body) {
    gap: 4px;
  }

  /* ── Stream layout ── */
  .layout-stream {
    padding: 0;
  }

  .layout-stream .virtual-row {
    padding: 0;
  }

  .layout-stream :global(.message) {
    border-left: none;
    border-radius: 0;
    padding: 16px 24px;
  }

  .layout-stream :global(.message.is-user) {
    background: color-mix(
      in srgb,
      var(--accent-blue) 5%,
      transparent
    ) !important;
  }

  .layout-stream :global(.message:not(.is-user)) {
    background: transparent !important;
  }

  .layout-stream :global(.message-header) {
    display: none;
  }

  .layout-stream :global(.text-content) {
    font-size: 14px;
    line-height: 1.75;
  }
</style>
