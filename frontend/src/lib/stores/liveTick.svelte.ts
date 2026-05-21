import { sessionTiming } from "./sessionTiming.svelte.js";
import { liveSession } from "./liveSession.svelte.js";

/** Reactive Date.now() that ticks every second while the loaded
 *  session has `running: true` OR while there are in-progress
 *  provisional tool calls streaming. A $derived that reads
 *  `liveTick.now` re-runs on each tick, so running-duration
 *  labels can refresh at 1Hz without a per-component setInterval. */
class LiveTickStore {
  now: number = $state(Date.now());

  private timer: ReturnType<typeof setInterval> | null = null;

  constructor() {
    $effect.root(() => {
      $effect(() => {
        const running = sessionTiming.timing?.running ?? false;
        const hasLiveCalls = liveSession.provisionalTurns.some((t) =>
          t.toolCalls.some((tc) => tc.status !== "completed"),
        );
        const shouldTick = running || hasLiveCalls;
        if (shouldTick) {
          if (this.timer == null) {
            this.now = Date.now();
            this.timer = setInterval(() => {
              this.now = Date.now();
            }, 1000);
          }
        } else if (this.timer != null) {
          clearInterval(this.timer);
          this.timer = null;
          this.now = Date.now();
        }
      });
    });
  }
}

export const liveTick = new LiveTickStore();
