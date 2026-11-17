/**
 * The explorer's selection and route picking, from genuine.dev
 * (frontend/lib/graphInteraction.ts): which note is selected, and the two
 * ends of a shortest path, picked with the selects or by clicking twice on
 * the canvas. Pure, so every transition is tested.
 */

export type Picking = "from" | "to" | null;
export type Interaction = { selected: string | null; pathFrom: string; pathTo: string; picking: Picking };

export type InteractionEvent =
  | { type: "select"; id: string | null }
  | { type: "pick-route" }
  | { type: "set-route"; from: string; to: string }
  | { type: "clear" };

export const initialInteraction = (selected: string | null): Interaction => ({ selected, pathFrom: "", pathTo: "", picking: null });

export function interact(state: Interaction, event: InteractionEvent): Interaction {
  switch (event.type) {
    case "select":
      if (!state.picking) return { ...state, selected: event.id };
      // A click on empty canvas abandons the pick.
      if (!event.id) return { ...state, picking: null };
      if (state.picking === "from") return { ...state, selected: event.id, pathFrom: event.id, pathTo: "", picking: "to" };
      // The route needs two different ends; keep waiting.
      if (event.id === state.pathFrom) return state;
      return { ...state, selected: event.id, pathTo: event.id, picking: null };
    case "pick-route":
      return { ...state, pathFrom: "", pathTo: "", picking: "from" };
    case "set-route":
      return { ...state, pathFrom: event.from, pathTo: event.to, picking: null };
    case "clear":
      return { ...state, selected: null, picking: null };
  }
}

export function routeEnds(state: Interaction): { from: string; to: string } | null {
  return state.pathFrom && state.pathTo && state.pathFrom !== state.pathTo ? { from: state.pathFrom, to: state.pathTo } : null;
}
