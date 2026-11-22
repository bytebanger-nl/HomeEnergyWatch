package forecast

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

func TestPick(t *testing.T) {
	u := Pick("usage_gas", nil, 6.04, SourceComputed, 3)
	if u.Pct != 6 || u.Source != SourceComputed || u.Months != 3 || u.Auto != nil {
		t.Fatalf("automatic %+v", u)
	}
	own := 12.0
	u = Pick("usage_gas", &own, 6.04, SourceComputed, 3)
	if u.Pct != 12 || u.Source != SourceSetting || *u.Auto != 6 || u.AutoSource != SourceComputed {
		t.Fatalf("setting %+v", u)
	}
	if v, src := Computed(1, true, 8); v != MinComputedPct || src != SourceComputed {
		t.Fatalf("lower bound %v %s", v, src)
	}
	if v, src := Computed(40, true, 8); v != MaxComputedPct || src != SourceComputed {
		t.Fatalf("upper bound %v %s", v, src)
	}
	if v, src := Computed(0, false, 8); v != 8 || src != SourceDefault {
		t.Fatalf("default %v %s", v, src)
	}
}

func TestSaveLoad(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	s := &Service{Store: st}
	if set, err := s.Load(ctx); err != nil || set != (Settings{}) {
		t.Fatalf("empty: %+v %v", set, err)
	}
	bad := 40.0
	if err := s.Save(ctx, Settings{PeriodMinPct: &bad}); err == nil {
		t.Fatal("period minimum of 40% accepted")
	}
	v, off := 80.0, false
	if err := s.Save(ctx, Settings{PeriodMinPct: &v, Trend: &off}); err != nil {
		t.Fatal(err)
	}
	set, err := s.Load(ctx)
	if err != nil || set.PeriodMinPct == nil || *set.PeriodMinPct != 80 || set.TrendOn() {
		t.Fatalf("loaded %+v %v", set, err)
	}
	// all automatic again removes the setting
	if err := s.Save(ctx, Settings{}); err != nil {
		t.Fatal(err)
	}
	if raw, _ := st.Setting(ctx, "forecast"); raw != "" {
		t.Fatalf("still stored: %q", raw)
	}
	var none *Service
	if set, err := none.Load(ctx); err != nil || set != (Settings{}) {
		t.Fatalf("nil service: %+v %v", set, err)
	}
}
