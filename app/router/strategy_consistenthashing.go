package router

// The "consistentHashing" balancing strategy: every connection to a site
// (its registrable domain, or its IP) goes through the same outbound, picked
// by rendezvous hashing. When an outbound drops out only its sites move.
//
// Eligible are the outbounds leastLoad would choose from (without settings:
// all alive ones), plus those not measured yet. The observatory is required
// only with settings.

import (
	"context"
	"hash/maphash"

	"golang.org/x/net/publicsuffix"

	"github.com/xtls/xray-core/app/observatory"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/extension"
	"github.com/xtls/xray-core/features/routing"
)

// ConsistentHashingStrategy keeps a site on one outbound.
type ConsistentHashingStrategy struct {
	FallbackTag string

	seed         maphash.Seed
	leastLoad    *LeastLoadStrategy
	withSettings bool
}

// NewConsistentHashingStrategy creates the strategy with a fresh seed;
// settings (leastLoad's) may be nil.
func NewConsistentHashingStrategy(fallbackTag string, settings *StrategyLeastLoadConfig) *ConsistentHashingStrategy {
	s := &ConsistentHashingStrategy{FallbackTag: fallbackTag, seed: maphash.MakeSeed(), withSettings: settings != nil}
	if settings == nil {
		settings = &StrategyLeastLoadConfig{}
	}
	s.leastLoad = NewLeastLoadStrategy(settings)
	return s
}

func (s *ConsistentHashingStrategy) InjectContext(ctx context.Context) {
	if s.withSettings {
		s.leastLoad.InjectContext(ctx)
		return
	}
	s.leastLoad.ctx = ctx
	// The observatory is optional here; there is no core in tests.
	if core.FromContext(ctx) == nil {
		return
	}
	common.Must(core.OptionalFeatures(ctx, func(observatory extension.Observatory) error {
		s.leastLoad.observer = observatory
		return nil
	}))
}

// GetPrincipleTarget returns the outbounds the sites are spread over now.
func (s *ConsistentHashingStrategy) GetPrincipleTarget(candidates []string) []string {
	eligible := s.eligible(candidates)
	var tags []string
	for _, tag := range candidates {
		if eligible(tag) {
			tags = append(tags, tag)
		}
	}
	return tags
}

// PickOutbound is used when the connection has no site.
func (s *ConsistentHashingStrategy) PickOutbound(candidates []string) string {
	if s.withSettings {
		return s.leastLoad.PickOutbound(candidates)
	}
	eligible := s.eligible(candidates)
	for _, tag := range candidates {
		if eligible(tag) {
			return tag
		}
	}
	return ""
}

// PickOutboundFor picks the outbound of the connection's site; "" when none
// is eligible.
func (s *ConsistentHashingStrategy) PickOutboundFor(ctx routing.Context, candidates []string) string {
	key := consistentHashingKey(ctx)
	if key == "" {
		return s.PickOutbound(candidates)
	}
	eligible := s.eligible(candidates)
	best, bestWeight := "", uint64(0)
	for _, tag := range candidates {
		if !eligible(tag) {
			continue
		}
		if weight := maphash.String(s.seed, key+"\x00"+tag); best == "" || weight > bestWeight {
			best, bestWeight = tag, weight
		}
	}
	return best
}

func (s *ConsistentHashingStrategy) eligible(candidates []string) func(string) bool {
	if s.leastLoad.observer == nil {
		return func(string) bool { return true }
	}
	nodes := s.leastLoad.getNodes(candidates)
	settings := s.leastLoad.settings
	if settings.Expected > 0 || len(settings.Baselines) > 0 {
		nodes = s.leastLoad.selectLeastLoad(nodes)
	}
	chosen := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		chosen[n.Tag] = true
	}
	measured := s.measured()
	return func(tag string) bool { return chosen[tag] || !measured[tag] }
}

func (s *ConsistentHashingStrategy) measured() map[string]bool {
	seen := map[string]bool{}
	report, err := s.leastLoad.observer.GetObservation(s.leastLoad.ctx)
	if err != nil {
		return seen
	}
	if result, ok := report.(*observatory.ObservationResult); ok {
		for _, st := range result.Status {
			seen[st.OutboundTag] = true
		}
	}
	return seen
}

func consistentHashingKey(ctx routing.Context) string {
	if domain := ctx.GetTargetDomain(); domain != "" {
		if site, err := publicsuffix.EffectiveTLDPlusOne(domain); err == nil {
			return site
		}
		return domain
	}
	if ips := ctx.GetTargetIPs(); len(ips) > 0 {
		return ips[0].String()
	}
	return ""
}
