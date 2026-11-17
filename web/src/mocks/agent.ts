/**
 * A scripted agent for mock mode. It produces the same SendMessageResponse
 * events the Python agent does (tool calls and results, text deltas,
 * citations, confirmations, one `finished`), acting as the user with the
 * user's permissions (ADR-0009):
 *
 * - every run searches by meaning and reads the chat's note, if any;
 * - `replace "x" with "y"` (or `change "x" to "y"`) runs apply_edit: the edit
 *   is applied to the note from the run's own replica, so the editor sees it
 *   as an agent suggestion, exactly as services/agent does;
 * - asking to share or delete a note pauses for a confirmation;
 * - anything else gets an answer quoting and citing the best blocks.
 */

import type { SendMessageResponse } from "../api/schema.gen";
import { MockError, mergeBatches, type MockServer } from "./server";
import type { SeedChat } from "./seed";
import type { MockSocket } from "./sockets";
import { cosine, embed } from "./text";

type Run = {
  runId: string;
  userId: string;
  chatId: string;
  events: SendMessageResponse[];
  waiting: { confirmationId: string; resolve: (approved: boolean) => void } | null;
  answered: Set<string>;
  cancelled: boolean;
};

const STEP_MS = 140;

export class MockAgent {
  private runs = new Map<string, Run>();
  stepMs = STEP_MS;

  constructor(private readonly server: MockServer) {}

  async start(socket: MockSocket, userId: string, chat: SeedChat & { runs: string[] }, text: string): Promise<void> {
    const run: Run = { runId: crypto.randomUUID(), userId, chatId: chat.id, events: [], waiting: null, answered: new Set(), cancelled: false };
    this.runs.set(run.runId, run);
    chat.runs.push(run.runId);
    chat.messages.push({ role: "user", text, runId: null });
    if (chat.title === "New chat" || chat.title.startsWith("About ")) chat.title = text.slice(0, 60);
    chat.updatedAt = new Date(this.server.now()).toISOString();
    const emit = async (event: Omit<SendMessageResponse, "runId">) => {
      if (run.cancelled && !event.finished) return;
      const full = { runId: run.runId, ...event };
      run.events.push(full);
      socket.deliver(JSON.stringify(full));
      await new Promise((r) => setTimeout(r, this.stepMs));
    };
    let steps = 0;
    const call = async (tool: string, args: Record<string, unknown>) => {
      steps += 1;
      const callId = `call_${steps}`;
      await emit({ toolCall: { callId, tool, argumentsJson: JSON.stringify(args) } });
      return callId;
    };
    const answer = async (sentence: string) => {
      for (const chunk of sentence.match(/.{1,48}(\s|$)/g) ?? [sentence]) await emit({ textDelta: chunk });
    };

    const hits = this.bestBlocks(userId, chat.workspaceId, text, 3);
    const searchId = await call("semantic_search", { query: text });
    await emit({ toolResult: { callId: searchId, summary: `Found ${new Set(hits.map((h) => h.noteId)).size} notes` } });

    const anchorNote = chat.noteId ? this.server.notes.get(chat.noteId) : undefined;
    if (anchorNote) {
      const id = await call("get_note", { note_id: anchorNote.id });
      await emit({ toolResult: { callId: id, summary: `Read “${anchorNote.title}”` } });
    }

    const edit = /(?:replace|change)\s+"([^"]+)"\s+(?:with|to)\s+"([^"]*)"/i.exec(text);
    const targetNote = anchorNote ?? (hits[0] ? this.server.notes.get(hits[0].noteId) : undefined);
    if (edit && targetNote) {
      const id = await call("apply_edit", { note_id: targetNote.id, edits: [{ find: edit[1], replace: edit[2] }] });
      if (this.server.noteRole(userId, targetNote.id) !== "NOTE_ROLE_EDITOR") {
        await emit({ toolResult: { callId: id, summary: "permission_denied: you can't edit this note", isError: true } });
        await answer(`You're a viewer on “${targetNote.title}”, so I can't suggest edits to it. I can draft the change in a note of your own.`);
      } else if (await this.applyEdit(run, targetNote.id, edit[1]!, edit[2]!)) {
        await emit({ toolResult: { callId: id, summary: "Suggested 1 edit in 1 note" } });
        await answer(`I suggested the change in “${targetNote.title}”. Accept or reject it in the note.`);
      } else {
        await emit({ toolResult: { callId: id, summary: "invalid_argument: text not found in the note", isError: true } });
        await answer(`I couldn't find “${edit[1]}” in “${targetNote.title}”, so I changed nothing.`);
      }
    } else if (/\bshare\b/i.test(text) && targetNote) {
      await this.confirmThen(run, emit, call, "create_share_link", `Share “${targetNote.title}” with anyone who has the link, as Viewer, for 7 days`, { note_id: targetNote.id, role: "NOTE_ROLE_VIEWER" }, async () => {
        const expires = new Date(this.server.now() + 7 * 86_400_000).toISOString();
        this.server.shareLinks.push({ id: crypto.randomUUID(), noteId: targetNote.id, role: "viewer", createdBy: userId, createdAt: new Date(this.server.now()).toISOString(), expiresAt: expires, revokedAt: null });
        return "Created a viewer link that expires in 7 days.";
      });
    } else if (/\bdelete\b/i.test(text) && targetNote) {
      try {
        this.server.requireNoteRole(userId, targetNote.id, "delete");
        await this.confirmThen(run, emit, call, "delete_note", `Move “${targetNote.title}” to the trash`, { note_id: targetNote.id }, async () => {
          targetNote.trashed = true;
          return `Moved “${targetNote.title}” to the trash. It can be restored.`;
        });
      } catch (e) {
        if (!(e instanceof MockError)) throw e;
        await answer(`I can't delete “${targetNote.title}”: deleting needs its owner or a workspace admin, and you're not one here.`);
      }
    } else if (hits.length === 0) {
      await answer("I couldn't find anything about that in the notes you can open.");
    } else {
      await answer(`From what I found: ${hits.map((h, i) => `“${h.quote}”[${i + 1}]`).join(" and ")}.`);
      for (const h of hits.slice(0, 2)) await emit({ citation: { location: { noteId: h.noteId, blockId: h.blockId }, quote: h.quote } });
    }

    await emit({
      finished: {
        outcome: run.cancelled ? "OUTCOME_CANCELLED" : "OUTCOME_COMPLETED",
        steps,
        inputTokens: String(800 + text.length * 4),
        outputTokens: String(60 + steps * 20),
      },
    });
    chat.messages.push({ role: "assistant", text: run.events.map((e) => e.textDelta ?? "").join(""), runId: run.runId });
    const notification = {
      id: crypto.randomUUID(),
      userId,
      kind: "NOTIFICATION_KIND_AGENT_RUN_FINISHED",
      title: `The agent finished: ${chat.title}`,
      body: "",
      actorId: "",
      createdAt: new Date(this.server.now()).toISOString(),
      read: false,
      location: null,
    };
    this.server.notifications.push(notification);
    this.server.sockets.pushNotification(userId, this.server.notificationJson(notification));
  }

  private async confirmThen(
    run: Run,
    emit: (e: Omit<SendMessageResponse, "runId">) => Promise<void>,
    call: (tool: string, args: Record<string, unknown>) => Promise<string>,
    tool: string,
    description: string,
    args: Record<string, unknown>,
    act: () => Promise<string>,
  ): Promise<void> {
    const callId = await call(tool, args);
    const confirmationId = `conf_${callId}`;
    const approved = await new Promise<boolean>((resolve) => {
      run.waiting = { confirmationId, resolve };
      void emit({ confirmation: { confirmationId, description, tool, argumentsJson: JSON.stringify(args) } });
    });
    run.waiting = null;
    if (!approved) {
      await emit({ toolResult: { callId, summary: "Not confirmed", isError: true } });
      await emit({ textDelta: "Okay, I didn't change anything." });
      return;
    }
    const summary = await act();
    await emit({ toolResult: { callId, summary } });
    await emit({ textDelta: summary });
  }

  /** Applies find/replace as the run's own registered replica, the way services/agent's apply_edit does. */
  private async applyEdit(run: Run, noteId: string, find: string, replace: string): Promise<boolean> {
    const nd = await this.server.loadDoc(noteId);
    const replica = String(BigInt.asUintN(53, BigInt(Math.floor(Math.random() * 2 ** 52))) + 1000n);
    this.server.replicaOwner.set(replica, { userId: run.userId, agentRunId: run.runId });
    const agentDoc = this.server.crdt.open(replica);
    try {
      agentDoc.applyRemote(nd.doc.allOps());
      const at = agentDoc.text().indexOf(find);
      if (at < 0 || find.includes("\u001e")) return false;
      const parts = [agentDoc.deleteInNote(at, at + find.length)];
      if (replace) parts.push(agentDoc.insertInNote(at, replace));
      return (await this.server.acceptOps(run.userId, noteId, replica, mergeBatches(parts), null)) !== null;
    } finally {
      agentDoc.free();
    }
  }

  private bestBlocks(userId: string, workspaceId: string, text: string, k: number) {
    const q = embed(text, this.server.stopwords);
    return this.server
      .readableNotes(userId, workspaceId)
      .flatMap((n) => this.server.blocksOf(n.id).filter((b) => b.type !== "code" && b.text.length > 30).map((b) => ({ noteId: n.id, blockId: b.blockId, text: b.text, score: cosine(q, embed(b.text, this.server.stopwords)) })))
      .filter((h) => h.score > 0.15)
      .sort((a, b) => b.score - a.score)
      .slice(0, k)
      .map((h) => ({ ...h, quote: (h.text.split(/(?<=[.!?])\s/)[0] ?? h.text).slice(0, 140) }));
  }

  confirm(userId: string, runId: string, confirmationId: string, approved: boolean) {
    const run = this.runs.get(runId);
    if (!run || run.userId !== userId) throw new MockError("NOT_FOUND", "run not found");
    if (run.answered.has(confirmationId) || run.waiting?.confirmationId !== confirmationId)
      throw new MockError("FAILED_PRECONDITION", "confirmation already answered or not pending", [{ "@type": "type.googleapis.com/google.rpc.PreconditionFailure", violations: [{ type: "CONFIRMATION", description: "This confirmation was already answered." }] }]);
    run.answered.add(confirmationId);
    run.waiting.resolve(approved);
    return {};
  }

  cancel(userId: string, runId: string) {
    const run = this.runs.get(runId);
    if (!run || run.userId !== userId) throw new MockError("NOT_FOUND", "run not found");
    run.cancelled = true;
    run.waiting?.resolve(false);
    return {};
  }

  getRun(userId: string, runId: string) {
    const run = this.runs.get(runId);
    if (!run || run.userId !== userId) throw new MockError("NOT_FOUND", "run not found");
    return { runId, events: run.events };
  }
}
