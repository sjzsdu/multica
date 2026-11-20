package main

import (
	"bufio"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/presets"
)

func TestParseSelection(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		size    int
		want    []int
		wantErr string
	}{
		{name: "single", input: "2", size: 5, want: []int{1}},
		{name: "list", input: "1,3,5", size: 5, want: []int{0, 2, 4}},
		{name: "range", input: "2-4", size: 5, want: []int{1, 2, 3}},
		{name: "mixed", input: "1,3-4", size: 5, want: []int{0, 2, 3}},
		{name: "dedupe", input: "1,1,1-2", size: 5, want: []int{0, 1}},
		{name: "out of range", input: "6", size: 5, wantErr: "out of range"},
		{name: "bad range", input: "4-2", size: 5, wantErr: "invalid range"},
		{name: "empty", input: " , ", size: 5, wantErr: "no items selected"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseSelection(tc.input, tc.size)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSelection: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestChooseItemsPullsSquadMembersAndAutopilotAgents(t *testing.T) {
	agents := []presets.Agent{
		{Name: "leader"},
		{Name: "member"},
		{Name: "operator"},
	}
	catalog := []presets.Preset{{
		ID:     "demo",
		Agents: agents,
		Squads: []presets.Squad{{
			Name:    "team",
			Leader:  "leader",
			Members: []presets.SquadMember{{Name: "leader", Role: "leader"}, {Name: "member", Role: "member"}},
		}},
		Autopilots: []presets.Autopilot{{Title: "nightly", Agent: "operator"}},
	}}

	selection, err := chooseItems(catalog, &onboardPrompt{in: bufio.NewReader(strings.NewReader(""))}, true, false)
	if err != nil {
		t.Fatalf("chooseItems: %v", err)
	}
	if len(selection.squads) != 1 || len(selection.autopilots) != 1 {
		t.Fatalf("selection = %+v", selection)
	}

	// Selecting only the squad must pull in its member agents.
	squadOnly := onboardSelection{
		agents: agents,
		squads: catalog[0].Squads,
	}
	if names := squadAgentNames(squadOnly.squads[0]); len(names) != 2 {
		t.Fatalf("squadAgentNames = %v", names)
	}
}
