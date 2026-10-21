import { expect, test } from "../../_support/fixtures.ts";
import { login } from "./helpers.ts";

test("demo user can log in", async ({ page }) => {
  await page.goto("/login");
  await page.getByLabel("Username").fill("demo");
  await page.getByLabel("Password").fill("demo123");
  await page.getByRole("button", { name: "Log in" }).click();

  await expect(page).toHaveURL(/\/products$/);
  await expect(page.getByRole("heading", { name: "Products" })).toBeVisible();
  await expect(page.getByText("Signed in as demo")).toBeVisible();
});

test("locked user sees an account locked error", async ({ page }) => {
  await page.goto("/login");
  await page.getByLabel("Username").fill("locked");
  await page.getByLabel("Password").fill("demo123");
  await page.getByRole("button", { name: "Log in" }).click();

  await expect(page.getByRole("alert")).toContainText("account is locked");
  await expect(page.getByRole("heading", { name: "Welcome back" })).toBeVisible();
});

test("user can log out", async ({ page }) => {
  await login(page);

  await page.getByRole("button", { name: "Log out" }).click();

  await expect(page).toHaveURL(/\/login$/);
  await expect(page.getByRole("heading", { name: "Welcome back" })).toBeVisible();
  // The session is gone, so protected pages send us back to the login page.
  await page.goto("/cart");
  await expect(page).toHaveURL(/\/login$/);
});
