/**
 * The API's error model (docs/api/README.md#errors): a google.rpc.Status body
 * with a gRPC code and typed details. ApiError carries the parsed details so
 * callers branch on meaning (a token expired, a field was wrong, wait N
 * seconds), never on message text, which is for developers.
 */

/** gRPC status codes by name, as the API reports them in `code`. */
export const GrpcCode = {
  OK: 0,
  CANCELLED: 1,
  UNKNOWN: 2,
  INVALID_ARGUMENT: 3,
  DEADLINE_EXCEEDED: 4,
  NOT_FOUND: 5,
  ALREADY_EXISTS: 6,
  PERMISSION_DENIED: 7,
  RESOURCE_EXHAUSTED: 8,
  FAILED_PRECONDITION: 9,
  ABORTED: 10,
  OUT_OF_RANGE: 11,
  UNIMPLEMENTED: 12,
  INTERNAL: 13,
  UNAVAILABLE: 14,
  DATA_LOSS: 15,
  UNAUTHENTICATED: 16,
} as const;

export type GrpcCodeName = keyof typeof GrpcCode;

const NAME_BY_CODE = new Map<number, GrpcCodeName>(
  Object.entries(GrpcCode).map(([name, code]) => [code, name as GrpcCodeName]),
);

/** The gRPC code a bare HTTP status implies, for errors without a Status body (a proxy's 502). */
const CODE_BY_HTTP: Record<number, GrpcCodeName> = {
  400: "INVALID_ARGUMENT",
  401: "UNAUTHENTICATED",
  403: "PERMISSION_DENIED",
  404: "NOT_FOUND",
  409: "ABORTED",
  429: "RESOURCE_EXHAUSTED",
  499: "CANCELLED",
  501: "UNIMPLEMENTED",
  503: "UNAVAILABLE",
  504: "DEADLINE_EXCEEDED",
};

export type FieldViolation = { field: string; description: string };

/** The typed details the API documents, flattened. Missing details are undefined. */
export type ErrorDetails = {
  /** ErrorInfo.reason: TOKEN_EXPIRED, TOKEN_INVALID, ACTION_NOT_ALLOWED, ... */
  reason?: string;
  /** RequestInfo.requestId, for support and logs. */
  requestId?: string;
  /** RetryInfo.retryDelay in seconds. */
  retryDelaySeconds?: number;
  /** BadRequest.fieldViolations. */
  fieldViolations: FieldViolation[];
  /** QuotaFailure: the budget that ran out. */
  quotaSubjects: string[];
  /** ResourceInfo.resourceType and name. */
  resource?: { type?: string; name?: string };
  /** PreconditionFailure violation descriptions. */
  preconditions: string[];
};

/** A failed API call. */
export class ApiError extends Error {
  override readonly name = "ApiError";
  constructor(
    readonly code: GrpcCodeName,
    readonly httpStatus: number,
    message: string,
    readonly details: ErrorDetails,
    /** Seconds from a Retry-After header, when the response had one. */
    readonly retryAfterSeconds?: number,
  ) {
    super(message);
  }

  /** The wait a caller should honour before retrying, header first, then RetryInfo. */
  get retryAfter(): number | undefined {
    return this.retryAfterSeconds ?? this.details.retryDelaySeconds;
  }

  /** Worth retrying unchanged after a pause (docs/api/README.md error table). */
  get retryable(): boolean {
    return this.code === "UNAVAILABLE" || this.code === "RESOURCE_EXHAUSTED" || this.code === "ABORTED" || this.code === "DEADLINE_EXCEEDED";
  }

  get tokenExpired(): boolean {
    return this.code === "UNAUTHENTICATED" && this.details.reason === "TOKEN_EXPIRED";
  }
}

/** The request never reached the API or its answer never came back (offline, DNS, CORS). */
export class NetworkError extends Error {
  override readonly name = "NetworkError";
  constructor(override readonly cause: unknown) {
    super("network request failed");
  }
}

type AnyDetail = { "@type"?: string; [key: string]: unknown };

/** "3.5s" (proto Duration in JSON) to 3.5. */
export function parseDurationSeconds(value: unknown): number | undefined {
  if (typeof value !== "string") return undefined;
  const match = /^(-?\d+(?:\.\d+)?)s$/.exec(value.trim());
  return match ? Number(match[1]) : undefined;
}

export function parseDetails(raw: unknown): ErrorDetails {
  const details: ErrorDetails = { fieldViolations: [], quotaSubjects: [], preconditions: [] };
  if (!Array.isArray(raw)) return details;
  for (const item of raw as AnyDetail[]) {
    const type = String(item["@type"] ?? "").replace("type.googleapis.com/", "");
    switch (type) {
      case "google.rpc.ErrorInfo":
        details.reason = typeof item.reason === "string" ? item.reason : undefined;
        break;
      case "google.rpc.RequestInfo":
        details.requestId = typeof item.requestId === "string" ? item.requestId : undefined;
        break;
      case "google.rpc.RetryInfo":
        details.retryDelaySeconds = parseDurationSeconds(item.retryDelay);
        break;
      case "google.rpc.BadRequest":
        for (const v of (item.fieldViolations as FieldViolation[] | undefined) ?? []) {
          details.fieldViolations.push({ field: String(v.field ?? ""), description: String(v.description ?? "") });
        }
        break;
      case "google.rpc.QuotaFailure":
        for (const v of (item.violations as { subject?: string }[] | undefined) ?? []) {
          if (v.subject) details.quotaSubjects.push(v.subject);
        }
        break;
      case "google.rpc.ResourceInfo":
        details.resource = { type: item.resourceType as string | undefined, name: item.resourceName as string | undefined };
        break;
      case "google.rpc.PreconditionFailure":
        for (const v of (item.violations as { description?: string }[] | undefined) ?? []) {
          if (v.description) details.preconditions.push(v.description);
        }
        break;
      default:
        break;
    }
  }
  return details;
}

/** Builds an ApiError from a failed response's status, body text and Retry-After header. */
export function errorFromResponse(httpStatus: number, bodyText: string, retryAfterHeader: string | null): ApiError {
  type StatusBody = { code?: unknown; message?: unknown; details?: unknown };
  const body = ((): StatusBody | null => {
    try {
      return bodyText ? (JSON.parse(bodyText) as StatusBody) : null;
    } catch {
      return null;
    }
  })();
  const numeric = typeof body?.code === "number" ? body.code : undefined;
  const code: GrpcCodeName =
    (numeric !== undefined ? NAME_BY_CODE.get(numeric) : undefined) ?? CODE_BY_HTTP[httpStatus] ?? (httpStatus >= 500 ? "INTERNAL" : "UNKNOWN");
  const message = typeof body?.message === "string" && body.message ? body.message : `HTTP ${httpStatus}`;
  const retryAfter = retryAfterHeader !== null && retryAfterHeader.trim() !== "" ? Number(retryAfterHeader) : undefined;
  return new ApiError(code, httpStatus, message, parseDetails(body?.details), Number.isFinite(retryAfter) ? retryAfter : undefined);
}

/**
 * A sentence for people, by meaning. Server messages are for developers and
 * never shown as they are.
 */
export function describeError(error: unknown): string {
  if (error instanceof NetworkError) return "Can't reach the server. Your changes stay on this device.";
  if (!(error instanceof ApiError)) return "Something went wrong.";
  switch (error.code) {
    case "UNAUTHENTICATED":
      return error.tokenExpired ? "Your session expired. Sign in again." : "You're signed out. Sign in again.";
    case "PERMISSION_DENIED":
      return "You don't have access to do that.";
    case "NOT_FOUND":
      return "That doesn't exist, or you can't open it.";
    case "ALREADY_EXISTS":
      return "That already exists.";
    case "INVALID_ARGUMENT":
    case "OUT_OF_RANGE": {
      const first = error.details.fieldViolations[0];
      return first ? `${first.field}: ${first.description}` : "That request wasn't valid.";
    }
    case "FAILED_PRECONDITION":
      return error.details.preconditions[0] ?? "That can't be done right now.";
    case "RESOURCE_EXHAUSTED":
      return error.retryAfter !== undefined ? `Too many requests. Try again in ${Math.ceil(error.retryAfter)} s.` : "Too many requests. Try again shortly.";
    case "ABORTED":
      return "Someone else changed this at the same time. Try again.";
    case "UNAVAILABLE":
    case "DEADLINE_EXCEEDED":
      return "The server is busy. Trying again shortly.";
    case "UNIMPLEMENTED":
      return "The server doesn't support this yet.";
    default:
      return error.details.requestId ? `Something went wrong (request ${error.details.requestId}).` : "Something went wrong.";
  }
}
