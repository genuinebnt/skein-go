import { describe, expect, it } from "vitest";
import { applyEvent, newRun, setSuggestion, stepsSummary, type AgentRun } from "./run";
import type { SendMessageResponse } from "../api/schema.gen";

const fold = (events: SendMessageResponse[]): AgentRun => events.reduce(applyEvent, newRun("r1", "fix it"));

const edit = JSON.stringify({ note_id: "n1", edits: [{ find: "50 ms", replace: "150 ms" }, { find: "consensous", replace: "consensus" }] });

describe("agent run", () => {
  it("folds a streamed run into steps, text, citations and a finish", () => {
    const run = fold([
      { runId: "r1", toolCall: { callId: "c1", tool: "search", argumentsJson: '{"query":"heartbeat"}' } },
      { runId: "r1", toolResult: { callId: "c1", summary: "Found 2 notes" } },
      { runId: "r1", textDelta: "Three notes " },
      { runId: "r1", textDelta: "changed." },
      { runId: "r1", citation: { location: { noteId: "n1", blockId: "1:2" }, quote: "q" } },
      { runId: "r1", finished: { outcome: "OUTCOME_COMPLETED", steps: 2, inputTokens: "10", outputTokens: "3" } },
    ]);
    expect(run.steps).toEqual([{ callId: "c1", tool: "search", args: { query: "heartbeat" }, summary: "Found 2 notes", isError: undefined, done: true }]);
    expect(run.text).toBe("Three notes changed.");
    expect(run.citations).toHaveLength(1);
    expect(run.finished?.outcome).toBe("OUTCOME_COMPLETED");
  });

  it("turns a successful apply_edit into one suggestion per find/replace pair", () => {
    const run = fold([
      { runId: "r1", toolCall: { callId: "c2", tool: "apply_edit", argumentsJson: edit } },
      { runId: "r1", toolResult: { callId: "c2", summary: "Suggested 2 edits in 1 note" } },
    ]);
    expect(run.suggestions.map((s) => [s.find, s.replace, s.state])).toEqual([
      ["50 ms", "150 ms", "pending"],
      ["consensous", "consensus", "pending"],
    ]);
    expect(stepsSummary(setSuggestion(run, run.suggestions[0]!.id, "accepted"))).toBe("proposed 2 edits");
  });

  it("adds no suggestion when apply_edit failed", () => {
    const run = fold([
      { runId: "r1", toolCall: { callId: "c2", tool: "apply_edit", argumentsJson: edit } },
      { runId: "r1", toolResult: { callId: "c2", summary: "permission denied", isError: true } },
    ]);
    expect(run.suggestions).toEqual([]);
  });

  it("is idempotent when a reconnect replays events from GetRun", () => {
    const events: SendMessageResponse[] = [
      { runId: "r1", toolCall: { callId: "c1", tool: "delete_note", argumentsJson: "{}" } },
      { runId: "r1", confirmation: { confirmationId: "k1", description: "Delete Old notes", tool: "delete_note" } },
    ];
    const run = fold([...events, ...events]);
    expect(run.steps).toHaveLength(1);
    expect(run.confirmations).toHaveLength(1);
  });

  it("marks confirmations nobody answered as declined when the run ends", () => {
    const run = fold([
      { runId: "r1", confirmation: { confirmationId: "k1", description: "Share", tool: "create_share_link" } },
      { runId: "r1", finished: { outcome: "OUTCOME_CANCELLED" } },
    ]);
    expect(run.confirmations[0]?.state).toBe("declined");
  });
});
