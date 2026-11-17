/**
 * Component tests for the key screens, rendered as the app renders them:
 * the whole App against the in-process mock gateway over the seed.
 */

import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { App } from "../app/App";
import { mockServicesFor, type Services } from "../app/services";
import { createTestServer, OWNER_EMAIL, testPassword } from "../test/mock";

let services: Services;

beforeEach(async () => {
  localStorage.clear();
  sessionStorage.clear();
  const { server, crdt } = await createTestServer();
  services = mockServicesFor(server, crdt);
});

afterEach(() => {
  services.mock?.sockets.closeAll(1001, "test over");
});

async function signIn() {
  const res = await services.api.call("IdentityService_SignIn", {}, { email: OWNER_EMAIL, password: testPassword() }, { anonymous: true });
  services.session.set(res.session!, res.user!, false);
}

async function createNote(title: string, markdown?: string) {
  const ws = await services.api.proposed<{ workspaces: { id: string }[] }>("GET", "/v1/workspaces");
  const res = await services.api.call("DocumentService_CreateNote", { workspaceId: ws.workspaces[0]!.id }, { title, markdown }, { idempotencyKey: true });
  return res.note!.id!;
}

describe("sign in", () => {
  it("shows the server's error as an alert and marks the fields invalid", async () => {
    const user = userEvent.setup();
    render(<App services={services} initialPath="/signin" />);
    await user.clear(screen.getByLabelText("Email"));
    await user.type(screen.getByLabelText("Email"), "nobody@example.invalid");
    await user.type(screen.getByLabelText("Password"), testPassword());
    await user.click(screen.getByRole("button", { name: "Sign in" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("That email and password don't match.");
    expect(screen.getByLabelText("Password")).toHaveAttribute("aria-invalid", "true");
    expect(services.session.signedIn).toBe(false);
  });

  it("signs a seed user in and lands on the notes list", async () => {
    const user = userEvent.setup();
    render(<App services={services} initialPath="/signin" />);
    await user.clear(screen.getByLabelText("Email"));
    await user.type(screen.getByLabelText("Email"), OWNER_EMAIL);
    await user.type(screen.getByLabelText("Password"), testPassword());
    await user.click(screen.getByRole("button", { name: "Sign in" }));
    await waitFor(() => expect(services.session.signedIn).toBe(true));
    expect(await screen.findByRole("link", { name: /Raft overview/ }, { timeout: 10_000 })).toBeInTheDocument();
  });
});

describe("search palette", () => {
  it("opens on mod+K, moves the active option with the arrows and closes on Escape", async () => {
    await signIn();
    const user = userEvent.setup();
    render(<App services={services} initialPath="/notes" />);
    await screen.findByRole("link", { name: /Raft overview/ }, { timeout: 10_000 });
    await user.keyboard("{Control>}k{/Control}");
    const input = await screen.findByRole("combobox");
    expect(input).toHaveFocus();
    await user.type(input, "raft");
    const options = await screen.findAllByRole("option", {}, { timeout: 5_000 });
    expect(options.length).toBeGreaterThan(1);
    expect(options[0]).toHaveAttribute("aria-selected", "true");
    expect(input).toHaveAttribute("aria-activedescendant", options[0]!.id);
    await user.keyboard("{ArrowDown}");
    expect(screen.getAllByRole("option")[1]).toHaveAttribute("aria-selected", "true");
    expect(input).toHaveAttribute("aria-activedescendant", screen.getAllByRole("option")[1]!.id);
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Search" })).not.toBeInTheDocument());
  });
});

describe("graph explorer", () => {
  it("moves focus along the algorithm toolbar with the arrow keys and Home/End", async () => {
    await signIn();
    const user = userEvent.setup();
    render(<App services={services} initialPath="/graph" />);
    const toolbar = await screen.findByRole("toolbar", { name: "Algorithms" }, { timeout: 10_000 });
    const buttons = within(toolbar).getAllByRole("button");
    expect(buttons.filter((b) => b.tabIndex === 0)).toHaveLength(1);
    const first = buttons.find((b) => b.tabIndex === 0)!;
    act(() => first.focus());
    await user.keyboard("{ArrowRight}");
    expect(buttons[buttons.indexOf(first) + 1]).toHaveFocus();
    await user.keyboard("{End}");
    expect(buttons[buttons.length - 1]).toHaveFocus();
    await user.keyboard("{Home}");
    expect(buttons[0]).toHaveFocus();
    await user.keyboard("{Enter}");
    expect(buttons[0]).toHaveAttribute("aria-pressed", "true");
  });
});

describe("editor", () => {
  it("writes typed text into the note through the CRDT and the server sees it", async () => {
    await signIn();
    const noteId = await createNote("Typing test", "hello");
    const user = userEvent.setup();
    render(<App services={services} initialPath={`/notes/${noteId}`} />);
    const view = await screen.findByRole("textbox", { name: /paragraph block/ }, { timeout: 10_000 });
    await user.click(view);
    const textarea = await screen.findByRole("textbox", { name: "Edit paragraph block" });
    await user.type(textarea, " world", { initialSelectionStart: 5, initialSelectionEnd: 5 });
    await waitFor(
      async () => {
        const res = await services.api.call("DocumentService_GetNote", { noteId, includeMarkdown: true });
        expect(res.markdown).toContain("hello world");
      },
      { timeout: 8_000 },
    );
  });

  it("keeps fast typing intact when saving to IndexedDB is slower than the keystrokes", async () => {
    await signIn();
    const noteId = await createNote("Fast typing", "start");
    const store = services.store;
    const appendLocal = store.appendLocal.bind(store);
    store.appendLocal = async (...args) => {
      await new Promise((resolve) => setTimeout(resolve, 30));
      return appendLocal(...args);
    };
    const user = userEvent.setup({ delay: null });
    render(<App services={services} initialPath={`/notes/${noteId}`} />);
    await user.click(await screen.findByRole("textbox", { name: /paragraph block/ }, { timeout: 10_000 }));
    const textarea = await screen.findByRole("textbox", { name: "Edit paragraph block" });
    await user.type(textarea, " written fast", { initialSelectionStart: 5, initialSelectionEnd: 5 });
    await waitFor(
      async () => {
        const res = await services.api.call("DocumentService_GetNote", { noteId, includeMarkdown: true });
        expect(res.markdown?.trim()).toBe("start written fast");
      },
      { timeout: 8_000 },
    );
  });

  it("opens the slash menu on '/', filters it, and turns the block into the chosen type", async () => {
    await signIn();
    const noteId = await createNote("Slash test", "x");
    const user = userEvent.setup();
    render(<App services={services} initialPath={`/notes/${noteId}`} />);
    await user.click(await screen.findByRole("textbox", { name: /paragraph block/ }, { timeout: 10_000 }));
    const textarea = await screen.findByRole("textbox", { name: "Edit paragraph block" });
    await user.clear(textarea);
    await user.type(textarea, "/");
    const menu = await screen.findByRole("listbox", { name: "Insert a block" });
    expect(within(menu).getAllByRole("option")[0]).toHaveAttribute("aria-selected", "true");
    await user.type(textarea, "quo");
    await waitFor(() => expect(within(screen.getByRole("listbox", { name: "Insert a block" })).getAllByRole("option")[0]).toHaveTextContent(/quote/i));
    await user.keyboard("{Enter}");
    await waitFor(() => expect(screen.queryByRole("listbox", { name: "Insert a block" })).not.toBeInTheDocument());
    await waitFor(async () => {
      const res = await services.api.call("DocumentService_GetNote", { noteId, includeMarkdown: true });
      expect(res.markdown?.trim()).toMatch(/^>/);
    }, { timeout: 8_000 });
  });
});

describe("agent panel", () => {
  it("shows the agent's edit as a suggestion, and rejecting it puts the old text back", async () => {
    await signIn();
    const noteId = await createNote("Agent test", "the quick fox");
    const user = userEvent.setup();
    const markdown = async () => (await services.api.call("DocumentService_GetNote", { noteId, includeMarkdown: true })).markdown ?? "";
    render(<App services={services} initialPath={`/notes/${noteId}`} />);
    const ask = await screen.findByLabelText("Message the agent", {}, { timeout: 10_000 });
    await user.type(ask, 'replace "quick" with "slow"{Enter}');
    const card = await screen.findByRole("region", { name: "Suggested edits" }, { timeout: 8_000 });
    expect(within(card).getByRole("listitem")).toHaveTextContent("quick → slow");
    await waitFor(async () => expect(await markdown()).toContain("the slow fox"), { timeout: 8_000 });
    await user.click(within(card).getByRole("button", { name: "Reject" }));
    expect(await within(card).findByText("rejected")).toBeInTheDocument();
    await waitFor(async () => expect(await markdown()).toContain("the quick fox"), { timeout: 8_000 });
  });
});
