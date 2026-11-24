package draft

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// Meta holds the persistent state of a local draft.
type Meta struct {
	ID        string    `json:"id"`
	Account   string    `json:"account"`
	Subject   string    `json:"subject"`
	SyncedID  string    `json:"synced_id,omitempty"` // provider id of the server copy; "" if none
	Dirty     bool      `json:"dirty"`
	LocalOnly bool      `json:"local_only,omitempty"` // kept as a Local Draft: never uploaded
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// UnmarshalJSON also accepts the legacy numeric synced_uid written before
// message ids became text; synced_id wins when both are present.
func (m *Meta) UnmarshalJSON(data []byte) error {
	type plain Meta
	aux := struct {
		*plain
		LegacyUID uint32 `json:"synced_uid"`
	}{plain: (*plain)(m)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if m.SyncedID == "" && aux.LegacyUID != 0 {
		m.SyncedID = strconv.FormatUint(uint64(aux.LegacyUID), 10)
	}
	return nil
}

func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("draft: newID: %v", err))
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
