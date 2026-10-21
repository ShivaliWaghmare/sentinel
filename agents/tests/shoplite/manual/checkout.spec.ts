import { expect, test, type Page } from "../../_support/fixtures.ts";
import { addProductToCart, login } from "./helpers.ts";

// Logs in, puts one product in the cart and opens the checkout form.
async function openCheckout(page: Page): Promise<void> {
  await login(page);
  await addProductToCart(page, "Classic Mug");
  await page.getByRole("link", { name: /^Cart/ }).click();
  await page.getByRole("button", { name: "Checkout" }).click();
  await expect(page.getByRole("heading", { name: "Checkout" })).toBeVisible();
}

test("checkout happy path shows an order confirmation", async ({ page }) => {
  await openCheckout(page);

  await page.getByLabel("Full name").fill("Asha Rao");
  await page.getByLabel("Address").fill("12 MG Road");
  await page.getByLabel("Zip code").fill("56001");
  await page.getByLabel("Card number").fill("4242 4242 4242 4242");
  await page.getByRole("button", { name: "Place order" }).click();

  await expect(page.getByRole("heading", { name: "Thank you for your order" })).toBeVisible();
  await expect(page.getByText(/Order number #\d+/)).toBeVisible();
});

test("checkout requires a name", async ({ page }) => {
  await openCheckout(page);

  await page.getByLabel("Address").fill("12 MG Road");
  await page.getByLabel("Zip code").fill("56001");
  await page.getByLabel("Card number").fill("4242 4242 4242 4242");
  await page.getByRole("button", { name: "Place order" }).click();

  await expect(page.getByRole("heading", { name: "Checkout" })).toBeVisible();
  await expect(page.getByText("Enter your full name.")).toBeVisible();
  await expect(page.getByLabel("Full name")).toHaveAttribute("aria-invalid", "true");
});
