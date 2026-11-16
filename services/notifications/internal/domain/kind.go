package domain

import (
	"fmt"
	"strings"
)

// Kind is what a notification is about. The numbers are the proto's
// NotificationKind values, so conversion at the edge is a cast.
type Kind uint8

// The kinds this version knows, in proto order. KindUnspecified is never
// stored.
const (
	KindUnspecified      Kind = 0
	KindMention          Kind = 1
	KindComment          Kind = 2
	KindShare            Kind = 3
	KindAgentRunFinished Kind = 4
	KindAccessChanged    Kind = 5
)

// kindCount is one past the largest known kind; arrays indexed by Kind use it.
const kindCount = 6

// Kinds lists every storable kind in ascending order.
var Kinds = []Kind{KindMention, KindComment, KindShare, KindAgentRunFinished, KindAccessChanged}

// Known reports whether k is a storable kind.
func (k Kind) Known() bool { return k >= KindMention && k < kindCount }

// String returns the short lowercase name used in logs and metrics.
func (k Kind) String() string {
	switch k {
	case KindMention:
		return "mention"
	case KindComment:
		return "comment"
	case KindShare:
		return "share"
	case KindAgentRunFinished:
		return "agent_run_finished"
	case KindAccessChanged:
		return "access_changed"
	default:
		return "unspecified"
	}
}

// ProtoName returns the proto enum value name, as the seed files and JSON use.
func (k Kind) ProtoName() string {
	if !k.Known() {
		return "NOTIFICATION_KIND_UNSPECIFIED"
	}
	return "NOTIFICATION_KIND_" + strings.ToUpper(k.String())
}

// ParseKind accepts a proto enum value name ("NOTIFICATION_KIND_MENTION").
func ParseKind(name string) (Kind, error) {
	for _, k := range Kinds {
		if k.ProtoName() == name {
			return k, nil
		}
	}
	return KindUnspecified, fmt.Errorf("%w: %q", ErrUnknownKind, name)
}
