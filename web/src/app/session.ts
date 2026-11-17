/**
 * The signed-in session: tokens and user, persisted per the "keep me signed
 * in" choice (localStorage) or for the tab only (sessionStorage), with one
 * refresh at a time. Refresh rotates the refresh token (identity.md); a
 * failed refresh signs the user out.
 */

import type { ApiClient } from "../api/client";
import type { Session, User } from "../api/schema.gen";

const KEY = "skein.session";

type Stored = { session: Session; user: User; persistent: boolean };

export class SessionStore {
  private state: Stored | null = null;
  private listeners = new Set<() => void>();
  private refreshing: Promise<boolean> | null = null;

  constructor() {
    for (const storage of [sessionStorage, localStorage]) {
      try {
        const raw = storage.getItem(KEY);
        if (raw) {
          this.state = JSON.parse(raw) as Stored;
          break;
        }
      } catch {
        // storage unavailable or corrupt: start signed out
      }
    }
  }

  get user(): User | null {
    return this.state?.user ?? null;
  }

  get accessToken(): string | null {
    return this.state?.session.accessToken ?? null;
  }

  get signedIn(): boolean {
    return this.state !== null;
  }

  set(session: Session, user: User, persistent: boolean): void {
    this.state = { session, user, persistent };
    this.save();
  }

  clear(): void {
    this.state = null;
    localStorage.removeItem(KEY);
    sessionStorage.removeItem(KEY);
    this.emit();
  }

  /** Rotates the session once; concurrent callers share the attempt. */
  refresh(api: ApiClient): Promise<boolean> {
    this.refreshing ??= (async () => {
      const current = this.state;
      if (!current?.session.refreshToken) return false;
      try {
        const res = await api.call("IdentityService_Refresh", {}, { refreshToken: current.session.refreshToken }, { anonymous: true });
        if (!res.session) return false;
        this.state = { ...current, session: res.session };
        this.save();
        return true;
      } catch {
        this.clear();
        return false;
      }
    })().finally(() => {
      this.refreshing = null;
    });
    return this.refreshing;
  }

  async signOut(api: ApiClient): Promise<void> {
    const token = this.state?.session.refreshToken;
    this.clear();
    if (token) await api.call("IdentityService_SignOut", {}, { refreshToken: token }, { anonymous: true }).catch(() => undefined);
  }

  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  };

  private save(): void {
    if (!this.state) return;
    const text = JSON.stringify(this.state);
    (this.state.persistent ? localStorage : sessionStorage).setItem(KEY, text);
    (this.state.persistent ? sessionStorage : localStorage).removeItem(KEY);
    this.emit();
  }

  private emit(): void {
    for (const l of this.listeners) l();
  }
}
