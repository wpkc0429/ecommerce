package design

// The transition table is the ONE place that defines which status changes are
// legal and who may make them (design D4). Every mutation of a request's
// status goes through Service.step, which consults Allowed.

// Who may make a transition.
type who uint8

const (
	byService who = 1 << iota
	byUser
	bySystem
)

type edge struct{ from, to string }

// transitions maps every legal (from,to) edge to the actors allowed to take it.
// A human approval is user-only: a service credential can never produce
// `approved`, which is what makes "the service cannot approve" a property of
// this table and not of a route list.
var transitions = map[edge]who{
	{StatusDrafting, StatusPreviewing}:         byService,
	{StatusPreviewing, StatusAwaitingApproval}: byService,
	{StatusPreviewing, StatusDrafting}:         byService,
	{StatusAwaitingApproval, StatusDrafting}:   byService | byUser, // service redo / owner "request changes"
	{StatusAwaitingApproval, StatusApproved}:   byUser,
	{StatusAwaitingApproval, StatusRejected}:   byUser,
	{StatusApproved, StatusPublished}:          byService,
	{StatusPublished, StatusReverted}:          bySystem,
	{StatusDrafting, StatusFailed}:             byService | bySystem,
	{StatusPreviewing, StatusFailed}:           byService | bySystem,
	{StatusAwaitingApproval, StatusFailed}:     byService | bySystem,
	{StatusApproved, StatusFailed}:             byService | bySystem,
	{StatusDrafting, StatusCancelled}:          byUser,
	{StatusPreviewing, StatusCancelled}:        byUser,
	{StatusAwaitingApproval, StatusCancelled}:  byUser,
	{StatusApproved, StatusCancelled}:          byUser,
}

// Allowed reports whether actor type t may move a request from -> to.
func Allowed(from, to, actorType string) bool {
	w, ok := transitions[edge{from, to}]
	if !ok {
		return false
	}
	switch actorType {
	case ActorService:
		return w&byService != 0
	case ActorUser:
		return w&byUser != 0
	case ActorSystem:
		return w&bySystem != 0
	}
	return false
}

// SourcesOf returns every status from which `to` is reachable by actorType.
func SourcesOf(to, actorType string) []string {
	var out []string
	for e := range transitions {
		if e.to == to && Allowed(e.from, e.to, actorType) {
			out = append(out, e.from)
		}
	}
	return out
}

// AllStatuses lists the nine states (tests walk the full matrix).
var AllStatuses = []string{
	StatusDrafting, StatusPreviewing, StatusAwaitingApproval, StatusApproved, StatusPublished,
	StatusRejected, StatusFailed, StatusCancelled, StatusReverted,
}
