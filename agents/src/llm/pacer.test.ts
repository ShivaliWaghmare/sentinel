import { describe, expect, it } from "vitest";
import { Pacer } from "./pacer.ts";

describe("Pacer", () => {
  it("spaces requests evenly at the given rate", () => {
    let t = 0;
    const pacer = new Pacer(5, () => t); // 12 s apart

    expect(pacer.reserve()).toBe(0);
    expect(pacer.reserve()).toBe(12_000); // booked right after: wait a full interval
    t = 30_000; // the slot at 24 s has passed
    expect(pacer.reserve()).toBe(0);
    t = 35_000;
    expect(pacer.reserve()).toBe(7_000); // next slot at 42 s
  });

  it("never waits with rpm 0", () => {
    const pacer = new Pacer(0, () => 0);
    expect([pacer.reserve(), pacer.reserve(), pacer.reserve()]).toEqual([0, 0, 0]);
  });
});
