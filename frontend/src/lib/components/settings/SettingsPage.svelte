<script lang="ts">
  import { onMount } from "svelte";
  import { settings } from "../../stores/settings.svelte.js";
  import { ui } from "../../stores/ui.svelte.js";
  import { setAuthToken, getAuthToken, setServerUrl, isRemoteConnection } from "../../api/client.js";
  import AppearanceSettings from "./AppearanceSettings.svelte";
  import AgentDirSettings from "./AgentDirSettings.svelte";
  import TerminalSettings from "./TerminalSettings.svelte";
  import GithubSettings from "./GithubSettings.svelte";
  import RemoteSettings from "./RemoteSettings.svelte";
  import WorktreeMappingSettings from "./WorktreeMappingSettings.svelte";
  import SettingsSection from "./SettingsSection.svelte";

  let authTokenInput: string = $state("");

  onMount(() => {
    authTokenInput = getAuthToken();
    settings.load();
  });

  function handleAuthSubmit() {
    const token = authTokenInput.trim();
    if (!token) return;
    setAuthToken(token);
    window.location.reload();
  }
</script>

<div class="settings-page">
  <div class="settings-header">
    <h2 class="settings-title">Settings</h2>
  </div>

  {#if settings.loading}
    <div class="settings-loading">Loading settings...</div>
  {:else if settings.needsAuth}
    <div class="auth-prompt">
      <h3 class="auth-title">Authentication Required</h3>
      <p class="auth-description">
        This server requires an auth token. Enter the token displayed
        on the server's console or settings page.
      </p>
      <div class="auth-field">
        <input
          class="auth-input"
          type="password"
          placeholder="Paste auth token"
          bind:value={authTokenInput}
          onkeydown={(e) => { if (e.key === "Enter") handleAuthSubmit(); }}
        />
        <button
          class="auth-btn"
          disabled={!authTokenInput.trim()}
          onclick={handleAuthSubmit}
        >
          Authenticate
        </button>
      </div>
      <button
        class="auth-disconnect"
        onclick={() => {
          setAuthToken("");
          setServerUrl("");
          settings.needsAuth = false;
          settings.load();
        }}
      >
        Disconnect and reset
      </button>
    </div>
  {:else if settings.error}
    <div class="settings-error">
      <p>{settings.error}</p>
      {#if isRemoteConnection()}
        <button
          class="auth-disconnect"
          onclick={() => {
            setAuthToken("");
            setServerUrl("");
            window.location.reload();
          }}
        >
          Disconnect and reset
        </button>
      {/if}
    </div>
  {:else}
    <div class="settings-sections">
      <AppearanceSettings />
      <AgentDirSettings />
      <TerminalSettings />
      <WorktreeMappingSettings />
      <GithubSettings />
      <RemoteSettings />

      <SettingsSection title="Live Chat" description="Chat with a running Codex agent in real time.">
        <div class="setting-row">
          <span class="setting-label">Enable live chat</span>
          <button
            class="toggle-btn"
            class:on={settings.liveChatEnabled}
            disabled={settings.saving}
            onclick={() => settings.save({ live_chat_enabled: !settings.liveChatEnabled })}
            role="switch"
            aria-checked={settings.liveChatEnabled}
          >
            <span class="toggle-knob"></span>
          </button>
        </div>
        <p class="setting-hint">
          When enabled, a chat composer appears in the sidebar for Codex sessions.
          Requires the <code>codex</code> CLI on PATH.
        </p>
      </SettingsSection>

      <div class="settings-actions">
        <button
          class="resync-btn"
          onclick={() => (ui.activeModal = "resync")}
        >
          Full Resync
        </button>
        <span class="settings-actions-hint">
          Re-scan all session files from scratch
        </span>
      </div>
    </div>
  {/if}
</div>

<style>
  .settings-page {
    max-width: 640px;
    margin: 0 auto;
    padding: 24px 20px 48px;
  }

  .settings-header {
    margin-bottom: 20px;
  }

  .settings-title {
    font-size: 18px;
    font-weight: 600;
    color: var(--text-primary);
    margin: 0;
  }

  .settings-sections {
    display: flex;
    flex-direction: column;
    gap: 16px;
  }

  .settings-loading,
  .settings-error {
    font-size: 13px;
    color: var(--text-muted);
    padding: 40px 0;
    text-align: center;
  }

  .settings-error {
    color: var(--accent-red, #ef4444);
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 8px;
  }

  .settings-error p {
    margin: 0;
  }

  .settings-actions {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 16px 0 0;
    border-top: 1px solid var(--border-muted);
  }

  .resync-btn {
    height: 30px;
    padding: 0 14px;
    border-radius: var(--radius-sm);
    font-size: 12px;
    font-weight: 500;
    color: var(--text-primary);
    background: var(--bg-inset);
    border: 1px solid var(--border-muted);
    cursor: pointer;
    white-space: nowrap;
    transition: opacity 0.12s;
  }

  .resync-btn:hover {
    opacity: 0.8;
  }

  .settings-actions-hint {
    font-size: 11px;
    color: var(--text-muted);
  }

  .auth-prompt {
    text-align: center;
    padding: 40px 20px;
  }

  .auth-title {
    font-size: 16px;
    font-weight: 600;
    color: var(--text-primary);
    margin: 0 0 8px;
  }

  .auth-description {
    font-size: 13px;
    color: var(--text-muted);
    margin: 0 0 20px;
    max-width: 400px;
    margin-left: auto;
    margin-right: auto;
  }

  .auth-field {
    display: flex;
    gap: 8px;
    justify-content: center;
    max-width: 400px;
    margin: 0 auto;
  }

  .auth-input {
    flex: 1;
    height: 34px;
    padding: 0 12px;
    border-radius: var(--radius-sm);
    font-size: 13px;
    font-family: var(--font-mono, monospace);
    color: var(--text-primary);
    background: var(--bg-inset);
    border: 1px solid var(--border-muted);
  }

  .auth-input:focus {
    outline: none;
    border-color: var(--accent-blue);
  }

  .auth-btn {
    height: 34px;
    padding: 0 16px;
    border-radius: var(--radius-sm);
    font-size: 13px;
    font-weight: 500;
    color: white;
    background: var(--accent-blue);
    border: none;
    cursor: pointer;
    white-space: nowrap;
  }

  .auth-btn:disabled {
    opacity: 0.6;
    cursor: default;
  }

  .auth-btn:hover:not(:disabled) {
    opacity: 0.9;
  }

  .auth-disconnect {
    margin-top: 12px;
    background: none;
    border: none;
    color: var(--text-muted);
    font-size: 12px;
    cursor: pointer;
    text-decoration: underline;
  }

  .auth-disconnect:hover {
    color: var(--text-secondary);
  }

  .setting-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
  }

  .setting-label {
    font-size: 12px;
    font-weight: 500;
    color: var(--text-secondary);
  }

  .setting-hint {
    font-size: 11px;
    color: var(--text-muted);
    margin: 0;
    line-height: 1.5;
  }

  .setting-hint code {
    font-family: var(--font-mono);
    font-size: 10px;
    background: var(--bg-inset);
    border: 1px solid var(--border-muted);
    border-radius: 3px;
    padding: 1px 4px;
  }

  .toggle-btn {
    position: relative;
    width: 36px;
    height: 20px;
    border-radius: 10px;
    border: none;
    background: var(--border-default);
    cursor: pointer;
    transition: background 0.15s;
    flex-shrink: 0;
    padding: 0;
  }

  .toggle-btn.on {
    background: var(--accent-blue);
  }

  .toggle-btn:disabled {
    opacity: 0.5;
    cursor: default;
  }

  .toggle-knob {
    position: absolute;
    top: 2px;
    left: 2px;
    width: 16px;
    height: 16px;
    border-radius: 50%;
    background: white;
    transition: transform 0.15s;
    pointer-events: none;
  }

  .toggle-btn.on .toggle-knob {
    transform: translateX(16px);
  }
</style>
