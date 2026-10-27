import { describe, expect, it } from "vitest";
import { loadPrompt, parseTemplate, promptVersion, render } from "./loader.ts";

describe("prompt loader", () => {
  it("parses the version header", () => {
    expect(parseTemplate("t", "version: t-v2\n\nHello {{name}}")).toEqual({ version: "t-v2", template: "Hello {{name}}" });
  });

  it("rejects a template without a version header", () => {
    expect(() => parseTemplate("t", "Hello")).toThrow(/version/);
  });

  it("fills variables, once", () => {
    expect(render("t", "Hi {{ name }}, {{x}}", { name: "{{x}}", x: "y" })).toBe("Hi {{x}}, y");
  });

  it("refuses a missing variable", () => {
    expect(() => render("t", "Hi {{nmae}}", { name: "a" })).toThrow(/nmae/);
  });

  it("loads the probe prompts", async () => {
    const task = await loadPrompt("probe.task", { app_name: "ShopLite", base_url: "http://x", notes: "n" });
    expect(task.version).toBe("probe.task-v1");
    expect(task.text).toContain("App: ShopLite");
    expect(await promptVersion("probe.system")).toBe("probe.system-v2");
  });
});
