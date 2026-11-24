package mail

import (
	"encoding/json"
	"strings"
	"testing"
)

// Categories travel in the stored envelope JSON, and only when there are
// some: rows of IMAP and Gmail accounts stay as they were.
func TestEnvelopeCategoriesJSON(t *testing.T) {
	b, _ := json.Marshal(&Envelope{ID: "1", Categories: []string{"Work"}})
	var back Envelope
	if err := json.Unmarshal(b, &back); err != nil || len(back.Categories) != 1 || back.Categories[0] != "Work" {
		t.Fatalf("round trip: %s → %+v %v", b, back.Categories, err)
	}
	b, _ = json.Marshal(&Envelope{ID: "2"})
	if strings.Contains(string(b), "Categories") {
		t.Fatalf("empty categories written: %s", b)
	}
}
