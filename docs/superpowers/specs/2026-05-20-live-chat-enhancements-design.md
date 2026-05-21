# Live Chat Enhancements Design

Four features to improve the live chat experience: two frontend-only productivity
features and two that surface existing codex connector data.

## Feature 1: Persisted Prompt History

Replace the in-memory `promptHistory` array in `liveSession.svelte.ts` with
localStorage-backed storage so sent messages survive page navigation, browser
refresh, and tab close.

**Storage:**

- Key: `livechat-history:{sessionId}`
- Value: JSON array of strings
- Max 50 entries per session (FIFO eviction — oldest dropped first)
- Deduplicate consecutive identical entries (don't append if last === new)

**Lifecycle:**

- Load from localStorage in `watch(sessionId)` when a session becomes active
- Append after each successful `sendWithImages()` call
- `clear()` resets the in-memory array but does not delete from localStorage —
  history persists across live chat reconnections
- Arrow key navigation in the textarea is unchanged

**Files:** `frontend/src/lib/stores/liveSession.svelte.ts`

## Feature 2: Draft Auto-Save

Auto-save unsent textarea text when navigating away from a session. Restore on
return.

**Storage:**

- Key: `livechat-draft:{sessionId}`
- Value: plain string (text only, no images)

**Lifecycle:**

- Save: debounced 300ms on textarea `input` event
- Restore: on component mount or session change, populate textarea from stored
  draft
- Clear: on successful send, delete from localStorage

**Implementation:** Helper functions (`saveDraft`, `loadDraft`, `clearDraft`)
used directly by `LiveChatPanel.svelte`. No store changes — this is component-
local textarea state.

**Files:** `frontend/src/lib/components/content/LiveChatPanel.svelte`

## Feature 3: Tool Call Detail Expansion

Show tool inputs and outputs inline in the live chat panel. The data already
flows through SSE (`input_json` and `result_content` in `LiveToolCallEvent`) and
is stored in `ProvisionalToolCall.toolCall` — the UI just doesn't render it.

**UI:**

- Each tool call row gets a clickable expand/collapse toggle (chevron icon)
- Collapsed (default): tool name + status badge (current behavior)
- Expanded: parsed tool details in a monospace block:
  - `exec_command`: command string, output truncated at ~500 chars with "show
    more"
  - `apply_patch`: file path, changes content
  - Others: formatted `input_json` + `result_content`
- Max-height with overflow scroll for long output

**No store or backend changes.** Pure UI addition in `LiveChatPanel.svelte`.

**Files:** `frontend/src/lib/components/content/LiveChatPanel.svelte`

## Feature 4: File Diff Preview in Approvals

Show a syntax-colored diff inside `file_change_approval` pending request cards.

**Backend changes:**

The codex `item/fileChange/requestApproval` RPC may include `filePath` and
`changes` fields alongside `reason` and `grantRoot`. Extract these in
`buildPendingRequest()` and add them to the `PendingRequest` struct:

```go
type PendingRequest struct {
    // ... existing fields ...
    FilePath string `json:"file_path,omitempty"`
    Changes  string `json:"changes,omitempty"`
}
```

In `codex_server.go`, expand the file change approval parsing to also extract
`filePath`/`path` and `changes` from the RPC params.

**Frontend changes:**

- Add `file_path?: string` and `changes?: string` to `LivePendingRequest` in
  `client.ts`
- In the file change approval card in `LiveChatPanel.svelte`:
  - Show file path as a header above the diff
  - Render `changes` as a diff view: lines starting with `+` green, `-` red,
    `@@` blue/grey, other lines default
  - Collapsible if diff exceeds 20 lines
  - Fall back to current `body` text if `changes` is empty

**Files:**

- `internal/live/types.go`
- `internal/live/codex_server.go`
- `frontend/src/lib/api/client.ts`
- `frontend/src/lib/components/content/LiveChatPanel.svelte`
