package ista

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/plugin"
)

// Provider is the "ista" plugin type: an account on mijn.ista.nl, with a
// device per radiator and one for the address's total. Polling only reads
// the local database; the Service fetches from ista once a day.
type Provider struct{ Svc *Service }

// Info describes the type.
func (p Provider) Info() plugin.ProviderInfo {
	return plugin.ProviderInfo{
		Type: PluginType,
		Name: "ista (verwarming)",
		Description: "Verwarmingsverbruik per radiator uit mijn.ista.nl: eenmalig de historie sinds je intrek, daarna elke dag bijgewerkt. " +
			"Je wachtwoord wordt versleuteld bewaard.",
		InstanceForm: []plugin.Field{
			{Key: "username", Label: "Gebruikersnaam (e-mailadres)", Type: plugin.FieldText, Required: true},
			{Key: "password", Label: "Wachtwoord", Type: plugin.FieldPassword, Required: true, Secret: true,
				Help: "Het wachtwoord van mijn.ista.nl. Er is geen andere manier om in te loggen."},
		},
		DeviceForm:      []plugin.Field{},
		CanScan:         true,
		IntervalSeconds: 600,
	}
}

// Instance builds an account. It does not touch the network.
func (p Provider) Instance(config map[string]string, env plugin.Env) (plugin.Instance, error) {
	if p.Svc == nil {
		return nil, errors.New("ista: niet beschikbaar")
	}
	return &instance{svc: p.Svc, id: env.InstanceID, user: strings.TrimSpace(config["username"]), pass: config["password"]}, nil
}

type instance struct {
	svc        *Service
	id         string
	user, pass string
}

func (i *instance) Close() {}

// Address forms of the devices.
const (
	totalPrefix = "total:"
	meterPrefix = "meter:"
)

// Scan logs in and lists the radiators of every address, plus a total.
func (i *instance) Scan(ctx context.Context) ([]plugin.Candidate, error) {
	if i.user == "" || i.pass == "" {
		return nil, errors.New("vul gebruikersnaam en wachtwoord in")
	}
	addrs, meters, err := i.svc.Discover(ctx, i.id, i.user, i.pass)
	if err != nil {
		if errors.Is(err, ErrAuth) {
			return nil, errors.New("ista weigerde de gebruikersnaam of het wachtwoord")
		}
		return nil, err
	}
	var out []plugin.Candidate
	for _, a := range addrs {
		out = append(out, plugin.Candidate{Address: totalPrefix + a.Cuid, Name: "Verwarming totaal",
			Config: map[string]string{"cuid": a.Cuid}})
		for _, m := range meters[a.Cuid] {
			out = append(out, plugin.Candidate{Address: fmt.Sprintf("%s%d", meterPrefix, m.MeterID),
				Name:   fmt.Sprintf("%s (radiator %d)", m.Position, m.RadNr),
				Config: map[string]string{"cuid": a.Cuid}})
		}
	}
	return out, nil
}

// Poll reads the device's figures from the database.
func (i *instance) Poll(ctx context.Context, dev plugin.Device) (plugin.Reading, error) {
	cuid := dev.Config["cuid"]
	switch {
	case strings.HasPrefix(dev.Address, totalPrefix):
		return i.svc.totalReading(ctx, strings.TrimPrefix(dev.Address, totalPrefix))
	case strings.HasPrefix(dev.Address, meterPrefix):
		id, err := strconv.ParseInt(strings.TrimPrefix(dev.Address, meterPrefix), 10, 64)
		if err != nil {
			return plugin.Reading{}, err
		}
		return i.svc.roomReading(ctx, cuid, id)
	}
	return plugin.Reading{}, fmt.Errorf("ista: onbekend apparaat %q", dev.Address)
}

// weekBounds returns the Monday of last complete week and its Sunday.
func lastWeek(today time.Time) (time.Time, time.Time) {
	mon := bucketStart(BucketWeek, today, nil).AddDate(0, 0, -7)
	return mon, mon.AddDate(0, 0, 6)
}

func num(key, label string, v *float64, unit string) plugin.Metric {
	var val any
	if v != nil {
		val = *v
	}
	return plugin.Metric{Key: key, Label: label, Kind: plugin.MetricNumber, Value: val, Unit: unit}
}

// roomReading: this period so far, last week, the last day and the same
// moment a year earlier, for one radiator.
func (s *Service) roomReading(ctx context.Context, cuid string, meterID int64) (plugin.Reading, error) {
	a, err := s.pickAddress(ctx, cuid)
	if err != nil {
		return plugin.Reading{}, err
	}
	idx := -1
	for k, r := range a.Rooms {
		if r.ID == meterID {
			idx = k
		}
	}
	if idx < 0 || a.LastDay == "" {
		return plugin.Reading{Online: false, Err: "nog geen gegevens"}, nil
	}
	return s.reading(ctx, a, idx)
}

func (s *Service) totalReading(ctx context.Context, cuid string) (plugin.Reading, error) {
	a, err := s.pickAddress(ctx, cuid)
	if err != nil || a.LastDay == "" {
		return plugin.Reading{Online: false, Err: "nog geen gegevens"}, nil
	}
	r, err := s.reading(ctx, a, -1)
	if err != nil {
		return r, err
	}
	if c, err := s.Costs(ctx, a.Cuid, 0); err == nil {
		if c.CostExpected != nil {
			r.Metrics = append(r.Metrics, num("cost_expected", "Verwachte kosten periode", c.CostExpected, "€"))
		}
		if c.Balance != nil {
			r.Metrics = append(r.Metrics, num("balance", "Verwacht bijbetalen (−: terug)", c.Balance, "€"))
		}
	}
	return r, nil
}

// reading builds the metrics of one room (idx) or the whole address (-1).
func (s *Service) reading(ctx context.Context, a Address, idx int) (plugin.Reading, error) {
	// ista's estimates after the last measured day are never shown
	last, _ := time.Parse(DayLayout, a.LastMeasured)
	p, ok := pickPeriod(a, 0, s.today())
	if !ok {
		return plugin.Reading{Online: false, Err: "nog geen gegevens"}, nil
	}
	from, _ := time.Parse(DayLayout, p.From)
	pick := func(b Bucket) *float64 {
		if idx < 0 {
			return b.Total
		}
		return b.Units[idx]
	}
	unit := a.Unit
	if unit == "" {
		unit = "eenheden"
	}
	var metrics []plugin.Metric
	// the season, the building average and last year only count the days
	// ista measured, never its estimates
	measured, err := time.Parse(DayLayout, a.LastMeasured)
	if err == nil && !measured.Before(from) {
		season, err := s.Usage(ctx, a.Cuid, from, measured, BucketPeriod)
		if err != nil {
			return plugin.Reading{}, err
		}
		metrics = append(metrics, num("units_period", "Dit seizoen", pick(season.Total), unit))
		if idx < 0 {
			metrics = append(metrics, num("building_period", "Gebouwgemiddelde dit seizoen", season.Total.Building, unit))
		} else {
			metrics = append(metrics, num("ticks_period", "Tikken dit seizoen", season.Total.Ticks[idx], ""))
		}
		ly, err := s.Usage(ctx, a.Cuid, from.AddDate(-1, 0, 0), measured.AddDate(-1, 0, 0), BucketPeriod)
		if err == nil && ly.Total.Missing < ly.Total.Days {
			metrics = append(metrics, num("units_last_year", "Zelfde moment vorig jaar", pick(ly.Total), unit))
		}
	}
	mon, sun := lastWeek(s.today())
	if wk, err := s.Usage(ctx, a.Cuid, mon, sun, BucketPeriod); err == nil && wk.Total.Missing < wk.Total.Days {
		metrics = append(metrics, num("units_last_week", "Vorige week", pick(wk.Total), unit))
	}
	if day, err := s.Usage(ctx, a.Cuid, last, last, BucketDay); err == nil {
		metrics = append(metrics, num("units_last_day", "Laatste dag", pick(day.Total), unit))
	}
	if m, err := time.Parse(DayLayout, a.LastMeasured); err == nil {
		metrics = append(metrics, plugin.Metric{Key: "last_measured", Label: "Gemeten t/m", Kind: plugin.MetricText, Value: m.Format("02-01-2006")})
	}
	return plugin.Reading{Online: true, Metrics: metrics}, nil
}
