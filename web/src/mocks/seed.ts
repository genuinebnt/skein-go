/**
 * The seed records mock mode serves (/mock-seed/seed.json, built into web/.mock-seed from
 * testdata/seed/small by scripts/build-mock-seed.mjs), and where they come
 * from: over HTTP in the browser, from disk in tests.
 */

export type SeedUser = { id: string; email: string; displayName: string; handle: string; createdAt: string; cast: string | null };
export type SeedWorkspace = { id: string; name: string; slug: string; ownerId: string; createdAt: string };
export type SeedMembership = { workspaceId: string; userId: string; role: "owner" | "admin" | "member" | "guest"; joinedAt: string; removedAt: string | null };
export type SeedReplica = { replicaId: string; userId: string; agentRunId: string | null; device: string };
export type SeedNote = {
  id: string;
  workspaceId: string;
  title: string;
  createdBy: string;
  createdAt: string;
  updatedAt: string;
  trashed: boolean;
  isRoot: boolean;
  charCount: number;
  tags: string[];
  topic: string | null;
};
export type SeedBlock = { noteId: string; blockId: string; type: string; indent: number; checked: boolean; language: string; text: string; authorId: string };
export type SeedBatch = { noteId: string; seq: number; replicaId: string; userId: string; agentRunId: string | null; kind: string; deliveredAt: string; opCount: number };
export type SeedRejected = { noteId: string; replicaId: string; userId: string; reason: string; opsBase64: string; deliveredAt: string };
export type SeedComment = {
  id: string;
  noteId: string;
  blockId: string;
  start: number;
  end: number;
  quote: string;
  parentId: string | null;
  authorId: string;
  body: string;
  createdAt: string;
  resolved: boolean;
};
export type SeedNotification = {
  id: string;
  userId: string;
  kind: string;
  title: string;
  body: string;
  actorId: string;
  createdAt: string;
  read: boolean;
  location: { noteId: string; blockId: string; start: number; end: number } | null;
};
export type SeedChat = { id: string; workspaceId: string; userId: string; noteId: string | null; title: string; updatedAt: string; messages: { role: string; text: string; runId: string | null }[] };

export type Seed = {
  referenceTime: string;
  users: SeedUser[];
  workspaces: SeedWorkspace[];
  memberships: SeedMembership[];
  replicas: SeedReplica[];
  notes: SeedNote[];
  blocks: SeedBlock[];
  batches: SeedBatch[];
  rejected: SeedRejected[];
  links: { workspaceId: string; from: string; to: string }[];
  comments: SeedComment[];
  notifications: SeedNotification[];
  chats: SeedChat[];
  acl: { noteId: string; userId: string; role: "viewer" | "commenter" | "editor" }[];
  shareLinks: { id: string; noteId: string; role: string; createdBy: string; createdAt: string; expiresAt: string | null; revokedAt: string | null }[];
  topics: { id: string; name: string; vocab: string[] }[];
  stopwords: string[];
};

export type SeedSource = {
  seed(): Promise<Seed>;
  /** A note's op log (frames of uvarint length + batch), or null when it has none. */
  ops(noteId: string): Promise<Uint8Array | null>;
};

export function httpSeedSource(base = `${import.meta.env.BASE_URL}mock-seed`): SeedSource {
  return {
    seed: async () => {
      const res = await fetch(`${base}/seed.json`);
      if (!res.ok) throw new Error("mock seed missing: run `pnpm gen:seed`");
      return (await res.json()) as Seed;
    },
    ops: async (noteId) => {
      const res = await fetch(`${base}/ops/${noteId}.ops`);
      return res.ok ? new Uint8Array(await res.arrayBuffer()) : null;
    },
  };
}

/**
 * Markdown for a note's seed blocks, one Markdown block per seed block in
 * order. Only the structure matters (it is used to recover block types, not
 * text), so text is written as is.
 */
export function composeMarkdown(blocks: Pick<SeedBlock, "type" | "indent" | "checked" | "language" | "text">[]): string {
  return blocks
    .map((b) => {
      const pad = "  ".repeat(b.indent ?? 0);
      const line = b.text.replace(/\n/g, " ");
      switch (b.type) {
        case "heading1":
          return `# ${line}`;
        case "heading2":
          return `## ${line}`;
        case "heading3":
          return `### ${line}`;
        case "bulleted":
          return `${pad}- ${line}`;
        case "numbered":
          return `${pad}1. ${line}`;
        case "todo":
          return `${pad}- [${b.checked ? "x" : " "}] ${line}`;
        case "quote":
          return `> ${line}`;
        case "code":
          return `\`\`\`\`${b.language ?? ""}\n${b.text}\n\`\`\`\``;
        case "divider":
          return "---";
        case "table":
          return b.text;
        default:
          return line || "&nbsp;";
      }
    })
    .join("\n\n");
}

/** Splits an op log into its batches (docs/seed-data.md "Ops files"). */
export function splitFrames(log: Uint8Array): Uint8Array[] {
  const frames: Uint8Array[] = [];
  let pos = 0;
  while (pos < log.length) {
    let length = 0;
    let shift = 0;
    for (;;) {
      const b = log[pos++]!;
      length += (b & 0x7f) * 2 ** shift;
      if ((b & 0x80) === 0) break;
      shift += 7;
    }
    frames.push(log.subarray(pos, pos + length));
    pos += length;
  }
  return frames;
}
