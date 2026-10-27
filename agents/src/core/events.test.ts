import { describe, expect, it } from "vitest";
import { Events } from "./events.ts";
import { recordingEvents } from "./test-helpers.ts";

describe("Events", () => {
  it("writes one JSON line per event with increasing seq and an RFC 3339 ts", async () => {
    const lines: string[] = [];
    const events = new Events(
      async (l) => {
        lines.push(l);
      },
      () => new Date("2026-10-27T10:00:00.000Z"),
    );

    expect(events.emit("phase", { name: "a" })).toBe(1);
    expect(events.emit("warning", { message: "b" })).toBe(2);
    await events.flush();

    expect(lines).toEqual([
      '{"seq":1,"ts":"2026-10-27T10:00:00.000Z","type":"phase","data":{"name":"a"}}',
      '{"seq":2,"ts":"2026-10-27T10:00:00.000Z","type":"warning","data":{"message":"b"}}',
    ]);
  });

  it("drops events after done", async () => {
    const { events, recorded } = recordingEvents();
    events.emit("done", { status: "succeeded", summary: {} });
    events.emit("warning", { message: "late" });
    await events.flush();

    expect(recorded().map((e) => e.type)).toEqual(["done"]);
    expect(events.isDone).toBe(true);
  });

  it("flush waits for slow writes", async () => {
    const written: number[] = [];
    const events = new Events(
      (line) =>
        new Promise((resolve) =>
          setTimeout(() => {
            written.push((JSON.parse(line) as { seq: number }).seq);
            resolve();
          }, 5),
        ),
    );
    events.emit("phase", { name: "a" });
    events.emit("phase", { name: "b" });
    await events.flush();

    expect(written).toEqual([1, 2]);
  });
});
