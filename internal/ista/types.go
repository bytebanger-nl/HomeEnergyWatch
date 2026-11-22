package ista

import (
	"fmt"
	"strings"
	"time"
)

// The JSON shapes of the mijn.ista.nl API. Field names are ista's own
// (including the misspelled Adress and BuldingAverage).

// UserValues is the answer of /api/Values/UserValues.
type UserValues struct {
	DisplayName string     `json:"DisplayName"`
	UserKey     string     `json:"UserKey"`
	Cus         []Customer `json:"Cus"`
}

// Customer is one address (consumption unit) of the account.
type Customer struct {
	Cuid           string         `json:"Cuid"`
	Adress         string         `json:"Adress"`
	Zip            string         `json:"Zip"`
	City           string         `json:"City"`
	DateStart      string         `json:"DateStart"` // move-in, ISO date-time
	CurConsumption CurConsumption `json:"curConsumption"`
}

// CurConsumption holds the billing periods, the services and the running
// period compared with the same window a year earlier.
type CurConsumption struct {
	BillingPeriods  []BillingPeriod  `json:"BillingPeriods"`
	Billingservices []BillingService `json:"Billingservices"`
	ServicesComp    []ServiceComp    `json:"ServicesComp"`
	CurStart        string           `json:"CurStart"` // dd-mm-yyyy
	CurEnd          string           `json:"CurEnd"`
	CompStart       string           `json:"CompStart"`
	CompEnd         string           `json:"CompEnd"`
	CurAverageTemp  float64          `json:"CurAverageTemp"`
	CompAverageTemp float64          `json:"CompAverageTemp"`
}

// BillingPeriod is one settlement year. It is sent back as is (with e
// moved) to ConsumptionValues.
type BillingPeriod struct {
	Y  int      `json:"y"`
	S  string   `json:"s"` // ISO date-time
	E  string   `json:"e"`
	TA *float64 `json:"ta"`
}

// BillingService is a billed service, e.g. heating in units.
type BillingService struct {
	ID          int    `json:"Id"`
	Description string `json:"Description"`
	MeterType   string `json:"MeterType"`
	Unit        string `json:"Unit"`
}

// ServiceComp is a service's totals and the readings per meter.
type ServiceComp struct {
	ID                 int     `json:"Id"`
	DecPos             int     `json:"DecPos"`
	TotalNow           float64 `json:"TotalNow"`
	TotalPrevious      float64 `json:"TotalPrevious"`
	TotalDiffperc      float64 `json:"TotalDiffperc"`
	TotalWholePrevious float64 `json:"TotalWholePrevious"`
	CurMeters          []Meter `json:"CurMeters"`
	CompMeters         []Meter `json:"CompMeters"`
}

// Meter is one heat cost allocator (one radiator) with its reading over a
// window: CValue ticks, CCValue = ticks × CalcFactor, CCDValue = CCValue
// less the Reduction percentage (the units that are billed).
type Meter struct {
	MeterID         int64    `json:"MeterId"`
	ServiceID       int      `json:"serviceId"`
	BillingPeriodID int      `json:"BillingPeriodId"`
	RadNr           int      `json:"RadNr"`
	Order           int      `json:"Order"`
	Position        string   `json:"Position"`
	ArtNr           int64    `json:"ArtNr"`
	MeterNr         int64    `json:"MeterNr"`
	TransferLoss    float64  `json:"TransferLoss"`
	Multiply        float64  `json:"Multiply"`
	Reduction       float64  `json:"Reduction"`
	CalcFactor      float64  `json:"CalcFactor"`
	BsDate          string   `json:"BsDate"`
	BeginValue      float64  `json:"BeginValue"`
	EsDate          string   `json:"EsDate"`
	EndValue        float64  `json:"EndValue"`
	CValue          float64  `json:"CValue"`
	CCValue         float64  `json:"CCValue"`
	CCDValue        float64  `json:"CCDValue"`
	DecPos          int      `json:"DecPos"`
	EvalR           *float64 `json:"EvalR,omitempty"`
}

// Consumption is the answer of /api/Values/ConsumptionValues: the readings
// of every meter from the period's start up to the requested end date.
type Consumption struct {
	CurStart     string        `json:"CurStart"`
	CurEnd       string        `json:"CurEnd"`
	ServicesComp []ServiceComp `json:"ServicesComp"`
}

// Averages is the answer of /api/Values/ConsumptionAverages: the building
// average per service over a window, normalised to this dwelling.
type Averages struct {
	Averages []struct {
		BillingServiceID int     `json:"BillingServiceId"`
		DecPos           int     `json:"DecPos"`
		NormalizedValue  float64 `json:"NormalizedValue"`
	} `json:"Averages"`
	CurStart string `json:"CurStart"`
	CurEnd   string `json:"CurEnd"`
}

// Months is the answer of /api/Consumption/MonthValues.
type Months struct {
	Cuid   string  `json:"cuid"`
	Loaded int     `json:"hs"` // shards loaded
	Shards int     `json:"sh"` // shards in total
	Start  string  `json:"sd"`
	Years  []int   `json:"ys"`
	Months []Month `json:"mc"`
}

// Month is one month's consumption per service.
type Month struct {
	Year        int      `json:"y"`
	Month       int      `json:"m"`
	AverageTemp *float64 `json:"at"`
	CuToken     string   `json:"CuToken"`
	Services    []struct {
		ServiceID        int      `json:"ServiceId"`
		TotalConsumption float64  `json:"TotalConsumption"`
		BuldingAverage   *float64 `json:"BuldingAverage"`
		HasApproximation bool     `json:"HasApproximation"`
	} `json:"ServiceConsumptions"`
}

const (
	isoLayout = "2006-01-02T15:04:05"
	dmyLayout = "02-01-2006"
	// DayLayout is how days are written in the store and the API.
	DayLayout = "2006-01-02"
)

// ParseDate reads an ista date in either of its two forms
// ("2025-07-01T00:00:00" or "01-07-2025") as a calendar day in UTC.
func ParseDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if len(s) >= 10 && s[4] == '-' {
		return time.Parse(DayLayout, s[:10])
	}
	if t, err := time.Parse(dmyLayout, s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("ista: onbekende datum %q", s)
}

// Start and End of a billing period as calendar days.
func (p BillingPeriod) Start() (time.Time, error) { return ParseDate(p.S) }
func (p BillingPeriod) End() (time.Time, error)   { return ParseDate(p.E) }
