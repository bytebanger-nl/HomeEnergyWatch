package prices

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// EntsoE reads day-ahead prices (document type A44) from the ENTSO-E
// Transparency Platform. A free API token can be requested from the
// platform's helpdesk after registering.
type EntsoE struct {
	Token   string
	Client  *http.Client
	BaseURL string // default https://web-api.tp.entsoe.eu/api
	Domain  string // default 10YNL----------L (Netherlands)
}

// Name implements Source.
func (e *EntsoE) Name() string { return "entsoe" }

type entsoeDoc struct {
	XMLName    xml.Name `xml:"Publication_MarketDocument"`
	TimeSeries []struct {
		Currency string `xml:"currency_Unit.name"`
		Unit     string `xml:"price_Measure_Unit.name"`
		Curve    string `xml:"curveType"`
		Period   []struct {
			Interval struct {
				Start string `xml:"start"`
				End   string `xml:"end"`
			} `xml:"timeInterval"`
			Resolution string `xml:"resolution"`
			Points     []struct {
				Position int     `xml:"position"`
				Price    float64 `xml:"price.amount"`
			} `xml:"Point"`
		} `xml:"Period"`
	} `xml:"TimeSeries"`
}

type entsoeAck struct {
	XMLName xml.Name `xml:"Acknowledgement_MarketDocument"`
	Reason  []struct {
		Code string `xml:"code"`
		Text string `xml:"text"`
	} `xml:"Reason"`
}

// Day implements Source.
func (e *EntsoE) Day(ctx context.Context, day time.Time) ([]Price, error) {
	if e.Token == "" {
		return nil, errors.New("entsoe: no API token (ENTSOE_API_TOKEN)")
	}
	base := e.BaseURL
	if base == "" {
		base = "https://web-api.tp.entsoe.eu/api"
	}
	domain := e.Domain
	if domain == "" {
		domain = "10YNL----------L"
	}
	from, to := DayBounds(day)
	q := url.Values{
		"securityToken": {e.Token},
		"documentType":  {"A44"},
		"in_Domain":     {domain},
		"out_Domain":    {domain},
		"periodStart":   {from.Format("200601021504")},
		"periodEnd":     {to.Format("200601021504")},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	client := e.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		// never leak the token in error messages
		var uerr *url.Error
		if errors.As(err, &uerr) {
			return nil, fmt.Errorf("entsoe: %w", uerr.Err)
		}
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, ErrTokenRejected
	}
	ps, err := parseEntsoE(body)
	if err != nil {
		return nil, err
	}
	// keep only the requested day
	var out []Price
	for _, p := range ps {
		if !p.Start.Before(from) && p.Start.Before(to) {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil, ErrNotPublished
	}
	return out, nil
}

func parseEntsoE(body []byte) ([]Price, error) {
	if strings.Contains(string(body[:min(len(body), 400)]), "Acknowledgement_MarketDocument") {
		var ack entsoeAck
		_ = xml.Unmarshal(body, &ack)
		for _, r := range ack.Reason {
			if r.Code == "999" && strings.Contains(strings.ToLower(r.Text), "no matching data") {
				return nil, ErrNotPublished
			}
		}
		if len(ack.Reason) > 0 {
			return nil, fmt.Errorf("entsoe: %s", ack.Reason[0].Text)
		}
		return nil, errors.New("entsoe: request rejected")
	}
	var doc entsoeDoc
	if err := xml.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("entsoe: unexpected response: %w", err)
	}
	// One delivery period can occur in several series (e.g. both PT15M and
	// PT60M); the finest resolution wins.
	best := map[int64]Price{}
	for _, ts := range doc.TimeSeries {
		if ts.Currency != "" && ts.Currency != "EUR" {
			continue
		}
		for _, per := range ts.Period {
			start, err := parseEntsoETime(per.Interval.Start)
			if err != nil {
				return nil, err
			}
			end, err := parseEntsoETime(per.Interval.End)
			if err != nil {
				return nil, err
			}
			res, err := parseResolution(per.Resolution)
			if err != nil {
				return nil, err
			}
			n := int(end.Sub(start) / res)
			byPos := map[int]float64{}
			for _, pt := range per.Points {
				byPos[pt.Position] = pt.Price
			}
			// curve A03: a position without a point repeats the previous price
			var last float64
			have := false
			for pos := 1; pos <= n; pos++ {
				v, ok := byPos[pos]
				if ok {
					last, have = v, true
				} else if !have {
					continue
				}
				p := Price{Start: start.Add(time.Duration(pos-1) * res), Duration: res, Value: last, Source: "entsoe"}
				key := p.Start.Unix()
				if old, exists := best[key]; !exists || p.Duration < old.Duration {
					best[key] = p
				}
			}
		}
	}
	out := make([]Price, 0, len(best))
	for _, p := range best {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	// drop hourly periods that are fully covered by quarter-hour ones
	var cleaned []Price
	for i, p := range out {
		if i+1 < len(out) && out[i+1].Start.Before(p.End()) {
			continue
		}
		cleaned = append(cleaned, p)
	}
	return cleaned, nil
}

func parseEntsoETime(s string) (time.Time, error) {
	for _, layout := range []string{"2006-01-02T15:04Z", time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("entsoe: bad time %q", s)
}

func parseResolution(s string) (time.Duration, error) {
	switch s {
	case "PT15M":
		return 15 * time.Minute, nil
	case "PT30M":
		return 30 * time.Minute, nil
	case "PT60M", "PT1H":
		return time.Hour, nil
	}
	return 0, fmt.Errorf("entsoe: unsupported resolution %q", s)
}
