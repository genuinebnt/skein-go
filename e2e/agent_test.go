//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
)

// The agent runs with its offline model in make up-native (no API key),
// which understands "/search <query>" and "/delete <note id>"
// (services/agent fakes/model.offline_step); with ANTHROPIC_API_KEY set the
// same requests go to the real model, which follows them too.

// chat opens a chat in workspace and its socket.
func openChat(t *testing.T, u *user, workspace string) (string, *websocket.Conn) {
	t.Helper()
	var chat skeinv1.CreateChatResponse
	u.call(http.MethodPost, "/v1/workspaces/"+workspace+"/chats", &skeinv1.CreateChatRequest{}, &chat)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := u.dial(ctx, "/v1/chats/"+chat.GetChatId()+"/messages")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "") })
	return chat.GetChatId(), conn
}

// runEvents sends text and collects the run's events until it finishes or
// stop says so (stop sees every event first).
func runEvents(t *testing.T, conn *websocket.Conn, text string, stop func(*skeinv1.SendMessageResponse) bool) []*skeinv1.SendMessageResponse {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), longWait)
	defer cancel()
	if err := writeProto(ctx, conn, &skeinv1.SendMessageRequest{Text: text}); err != nil {
		t.Fatal(err)
	}
	var events []*skeinv1.SendMessageResponse
	for {
		var ev skeinv1.SendMessageResponse
		if err := readProto(ctx, conn, &ev); err != nil {
			t.Fatalf("agent socket after %d events: %v (close %d)", len(events), err, closeCode(err))
		}
		events = append(events, &ev)
		if (stop != nil && stop(&ev)) || ev.GetFinished() != nil {
			return events
		}
	}
}

func TestAgentSearchesAndCitesABlock(t *testing.T) {
	t.Parallel()
	u := signUp(t, "asker")
	ws := u.createWorkspace("Agent " + randomHex(t, 3))
	word := codeword(t)
	note := u.createNote(ws, "Compaction design", "Background.\n\nThe "+word+" compactor merges small segments overnight.")
	eventually(t, longWait, "search indexes the note", func() (bool, string) {
		return hitIn(u.search(ws, map[string][]string{"query": {word}}), note.GetId()) != nil, "not yet"
	})

	_, conn := openChat(t, u, ws)
	events := runEvents(t, conn, "/search "+word, nil)
	var calls []string
	var citation *skeinv1.Citation
	var text strings.Builder
	for _, ev := range events {
		if c := ev.GetToolCall(); c != nil {
			calls = append(calls, c.GetTool())
		}
		if c := ev.GetCitation(); c != nil && citation == nil {
			citation = c
		}
		text.WriteString(ev.GetTextDelta())
	}
	finished := events[len(events)-1].GetFinished()
	if finished.GetOutcome() != skeinv1.RunFinished_OUTCOME_COMPLETED {
		t.Fatalf("run finished %v after %v; text %q", finished, calls, text.String())
	}
	if len(calls) == 0 || calls[0] != "search" {
		t.Errorf("tool calls %v, want a search", calls)
	}
	if citation == nil || citation.GetLocation().GetNoteId() != note.GetId() || citation.GetLocation().GetBlockId() == "" {
		t.Fatalf("citation %v, want a block of the note (answer %q)", citation, text.String())
	}
	if !strings.Contains(citation.GetQuote(), word) {
		t.Errorf("quote %q does not contain the matched text", citation.GetQuote())
	}

	// The run is readable afterwards with the same events.
	var run skeinv1.GetRunResponse
	u.call(http.MethodGet, "/v1/runs/"+events[0].GetRunId(), nil, &run)
	if len(run.GetEvents()) == 0 {
		t.Error("GetRun returned no events")
	}
}

func TestAgentDeletePausesForConfirmationAndDecliningKeepsTheNote(t *testing.T) {
	t.Parallel()
	u := signUp(t, "deleter")
	ws := u.createWorkspace("Agent " + randomHex(t, 3))
	note := u.createNote(ws, "Scratch", "Temporary notes to throw away.")

	_, conn := openChat(t, u, ws)
	events := runEvents(t, conn, "/delete "+note.GetId(), func(ev *skeinv1.SendMessageResponse) bool {
		return ev.GetConfirmation() != nil
	})
	confirmation := events[len(events)-1].GetConfirmation()
	if confirmation == nil {
		t.Fatalf("the run finished without asking: %v", events[len(events)-1])
	}
	if confirmation.GetTool() != "delete_note" {
		t.Errorf("confirmation for %q, want delete_note", confirmation.GetTool())
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(confirmation.GetArgumentsJson()), &args); err != nil || args["note_id"] != note.GetId() {
		t.Errorf("confirmation arguments %s, want the note", confirmation.GetArgumentsJson())
	}
	runID := events[len(events)-1].GetRunId()

	u.call(http.MethodPost, "/v1/runs/"+runID+":confirm",
		&skeinv1.ConfirmRequest{ConfirmationId: confirmation.GetConfirmationId(), Approved: false}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), longWait)
	defer cancel()
	for {
		var ev skeinv1.SendMessageResponse
		if err := readProto(ctx, conn, &ev); err != nil {
			t.Fatalf("waiting for the run to finish: %v", err)
		}
		if f := ev.GetFinished(); f != nil {
			if f.GetOutcome() != skeinv1.RunFinished_OUTCOME_CANCELLED {
				t.Errorf("declined run finished %v, want CANCELLED", f)
			}
			break
		}
	}
	var got skeinv1.GetNoteResponse
	u.call(http.MethodGet, "/v1/notes/"+note.GetId(), nil, &got)
	if got.GetNote().GetTrashed() {
		t.Error("the note was trashed although the deletion was declined")
	}
	// Answering again is a precondition failure, not a second decision.
	again := u.do(http.MethodPost, "/v1/runs/"+runID+":confirm",
		&skeinv1.ConfirmRequest{ConfirmationId: confirmation.GetConfirmationId(), Approved: true})
	if again.status != http.StatusBadRequest && again.status != http.StatusNotFound {
		t.Errorf("confirming a finished run: HTTP %d %s", again.status, again.body)
	}
}
