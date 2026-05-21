# Live Connect On Selection And Per-Session Drafts Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Resume the live controller as soon as a session is selected, and keep unsent composer drafts and prompt history isolated per session.

**Architecture:** Keep `App.svelte` unchanged at the call site and move the new behavior into `liveSession.watch()`, which will explicitly call `connectLiveSession()` before opening SSE. Move unsent draft ownership into `liveSession` as session-keyed state so `LiveChatPanel.svelte` reads and writes the active session’s draft instead of a single local string, while keeping history navigation transient UI state in the panel.

**Tech Stack:** Svelte 5, TypeScript, Vitest

---

### Task 1: Resume Live Controllers On Session Selection

**Files:**
- Modify: `frontend/src/lib/stores/liveSession.svelte.ts`
- Modify: `frontend/src/lib/stores/liveSession.test.ts`

- [ ] **Step 1: Write the failing store test for connect-on-watch**

```ts
it("connects the live controller before opening the event stream", async () => {
  const { liveSession } = await import("./liveSession.svelte.js");
  const order: string[] = [];

  vi.mocked(api.connectLiveSession).mockImplementation(async () => {
    order.push("connect");
    return {
      enabled: true,
      available: true,
      state: "starting",
      pending_requests: [],
    };
  });
  vi.mocked(api.watchLiveSession).mockImplementation((_sessionId, handlers = {}) => {
    order.push("watch");
    handlers.onStateChanged?.({
      enabled: true,
      available: true,
      state: "live",
      pending_requests: [],
    });
    return { close() {} } as EventSource;
  });

  await liveSession.watch("sess-1");

  expect(api.connectLiveSession).toHaveBeenCalledWith("sess-1");
  expect(order).toEqual(["connect", "watch"]);
  expect(liveSession.state?.state).toBe("live");
});
```

- [ ] **Step 2: Run the targeted store test to verify it fails**

Run: `npm test -- --run src/lib/stores/liveSession.test.ts`
Expected: FAIL because `watch()` still calls `getLiveState()` and never invokes `connectLiveSession()`.

- [ ] **Step 3: Implement connect-before-watch in the store**

```ts
async watch(sessionId: string) {
  if (this.sessionId === sessionId && this.eventSource) {
    return;
  }

  this.clear();
  this.sessionId = sessionId;
  this.loading = true;
  this.error = null;

  try {
    this.state = await api.connectLiveSession(sessionId);
    this.pendingRequests = this.state.pending_requests ?? [];
    if (!this.state.enabled) {
      return;
    }
    this.eventSource = api.watchLiveSession(sessionId, { ...handlers });
  } catch (err) {
    if (this.sessionId === sessionId) {
      this.error =
        err instanceof Error ? err.message : "Failed to open live chat";
    }
  } finally {
    if (this.sessionId === sessionId) {
      this.loading = false;
    }
  }
}
```

- [ ] **Step 4: Re-run the targeted store test**

Run: `npm test -- --run src/lib/stores/liveSession.test.ts`
Expected: PASS

- [ ] **Step 5: Commit the controller-connect slice**

```bash
git add frontend/src/lib/stores/liveSession.svelte.ts frontend/src/lib/stores/liveSession.test.ts
git commit -m "fix: connect live controller on selection"
```

### Task 2: Store Drafts Per Session And Restore Them In The Panel

**Files:**
- Modify: `frontend/src/lib/stores/liveSession.svelte.ts`
- Modify: `frontend/src/lib/stores/liveSession.test.ts`
- Modify: `frontend/src/lib/components/content/LiveChatPanel.svelte`
- Modify: `frontend/src/lib/components/content/LiveChatPanel.test.ts`

- [ ] **Step 1: Write the failing regressions for session-scoped drafts**

```ts
it("stores unsent drafts per session", async () => {
  const { liveSession } = await import("./liveSession.svelte.js");

  liveSession.setDraft("sess-1", "draft one");
  liveSession.setDraft("sess-2", "draft two");

  expect(liveSession.getDraft("sess-1")).toBe("draft one");
  expect(liveSession.getDraft("sess-2")).toBe("draft two");
});
```

```ts
it("restores a distinct unsent draft for each session", async () => {
  liveSession.sessionId = "sess-1";
  liveSession.state = {
    enabled: true,
    available: true,
    state: "live",
    turn_active: false,
    pending_requests: [],
  };

  component = mount(LiveChatPanel, {
    target: document.body,
    props: { sessionId: "sess-1" },
  });
  await tick();

  const textarea = document.querySelector<HTMLTextAreaElement>(".composer-input");
  textarea!.value = "draft one";
  textarea!.dispatchEvent(new Event("input", { bubbles: true }));
  await tick();

  component!.set?.({ sessionId: "sess-2" });
  liveSession.sessionId = "sess-2";
  await tick();
  expect(textarea!.value).toBe("");

  textarea!.value = "draft two";
  textarea!.dispatchEvent(new Event("input", { bubbles: true }));
  await tick();

  component!.set?.({ sessionId: "sess-1" });
  liveSession.sessionId = "sess-1";
  await tick();
  expect(textarea!.value).toBe("draft one");
});
```

- [ ] **Step 2: Run the panel and store tests to verify they fail**

Run: `npm test -- --run src/lib/stores/liveSession.test.ts src/lib/components/content/LiveChatPanel.test.ts`
Expected: FAIL because the store has no per-session draft API and the panel still owns a single local `draft`.

- [ ] **Step 3: Add session-scoped draft helpers to the store**

```ts
private draftsBySession = new Map<string, string>();

getDraft(sessionId: string): string {
  return this.draftsBySession.get(sessionId) ?? "";
}

setDraft(sessionId: string, draft: string) {
  if (!draft) {
    this.draftsBySession.delete(sessionId);
    return;
  }
  this.draftsBySession.set(sessionId, draft);
}
```

- [ ] **Step 4: Bind the panel to the session draft instead of local draft ownership**

```ts
let draft = $derived.by(() => liveSession.getDraft(sessionId));

function updateDraft(value: string) {
  liveSession.setDraft(sessionId, value);
}

async function handleSend() {
  const content = draft.trim();
  if ((!content && queuedImages.length === 0) || !canSend) return;
  const images = queuedImages.map((image) => image.file);
  resetHistoryNavigation();
  liveSession.setDraft(sessionId, "");
  clearQueuedImages();
  await liveSession.sendWithImages(content, images);
}
```

```svelte
<textarea
  bind:this={textareaRef}
  value={draft}
  oninput={(event) =>
    updateDraft((event.currentTarget as HTMLTextAreaElement).value)}
  class="composer-input"
  ...
/>
```

- [ ] **Step 5: Keep history navigation session-scoped**

```ts
if (direction === "up") {
  if (historyIndex == null) {
    draftBeforeHistory = draft;
    historyIndex = history.length - 1;
  } else if (historyIndex > 0) {
    historyIndex -= 1;
  }
  updateDraft(history[historyIndex ?? history.length - 1] ?? draft);
  return true;
}

...

updateDraft(draftBeforeHistory);
```

- [ ] **Step 6: Re-run the focused panel and store tests**

Run: `npm test -- --run src/lib/stores/liveSession.test.ts src/lib/components/content/LiveChatPanel.test.ts`
Expected: PASS

- [ ] **Step 7: Commit the per-session draft slice**

```bash
git add frontend/src/lib/stores/liveSession.svelte.ts frontend/src/lib/stores/liveSession.test.ts frontend/src/lib/components/content/LiveChatPanel.svelte frontend/src/lib/components/content/LiveChatPanel.test.ts
git commit -m "fix: keep live drafts per session"
```

### Task 3: Final Verification And Plan Commit

**Files:**
- Modify: `docs/superpowers/plans/2026-05-20-live-connect-on-selection-and-per-session-drafts.md`
- Modify: `frontend/src/lib/stores/liveSession.svelte.ts`
- Modify: `frontend/src/lib/stores/liveSession.test.ts`
- Modify: `frontend/src/lib/components/content/LiveChatPanel.svelte`
- Modify: `frontend/src/lib/components/content/LiveChatPanel.test.ts`

- [ ] **Step 1: Run final focused verification**

Run: `npm test -- --run src/lib/stores/liveSession.test.ts src/lib/components/content/LiveChatPanel.test.ts`
Expected: PASS

Run: `npm run check`
Expected: PASS

- [ ] **Step 2: Inspect the final diff**

```bash
git diff -- frontend/src/lib/stores/liveSession.svelte.ts frontend/src/lib/stores/liveSession.test.ts frontend/src/lib/components/content/LiveChatPanel.svelte frontend/src/lib/components/content/LiveChatPanel.test.ts docs/superpowers/plans/2026-05-20-live-connect-on-selection-and-per-session-drafts.md
```

Expected: only the connect-on-selection, per-session draft/history ownership, and plan doc changes appear.

- [ ] **Step 3: Commit the saved execution plan**

```bash
git add docs/superpowers/plans/2026-05-20-live-connect-on-selection-and-per-session-drafts.md
git commit -m "docs: add live connect and drafts plan"
```
