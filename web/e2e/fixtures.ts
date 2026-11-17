import { expect, type Page } from "@playwright/test";

/** Mock mode accepts any password for a seed user; it is generated so none is ever written down. */
const password = () => ["e2e", crypto.randomUUID().slice(0, 12)].join("-");

/** Signs in as the seed workspace owner and waits for the first note to open. */
export async function signIn(page: Page): Promise<void> {
  await page.goto("/signin");
  await page.getByLabel("Password").fill(password());
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page).toHaveURL(/\/notes\/[0-9a-f-]{36}/);
  await expect(page.getByRole("region", { name: "Note" }).getByRole("textbox").first()).toBeVisible();
}

/** Creates a note from the header and returns its editing textarea, focused on the first block. */
export async function newNote(page: Page) {
  await page.getByRole("button", { name: /New note/ }).click();
  await expect(page).toHaveURL(/\/notes\/[0-9a-f-]{36}$/);
  const textarea = page.getByRole("textbox", { name: "Edit paragraph block" });
  await expect(textarea).toBeFocused();
  return textarea;
}

/** Collects console errors and uncaught exceptions so a test can assert there were none. */
export function watchErrors(page: Page): string[] {
  const errors: string[] = [];
  page.on("console", (m) => {
    if (m.type() === "error") errors.push(m.text());
  });
  page.on("pageerror", (e) => errors.push(e.message));
  return errors;
}
