package conf_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/router"
	"github.com/xtls/xray-core/common"

	. "github.com/xtls/xray-core/infra/conf"
)

func TestConsistentHashingBalancerJSON(t *testing.T) {
	var c RouterConfig
	raw := `{"balancers": [{"tag": "auto", "selector": ["proxy-"],
		"strategy": {"type": "consistentHashing"}}]}`
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatal(err)
	}
	config, err := c.Build()
	if err != nil {
		t.Fatal(err)
	}
	if got := config.BalancingRule[0].Strategy; got != "consistenthashing" {
		t.Fatalf("strategy %q", got)
	}
}

func TestConsistentHashingBalancerJSONSettings(t *testing.T) {
	var c RouterConfig
	raw := `{"balancers": [{"tag": "auto", "selector": ["proxy-"],
		"strategy": {"type": "consistentHashing",
			"settings": {"maxRTT": "1s", "tolerance": 0.2, "expected": 3}}}]}`
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatal(err)
	}
	config, err := c.Build()
	if err != nil {
		t.Fatal(err)
	}
	rule := config.BalancingRule[0]
	if rule.StrategySettings == nil {
		t.Fatal("settings dropped")
	}
	i, err := rule.StrategySettings.GetInstance()
	if err != nil {
		t.Fatal(err)
	}
	s, ok := i.(*router.StrategyLeastLoadConfig)
	if !ok || s.MaxRTT != int64(time.Second) || s.Expected != 3 {
		t.Fatalf("settings %v", i)
	}

	var plain RouterConfig
	common.Must(json.Unmarshal([]byte(`{"balancers": [{"tag": "auto", "selector": ["p"],
		"strategy": {"type": "consistentHashing"}}]}`), &plain))
	built, err := plain.Build()
	if err != nil {
		t.Fatal(err)
	}
	if built.BalancingRule[0].StrategySettings != nil {
		t.Fatal("settings without any given")
	}
}
