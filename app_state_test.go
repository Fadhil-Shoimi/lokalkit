package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStateNeverHasNullLists(t *testing.T) {
	b, _ := json.Marshal(State{}.withEmptyLists())
	if strings.Contains(string(b), "null") {
		t.Fatalf("state JSON has null: %s", b)
	}
}
