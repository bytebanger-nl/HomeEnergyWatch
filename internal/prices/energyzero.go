package prices

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"
)

// EnergyZero reads the public price API of EnergyZero (no token). Its "base"
// price is the market price without VAT, purchase fee or energy tax: for
// electricity the day-ahead price per quarter hour (in EUR/kWh, identical to
// Nord Pool), for gas the price per gas day (in EUR/m³).
//
// A request for one date returns the day before and the day after as well
// when they are available; those are returned (and stored) too, so a
// backfill needs a third of the requests.
type EnergyZero struct {
	Client  *http.Client
	BaseURL string // default https://public.api.energyzero.nl
	Gas     bool   // gas day prices instead of electricity
}

// Name implements Source.
func (e *EnergyZero) Name() string { return "energyzero" }

type energyZeroResponse struct {
	Interval string `json:"interval"`
	Base     []struct {
		Start time.Time `json:"start"`
		End   time.Time `json:"end"`
		Price struct {
			Value string `json:"value"`
		} `json:"price"`
	} `json:"base"`
}

// Day implements Source.
func (e *EnergyZero) Day(ctx context.Context, day time.Time) ([]Price, error) {
	base := e.BaseURL
	if base == "" {
		base = "https://public.api.energyzero.nl"
	}
	q := url.Values{
		"date":        {day.In(Amsterdam).Format("02-01-2006")},
		"interval":    {"INTERVAL_QUARTER"},
		"energy_type": {"ENERGY_TYPE_ELECTRICITY"},
	}
	if e.Gas {
		q.Set("interval", "INTERVAL_DAY")
		q.Set("energy_type", "ENERGY_TYPE_GAS")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/prices?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "homeenergywatch (personal use)")
	client := e.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		// {"code":5,"message":"prices do not span the whole requested date"}
		return nil, ErrNotPublished
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("energyzero: HTTP %d", resp.StatusCode)
	}
	return parseEnergyZero(body, e.Gas)
}

func parseEnergyZero(body []byte, gas bool) ([]Price, error) {
	var r energyZeroResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("energyzero: unexpected response: %w", err)
	}
	want := "RESPONSE_INTERVAL_QUARTER"
	if gas {
		want = "RESPONSE_INTERVAL_DAY"
	}
	if r.Interval != "" && r.Interval != want {
		return nil, fmt.Errorf("energyzero: unexpected interval %q", r.Interval)
	}
	var out []Price
	for _, p := range r.Base {
		v, err := strconv.ParseFloat(p.Price.Value, 64)
		if err != nil {
			return nil, fmt.Errorf("energyzero: price %q: %w", p.Price.Value, err)
		}
		if !p.End.After(p.Start) {
			continue
		}
		if !gas {
			v = math.Round(v*1e9) / 1e6 // EUR/kWh -> EUR/MWh, without float noise
		}
		out = append(out, Price{Start: p.Start.UTC(), Duration: p.End.Sub(p.Start), Value: v, Source: "energyzero"})
	}
	if len(out) == 0 {
		return nil, ErrNotPublished
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out, nil
}
