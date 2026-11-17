#!/usr/bin/env node
// Builds web/.mock-seed/ from testdata/seed/small for mock mode
// (src/mocks): one seed.json with the records the fake server serves, and a
// copy of every note's op log (ops/<noteId>.ops, the frame format of
// docs/seed-data.md). The seed's own manifest is the source of truth; this
// only reshapes it, dropping fields the web client never reads.
//
// Usage: node scripts/build-mock-seed.mjs [--if-missing] [--seed-dir DIR]

import { copyFileSync, existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const args = process.argv.slice(2);
const dirFlag = args.indexOf("--seed-dir");
const seedDir = resolve(dirFlag >= 0 ? args[dirFlag + 1] : resolve(here, "../../testdata/seed/small"));
// Outside public/ on purpose: vite.config.ts serves it in mock mode only, so a
// live build never ships 15 MB of seed data.
const outDir = resolve(here, "../.mock-seed");

if (args.includes("--if-missing") && existsSync(resolve(outDir, "seed.json"))) process.exit(0);

const jsonl = (name) =>
  readFileSync(resolve(seedDir, name), "utf8")
    .split("\n")
    .filter(Boolean)
    .map((line) => JSON.parse(line));

const manifest = JSON.parse(readFileSync(resolve(seedDir, "manifest.json"), "utf8"));
const notes = jsonl("notes.jsonl");
const tags = jsonl("tags.jsonl");
const tagsByNote = new Map();
for (const t of tags) tagsByNote.set(t.note_id, [...(tagsByNote.get(t.note_id) ?? []), t.tag]);
const topicsFile = JSON.parse(readFileSync(resolve(seedDir, "truth/clustering/topics.json"), "utf8"));
const topics = topicsFile.topics.map((t) => ({ id: t.id, name: t.name, vocab: t.vocab }));
const noteTopics = Object.fromEntries(jsonl("truth/clustering/note_topics.jsonl").map((r) => [r.note_id, r.topic]));

const seed = {
  referenceTime: manifest.reference_time,
  users: jsonl("users.jsonl").map((u) => ({ id: u.id, email: u.email, displayName: u.display_name, handle: u.handle, createdAt: u.created_at, cast: u.cast_key })),
  workspaces: jsonl("workspaces.jsonl").map((w) => ({ id: w.id, name: w.name, slug: w.slug, ownerId: w.owner_id, createdAt: w.created_at })),
  memberships: jsonl("memberships.jsonl").map((m) => ({ workspaceId: m.workspace_id, userId: m.user_id, role: m.role, joinedAt: m.joined_at, removedAt: m.removed_at })),
  replicas: jsonl("replicas.jsonl").map((r) => ({ replicaId: r.replica_id, userId: r.user_id, agentRunId: r.agent_run_id, device: r.device })),
  notes: notes.map((n) => ({
    id: n.id,
    workspaceId: n.workspace_id,
    title: n.title,
    createdBy: n.created_by,
    createdAt: n.created_at,
    updatedAt: n.updated_at,
    trashed: n.trashed,
    isRoot: n.is_root,
    charCount: n.char_count,
    tags: tagsByNote.get(n.id) ?? [],
    topic: noteTopics[n.id] ?? null,
  })),
  blocks: jsonl("blocks.jsonl").map((b) => ({ noteId: b.note_id, blockId: b.block_id, type: b.type, indent: b.indent, checked: b.checked ?? false, language: b.language ?? "", text: b.text, authorId: b.author_id })),
  batches: jsonl("batches.jsonl").map((b) => ({ noteId: b.note_id, seq: b.seq, replicaId: b.replica_id, userId: b.user_id, agentRunId: b.agent_run_id, kind: b.kind, deliveredAt: b.delivered_at, opCount: b.op_count })),
  rejected: jsonl("rejected_ops.jsonl").map((r) => ({ noteId: r.note_id, replicaId: r.replica_id, userId: r.user_id, reason: r.reason, opsBase64: r.ops_base64, deliveredAt: r.delivered_at })),
  links: jsonl("links.jsonl").filter((l) => !l.removed_at && l.target_note_id).map((l) => ({ workspaceId: l.workspace_id, from: l.source_note_id, to: l.target_note_id })),
  comments: jsonl("comments.jsonl").map((c) => ({ id: c.id, noteId: c.note_id, blockId: c.block_id, start: c.start, end: c.end, quote: c.quote, parentId: c.parent_id, authorId: c.author_id, body: c.body, createdAt: c.created_at, resolved: c.resolved })),
  notifications: jsonl("notifications.jsonl").map((n) => ({
    id: n.id,
    userId: n.user_id,
    kind: n.kind,
    title: n.title,
    body: n.body ?? "",
    actorId: n.actor_id ?? "",
    createdAt: n.created_at,
    read: n.read,
    location: n.location ? { noteId: n.location.note_id, blockId: n.location.block_id ?? "", start: n.location.start ?? 0, end: n.location.end ?? 0 } : null,
  })),
  chats: jsonl("chats.jsonl").map((c) => ({ id: c.id, workspaceId: c.workspace_id, userId: c.user_id, noteId: c.note_id, title: c.title, updatedAt: c.updated_at, messages: c.messages.map((m) => ({ role: m.role, text: m.text, runId: m.run_id })) })),
  acl: jsonl("acl.jsonl").map((a) => ({ noteId: a.note_id, userId: a.user_id, role: a.role })),
  shareLinks: jsonl("share_links.jsonl").map((s) => ({ id: s.id, noteId: s.note_id, role: s.role, createdBy: s.created_by, createdAt: s.created_at, expiresAt: s.expires_at, revokedAt: s.revoked_at })),
  topics,
  stopwords: topicsFile.embedder.stopwords,
};

mkdirSync(resolve(outDir, "ops"), { recursive: true });
for (const n of notes) copyFileSync(resolve(seedDir, n.ops_path), resolve(outDir, "ops", `${n.id}.ops`));
writeFileSync(resolve(outDir, "seed.json"), JSON.stringify(seed));
console.log(`wrote web/.mock-seed: ${seed.notes.length} notes, ${seed.blocks.length} blocks, ${notes.length} op logs (seed ${manifest.seed}, ${manifest.profile})`);
