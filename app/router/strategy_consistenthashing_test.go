package router

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/xtls/xray-core/app/observatory"

	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/features/routing"
	routing_session "github.com/xtls/xray-core/features/routing/session"
)

func siteContext(host string) routing.Context {
	var addr net.Address = net.DomainAddress(host)
	if ip := net.ParseAddress(host); ip.Family().IsIP() {
		addr = ip
	}
	ctx := session.ContextWithOutbounds(context.Background(), []*session.Outbound{{
		Target: net.TCPDestination(addr, 443),
	}})
	return routing_session.AsRoutingContext(ctx)
}

func TestConsistentHashingKeepsASiteOnOneOutbound(t *testing.T) {
	s := NewConsistentHashingStrategy("", nil)
	tags := []string{"a", "b", "c", "d", "e"}
	first := s.PickOutboundFor(siteContext("www.google.com"), tags)
	for _, host := range []string{"www.google.com", "mail.google.com", "google.com", "a.b.google.com"} {
		for i := 0; i < 20; i++ {
			if got := s.PickOutboundFor(siteContext(host), tags); got != first {
				t.Fatalf("%s: %s, want %s like www.google.com", host, got, first)
			}
		}
	}
	ip := s.PickOutboundFor(siteContext("1.2.3.4"), tags)
	if got := s.PickOutboundFor(siteContext("1.2.3.4"), tags); got != ip {
		t.Fatalf("1.2.3.4: %s then %s", ip, got)
	}
}

func TestConsistentHashingSpreadsSites(t *testing.T) {
	s := NewConsistentHashingStrategy("", nil)
	tags := []string{"a", "b", "c", "d"}
	used := map[string]int{}
	for i := 0; i < 400; i++ {
		used[s.PickOutboundFor(siteContext(fmt.Sprintf("site%d.com", i)), tags)]++
	}
	for _, tag := range tags {
		if used[tag] < 50 {
			t.Fatalf("outbound %s got %d of 400 sites: %v", tag, used[tag], used)
		}
	}
}

func TestConsistentHashingMovesFewSitesWhenOneIsAdded(t *testing.T) {
	s := NewConsistentHashingStrategy("", nil)
	four := []string{"a", "b", "c", "d"}
	five := append(append([]string{}, four...), "e")
	moved := 0
	const sites = 1000
	for i := 0; i < sites; i++ {
		ctx := siteContext(fmt.Sprintf("site%d.org", i))
		before, after := s.PickOutboundFor(ctx, four), s.PickOutboundFor(ctx, five)
		if before != after {
			if after != "e" {
				t.Fatalf("site%d.org moved %s -> %s, not to the new one", i, before, after)
			}
			moved++
		}
	}
	if moved < sites/10 || moved > sites*3/10 {
		t.Fatalf("%d of %d sites moved", moved, sites)
	}
}

func TestConsistentHashingWithoutAConnection(t *testing.T) {
	s := NewConsistentHashingStrategy("", nil)
	if got := s.PickOutbound([]string{"a", "b"}); got != "a" {
		t.Fatalf("no connection: %s, want the first", got)
	}
	if got := s.PickOutboundFor(siteContext("x.com"), nil); got != "" {
		t.Fatalf("no candidates: %q", got)
	}
}

func TestConsistentHashingMovesOnlyTheSitesOfAServerGone(t *testing.T) {
	s := NewConsistentHashingStrategy("", nil)
	all := []string{"a", "b", "c", "d", "e"}
	without := []string{"a", "b", "d", "e"}
	for i := 0; i < 1000; i++ {
		ctx := siteContext(fmt.Sprintf("site%d.net", i))
		before, after := s.PickOutboundFor(ctx, all), s.PickOutboundFor(ctx, without)
		if before != "c" && before != after {
			t.Fatalf("site%d.net moved %s -> %s though %s stayed", i, before, after, before)
		}
	}
}

type fakeObservatory struct{ status []*observatory.OutboundStatus }

func (o *fakeObservatory) Type() interface{} { return nil }
func (o *fakeObservatory) Start() error      { return nil }
func (o *fakeObservatory) Close() error      { return nil }
func (o *fakeObservatory) GetObservation(context.Context) (proto.Message, error) {
	return &observatory.ObservationResult{Status: o.status}, nil
}

func TestConsistentHashingWithLeastLoadSettings(t *testing.T) {
	settings := &StrategyLeastLoadConfig{MaxRTT: int64(time.Second)}
	s := NewConsistentHashingStrategy("", settings)
	s.leastLoad.observer = &fakeObservatory{status: []*observatory.OutboundStatus{
		{OutboundTag: "fast", Alive: true, Delay: 100},
		{OutboundTag: "ok", Alive: true, Delay: 200},
		{OutboundTag: "slow", Alive: true, Delay: 3000},
		{OutboundTag: "dead", Alive: false},
	}}
	tags := []string{"fast", "ok", "slow", "dead"}
	used := map[string]int{}
	for i := 0; i < 400; i++ {
		ctx := siteContext(fmt.Sprintf("site%d.io", i))
		tag := s.PickOutboundFor(ctx, tags)
		if again := s.PickOutboundFor(ctx, tags); again != tag {
			t.Fatalf("site%d.io: %s then %s", i, tag, again)
		}
		used[tag]++
	}
	if used["slow"] != 0 || used["dead"] != 0 {
		t.Fatalf("over maxRTT or dead got sites: %v", used)
	}
	if used["fast"] < 100 || used["ok"] < 100 {
		t.Fatalf("the two fast ones should share the sites: %v", used)
	}
	if got := s.GetPrincipleTarget(tags); !slices.Equal(got, []string{"fast", "ok"}) {
		t.Fatalf("principle target %v", got)
	}

	s.leastLoad.settings.Expected = 1
	if got := s.PickOutboundFor(siteContext("x.org"), tags); got != "fast" {
		t.Fatalf("expected 1: %s", got)
	}
	s.leastLoad.settings.MaxRTT = int64(50 * time.Millisecond)
	s.leastLoad.settings.Expected = 0
	if got := s.PickOutboundFor(siteContext("x.org"), tags); got != "" {
		t.Fatalf("none under maxRTT: %q", got)
	}
}

func TestConsistentHashingWithSettingsBeforeResults(t *testing.T) {
	settings := &StrategyLeastLoadConfig{MaxRTT: int64(time.Second)}
	s := NewConsistentHashingStrategy("", settings)
	observer := &fakeObservatory{}
	s.leastLoad.observer = observer
	tags := []string{"a", "b", "c"}

	before := map[string]string{}
	used := map[string]int{}
	for i := 0; i < 300; i++ {
		site := fmt.Sprintf("site%d.io", i)
		tag := s.PickOutboundFor(siteContext(site), tags)
		before[site] = tag
		used[tag]++
	}
	if used[""] != 0 || used["a"] < 60 || used["b"] < 60 || used["c"] < 60 {
		t.Fatalf("before any result: %v", used)
	}

	observer.status = []*observatory.OutboundStatus{
		{OutboundTag: "a", Alive: true, Delay: 100},
		{OutboundTag: "c", Alive: true, Delay: 3000},
	}
	for site, was := range before {
		now := s.PickOutboundFor(siteContext(site), tags)
		if now == "c" {
			t.Fatalf("%s still on the slow one", site)
		}
		if was != "c" && now != was {
			t.Fatalf("%s moved from %s to %s", site, was, now)
		}
	}
}

func TestConsistentHashingPrincipleTargetWithoutSettings(t *testing.T) {
	s := NewConsistentHashingStrategy("", nil)
	tags := []string{"a", "dead", "new"}
	if got := s.GetPrincipleTarget(tags); !slices.Equal(got, tags) {
		t.Fatalf("no observatory: %v", got)
	}
	s.leastLoad.observer = &fakeObservatory{status: []*observatory.OutboundStatus{
		{OutboundTag: "a", Alive: true, Delay: 100},
		{OutboundTag: "dead", Alive: false},
	}}
	if got := s.GetPrincipleTarget(tags); !slices.Equal(got, []string{"a", "new"}) {
		t.Fatalf("principle target %v", got)
	}
}
