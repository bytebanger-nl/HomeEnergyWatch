package plugin

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/bytebanger-nl/homeenergywatch/internal/gateway"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// MaxTabs is how many tabs of their own plugin devices can have. The nav
// already has seven tabs; more would not fit on a phone.
const MaxTabs = 3

// ErrTabs is a refused tab change (shown to the user, Dutch).
var ErrTabs = errors.New("plugin: tab")

type tabError struct{ msg string }

func (e *tabError) Error() string { return e.msg }
func (e *tabError) Unwrap() error { return ErrTabs }

func tabName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 24 {
		return "", &tabError{"geef het tabblad een naam van hooguit 24 tekens"}
	}
	return name, nil
}

// Tabs returns the user's own tabs for plugin devices, in order.
func (m *Manager) Tabs(ctx context.Context) ([]store.PluginTab, error) {
	return m.store.PluginTabs(ctx)
}

// AddTab adds a tab, at most MaxTabs.
func (m *Manager) AddTab(ctx context.Context, name string) (store.PluginTab, error) {
	name, err := tabName(name)
	if err != nil {
		return store.PluginTab{}, err
	}
	tabs, err := m.store.PluginTabs(ctx)
	if err != nil {
		return store.PluginTab{}, err
	}
	if len(tabs) >= MaxTabs {
		return store.PluginTab{}, &tabError{"hooguit 3 eigen tabbladen"}
	}
	t, err := m.store.AddPluginTab(ctx, name)
	if err != nil {
		return store.PluginTab{}, err
	}
	m.tabsChanged(ctx, false)
	return t, nil
}

// RenameTab renames a tab.
func (m *Manager) RenameTab(ctx context.Context, id int64, name string) error {
	name, err := tabName(name)
	if err != nil {
		return err
	}
	if err := m.store.RenamePluginTab(ctx, id, name); err != nil {
		return err
	}
	m.tabsChanged(ctx, false)
	return nil
}

// DeleteTab removes a tab; its devices go back to Klimaat.
func (m *Manager) DeleteTab(ctx context.Context, id int64) error {
	if err := m.store.DeletePluginTab(ctx, id); err != nil {
		return err
	}
	m.tabsChanged(ctx, true)
	return nil
}

// SetDeviceTab puts a plugin device on a tab (0 = Klimaat).
func (m *Manager) SetDeviceTab(ctx context.Context, deviceID string, tabID int64) error {
	if err := m.store.SetPluginDeviceTab(ctx, deviceID, tabID); err != nil {
		return err
	}
	m.tabsChanged(ctx, true)
	return nil
}

// tabsChanged publishes the tabs, and the devices when they moved.
func (m *Manager) tabsChanged(ctx context.Context, devices bool) {
	if tabs, err := m.store.PluginTabs(ctx); err == nil {
		m.hub.Publish(gateway.Event{Type: "plugin_tabs", Data: tabs})
	}
	if devices {
		m.Reload(ctx)
	}
}
