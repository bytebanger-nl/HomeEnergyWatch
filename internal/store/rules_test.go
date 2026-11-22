package store

import (
	"context"
	"testing"
)

// Saving a rule clears whether it may fire but keeps whether a manual
// action is switched on.
func TestRuleStateKeptOnSave(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	id, err := s.SaveRule(ctx, StoredRule{Data: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetRuleState(ctx, id, true, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveRule(ctx, StoredRule{ID: id, Data: []byte(`{"name":"x"}`)}); err != nil {
		t.Fatal(err)
	}
	list, err := s.Rules(ctx)
	if err != nil || len(list) != 1 || list[0].Armed || !list[0].Active {
		t.Fatalf("rules %+v %v", list, err)
	}
}
