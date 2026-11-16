package generate

import (
	"fmt"
	"strings"
	"time"

	"github.com/genuinebnt/skein-go/tools/seed/internal/blocks"
	"github.com/genuinebnt/skein-go/tools/seed/internal/policy"
	"github.com/genuinebnt/skein-go/tools/seed/internal/prng"
	"github.com/genuinebnt/skein-go/tools/seed/internal/uuid7"
)

// Notification rules the expected inbox follows (notifications service):
//
//   - MENTION: every user named with @handle in a comment, except the
//     author, who can read the note when the comment is made. Mentioning
//     someone who cannot read the note notifies nobody (the mention stays in
//     the comment's text and mentions list).
//   - COMMENT: the note's creator, for every comment by someone else, and
//     the parent comment's author for every reply by someone else, unless
//     already notified for that comment as mentioned, and only if they can
//     read the note.
//   - SHARE: a user granted a role on a note by an override.
//   - ACCESS_CHANGED: a member removed from a workspace, and a member whose
//     role on a note is lowered by an override.
//   - AGENT_RUN_FINISHED: the user an agent run acted for, when it ends.

var commentBodies = []string{
	"Is this still accurate?", "Can we add numbers here?", "I think this contradicts the section above.",
	"+1, let's do this.", "Who owns this?", "Typo here.", "Should this link to the runbook?",
	"Can we get this reviewed before Friday?", "Nice write-up.", "This needs a decision.",
}

var replyBodies = []string{"Done, updated.", "Good catch, fixing.", "Agreed.", "I'll take it.", "Not sure, asking around.", "Resolved in the latest edit."}

func (p *plan) can(user int, a policy.Action, note *notePlan, at time.Time) bool {
	return p.policy.Evaluate(policy.Subject{UserID: p.users[user].ID}, a, note.ID, at).Allowed
}

// wordSpan picks a range of whole words in text, in code points.
func wordSpan(r *prng.Rand, text string) (int, int) {
	rs := []rune(text)
	var starts []int
	for i := range rs {
		if (i == 0 || rs[i-1] == ' ') && rs[i] != ' ' {
			starts = append(starts, i)
		}
	}
	if len(starts) == 0 {
		return 0, len(rs)
	}
	s := starts[r.IntN(len(starts))]
	e := s
	words := 1 + r.IntN(4)
	for e < len(rs) && words > 0 {
		e++
		if e == len(rs) || rs[e] == ' ' {
			words--
		}
	}
	return s, e
}

// comments writes the note's comment threads and the notifications they
// cause.
func (p *plan) comments(n *notePlan, res *noteResult) {
	if n.Trashed {
		return
	}
	r := p.src.Stream("note.comments", uint64(n.Index))
	ids := p.src.Stream("id.comment", uint64(n.Index))
	nids := p.src.Stream("id.notif-comment", uint64(n.Index))
	w := p.ws(n)
	lambda := p.prof.CommentsPerNote
	if n.Anchor != nil {
		lambda = 1.2
	}
	threads := 0
	for threads < 6 && r.Chance(lambda/(1+lambda)) {
		threads++
	}
	var targets []int
	for i, b := range res.finalBlk {
		if b.Block.Type != blocks.Code && b.Block.Type != blocks.Divider && len([]rune(b.Block.Text)) >= 8 {
			targets = append(targets, i)
		}
	}
	if len(targets) == 0 {
		return
	}
	members := make([]int, 0, len(w.Members))
	for _, m := range w.Members {
		members = append(members, m.User)
	}
	created := n.CreatedAt.Add(time.Hour)
	notify := func(user int, kind, title, body string, actor int, at time.Time, loc *Location, src SourceRef) {
		read := r.Chance(0.2)
		if at.Before(ReferenceTime.Add(-7 * 24 * time.Hour)) {
			read = r.Chance(0.7)
		}
		res.notifs = append(res.notifs, NotificationRecord{ID: uuid7.New(at, nids), UserID: p.users[user].ID, Kind: kind, Title: title, Body: body,
			ActorID: p.users[actor].ID, CreatedAt: ts(at), Read: read, Location: loc, Source: src})
	}
	for th := 0; th < threads; th++ {
		bi := targets[r.IntN(len(targets))]
		b := res.finalBlk[bi]
		start, end := wordSpan(r, b.Block.Text)
		quote := string([]rune(b.Block.Text)[start:end])
		at := between(r, created, ReferenceTime.Add(-2*time.Hour))
		var parentID *string
		parentAuthor := -1
		replies := r.IntN(3)
		resolved := r.Chance(0.3)
		for k := 0; k <= replies; k++ {
			var commenters []int
			for _, u := range members {
				if p.can(u, policy.NoteComment, n, at) {
					commenters = append(commenters, u)
				}
			}
			if len(commenters) == 0 {
				break
			}
			author := prng.Pick(r, commenters)
			body := prng.Pick(r, commentBodies)
			if k > 0 {
				body = prng.Pick(r, replyBodies)
			}
			var mentioned []int
			if r.Chance(0.35) {
				for _, mi := range r.Sample(len(members), 1+r.IntN(2)) {
					if members[mi] != author {
						mentioned = append(mentioned, members[mi])
					}
				}
				var hs []string
				for _, u := range mentioned {
					hs = append(hs, "@"+p.users[u].Handle)
				}
				if len(hs) > 0 {
					body = strings.Join(hs, " ") + " " + body
				}
			}
			mentionIDs := []string{}
			for _, u := range mentioned {
				mentionIDs = append(mentionIDs, p.users[u].ID)
			}
			c := CommentRecord{ID: uuid7.New(at, ids), WorkspaceID: w.ID, NoteID: n.ID, BlockID: b.ID, Start: start, End: end, Quote: quote,
				ParentID: parentID, AuthorID: p.users[author].ID, Body: body, Mentions: mentionIDs, CreatedAt: ts(at), Resolved: resolved && k == 0}
			res.comments = append(res.comments, c)
			loc := &Location{NoteID: n.ID, BlockID: b.ID, Start: start, End: end}
			src := SourceRef{Kind: "comment", ID: c.ID}
			notified := map[int]bool{author: true}
			for _, u := range mentioned {
				if !notified[u] && p.can(u, policy.NoteRead, n, at) {
					notified[u] = true
					notify(u, "NOTIFICATION_KIND_MENTION", fmt.Sprintf("%s mentioned you in %s", p.users[author].Name, n.Title), body, author, at, loc, src)
				}
			}
			for _, u := range []int{n.CreatedBy, parentAuthor} {
				if u >= 0 && !notified[u] && p.can(u, policy.NoteRead, n, at) {
					notified[u] = true
					notify(u, "NOTIFICATION_KIND_COMMENT", fmt.Sprintf("%s commented on %s", p.users[author].Name, n.Title), body, author, at, loc, src)
				}
			}
			if k == 0 {
				parentID, parentAuthor = &res.comments[len(res.comments)-1].ID, author
			}
			at = at.Add(time.Duration(10+r.IntN(3000)) * time.Minute)
			if at.After(ReferenceTime.Add(-time.Hour)) {
				break
			}
		}
	}
}

// planNotifications returns the notifications that follow from the plan:
// shares, role changes and removals.
func (p *plan) planNotifications() []NotificationRecord {
	var out []NotificationRecord
	ids := p.src.Stream("id.notif-plan", 0)
	r := p.src.Stream("plan.notifications", 0)
	for _, o := range p.overrides {
		n := p.notes[o.Note]
		w := p.ws(n)
		role := p.policy.WorkspaceRoleAt(w.ID, p.users[o.User].ID, o.At)
		kind, title := "NOTIFICATION_KIND_SHARE", fmt.Sprintf("%s shared %s with you", p.users[o.GrantedBy].Name, n.Title)
		if role >= policy.Member {
			kind, title = "NOTIFICATION_KIND_ACCESS_CHANGED", fmt.Sprintf("Your access to %s changed", n.Title)
		}
		out = append(out, NotificationRecord{ID: uuid7.New(o.At, ids), UserID: p.users[o.User].ID, Kind: kind, Title: title,
			Body: "Role: " + o.Role.String() + ".", ActorID: p.users[o.GrantedBy].ID, CreatedAt: ts(o.At), Read: r.Chance(0.6),
			Location: &Location{NoteID: n.ID}, Source: SourceRef{Kind: "note_role", ID: n.ID + "/" + p.users[o.User].ID}})
	}
	for _, w := range p.workspaces {
		for _, m := range w.Members {
			if m.RemovedAt != nil {
				out = append(out, NotificationRecord{ID: uuid7.New(*m.RemovedAt, ids), UserID: p.users[m.User].ID, Kind: "NOTIFICATION_KIND_ACCESS_CHANGED",
					Title: "You were removed from " + w.Name, Body: "You no longer have access to this workspace.", ActorID: p.users[w.Owner].ID,
					CreatedAt: ts(*m.RemovedAt), Read: false, Location: nil, Source: SourceRef{Kind: "membership", ID: w.ID + "/" + p.users[m.User].ID}})
			}
		}
	}
	return out
}
