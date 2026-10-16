import createClient, { type Middleware } from "openapi-fetch";
import type { components, paths } from "./schema";

export type S = components["schemas"];

let csrfToken = "";
let unauthorized: () => void = () => {};

export function setCsrfToken(t: string) {
  csrfToken = t;
}

export function onUnauthorized(fn: () => void) {
  unauthorized = fn;
}

const middleware: Middleware = {
  onRequest({ request }) {
    if (request.method !== "GET" && request.method !== "HEAD" && csrfToken) {
      request.headers.set("X-CSRF-Token", csrfToken);
    }
    return request;
  },
  onResponse({ request, response }) {
    const path = new URL(request.url).pathname;
    if (response.status === 401 && !path.startsWith("/api/v1/auth/") && !path.startsWith("/api/v1/setup")) {
      unauthorized();
    }
    return response;
  },
};

export const api = createClient<paths>({ baseUrl: "/api/v1", credentials: "same-origin" });
api.use(middleware);

/** Error thrown for non-2xx API responses. fields maps form fields to messages. */
export class ApiError extends Error {
  status: number;
  fields: Record<string, string>;
  constructor(status: number, body: S["Error"] | undefined) {
    super(body?.error ?? `HTTP ${status}`);
    this.status = status;
    this.fields = {};
    for (const f of body?.fields ?? []) this.fields[f.field] = f.message;
  }
  /** Human readable message including field errors. */
  get detail(): string {
    const parts = Object.entries(this.fields).map(([k, v]) => `${k}: ${v}`);
    return parts.length ? `${this.message} (${parts.join("; ")})` : this.message;
  }
}

/** Unwraps an openapi-fetch result: returns data or throws ApiError. */
export async function unwrap<T>(p: Promise<{ data?: T; error?: unknown; response: Response }>): Promise<T> {
  const { data, error, response } = await p;
  if (!response.ok) throw new ApiError(response.status, error as S["Error"] | undefined);
  return data as T;
}

export function errorMessage(e: unknown): string {
  if (e instanceof ApiError) return e.detail;
  if (e instanceof Error) return e.message;
  return String(e);
}
