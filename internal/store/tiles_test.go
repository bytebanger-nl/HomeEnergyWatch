package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestDashboardTiles(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()
	if tiles, err := st.DashboardTiles(ctx); err != nil || len(tiles) != 0 {
		t.Fatalf("new store: %v %v, want no tiles", tiles, err)
	}
	a, err := st.AddDashboardTile(ctx, DashboardTile{Name: "Klimaat", Size: "small", Items: json.RawMessage(`[]`)})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := st.AddDashboardTile(ctx, DashboardTile{Name: "Airco", Size: "wide", Items: json.RawMessage(`[{"type":"value"}]`)})
	c, _ := st.AddDashboardTile(ctx, DashboardTile{Name: "Tado", Size: "small", Items: json.RawMessage(`[]`)})
	if a.Position != 0 || b.Position != 1 || c.Position != 2 {
		t.Fatalf("positions %d %d %d", a.Position, b.Position, c.Position)
	}
	b.Name, b.Items = "Buiten", json.RawMessage(`[{"type":"chart"}]`)
	if err := st.UpdateDashboardTile(ctx, b); err != nil {
		t.Fatal(err)
	}
	got, err := st.DashboardTile(ctx, b.ID)
	if err != nil || got.Name != "Buiten" || string(got.Items) != `[{"type":"chart"}]` || got.Size != "wide" {
		t.Fatalf("tile %+v %v", got, err)
	}
	// c first, then a; b is not listed and goes after them
	if err := st.OrderDashboardTiles(ctx, []int64{c.ID, a.ID}); err != nil {
		t.Fatal(err)
	}
	tiles, _ := st.DashboardTiles(ctx)
	if len(tiles) != 3 || tiles[0].ID != c.ID || tiles[1].ID != a.ID || tiles[2].ID != b.ID {
		t.Fatalf("order %+v", tiles)
	}
	if err := st.DeleteDashboardTile(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteDashboardTile(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	if err := st.UpdateDashboardTile(ctx, a); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update deleted: %v", err)
	}
	if _, err := st.DashboardTile(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get deleted: %v", err)
	}
}
