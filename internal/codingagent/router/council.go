package router

// War council: Global Thermonuclear War asks every routed model the user
// set up, in parallel, and the orchestrator synthesizes their proposals.

// CouncilMember is one routed model the war council can ask, with Skip set
// to why it can't answer now.
type CouncilMember struct {
	Ref  ModelRef
	Skip string
}

// CouncilMembers lists every routed model once, in ranking order, marking
// the ones that can't answer: unreachable (the tier's probe failed),
// resting after failures, or not signed in. private keeps only private
// connections; exclude drops the given provider/model specs.
func (r *Router) CouncilMembers(private bool, exclude ...string) []CouncilMember {
	seen := map[string]bool{}
	for _, spec := range exclude {
		seen[spec] = true
	}
	var out []CouncilMember
	add := func(idx int, ref ModelRef) {
		spec := ref.Spec()
		if seen[spec] || (private && !r.cfg.private(ref.Provider)) {
			return
		}
		seen[spec] = true
		m := CouncilMember{Ref: ref}
		switch {
		case !r.probe(r.cfg.Tiers[idx]):
			m.Skip = "unreachable"
		case r.isDown(spec):
			m.Skip = "resting after errors"
		default:
			if _, ok := r.host.ModelInfo(ref.Provider, ref.Model); !ok {
				m.Skip = "not signed in"
			}
		}
		out = append(out, m)
	}
	for _, spec := range r.cfg.Ranking {
		for idx, ref := range r.cfg.refs(spec) {
			add(idx, ref)
			break
		}
	}
	for idx, tier := range r.cfg.Tiers {
		for _, ref := range tier.Models {
			add(idx, ref)
		}
	}
	return out
}
