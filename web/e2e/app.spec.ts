import { expect, test } from "@playwright/test";
import { newNote, signIn, watchErrors } from "./fixtures";

test.describe("desktop", () => {
  test.skip(({ isMobile }) => isMobile);

  test("signs in, edits a note and the change reaches the server", async ({ page }) => {
    const errors = watchErrors(page);
    await signIn(page);
    await (await newNote(page)).pressSequentially("written in e2e");
    await expect(page.locator(".sk-sync").first()).toHaveAccessibleName(/^Synced/);
    // The mock gateway lives in the page, so a reload would start it over: leave the note and
    // come back instead, which opens a new session that catches up from the gateway.
    const noteUrl = page.url();
    await page.getByRole("link", { name: /Kestrel Lab Home/ }).first().click();
    await expect(page).not.toHaveURL(noteUrl);
    await page.goBack();
    await expect(page).toHaveURL(noteUrl);
    await expect(page.getByRole("textbox", { name: /paragraph block/ }).first()).toHaveText(/^written in e2e/); // another person's cursor label may follow
    expect(errors).toEqual([]);
  });

  test("going offline queues edits and coming back sends them", async ({ page }) => {
    await signIn(page);
    const textarea = await newNote(page);
    await page.getByRole("button", { name: "simulate" }).click();
    await page.getByRole("menuitem", { name: /Go offline/ }).click();
    await expect(textarea).toBeHidden(); // the menu took focus; open the block again
    await page.getByRole("textbox", { name: /paragraph block/ }).first().click();
    await page.getByRole("textbox", { name: "Edit paragraph block" }).pressSequentially("typed offline");
    const badge = page.locator(".sk-sync").first();
    await expect(badge).toHaveAccessibleName(/^Offline/);
    await page.getByRole("button", { name: "simulate" }).click();
    await page.getByRole("menuitem", { name: /Go back online/ }).click();
    await expect(badge).toHaveAccessibleName(/^Synced/);
  });

  test("search palette finds a note by text and opens it from the keyboard", async ({ page }) => {
    await signIn(page);
    await page.keyboard.press("ControlOrMeta+k");
    const input = page.getByRole("combobox");
    await expect(input).toBeFocused();
    await input.fill("leader election");
    await expect(page.getByRole("option").first()).toBeVisible();
    await page.keyboard.press("Enter");
    await expect(page.getByRole("dialog", { name: "Search" })).toBeHidden();
    await expect(page).toHaveURL(/\/notes\/[0-9a-f-]{36}/);
  });

  test("graph explorer switches algorithms from the toolbar", async ({ page }) => {
    const errors = watchErrors(page);
    await signIn(page);
    await page.goto("/graph");
    const toolbar = page.getByRole("toolbar", { name: "Algorithms" });
    await toolbar.getByRole("button", { name: "PageRank" }).click();
    await expect(toolbar.getByRole("button", { name: "PageRank" })).toHaveAttribute("aria-pressed", "true");
    await expect(page.getByRole("heading", { name: "PageRank" }).first()).toBeVisible();
    await toolbar.getByRole("button", { name: "PageRank" }).press("ArrowRight");
    await expect(toolbar.getByRole("button", { name: "Communities" })).toBeFocused();
    await page.keyboard.press("Enter");
    await expect(page.getByRole("heading", { name: "Communities" }).first()).toBeVisible();
    expect(errors).toEqual([]);
  });
});

test("mobile layout shows the note with its formatting toolbar @mobile", async ({ page, isMobile }) => {
  test.skip(!isMobile);
  await signIn(page);
  await page.getByRole("textbox", { name: /block/ }).first().click();
  await expect(page.getByRole("navigation", { name: "Formatting" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Back to notes" })).toBeVisible();
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
  expect(overflow).toBeLessThanOrEqual(0);
});
