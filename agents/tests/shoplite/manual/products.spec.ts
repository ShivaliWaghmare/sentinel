import { expect, test } from "../../_support/fixtures.ts";
import { login } from "./helpers.ts";

test("search finds a product by name", async ({ page }) => {
  await login(page);

  await page.getByLabel("Search products").fill("Desk Lamp");
  await page.getByLabel("Search products").press("Enter");

  const results = page.getByRole("list", { name: "Products" }).getByRole("listitem");
  await expect(results).toHaveCount(1);
  await expect(results.getByRole("link", { name: "Desk Lamp" })).toBeVisible();
});
