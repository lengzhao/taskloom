// Package skilldelivery decides what one host does next with one exact
// release. The producer × host matrix is sparse and explicit: only Agent
// Skill → agent-skill-host@1.x is declared, and every other pair is
// unavailable. The decision is computed on each request and never stored.
package skilldelivery

import (
	"slices"
	"strings"

	"github.com/lengzhao/taskloom/cap/skills"
)

// Contract is the one declared target contract family.
const Contract = "agent-skill-host@1."

// Affordances an Agent Skill host must declare.
var required = []string{"agent-skills", "filesystem"}

// Trust derives from the latest review; nil means none.
func Trust(latest *skills.Review) string {
	switch {
	case latest == nil:
		return skills.TrustUnreviewed
	case latest.Verdict == skills.Rejected:
		return skills.TrustRejected
	default:
		return skills.TrustApproved
	}
}

// Decide returns exactly one state with at most one action.
func Decide(r skills.Release, latest *skills.Review, host skills.HostFacts) skills.Decision {
	d := skills.Decision{Ref: r.Ref, Name: r.Name, Version: r.Version, Trust: Trust(latest)}
	if !strings.HasPrefix(host.Contract, Contract) {
		d.State, d.Reason = skills.Unavailable, "bridge-not-declared"
		return d
	}
	var missing []string
	for _, a := range required {
		if !slices.Contains(host.Affordances, a) {
			missing = append(missing, a)
		}
	}
	if len(r.Declarations.EgressHosts) > 0 && !slices.Contains(host.Affordances, "outbound-network") {
		missing = append(missing, "outbound-network")
	}
	if len(missing) > 0 {
		d.State, d.Reason, d.Missing = skills.Unavailable, "host-affordance-missing", missing
		return d
	}
	if d.Trust == skills.TrustRejected {
		d.State, d.Reason = skills.Blocked, "review-rejected"
		return d
	}
	var apps []string
	for _, a := range r.Declarations.RequiredApps {
		if !slices.Contains(host.Apps, a) {
			apps = append(apps, a)
		}
	}
	if len(apps) > 0 {
		d.State, d.Reason, d.Missing = skills.ActionRequired, "apps-not-connected", apps
		d.Action = &skills.Action{Kind: "connect-apps", Apps: apps}
		return d
	}
	d.State = skills.Ready
	d.Action = &skills.Action{Kind: "materialize-exact-artifact", Ref: r.Ref, Digest: r.Digest, Size: r.Size}
	return d
}
