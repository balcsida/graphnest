package config

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestLoadSupplyChainDisabledByDefault(t *testing.T) {
	setValidEnvironment(t)
	setDurableEnvironment(t)
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.SupplyChain.Enabled {
		t.Fatalf("supply chain enabled without GRAPHNEST_SUPPLY_CHAIN: %#v", got.SupplyChain)
	}
	// Defaults are populated even while disabled so documentation and wiring can rely on them.
	if got.SupplyChain.Interval != 24*time.Hour || got.SupplyChain.Workers != 1 || got.SupplyChain.MaxDocumentBytes != 16<<20 || got.SupplyChain.MaxComponents != 50000 || got.SupplyChain.RetainSnapshots != 10 {
		t.Fatalf("defaults = %#v", got.SupplyChain)
	}
}

func TestLoadSupplyChainEnabledWithOverrides(t *testing.T) {
	setValidEnvironment(t)
	setDurableEnvironment(t)
	t.Setenv("GRAPHNEST_SUPPLY_CHAIN", "true")
	t.Setenv("GRAPHNEST_SUPPLY_CHAIN_INTERVAL", "6h")
	t.Setenv("GRAPHNEST_SUPPLY_CHAIN_WORKERS", "2")
	t.Setenv("GRAPHNEST_SUPPLY_CHAIN_MAX_DOCUMENT_BYTES", "1048576")
	t.Setenv("GRAPHNEST_SUPPLY_CHAIN_MAX_COMPONENTS", "1000")
	t.Setenv("GRAPHNEST_SUPPLY_CHAIN_RETAIN_SNAPSHOTS", "0")
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	want := SupplyChain{Enabled: true, Interval: 6 * time.Hour, Workers: 2, MaxDocumentBytes: 1 << 20, MaxComponents: 1000, RetainSnapshots: 0}
	if got.SupplyChain != want {
		t.Fatalf("supply chain = %#v, want %#v", got.SupplyChain, want)
	}
}

func TestLoadSupplyChainRejectsInvalidSettings(t *testing.T) {
	cases := map[string]map[string]string{
		"not boolean":        {"GRAPHNEST_SUPPLY_CHAIN": "yes"},
		"interval too short": {"GRAPHNEST_SUPPLY_CHAIN": "true", "GRAPHNEST_SUPPLY_CHAIN_INTERVAL": "10s"},
		"too many workers":   {"GRAPHNEST_SUPPLY_CHAIN": "true", "GRAPHNEST_SUPPLY_CHAIN_WORKERS": "9"},
		"document cap":       {"GRAPHNEST_SUPPLY_CHAIN": "true", "GRAPHNEST_SUPPLY_CHAIN_MAX_DOCUMENT_BYTES": "1073741824"},
		"negative":           {"GRAPHNEST_SUPPLY_CHAIN": "true", "GRAPHNEST_SUPPLY_CHAIN_MAX_COMPONENTS": "-1"},
	}
	for name, environment := range cases {
		t.Run(name, func(t *testing.T) {
			setValidEnvironment(t)
			setDurableEnvironment(t)
			for key, value := range environment {
				t.Setenv(key, value)
			}
			if _, err := Load(); !errors.Is(err, ErrInvalid) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestLoadSupplyChainRequiresDurableMode(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("GRAPHNEST_SUPPLY_CHAIN", "true")
	_, err := Load()
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "GRAPHNEST_DATABASE_URL") {
		t.Fatalf("error = %v", err)
	}
}
