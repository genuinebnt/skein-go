package domain

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

const conformanceDir = "../../../../testdata/conformance/notifications"

func loadFixture[T any](t *testing.T, name string) []T {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(conformanceDir, name))
	if err != nil {
		t.Fatal(err)
	}
	var cases []T
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if len(cases) == 0 {
		t.Fatalf("%s has no cases", name)
	}
	return cases
}

func TestMentionGrammarMatchesConformanceFixtures(t *testing.T) {
	type mentionCase struct {
		Text    string   `json:"text"`
		Handles []string `json:"handles"`
		Why     string   `json:"why"`
	}
	for _, c := range loadFixture[mentionCase](t, "mentions.json") {
		got := ExtractHandles(c.Text)
		if !slices.Equal(got, c.Handles) && !(len(got) == 0 && len(c.Handles) == 0) {
			t.Errorf("ExtractHandles(%q) = %q, want %q (%s)", c.Text, got, c.Handles, c.Why)
		}
	}
}

func TestEmailPlansMatchConformanceFixtures(t *testing.T) {
	type job struct {
		JobID       string    `json:"job_id"`
		RecipientID string    `json:"recipient_id"`
		CreatedAt   time.Time `json:"created_at"`
		DueAt       time.Time `json:"due_at"`
		Read        bool      `json:"read"`
	}
	type batch struct {
		RecipientID string   `json:"recipient_id"`
		JobIDs      []string `json:"job_ids"`
	}
	type planCase struct {
		Name        string    `json:"name"`
		Why         string    `json:"why"`
		Now         time.Time `json:"now"`
		MaxPerEmail int       `json:"max_per_email"`
		Pending     []job     `json:"pending"`
		Batches     []batch   `json:"batches"`
		Skipped     []string  `json:"skipped"`
	}
	for _, c := range loadFixture[planCase](t, "email_plans.json") {
		var pending []PendingEmail
		for _, j := range c.Pending {
			pending = append(pending, PendingEmail{JobID: j.JobID, RecipientID: j.RecipientID, NotificationID: "n-" + j.JobID,
				CreatedAt: j.CreatedAt, DueAt: j.DueAt, Read: j.Read})
		}
		plan := PlanEmails(pending, c.Now, c.MaxPerEmail)
		if err := plan.CheckInvariants(pending, c.Now, c.MaxPerEmail); err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		got := []batch{}
		for _, b := range plan.Batches {
			var ids []string
			for _, j := range b.Jobs {
				ids = append(ids, j.JobID)
			}
			got = append(got, batch{RecipientID: b.RecipientID, JobIDs: ids})
		}
		gotJSON, _ := json.Marshal(got)
		wantJSON, _ := json.Marshal(c.Batches)
		if string(gotJSON) != string(wantJSON) {
			t.Errorf("%s: batches %s, want %s (%s)", c.Name, gotJSON, wantJSON, c.Why)
		}
		if !slices.Equal(plan.Skipped, c.Skipped) && !(len(plan.Skipped) == 0 && len(c.Skipped) == 0) {
			t.Errorf("%s: skipped %q, want %q (%s)", c.Name, plan.Skipped, c.Skipped, c.Why)
		}
	}
}
