package propose

import (
	"testing"

	"github.com/MohamedBenTaher/tierbound/worker/internal/agentconfig"
)

// Plan step 3.3 done-when, without a database: mismatched hashes refuse,
// matching hashes allow.
func TestActivationRule(t *testing.T) {
	const h, other = "aaaa", "bbbb"
	ptr := func(s string) *string { return &s }
	cases := []struct {
		name   string
		agent  agentRow
		loaded string
		allow  bool
	}{
		{"all three agree", agentRow{Tier: agentconfig.TierDraft, ConfigHash: h, EvalsPassedHash: ptr(h)}, h, true},
		{"evals passed an older hash", agentRow{Tier: agentconfig.TierDraft, ConfigHash: h, EvalsPassedHash: ptr(other)}, h, false},
		{"evals never passed", agentRow{Tier: agentconfig.TierDraft, ConfigHash: h}, h, false},
		{"worker runs an unregistered config", agentRow{Tier: agentconfig.TierDraft, ConfigHash: h, EvalsPassedHash: ptr(h)}, other, false},
		{"read tier never proposes", agentRow{Tier: agentconfig.TierRead, ConfigHash: h, EvalsPassedHash: ptr(h)}, h, false},
	}
	for _, c := range cases {
		err := activationError(&c.agent, c.loaded)
		if (err == nil) != c.allow {
			t.Errorf("%s: allow=%v, got err=%v", c.name, c.allow, err)
		}
	}
}
