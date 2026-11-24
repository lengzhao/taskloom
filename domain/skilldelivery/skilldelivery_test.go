package skilldelivery_test

import (
	"testing"

	"github.com/lengzhao/taskloom/cap/skills"
	"github.com/lengzhao/taskloom/domain/skilldelivery"
)

func TestDecisionStates(t *testing.T) {
	host := skills.HostFacts{Contract: "agent-skill-host@1.0.0", Affordances: []string{"agent-skills", "filesystem"}}
	rel := skills.Release{Ref: "r", Name: "n", Version: "1.0.0", Digest: "d", Size: 3}
	rejected := &skills.Review{Verdict: skills.Rejected}
	cases := []struct {
		name   string
		rel    skills.Release
		review *skills.Review
		host   skills.HostFacts
		state  string
		reason string
	}{
		{"ready", rel, nil, host, skills.Ready, ""},
		{"other contract", rel, nil, skills.HostFacts{Contract: "remote-mcp-host@1.0.0", Affordances: host.Affordances}, skills.Unavailable, "bridge-not-declared"},
		{"no filesystem", rel, nil, skills.HostFacts{Contract: host.Contract, Affordances: []string{"agent-skills"}}, skills.Unavailable, "host-affordance-missing"},
		{"egress without network", skills.Release{Ref: "r", Declarations: skills.Declarations{EgressHosts: []string{"x"}}}, nil, host, skills.Unavailable, "host-affordance-missing"},
		{"rejected", rel, rejected, host, skills.Blocked, "review-rejected"},
		{"app missing", skills.Release{Ref: "r", Declarations: skills.Declarations{RequiredApps: []string{"drive"}}}, nil, host, skills.ActionRequired, "apps-not-connected"},
		{"app connected", skills.Release{Ref: "r", Declarations: skills.Declarations{RequiredApps: []string{"drive"}}}, nil, skills.HostFacts{Contract: host.Contract, Affordances: host.Affordances, Apps: []string{"drive"}}, skills.Ready, ""},
	}
	for _, c := range cases {
		d := skilldelivery.Decide(c.rel, c.review, c.host)
		if d.State != c.state || d.Reason != c.reason {
			t.Errorf("%s: %+v", c.name, d)
		}
		if (d.State == skills.Ready || d.State == skills.ActionRequired) != (d.Action != nil) {
			t.Errorf("%s: action %+v", c.name, d.Action)
		}
	}
	d := skilldelivery.Decide(rel, nil, host)
	if d.Trust != skills.TrustUnreviewed || d.Action.Kind != "materialize-exact-artifact" || d.Action.Digest != "d" {
		t.Fatalf("%+v", d)
	}
}
