import { expect, test } from "../../_support/fixtures.ts";
import { addProductToCart, login } from "./helpers.ts";

test("adding products updates the cart badge", async ({ page }) => {
  await login(page);
  // The badge is a bare number with no role or label, so a test id is the clearest locator.
  const badge = page.getByTestId("cart-badge");
  await expect(badge).toHaveText("0");

  await addProductToCart(page, "Classic Mug");
  await expect(badge).toHaveText("1");

  await addProductToCart(page, "Desk Lamp");
  await expect(badge).toHaveText("2");
});

test("removing a product empties the cart", async ({ page }) => {
  await login(page);
  await addProductToCart(page, "Classic Mug");

  await page.getByRole("link", { name: /^Cart/ }).click();
  await expect(page.getByRole("heading", { name: "Your cart" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Classic Mug" })).toBeVisible();

  await page.getByRole("button", { name: "Remove Classic Mug" }).click();

  await expect(page.getByText("Your cart is empty.")).toBeVisible();
});
