package gateway

import (
	"context"
	"errors"

	"github.com/bytebanger-nl/homeenergywatch/internal/homewizard"
)

// ErrNoBatteryControl: the device cannot control batteries here: it is no
// P1 or kWh meter, or it is not paired over API v2.
var ErrNoBatteryControl = errors.New("dit apparaat bestuurt geen batterijen: alleen een P1- of kWh-meter die via API v2 gekoppeld is")

// CanControlBatteries reports whether v may have a battery group: a P1 or
// kWh meter paired over API v2. Whether batteries are connected shows from
// Batteries.
func CanControlBatteries(v DeviceView) bool {
	return (v.Kind == homewizard.KindP1 || v.Kind == homewizard.KindKWH) && v.Paired && v.Enabled
}

// Batteries reads the battery group a meter controls.
func (m *Manager) Batteries(ctx context.Context, id string) (homewizard.Batteries, error) {
	w, err := m.batteryWorker(id)
	if err != nil {
		return homewizard.Batteries{}, err
	}
	return w.client().Batteries(ctx)
}

// SetBatteries changes the battery group a meter controls.
func (m *Manager) SetBatteries(ctx context.Context, id string, u homewizard.BatteriesUpdate) (homewizard.Batteries, error) {
	w, err := m.batteryWorker(id)
	if err != nil {
		return homewizard.Batteries{}, err
	}
	b, err := w.client().SetBatteries(ctx, u)
	if err == nil {
		m.log.Info("batterijen ingesteld", "id", id, "mode", b.Mode)
	}
	return b, err
}

func (m *Manager) batteryWorker(id string) (*worker, error) {
	w := m.worker(id)
	if w == nil {
		return nil, ErrNotFound
	}
	if !CanControlBatteries(w.snapshot()) {
		return nil, ErrNoBatteryControl
	}
	return w, nil
}
