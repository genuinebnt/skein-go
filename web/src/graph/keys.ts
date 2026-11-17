/**
 * Arrow-key movement between notes on the canvas, from genuine.dev
 * (frontend/lib/graphKeys.ts): the nearest note lying within 45° of the
 * pressed direction, or nothing, so a key never jumps somewhere surprising.
 */

export type Direction = "left" | "right" | "up" | "down";
type Point = { id: string; x: number; y: number };

const VECTOR: Record<Direction, [number, number]> = { left: [-1, 0], right: [1, 0], up: [0, -1], down: [0, 1] };

export const ARROW_DIRECTION: Record<string, Direction> = { ArrowLeft: "left", ArrowRight: "right", ArrowUp: "up", ArrowDown: "down" };

export function nextInDirection(from: Point, others: Point[], direction: Direction): string | null {
  const [dx, dy] = VECTOR[direction];
  let best: { id: string; distance: number } | null = null;
  for (const other of others) {
    if (other.id === from.id) continue;
    const along = (other.x - from.x) * dx + (other.y - from.y) * dy;
    const across = Math.abs((other.x - from.x) * dy - (other.y - from.y) * dx);
    if (along <= 0 || across > along) continue;
    const distance = Math.hypot(other.x - from.x, other.y - from.y);
    if (!best || distance < best.distance) best = { id: other.id, distance };
  }
  return best?.id ?? null;
}
