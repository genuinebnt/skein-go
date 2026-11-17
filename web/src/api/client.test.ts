import { describe, expect, it, vi } from "vitest";
import { ApiClient, buildUrl } from "./client";
import { ApiError, NetworkError, describeError, errorFromResponse } from "./errors";

type Handler = (url: string, init: RequestInit) => Response | Promise<Response>;

function client(handler: Handler, extra: Partial<ConstructorParameters<typeof ApiClient>[0]> = {}) {
  const calls: { url: string; init: RequestInit }[] = [];
  const fetchImpl = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ url: String(input), init: init ?? {} });
    return handler(String(input), init ?? {});
  }) as unknown as typeof fetch;
  let n = 0;
  const api = new ApiClient({ baseUrl: "https://gw.test", fetch: fetchImpl, sleep: async () => {}, random: () => 0, newId: () => `id-${++n}`, ...extra });
  return { api, calls };
}

const json = (status: number, body: unknown, headers: Record<string, string> = {}) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json", ...headers } });

const status = (code: number, message: string, details: unknown[] = []) => ({ code, message, details });

describe("buildUrl", () => {
  it("fills path variables and repeats array query parameters with their dotted names", () => {
    const url = buildUrl("https://gw.test/", "SearchService_Search", {
      workspaceId: "ws 1",
      query: "func \\w+",
      mode: "SEARCH_MODE_REGEX",
      languages: ["go", "rust"],
      "page.pageSize": 20,
      caseSensitive: undefined,
    });
    expect(url).toBe("https://gw.test/v1/workspaces/ws%201/search?query=func+%5Cw%2B&mode=SEARCH_MODE_REGEX&languages=go&languages=rust&page.pageSize=20");
  });

  it("refuses a missing path parameter instead of calling a wrong URL", () => {
    expect(() => buildUrl("https://gw.test", "DocumentService_GetNote", {})).toThrow(/noteId/);
  });
});

describe("error mapping", () => {
  it("parses a google.rpc.Status body into typed details", () => {
    const err = errorFromResponse(
      400,
      JSON.stringify(
        status(3, "title must not be empty", [
          { "@type": "type.googleapis.com/google.rpc.BadRequest", fieldViolations: [{ field: "title", description: "must not be empty" }] },
          { "@type": "type.googleapis.com/google.rpc.RequestInfo", requestId: "req-7" },
        ]),
      ),
      null,
    );
    expect(err.code).toBe("INVALID_ARGUMENT");
    expect(err.details.fieldViolations).toEqual([{ field: "title", description: "must not be empty" }]);
    expect(err.details.requestId).toBe("req-7");
    expect(describeError(err)).toBe("title: must not be empty");
  });

  it("falls back to the HTTP status when the body is not a Status (a proxy error page)", () => {
    const err = errorFromResponse(503, "<html>bad gateway</html>", "2");
    expect(err.code).toBe("UNAVAILABLE");
    expect(err.retryAfter).toBe(2);
    expect(err.retryable).toBe(true);
  });

  it("reads RetryInfo durations when there is no Retry-After header", () => {
    const err = errorFromResponse(429, JSON.stringify(status(8, "slow down", [{ "@type": "type.googleapis.com/google.rpc.RetryInfo", retryDelay: "3.5s" }])), null);
    expect(err.retryAfter).toBe(3.5);
    expect(describeError(err)).toBe("Too many requests. Try again in 4 s.");
  });
});

describe("ApiClient", () => {
  it("sends the bearer token and a request ID, and returns the parsed body", async () => {
    const { api, calls } = client(() => json(200, { note: { id: "n1", title: "Raft" } }), {
      auth: { accessToken: () => "tok", refresh: async () => true },
    });
    const res = await api.call("DocumentService_GetNote", { noteId: "n1", includeMarkdown: true });
    expect(res.note?.title).toBe("Raft");
    const headers = calls[0]!.init.headers as Record<string, string>;
    expect(headers.Authorization).toBe("Bearer tok");
    expect(headers["X-Request-Id"]).toBe("id-1");
    expect(calls[0]!.url).toBe("https://gw.test/v1/notes/n1?includeMarkdown=true");
  });

  it("refreshes once on TOKEN_EXPIRED and retries with the new token", async () => {
    let token = "old";
    const refresh = vi.fn(async () => {
      token = "new";
      return true;
    });
    let n = 0;
    const { api, calls } = client(
      () =>
        ++n === 1
          ? json(401, status(16, "expired", [{ "@type": "type.googleapis.com/google.rpc.ErrorInfo", reason: "TOKEN_EXPIRED" }]))
          : json(200, { user: { id: "u1" } }),
      { auth: { accessToken: () => token, refresh } },
    );
    await api.call("IdentityService_GetUser", { userId: "u1" });
    expect(refresh).toHaveBeenCalledTimes(1);
    expect((calls[1]!.init.headers as Record<string, string>).Authorization).toBe("Bearer new");
  });

  it("does not refresh for TOKEN_INVALID", async () => {
    const refresh = vi.fn(async () => true);
    const { api } = client(() => json(401, status(16, "bad", [{ "@type": "type.googleapis.com/google.rpc.ErrorInfo", reason: "TOKEN_INVALID" }])), {
      auth: { accessToken: () => "x", refresh },
    });
    await expect(api.call("IdentityService_GetUser", { userId: "u" })).rejects.toMatchObject({ code: "UNAUTHENTICATED" });
    expect(refresh).not.toHaveBeenCalled();
  });

  it("puts one Idempotency-Key on a create and reuses it when retrying", async () => {
    let n = 0;
    const { api, calls } = client(() => (++n === 1 ? json(503, status(14, "busy"), { "Retry-After": "1" }) : json(200, { note: { id: "n" } })));
    await api.call("DocumentService_CreateNote", { workspaceId: "w" }, { title: "T" }, { idempotencyKey: true });
    const keys = calls.map((c) => (c.init.headers as Record<string, string>)["Idempotency-Key"]);
    expect(keys[0]).toBeTruthy();
    expect(keys[0]).toBe(keys[1]);
  });

  it("sends no Idempotency-Key on naturally idempotent POSTs such as ApplyOps", async () => {
    const { api, calls } = client(() => json(200, {}));
    await api.call("DocumentService_ApplyOps", { noteId: "n" }, { replicaId: "1", ops: "AA==" }, { idempotencyKey: true });
    expect((calls[0]!.init.headers as Record<string, string>)["Idempotency-Key"]).toBeUndefined();
  });

  it("does not retry a POST without a key, since a retry could create twice", async () => {
    const { api, calls } = client(() => json(503, status(14, "busy"), { "Retry-After": "1" }));
    await expect(api.call("AgentService_CreateChat", { workspaceId: "w" }, {}, { idempotencyKey: false })).rejects.toBeInstanceOf(ApiError);
    expect(calls).toHaveLength(1);
  });

  it("waits Retry-After plus jitter on 429 for a GET, then gives the error to the caller when the wait is too long", async () => {
    const sleep = vi.fn(async () => {});
    let n = 0;
    const { api } = client(() => (++n === 1 ? json(429, status(8, "slow"), { "Retry-After": "2" }) : json(200, { hits: [] })), { sleep });
    await api.call("SearchService_Search", { workspaceId: "w", query: "raft" });
    expect(sleep).toHaveBeenCalledWith(2000);

    const { api: slow } = client(() => json(429, status(8, "slow"), { "Retry-After": "60" }), { sleep });
    await expect(slow.call("SearchService_Search", { workspaceId: "w", query: "raft" })).rejects.toMatchObject({ code: "RESOURCE_EXHAUSTED", retryAfter: 60 });
  });

  it("records RateLimit headers", async () => {
    const onRateLimit = vi.fn();
    const { api } = client(() => json(200, {}, { "RateLimit-Limit": "100", "RateLimit-Remaining": "42", "RateLimit-Reset": "17" }), { onRateLimit });
    const { meta } = await api.callWithMeta("NotificationService_GetPreferences", {});
    expect(meta.rateLimit).toEqual({ limit: 100, remaining: 42, resetSeconds: 17 });
    expect(api.lastRateLimit).toEqual(meta.rateLimit);
    expect(onRateLimit).toHaveBeenCalledOnce();
  });

  it("follows nextPageToken until the last page", async () => {
    const { api, calls } = client((url) => {
      const token = new URL(url).searchParams.get("page.pageToken");
      return json(200, token === "p2" ? { notes: [{ id: "c" }], page: {} } : { notes: [{ id: "a" }, { id: "b" }], page: { nextPageToken: "p2" } });
    });
    const notes = await api.listAll("DocumentService_ListNotes", { workspaceId: "w" }, (r) => r.notes);
    expect(notes.map((n) => n.id)).toEqual(["a", "b", "c"]);
    expect(calls).toHaveLength(2);
  });

  it("turns a failed fetch into a NetworkError", async () => {
    const { api } = client(() => {
      throw new TypeError("Failed to fetch");
    });
    await expect(api.call("NotificationService_GetPreferences", {})).rejects.toBeInstanceOf(NetworkError);
  });
});
