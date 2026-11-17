/**
 * An agent run as the panel shows it, folded from the SendMessageResponse
 * events the /messages socket streams (and GetRun replays): steps (tool
 * calls and their results), the answer text, citations, confirmations, and
 * suggested edits.
 *
 * Suggestions: the agent's `apply_edit` tool writes its edits straight into
 * the note as one change group from the run's own replica (services/agent,
 * tools/edit.py). The panel and the editor show each find/replace pair as a
 * suggestion to accept (keep, nothing to send) or reject (put the original
 * text back). The protocol has no suggestion state yet; see the PR.
 */

import type { BlockRef, Outcome, SendMessageResponse } from "../api/schema.gen";

export type Step = { callId: string; tool: string; args: Record<string, unknown>; summary?: string; isError?: boolean; done: boolean };
export type Citation = { location: BlockRef; quote: string };
export type Confirmation = {
  confirmationId: string;
  description: string;
  tool: string;
  args: Record<string, unknown>;
  state: "waiting" | "approved" | "declined" | "failed";
};
export type SuggestionState = "pending" | "accepted" | "rejected";
export type Suggestion = { id: string; noteId: string; find: string; replace: string; state: SuggestionState };

export type AgentRun = {
  runId: string;
  userText?: string;
  steps: Step[];
  text: string;
  citations: Citation[];
  confirmations: Confirmation[];
  suggestions: Suggestion[];
  finished: { outcome: Outcome; steps: number; inputTokens: string; outputTokens: string } | null;
};

export const newRun = (runId: string, userText?: string): AgentRun => ({
  runId,
  userText,
  steps: [],
  text: "",
  citations: [],
  confirmations: [],
  suggestions: [],
  finished: null,
});

function parseArgs(json: string | undefined): Record<string, unknown> {
  if (!json) return {};
  try {
    const value = JSON.parse(json) as unknown;
    return value && typeof value === "object" ? (value as Record<string, unknown>) : {};
  } catch {
    return {};
  }
}

/** The find/replace pairs of an apply_edit call, each a suggestion. */
export function suggestionsOf(callId: string, args: Record<string, unknown>): Suggestion[] {
  const noteId = typeof args.note_id === "string" ? args.note_id : "";
  const edits = Array.isArray(args.edits) ? (args.edits as { find?: unknown; replace?: unknown }[]) : [];
  return edits
    .filter((e) => typeof e.find === "string" && typeof e.replace === "string")
    .map((e, i) => ({ id: `${callId}#${i}`, noteId, find: e.find as string, replace: e.replace as string, state: "pending" as const }));
}

export function applyEvent(run: AgentRun, event: SendMessageResponse): AgentRun {
  if (event.toolCall) {
    const args = parseArgs(event.toolCall.argumentsJson);
    const step: Step = { callId: event.toolCall.callId ?? "", tool: event.toolCall.tool ?? "", args, done: false };
    // A replayed run (GetRun after a reconnect) repeats events; keep each call once.
    if (run.steps.some((s) => s.callId === step.callId)) return run;
    return { ...run, steps: [...run.steps, step] };
  }
  if (event.toolResult) {
    const { callId, summary, isError } = event.toolResult;
    const steps = run.steps.map((s) => (s.callId === callId ? { ...s, summary, isError, done: true } : s));
    const step = steps.find((s) => s.callId === callId);
    const fresh =
      step && step.tool === "apply_edit" && !isError && !run.suggestions.some((x) => x.id.startsWith(`${callId}#`))
        ? suggestionsOf(callId ?? "", step.args)
        : [];
    return { ...run, steps, suggestions: [...run.suggestions, ...fresh] };
  }
  if (event.textDelta) return { ...run, text: run.text + event.textDelta };
  if (event.citation) {
    return { ...run, citations: [...run.citations, { location: event.citation.location ?? {}, quote: event.citation.quote ?? "" }] };
  }
  if (event.confirmation) {
    const c = event.confirmation;
    if (run.confirmations.some((x) => x.confirmationId === c.confirmationId)) return run;
    return {
      ...run,
      confirmations: [
        ...run.confirmations,
        { confirmationId: c.confirmationId ?? "", description: c.description ?? "", tool: c.tool ?? "", args: parseArgs(c.argumentsJson), state: "waiting" },
      ],
    };
  }
  if (event.finished) {
    const f = event.finished;
    return {
      ...run,
      // Anything still waiting when the run ends was never answered.
      confirmations: run.confirmations.map((c) => (c.state === "waiting" ? { ...c, state: "declined" } : c)),
      finished: { outcome: f.outcome ?? "OUTCOME_UNSPECIFIED", steps: f.steps ?? run.steps.length, inputTokens: f.inputTokens ?? "0", outputTokens: f.outputTokens ?? "0" },
    };
  }
  return run;
}

export function setConfirmation(run: AgentRun, confirmationId: string, state: Confirmation["state"]): AgentRun {
  return { ...run, confirmations: run.confirmations.map((c) => (c.confirmationId === confirmationId ? { ...c, state } : c)) };
}

export function setSuggestion(run: AgentRun, id: string, state: SuggestionState): AgentRun {
  return { ...run, suggestions: run.suggestions.map((s) => (s.id === id ? { ...s, state } : s)) };
}

/** "searched, read 2 notes, proposed 2 edits": the collapsed steps line. */
export function stepsSummary(run: AgentRun): string {
  const parts: string[] = [];
  const count = (tool: string) => run.steps.filter((s) => s.tool === tool).length;
  if (count("search") + count("search_notes") + count("semantic_search") > 0) parts.push("searched");
  const reads = count("get_note");
  if (reads > 0) parts.push(`read ${reads} note${reads === 1 ? "" : "s"}`);
  if (run.suggestions.length > 0) parts.push(`proposed ${run.suggestions.length} edit${run.suggestions.length === 1 ? "" : "s"}`);
  const asks = run.confirmations.length;
  if (asks > 0) parts.push(`asked ${asks === 1 ? "once" : `${asks} times`}`);
  return parts.join(", ") || `${run.steps.length} step${run.steps.length === 1 ? "" : "s"}`;
}

/** A human description of a tool call, for the step list. */
export function describeStep(step: Step): string {
  const q = typeof step.args.query === "string" ? `“${step.args.query}”` : "";
  switch (step.tool) {
    case "search":
    case "search_notes":
      return `Searched ${q}`.trim();
    case "semantic_search":
      return `Searched by meaning ${q}`.trim();
    case "search_code":
      return `Searched code ${q}`.trim();
    case "get_note":
      return "Read a note";
    case "apply_edit":
      return "Proposed edits as suggestions";
    case "suggest_titles":
      return `Looked up titles ${typeof step.args.prefix === "string" ? `“${step.args.prefix}”` : ""}`.trim();
    case "delete_note":
      return "Asked to delete a note";
    case "set_note_role":
      return "Asked to change who can see a note";
    case "create_share_link":
      return "Asked to create a share link";
    default:
      return step.tool.replace(/_/g, " ");
  }
}
