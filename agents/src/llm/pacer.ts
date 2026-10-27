// Client-side request pacing: at most rpm requests per minute, spaced
// evenly, so a run stays under the provider's per-minute limit instead of
// bursting into 429s and their long retry delays (Gemini free tier).

export class Pacer {
  private next = 0; // earliest time the next request may start
  private readonly intervalMs: number;

  // rpm 0 turns pacing off.
  constructor(
    rpm: number,
    private readonly now: () => number = Date.now,
  ) {
    this.intervalMs = rpm > 0 ? 60_000 / rpm : 0;
  }

  // reserve books the next request slot and returns how long to wait
  // before sending it.
  reserve(): number {
    const t = this.now();
    const start = Math.max(t, this.next);
    this.next = start + this.intervalMs;
    return start - t;
  }
}
