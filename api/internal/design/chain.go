package design

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// GenesisHash is the prev_hash of every request's first event.
const GenesisHash = "0000000000000000000000000000000000000000000000000000000000000000"

// EventRecord is the part of an event the hash covers.
type EventRecord struct {
	ShopID     int
	RequestID  string
	Seq        int
	Kind       string
	From, To   string
	ActorType  string
	ActorUser  *int
	ActorCred  *int
	HeadSHA    string
	Detail     json.RawMessage
	CreatedAt  time.Time
	PrevHash   string
	StoredHash string
}

// Micros truncates to the precision Postgres keeps (timestamptz is
// microseconds), so a hash computed before the insert equals the one computed
// after reading the row back.
func Micros(t time.Time) time.Time { return t.UTC().Truncate(time.Microsecond) }

// CanonicalJSON re-encodes a JSON value with sorted keys and no insignificant
// whitespace. jsonb does not keep key order, so the hash is always computed
// over this form on both the write and the verify side.
func CanonicalJSON(raw []byte) (json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return json.RawMessage("{}"), nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

type hashInput struct {
	Prev      string          `json:"prev"`
	ShopID    int             `json:"shop_id"`
	RequestID string          `json:"request_id"`
	Seq       int             `json:"seq"`
	Kind      string          `json:"kind"`
	From      string          `json:"from"`
	To        string          `json:"to"`
	ActorType string          `json:"actor_type"`
	ActorUser *int            `json:"actor_user_id"`
	ActorCred *int            `json:"actor_credential_id"`
	HeadSHA   string          `json:"head_sha"`
	Detail    json.RawMessage `json:"detail"`
	CreatedAt string          `json:"created_at"`
}

// ComputeHash returns hex(sha256(canonical(event))) chained to e.PrevHash.
func ComputeHash(e EventRecord) (string, error) {
	detail, err := CanonicalJSON(e.Detail)
	if err != nil {
		return "", fmt.Errorf("design: canonical detail: %w", err)
	}
	in := hashInput{
		Prev: e.PrevHash, ShopID: e.ShopID, RequestID: e.RequestID, Seq: e.Seq, Kind: e.Kind,
		From: e.From, To: e.To, ActorType: e.ActorType, ActorUser: e.ActorUser, ActorCred: e.ActorCred,
		HeadSHA: e.HeadSHA, Detail: detail, CreatedAt: Micros(e.CreatedAt).Format(time.RFC3339Nano),
	}
	b, err := json.Marshal(in)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// VerifyChain checks one request's events (ordered by seq): contiguous
// sequence numbers from 1, each prev_hash equal to its predecessor's hash, and
// each stored hash equal to the recomputed one. It returns the seq of the first
// broken event, or 0 when the chain is intact.
func VerifyChain(events []EventRecord) (brokenSeq int, reason string) {
	prev := GenesisHash
	for i, e := range events {
		if e.Seq != i+1 {
			return i + 1, "sequence gap"
		}
		if e.PrevHash != prev {
			return e.Seq, "prev_hash does not match the predecessor"
		}
		h, err := ComputeHash(e)
		if err != nil || h != e.StoredHash {
			return e.Seq, "hash mismatch"
		}
		prev = e.StoredHash
	}
	return 0, ""
}
