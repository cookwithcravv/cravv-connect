package core

import (
	"encoding/json"
	"fmt"
)

// EnvelopeVersion is the peer-v1 envelope version.
const EnvelopeVersion = 1

// Envelope is the sealed plaintext exchanged between peers (peer-v1).
type Envelope struct {
	V           int             `json:"v"`
	ID          string          `json:"id"`
	TS          int64           `json:"ts"` // unix milliseconds
	FromMachine MachineID       `json:"from_machine"`
	FromSession string          `json:"from_session,omitempty"`
	ToMachine   MachineID       `json:"to_machine"`
	ToSession   string          `json:"to_session,omitempty"`
	Kind        Kind            `json:"kind"`
	Body        json.RawMessage `json:"body"`
}

// NewEnvelope builds a version 1 envelope with a fresh ID and the clock's
// current time. body is marshalled to JSON; a nil body becomes {}.
func NewEnvelope(clock Clock, from, to MachineID, kind Kind, body any) (Envelope, error) {
	if body == nil {
		body = EmptyBody{}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return Envelope{}, fmt.Errorf("core: marshal %s body: %w", kind, err)
	}
	return Envelope{
		V:           EnvelopeVersion,
		ID:          NewID(),
		TS:          clock.Now().UnixMilli(),
		FromMachine: from,
		ToMachine:   to,
		Kind:        kind,
		Body:        raw,
	}, nil
}

type ChatBody struct {
	Text string `json:"text"`
}

type FileRef struct {
	FileID string `json:"file_id"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
}

type TaskCreateBody struct {
	TaskID       string    `json:"task_id"`
	Instructions string    `json:"instructions"`
	Files        []FileRef `json:"files,omitempty"`
}

type TaskUpdateBody struct {
	TaskID string    `json:"task_id"`
	State  TaskState `json:"state"`
	Note   string    `json:"note,omitempty"`
	Result string    `json:"result,omitempty"`
	Files  []FileRef `json:"files,omitempty"`
}

type TaskCancelBody struct {
	TaskID string `json:"task_id"`
}

type FileOfferBody struct {
	FileID string `json:"file_id"`
	BlobID string `json:"blob_id"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Chunks uint32 `json:"chunks"`
	SHA256 []byte `json:"sha256"`
	Key    []byte `json:"key"`
	TaskID string `json:"task_id,omitempty"` // set when the file belongs to a task
}

// SignedPrekeyWire mirrors keys.SignedPrekey so bodies avoid a core -> keys import.
type SignedPrekeyWire struct {
	ID        string `json:"id"`
	Pub       []byte `json:"pub"`
	CreatedAt int64  `json:"created_at"`
	Sig       []byte `json:"sig"`
}

type PrekeyBody struct {
	Prekey SignedPrekeyWire `json:"prekey"`
}

type StalePrekeyBody struct {
	MsgID  string           `json:"msg_id"`
	Prekey SignedPrekeyWire `json:"prekey"`
}

type DeliveredBody struct {
	IDs []string `json:"ids"`
}

type EmptyBody struct{}

type RelayMovedBody struct {
	RelayURL string `json:"relay_url"`
}
