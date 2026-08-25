import { expect, test } from "@playwright/test";

const baseURL = process.env.GRAPHNEST_UI_SMOKE_URL;
const token = process.env.GRAPHNEST_UI_SMOKE_TOKEN;

test.use({ baseURL });

test("searches pinned public repositories through the static UI", async ({ page }) => {
  await page.goto("/");
  await page.getByLabel("Bearer token").fill(token);
  await page.getByRole("button", { name: "Connect" }).click();

  await page.getByRole("button", { name: "Repositories" }).click();
  const helloRow = page.getByRole("row").filter({ hasText: "octocat/Hello-World" });
  const spoonRow = page.getByRole("row").filter({ hasText: "octocat/Spoon-Knife" });
  await expect(helloRow).toContainText("master");
  await expect(helloRow).toContainText("7fd1a60");
  await expect(spoonRow).toContainText("main");
  await expect(spoonRow).toContainText("d0dd1f6");

  await page.locator("#search-nav").click();
  await page.locator("#repository-picker summary").click();
  await page.getByLabel("All authorized repositories").uncheck();
  const hello = page.getByLabel("octocat/Hello-World");
  const spoon = page.getByLabel("octocat/Spoon-Knife");
  await expect(hello).toBeEnabled();
  await expect(spoon).toBeEnabled();

  await hello.check();
  await page.getByRole("searchbox", { name: "Search code" }).fill("Hello");
  await page.locator("#search-button").click();
  await expect(page.locator("#results")).toContainText("octocat/Hello-World");
  await expect(page.locator("#results")).not.toContainText("octocat/Spoon-Knife");
  const helloFile = page.locator("#results .file-result").first();
  await expect(helloFile.locator("h3")).toHaveText("README");
  await expect(helloFile.locator("h3 button")).toHaveCount(0);
  await expect(helloFile.getByRole("link", { name: "Open indexed source" })).toHaveAttribute(
    "href",
    "https://github.com/octocat/Hello-World/blob/7fd1a60b01f91b314f59955a4e4d4e80d8edf11d/README#L1",
  );

  await hello.uncheck();
  await spoon.check();
  await page.getByRole("searchbox", { name: "Search code" }).fill("Forking");
  await page.locator("#search-button").click();
  await expect(page.locator("#results")).toContainText("octocat/Spoon-Knife");
  await expect(page.locator("#results")).not.toContainText("octocat/Hello-World");
  const spoonFile = page.locator("#results .file-result").first();
  await expect(spoonFile.locator("h3")).toHaveText("README.md");
  await expect(spoonFile.locator("h3 button")).toHaveCount(0);
  await expect(spoonFile.getByRole("link", { name: "Open indexed source" })).toHaveAttribute(
    "href",
    "https://github.com/octocat/Spoon-Knife/blob/d0dd1f61b33d64e29d8bc1372a94ef6a2fee76a9/README.md#L5",
  );
});
