import { expect, test } from "@playwright/test";

const baseURL = process.env.GRAPHNEST_UI_SMOKE_URL;
const token = process.env.GRAPHNEST_UI_SMOKE_TOKEN;

test.use({ baseURL });

test("searches pinned public repositories through the static UI", async ({ page }) => {
  await page.goto("/");
  await page.getByLabel("Bearer token").fill(token);
  await page.getByRole("button", { name: "Connect" }).click();

  await page.getByRole("link", { name: "Repositories", exact: true }).first().click();
  await expect(page.getByRole("heading", { name: "Repositories", level: 1 })).toBeVisible();
  const helloRow = page.getByRole("row").filter({ hasText: "octocat/Hello-World" });
  const spoonRow = page.getByRole("row").filter({ hasText: "octocat/Spoon-Knife" });
  await expect(helloRow).toContainText("master");
  await expect(helloRow).toContainText("7fd1a60");
  await expect(spoonRow).toContainText("main");
  await expect(spoonRow).toContainText("d0dd1f6");

  await page.getByRole("link", { name: "Search", exact: true }).click();
  const searchbox = page.getByRole("searchbox", { name: "Search code" });
  const searchButton = page.getByRole("button", { name: "Search", exact: true });
  await page.getByRole("button", { name: "All repositories" }).click();
  await page.getByRole("checkbox", { name: "All authorized repositories" }).uncheck();
  const hello = page.getByRole("checkbox", { name: "octocat/Hello-World", exact: true });
  const spoon = page.getByRole("checkbox", { name: "octocat/Spoon-Knife", exact: true });
  await expect(hello).toBeEnabled();
  await expect(spoon).toBeEnabled();

  const repositoryHeading = (name) => page.getByRole("heading", { level: 2, name: new RegExp(`^${name}`) });

  await hello.check();
  await searchbox.fill("Hello");
  await searchButton.click();
  await expect(repositoryHeading("octocat/Hello-World")).toBeVisible();
  await expect(repositoryHeading("octocat/Spoon-Knife")).toHaveCount(0);
  const helloFile = page.getByRole("article").first();
  await expect(helloFile.getByRole("heading", { level: 3 })).toHaveText("README");
  await expect(helloFile.getByRole("button")).toHaveCount(0);
  await expect(helloFile.getByRole("link", { name: "Open indexed source" })).toHaveAttribute(
    "href",
    "https://github.com/octocat/Hello-World/blob/7fd1a60b01f91b314f59955a4e4d4e80d8edf11d/README#L1",
  );

  await hello.uncheck();
  await spoon.check();
  await searchbox.fill("Forking");
  await searchButton.click();
  await expect(repositoryHeading("octocat/Spoon-Knife")).toBeVisible();
  await expect(repositoryHeading("octocat/Hello-World")).toHaveCount(0);
  const spoonFile = page.getByRole("article").first();
  await expect(spoonFile.getByRole("heading", { level: 3 })).toHaveText("README.md");
  await expect(spoonFile.getByRole("button")).toHaveCount(0);
  await expect(spoonFile.getByRole("link", { name: "Open indexed source" })).toHaveAttribute(
    "href",
    "https://github.com/octocat/Spoon-Knife/blob/d0dd1f61b33d64e29d8bc1372a94ef6a2fee76a9/README.md#L5",
  );
});
