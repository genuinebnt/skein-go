/**
 * The REST client. Every call goes through `call`, typed by the operations
 * generated from the Swagger spec (schema.gen.ts), and follows the API's
 * conventions (docs/api/README.md):
 *
 * - Bearer token on every request; on UNAUTHENTICATED with reason
 *   TOKEN_EXPIRED it refreshes once and retries.
 * - An Idempotency-Key on POSTs that create something, generated per logical
 *   call and reused for its retries, so a retry can never create twice.
 * - X-Request-Id on every request, surfaced on errors.
 * - RateLimit-* headers recorded per call; 429 and 503 honour Retry-After
 *   (plus jitter) for requests that are safe to repeat, up to a cap.
 * - Cursor pagination with `pages` and `listAll`.
 *
 * Transport is injected (`fetch`), which is how mock mode swaps in the fake
 * server without touching any other code.
 */

import { ApiError, NetworkError, errorFromResponse } from "./errors";
import { operations, type OperationId, type Operations } from "./schema.gen";

export type RateLimit = { limit: number; remaining: number; resetSeconds: number };

export type CallOptions = {
  /** true generates a key; a string is used as is (reuse it when the user retries by hand). */
  idempotencyKey?: string | boolean;
  signal?: AbortSignal;
  /** Skip the access token (sign-in, sign-up, refresh). */
  anonymous?: boolean;
};

export type CallMeta = { requestId: string; replayed: boolean; rateLimit: RateLimit | null };

/** What the client needs from the session: the token, and a way to renew it. */
export type AuthHooks = {
  accessToken: () => string | null;
  /** Refreshes the session; resolves false when it can't (the user must sign in). */
  refresh: () => Promise<boolean>;
};

export type ClientOptions = {
  baseUrl: string;
  fetch?: typeof fetch;
  auth?: AuthHooks;
  /** Longest automatic wait before retrying a 429/503, in seconds; longer waits are thrown to the caller. */
  maxAutoRetrySeconds?: number;
  sleep?: (ms: number) => Promise<void>;
  random?: () => number;
  newId?: () => string;
  onRateLimit?: (limit: RateLimit) => void;
};

/** POST operations that change state without creating anything, which the docs say need no key. */
const NATURALLY_IDEMPOTENT = new Set<OperationId>([
  "DocumentService_ApplyOps",
  "NotificationService_MarkRead",
  "DocumentService_GetTextAt",
  "DocumentService_Diff",
]);

const defaultSleep = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms));

export function parseRateLimit(headers: Headers): RateLimit | null {
  const limit = headers.get("RateLimit-Limit");
  const remaining = headers.get("RateLimit-Remaining");
  const reset = headers.get("RateLimit-Reset");
  if (limit === null || remaining === null) return null;
  return { limit: Number(limit), remaining: Number(remaining), resetSeconds: Number(reset ?? 0) };
}

/** Fills the path template and query string from params, repeating array parameters. */
export function buildUrl(baseUrl: string, id: OperationId, params: Record<string, unknown>): string {
  const op = operations[id];
  let path: string = op.path;
  for (const name of op.pathParams) {
    const value = params[name];
    if (typeof value !== "string" || value === "") throw new Error(`${id}: missing path parameter ${name}`);
    path = path.replace(`{${name}}`, encodeURIComponent(value));
  }
  const query = new URLSearchParams();
  for (const name of op.queryParams as readonly string[]) {
    const value = params[name];
    if (value === undefined || value === null || value === "") continue;
    for (const item of Array.isArray(value) ? value : [value]) query.append(name, String(item));
  }
  const qs = query.toString();
  return `${baseUrl.replace(/\/$/, "")}${path}${qs ? `?${qs}` : ""}`;
}

export class ApiClient {
  lastRateLimit: RateLimit | null = null;
  private readonly fetchImpl: typeof fetch;
  private readonly sleep: (ms: number) => Promise<void>;
  private readonly random: () => number;
  private readonly newId: () => string;
  private readonly maxAutoRetrySeconds: number;
  private refreshing: Promise<boolean> | null = null;

  constructor(private readonly options: ClientOptions) {
    this.fetchImpl = options.fetch ?? ((input, init) => globalThis.fetch(input, init));
    this.sleep = options.sleep ?? defaultSleep;
    this.random = options.random ?? Math.random;
    this.newId = options.newId ?? (() => crypto.randomUUID());
    this.maxAutoRetrySeconds = options.maxAutoRetrySeconds ?? 5;
  }

  get baseUrl(): string {
    return this.options.baseUrl;
  }

  /** Calls one operation and returns its response body. */
  async call<K extends OperationId>(
    id: K,
    params: Operations[K]["params"],
    body?: Operations[K]["body"],
    options: CallOptions = {},
  ): Promise<Operations[K]["response"]> {
    return (await this.callWithMeta(id, params, body, options)).body;
  }

  async callWithMeta<K extends OperationId>(
    id: K,
    params: Operations[K]["params"],
    body?: Operations[K]["body"],
    options: CallOptions = {},
  ): Promise<{ body: Operations[K]["response"]; meta: CallMeta }> {
    const op = operations[id];
    const url = buildUrl(this.options.baseUrl, id, params as Record<string, unknown>);
    let key: string | undefined;
    if (op.method === "POST" && options.idempotencyKey !== false && !NATURALLY_IDEMPOTENT.has(id)) {
      key = typeof options.idempotencyKey === "string" ? options.idempotencyKey : options.idempotencyKey ? this.newId() : undefined;
    }
    const payload = op.hasBody ? JSON.stringify(body ?? {}) : undefined;
    const response = await this.send(op.method, url, payload, { ...options, idempotencyKey: key }, op.method !== "POST" || key !== undefined || NATURALLY_IDEMPOTENT.has(id));
    const text = await response.text();
    return {
      body: (text ? JSON.parse(text) : {}) as Operations[K]["response"],
      meta: {
        requestId: response.headers.get("X-Request-Id") ?? "",
        replayed: response.headers.get("Idempotent-Replayed") === "true",
        rateLimit: parseRateLimit(response.headers),
      },
    };
  }

  /**
   * A route that is not in the spec yet (proposed in the PR, served by mock
   * mode). Callers treat NOT_FOUND and UNIMPLEMENTED as "not available".
   */
  async proposed<T>(method: "GET" | "POST" | "PUT" | "DELETE", path: string, body?: unknown): Promise<T> {
    const url = `${this.options.baseUrl.replace(/\/$/, "")}${path}`;
    const response = await this.send(method, url, body === undefined ? (method === "GET" ? undefined : "{}") : JSON.stringify(body), {}, method !== "POST");
    const text = await response.text();
    return (text ? JSON.parse(text) : {}) as T;
  }

  /** Yields each page's items, following nextPageToken until the last page. */
  async *pages<K extends OperationId, Item>(
    id: K,
    params: Operations[K]["params"],
    items: (response: Operations[K]["response"]) => Item[] | undefined,
    pageSize = 100,
  ): AsyncGenerator<Item[]> {
    let token: string | undefined;
    do {
      const response = await this.call(id, { ...params, "page.pageSize": pageSize, "page.pageToken": token } as Operations[K]["params"]);
      yield items(response) ?? [];
      token = (response as { page?: { nextPageToken?: string } }).page?.nextPageToken || undefined;
    } while (token);
  }

  /** Every item of a paginated list, stopping at `limit` items. */
  async listAll<K extends OperationId, Item>(
    id: K,
    params: Operations[K]["params"],
    items: (response: Operations[K]["response"]) => Item[] | undefined,
    limit = 5000,
  ): Promise<Item[]> {
    const all: Item[] = [];
    for await (const page of this.pages(id, params, items, 500)) {
      all.push(...page);
      if (all.length >= limit) return all.slice(0, limit);
    }
    return all;
  }

  private async send(
    method: string,
    url: string,
    body: string | undefined,
    options: CallOptions,
    safeToRepeat: boolean,
  ): Promise<Response> {
    let refreshed = false;
    let retries = 0;
    const requestId = this.newId();
    for (;;) {
      const headers: Record<string, string> = { Accept: "application/json", "X-Request-Id": requestId };
      if (body !== undefined) headers["Content-Type"] = "application/json";
      if (typeof options.idempotencyKey === "string") headers["Idempotency-Key"] = options.idempotencyKey;
      const token = options.anonymous ? null : this.options.auth?.accessToken();
      if (token) headers.Authorization = `Bearer ${token}`;

      let response: Response;
      try {
        response = await this.fetchImpl(url, { method, headers, body, signal: options.signal });
      } catch (cause) {
        if (cause instanceof DOMException && cause.name === "AbortError") throw cause;
        throw new NetworkError(cause);
      }
      const limit = parseRateLimit(response.headers);
      if (limit) {
        this.lastRateLimit = limit;
        this.options.onRateLimit?.(limit);
      }
      if (response.ok) return response;

      const error = errorFromResponse(response.status, await response.text(), response.headers.get("Retry-After"));
      if (error.tokenExpired && !refreshed && this.options.auth && !options.anonymous) {
        refreshed = true;
        if (await this.refreshOnce()) continue;
      }
      const wait = error.retryAfter ?? (error.code === "ABORTED" ? 0.5 : undefined);
      if (safeToRepeat && error.retryable && retries < 2 && wait !== undefined && wait <= this.maxAutoRetrySeconds) {
        retries += 1;
        // Jitter spreads clients that were limited together, as the docs ask.
        await this.sleep((wait + this.random() * Math.min(1, wait / 2 + 0.25)) * 1000);
        continue;
      }
      throw error;
    }
  }

  /** One refresh at a time: concurrent calls that hit an expired token share it. */
  private refreshOnce(): Promise<boolean> {
    if (!this.refreshing) {
      this.refreshing = this.options.auth!.refresh().finally(() => {
        this.refreshing = null;
      });
    }
    return this.refreshing;
  }
}

export { ApiError, NetworkError };
