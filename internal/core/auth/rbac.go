package auth

// Operation is an action an agent may perform on the wiki. The control channel
// and the HTTP routes decide access with this one matrix, so the two cannot drift
// apart (docs/srs/agent-operations.srs.md FR-115).
type Operation string

const (
	// OpPageGet reads one page.
	OpPageGet Operation = "page.get"
	// OpPageCreate creates content, which an unauthenticated agent may do: it is
	// the same level of trust as an external file edit (FR-113).
	OpPageCreate Operation = "page.create"
	// OpPageUpdate rewrites existing content, open to the same callers as create.
	OpPageUpdate Operation = "page.update"
	// OpPageDelete removes content, which needs an editor or an admin: nothing
	// restores it (FR-115).
	OpPageDelete Operation = "page.delete"
)

// Can reports whether role may perform operation. An empty role is an
// unauthenticated caller; an unknown operation is refused rather than allowed.
func Can(role string, operation Operation) bool {
	switch operation {
	case OpPageGet, OpPageCreate, OpPageUpdate:
		return true
	case OpPageDelete:
		return role == RoleEditor || role == RoleAdmin
	default:
		return false
	}
}

// NeedsAuthentication reports whether operation requires a signed-in account.
func NeedsAuthentication(operation Operation) bool {
	return !Can("", operation)
}
