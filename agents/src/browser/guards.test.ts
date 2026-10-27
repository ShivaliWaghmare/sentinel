import { describe, expect, it } from "vitest";
import { assertSafe, DestructiveActionBlocked, destructiveMatch } from "./guards.ts";

describe("destructiveMatch", () => {
  it.each([
    ["Delete", "Delete"],
    ["Delete product", "Delete"],
    ["Remove account", "Remove account"],
    ["Pay now", "Pay"],
    ["Purchase", "Purchase"],
    ["Place order", "Place order"],
    ["Send message", "Send"],
  ])("flags %s", (name, match) => {
    expect(destructiveMatch(name)).toBe(match);
  });

  it.each(["Remove", "Add to cart", "Log in", "Payment details", "Sender name", "Undelete", "Ordered list"])(
    "allows %s",
    (name) => {
      expect(destructiveMatch(name)).toBeUndefined();
    },
  );
});

describe("assertSafe", () => {
  it("blocks destructive names unless the app allows them", () => {
    expect(() => assertSafe(["Checkout", "Place order"], false)).toThrow(DestructiveActionBlocked);
    expect(() => assertSafe(["Place order"], true)).not.toThrow();
    expect(() => assertSafe(["Checkout"], false)).not.toThrow();
  });
});
