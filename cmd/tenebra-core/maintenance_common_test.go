package main

import (
	"strings"
	"testing"
)

func TestRootSocketRequiresJournalTransactionAndIdentifiedLaunch(t *testing.T) {
	for _, row := range []struct {
		socket, root   bool
		tx, launch     string
		valid, managed bool
	}{
		{true, true, "", "", false, false},
		{true, true, "it1_" + strings.Repeat("a", 32), "", false, false},
		{true, true, "it1_" + strings.Repeat("a", 32), strings.Repeat("b", 32), true, true},
		{true, true, "it1_" + strings.Repeat("A", 32), strings.Repeat("b", 32), false, false},
		{true, false, "it1_" + strings.Repeat("a", 32), strings.Repeat("b", 32), false, false},
		{false, true, "it1_" + strings.Repeat("a", 32), strings.Repeat("b", 32), false, false},
		{true, false, "", "", true, false},
		{false, false, "", "", true, false},
	} {
		got, err := selectRunAuthority(row.socket, row.root, row.tx, row.launch)
		if (err == nil) != row.valid || got.managed != row.managed {
			t.Fatal("root socket/development boundary", row, got, err)
		}
	}
}
