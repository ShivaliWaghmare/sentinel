import { expect, type Page } from "../../_support/fixtures.ts";

// Logs in as the demo user by posting the login form directly (the session
// cookie is shared with the page), then opens the product list. The login UI
// itself is covered in auth.spec.ts.
export async function login(page: Page): Promise<void> {
  const res = await page.request.post("/login", { form: { username: "demo", password: "demo123" } });
  expect(res.ok()).toBe(true);
  await page.goto("/products");
  await expect(page.getByRole("heading", { name: "Products" })).toBeVisible();
}

// Adds a product from the product list and waits for the confirmation.
export async function addProductToCart(page: Page, productName: string): Promise<void> {
  const card = page.getByRole("listitem").filter({ has: page.getByRole("link", { name: productName }) });
  await card.getByRole("button", { name: "Add to cart" }).click();
  await expect(page.getByRole("status")).toHaveText(`Added to cart: ${productName}`);
}
