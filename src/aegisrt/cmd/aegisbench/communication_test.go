package main

import "testing"

func TestCommunicationCommandUsesFixedProfile(t *testing.T) {
	options, err := parseArgs([]string{"communication", "--output", t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if options.kind != commandCommunication || options.communicationPlan.Repetitions != 3 {
		t.Fatalf("unexpected options: %+v", options)
	}
	for _, args := range [][]string{
		{"communication"},
		{"communication", "--output", t.TempDir(), "--profile", "formal"},
		{"communication", "--output", t.TempDir(), "--seeds", "1"},
	} {
		if _, err := parseArgs(args); err == nil {
			t.Fatalf("invalid arguments accepted: %v", args)
		}
	}
}
