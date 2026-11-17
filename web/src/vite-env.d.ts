/// <reference types="vite/client" />

interface ImportMetaEnv {
  /** Gateway origin for live mode, e.g. https://skein.example.com. Empty: same origin (the dev server proxies /v1). */
  readonly VITE_API_BASE?: string;
  /** Where the dev server proxies /v1 in live mode. Default http://localhost:8080. */
  readonly VITE_GATEWAY?: string;
  /** The workspace to open in live mode, until the API can list workspaces. */
  readonly VITE_WORKSPACE_ID?: string;
  readonly VITE_OAUTH_GITHUB_CLIENT_ID?: string;
  readonly VITE_OAUTH_GOOGLE_CLIENT_ID?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
