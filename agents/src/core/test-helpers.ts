// Shared helpers for unit tests.

import { Events } from "./events.ts";

export interface RecordedEvent {
  seq: number;
  ts: string;
  type: string;
  data: Record<string, unknown>;
}

// recordingEvents returns an Events whose lines are kept in memory.
export function recordingEvents(): { events: Events; recorded: () => RecordedEvent[] } {
  const lines: string[] = [];
  const events = new Events(async (line) => {
    lines.push(line);
  });
  return { events, recorded: () => lines.map((l) => JSON.parse(l) as RecordedEvent) };
}
