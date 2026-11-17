package app

import "github.com/genuinebnt/skein-go/services/documents/internal/ports"

// The ports the use cases need, defined in package ports so fakes and tests
// can import them without importing app.
type (
	Store           = ports.Store
	NoteQuery       = ports.NoteQuery
	Action          = ports.Action
	Check           = ports.Check
	Authorizer      = ports.Authorizer
	AuthCache       = ports.AuthCache
	ReplicaRegistry = ports.ReplicaRegistry
	NoteRegistry    = ports.NoteRegistry
	NoteCache       = ports.NoteCache
	Presence        = ports.Presence
	PresenceStore   = ports.PresenceStore
	Clock           = ports.Clock
	SystemClock     = ports.SystemClock
)

// The actions documents checks.
const (
	ActionRead   = ports.ActionRead
	ActionEdit   = ports.ActionEdit
	ActionCreate = ports.ActionCreate
	ActionDelete = ports.ActionDelete
)
