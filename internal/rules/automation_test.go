package rules

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/gateway"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

func (f *fakeDevices) setToday(id string, today map[string]float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v := f.views[id]
	v.Today = today
	f.views[id] = v
}

// An automation acts on the situation of the moment (it starts armed), fires
// once, and runs its else actions once when the conditions stop holding.
func TestAutomationStartsArmedAndRunsElse(t *testing.T) {
	h := newHarness(t)
	h.dev.set("heater", "Kachel", gateway.StatusOnline, map[string]float64{"power_w": 0})
	h.prices = map[string]float64{MetricPrice: -0.01}
	v, err := h.eng.Save(context.Background(), Rule{
		Name: "Gratis stroom", Enabled: true,
		Conditions:  []Condition{{Device: PriceDevice, Metric: MetricPrice, Op: OpBelowOrEqual, Value: 0}},
		Actions:     []Action{{Type: ActionSwitch, Device: "heater", On: true}},
		ElseActions: []Action{{Type: ActionSwitch, Device: "heater", On: false}, {Type: ActionNotify}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if v.Kind != KindAutomation || !v.State.Armed {
		t.Fatalf("kind %q armed %v, want an armed automation", v.Kind, v.State.Armed)
	}
	h.step(2 * time.Second)
	h.step(2 * time.Second)
	if got := strings.Join(h.dev.switched, ","); got != "heater=on" {
		t.Fatalf("switched %q, want heater=on once", got)
	}
	if n := len(h.messages()); n != 0 {
		t.Fatalf("sent %d messages without a notify action", n)
	}
	h.prices = map[string]float64{MetricPrice: 0.2}
	h.step(2 * time.Second)
	h.step(2 * time.Second)
	if got := strings.Join(h.dev.switched, ","); got != "heater=on,heater=off" {
		t.Fatalf("switched %q, want on then off", got)
	}
	msgs := h.messages()
	if len(msgs) != 1 || msgs[0].Title != "Gratis stroom" || msgs[0].Body != "Gestopt." || msgs[0].URL != "/#/automatisering" {
		t.Fatalf("messages %+v", msgs)
	}
	// armed state was stored: a restart while the price is high does not
	// run the else actions again
	h.eng = h.engine()
	if err := h.eng.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.step(2 * time.Second)
	if n := len(h.dev.switched); n != 2 {
		t.Fatalf("switched %v after restart", h.dev.switched)
	}
}

// Heat with whatever is cheaper: 30 minutes of central heating burn 0.5 m³
// of gas, the air conditioner uses 0.8 kWh for the same heat.
func TestCompareWithScaledReference(t *testing.T) {
	h := newHarness(t)
	h.dev.set("airco", "Airco", gateway.StatusOnline, map[string]float64{"power_w": 0})
	h.prices = map[string]float64{MetricGasPriceAllIn: 1.30, MetricPriceAllIn: 0.25}
	cond := Condition{Device: PriceDevice, Metric: MetricGasPriceAllIn, Scale: 0.5, Op: OpAbove,
		Ref: &Operand{Device: PriceDevice, Metric: MetricPriceAllIn, Scale: 0.8}}
	_, err := h.eng.Save(context.Background(), Rule{
		Name: "Airco is goedkoper", Enabled: true,
		Conditions:  []Condition{cond},
		Actions:     []Action{{Type: ActionSwitch, Device: "airco", On: true}, {Type: ActionNotify}},
		ElseActions: []Action{{Type: ActionSwitch, Device: "airco", On: false}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// an alert on the same condition says what it saw
	if _, err := h.eng.Save(context.Background(), Rule{Name: "Gas duurder", Enabled: true,
		Conditions: []Condition{cond}, Actions: []Action{{Type: ActionNotify}}}); err != nil {
		t.Fatal(err)
	}
	h.step(2 * time.Second) // arms the alert (an alert starts unarmed); the automation acts at once
	h.prices = map[string]float64{MetricGasPriceAllIn: 1.30, MetricPriceAllIn: 0.90}
	h.step(2 * time.Second)
	h.prices = map[string]float64{MetricGasPriceAllIn: 1.30, MetricPriceAllIn: 0.25}
	h.step(2 * time.Second) // 0.65 > 0.20
	if got := strings.Join(h.dev.switched, ","); got != "airco=on,airco=off,airco=on" {
		t.Fatalf("switched %q", got)
	}
	var alert, auto int
	for _, m := range h.messages() {
		switch {
		case m.Title == "Gas duurder" && strings.Contains(m.Body, "0,5 × Gasprijs all-in 1,300 €/m³ (hoger dan 0,8 × Stroomprijs all-in (0,250 €/kWh))"):
			alert++
		case m.Title == "Airco is goedkoper" && m.Body == "Afgegaan.":
			auto++
		default:
			t.Fatalf("message %+v", m)
		}
	}
	if alert != 1 || auto != 2 {
		t.Fatalf("%d alert, %d automation messages", alert, auto)
	}
	h.dev.switched = nil
	h.prices = map[string]float64{MetricGasPriceAllIn: 1.30, MetricPriceAllIn: 0.90} // 0.65 < 0.72
	h.step(2 * time.Second)
	if got := strings.Join(h.dev.switched, ","); got != "airco=off" {
		t.Fatalf("switched %q", got)
	}
	// unknown reference: neither fires nor arms
	h.prices = map[string]float64{MetricGasPriceAllIn: 1.30}
	h.step(2 * time.Second)
	if r := h.eng.List()[0]; !r.State.Armed || r.State.ActiveSince != nil {
		t.Fatalf("state %+v", r.State)
	}
}

// Today's usage, the new operators and "any of" conditions.
func TestTodayUsageAnyOf(t *testing.T) {
	h := newHarness(t)
	h.dev.set("p1", "P1-meter", gateway.StatusOnline, map[string]float64{"power_w": 300})
	h.dev.setToday("p1", map[string]float64{"energy_import_kwh": 4, "energy_export_kwh": 1, "gas_m3": 2})
	_, err := h.eng.Save(context.Background(), Rule{
		Name: "Veel verbruik", Enabled: true, Match: MatchAny,
		Conditions: []Condition{
			{Device: "p1", Metric: "today_net_kwh", Op: OpAboveOrEqual, Value: 10},
			{Device: "p1", Metric: "today_gas_m3", Op: OpAboveOrEqual, Value: 3},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	h.step(2 * time.Second) // 3 kWh net, 2 m³: false, arms
	h.dev.setToday("p1", map[string]float64{"energy_import_kwh": 4, "energy_export_kwh": 1, "gas_m3": 3})
	h.step(2 * time.Second)
	msgs := h.messages()
	if len(msgs) != 1 || !strings.Contains(msgs[0].Body, "P1-meter: gas vandaag 3 m³ (minstens 3 m³)") || !strings.Contains(msgs[0].Body, " of ") {
		t.Fatalf("messages %+v", msgs)
	}
	if msgs[0].URL != "/#/meldingen" {
		t.Fatalf("url %q", msgs[0].URL)
	}
}

// A rule stored before rules had a kind is an automation when it switches,
// else an alert.
func TestKindOfOldRules(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for _, r := range []Rule{
		{Name: "oud alert", Enabled: true, Conditions: dryerRule().Conditions, Actions: []Action{{Type: ActionNotify}}},
		{Name: "oud schakelen", Enabled: true, Conditions: dryerRule().Conditions, Actions: []Action{{Type: ActionSwitch, Device: "x"}}},
	} {
		data, _ := json.Marshal(r)
		if _, err := h.st.SaveRule(ctx, store.StoredRule{Data: data}); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.eng.Load(ctx); err != nil {
		t.Fatal(err)
	}
	list := h.eng.List()
	if list[0].Kind != KindAlert || list[1].Kind != KindAutomation || list[0].Match != MatchAll {
		t.Fatalf("kinds %q %q match %q", list[0].Kind, list[1].Kind, list[0].Match)
	}
}

func TestCompareOperators(t *testing.T) {
	for _, c := range []struct {
		op   string
		a, b float64
		want bool
	}{
		{OpBelow, 1, 1, false}, {OpBelowOrEqual, 1, 1, true}, {OpAbove, 2, 1, true},
		{OpAboveOrEqual, 0.1 + 0.2, 0.3, true}, {OpEqual, 0.1 + 0.2, 0.3, true}, {OpNotEqual, 1, 2, true}, {OpNotEqual, 1, 1, false},
	} {
		if got := compare(c.op, c.a, c.b); got != c.want {
			t.Errorf("%v %s %v = %v, want %v", c.a, c.op, c.b, got, c.want)
		}
	}
}
