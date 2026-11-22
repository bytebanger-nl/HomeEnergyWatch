package prices

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"time"
)

// NordPool reads the public (undocumented) data portal API of Nord Pool, the
// one behind its website. It needs no token but may change without notice,
// and Nord Pool's terms do not cover automated use; keep it for personal use
// and keep ENTSO-E configured as fallback.
type NordPool struct {
	Client  *http.Client
	BaseURL string // default https://dataportal-api.nordpoolgroup.com
	Area    string // default NL
}

// Name implements Source.
func (n *NordPool) Name() string { return "nordpool" }

type nordPoolResponse struct {
	DeliveryDateCET  string `json:"deliveryDateCET"`
	Currency         string `json:"currency"`
	MultiAreaEntries []struct {
		DeliveryStart time.Time          `json:"deliveryStart"`
		DeliveryEnd   time.Time          `json:"deliveryEnd"`
		EntryPerArea  map[string]float64 `json:"entryPerArea"`
	} `json:"multiAreaEntries"`
}

// Day implements Source.
func (n *NordPool) Day(ctx context.Context, day time.Time) ([]Price, error) {
	base := n.BaseURL
	if base == "" {
		base = "https://dataportal-api.nordpoolgroup.com"
	}
	area := n.Area
	if area == "" {
		area = "NL"
	}
	q := url.Values{
		"date":         {day.In(Amsterdam).Format("2006-01-02")},
		"market":       {"DayAhead"},
		"deliveryArea": {area},
		"currency":     {"EUR"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/DayAheadPrices?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "homeenergywatch (personal use)")
	client := n.Client
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
	case resp.StatusCode == http.StatusNoContent || (resp.StatusCode == http.StatusOK && len(body) == 0):
		return nil, ErrNotPublished
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("nordpool: HTTP %d", resp.StatusCode)
	}
	return parseNordPool(body, area)
}

func parseNordPool(body []byte, area string) ([]Price, error) {
	var r nordPoolResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("nordpool: unexpected response: %w", err)
	}
	if r.Currency != "" && r.Currency != "EUR" {
		return nil, fmt.Errorf("nordpool: unexpected currency %q", r.Currency)
	}
	var out []Price
	for _, e := range r.MultiAreaEntries {
		v, ok := e.EntryPerArea[area]
		if !ok || !e.DeliveryEnd.After(e.DeliveryStart) {
			continue
		}
		out = append(out, Price{Start: e.DeliveryStart.UTC(), Duration: e.DeliveryEnd.Sub(e.DeliveryStart), Value: v, Source: "nordpool"})
	}
	if len(out) == 0 {
		return nil, ErrNotPublished
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out, nil
}
