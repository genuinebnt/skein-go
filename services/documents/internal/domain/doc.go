// Package domain holds the documents service's entities and pure rules:
// notes and their lifecycle (trash, restore, purge), the op log's batches,
// change groups for undo and redo, version grouping for history, blame
// attribution, the two-level text diff, the projection published to search
// and ml, and the domain events. Nothing here does I/O or starts goroutines;
// the app layer runs these rules inside each note's actor.
//
// # Batches and the op log
//
// The op log (ADR-0006) is a sequence of batches per note, numbered from 1.
// A batch holds operations of one replica with consecutive counters (or
// only attribute writes), plus who sent them, when, and why (Kind). The
// note's text is the replay of every batch in order. Operations the
// service generates itself (Markdown import, undo, redo) use the reserved
// ServerReplica and record the user they were made for, so blame and undo
// still attribute them to a person.
//
// # Change groups
//
// Undo works per replica on change groups. A replica's edit batches form
// one group while each follows the previous within GroupWindow; every agent
// suggestion batch (one ApplyOps call from an agent run) is a group of its
// own, so rejecting a suggestion undoes all of it. An undo batch moves the
// group to the redo stack; a redo batch moves it back as a new group made of
// the redo batch; a new edit clears the redo stack. The stacks are derived
// from the log alone (UndoStacks), so they survive restarts.
//
// # Porting notes
//
// Kinds are tags with fields (Rust enums), errors are sentinels wrapped
// with context (one thiserror enum), and nothing depends on map order:
// every output built from a map is sorted.
package domain
