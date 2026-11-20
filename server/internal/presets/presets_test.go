package presets

import (
	"strings"
	"testing"
)

func TestLoadShipsUsablePresets(t *testing.T) {
	catalog, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(catalog.Presets) == 0 {
		t.Fatal("catalog has no presets")
	}

	for _, preset := range catalog.Presets {
		if preset.ID == "" || preset.Name == "" {
			t.Errorf("preset %+v is missing id or name", preset)
		}
		if len(preset.Agents) == 0 {
			t.Errorf("preset %q has no agents", preset.ID)
		}

		skillNames := map[string]bool{}
		for _, skill := range preset.Skills {
			if skill.Content == "" {
				t.Errorf("preset %q skill %q has empty content", preset.ID, skill.Name)
			}
			skillNames[skill.Name] = true
		}

		agentNames := map[string]bool{}
		for _, agent := range preset.Agents {
			if agent.Instructions == "" {
				t.Errorf("preset %q agent %q has empty instructions", preset.ID, agent.Name)
			}
			if agent.MaxConcurrentTasks < 1 {
				t.Errorf("preset %q agent %q has max_concurrent_tasks < 1", preset.ID, agent.Name)
			}
			for _, skill := range agent.Skills {
				if !skillNames[skill] {
					t.Errorf("preset %q agent %q references unknown skill %q", preset.ID, agent.Name, skill)
				}
			}
			agentNames[agent.Name] = true
		}

		for _, squad := range preset.Squads {
			if squad.Leader == "" {
				t.Errorf("preset %q squad %q has no leader", preset.ID, squad.Name)
			} else if !agentNames[squad.Leader] {
				t.Errorf("preset %q squad %q leader %q is not an agent in the preset", preset.ID, squad.Name, squad.Leader)
			}
			for _, member := range squad.Members {
				if member.Name != "" && !agentNames[member.Name] {
					t.Errorf("preset %q squad %q member %q is not an agent in the preset", preset.ID, squad.Name, member.Name)
				}
			}
		}

		for _, autopilot := range preset.Autopilots {
			if autopilot.Agent != "" && !agentNames[autopilot.Agent] {
				t.Errorf("preset %q autopilot %q assignee %q is not an agent in the preset", preset.ID, autopilot.Title, autopilot.Agent)
			}
			if len(autopilot.Triggers) == 0 {
				t.Errorf("preset %q autopilot %q has no triggers", preset.ID, autopilot.Title)
			}
		}
	}
}

func TestResolveMentions(t *testing.T) {
	ids := map[string]string{"agent/包公 - 质量工程师": "agent-1", "squad/独立开发团队": "squad-1"}
	lookup := func(kind, name string) (string, bool) {
		id, ok := ids[kind+"/"+name]
		return id, ok
	}

	resolved, missing := ResolveMentions(
		"请 [@包公]({{agent:包公 - 质量工程师}}) 复核，团队见 {{squad:独立开发团队}}，未知 {{agent:不存在}}。",
		lookup,
	)
	if want := "mention://agent/agent-1"; !strings.Contains(resolved, want) {
		t.Errorf("resolved text missing %q: %s", want, resolved)
	}
	if want := "mention://squad/squad-1"; !strings.Contains(resolved, want) {
		t.Errorf("resolved text missing %q: %s", want, resolved)
	}
	if len(missing) != 1 || missing[0] != "{{agent:不存在}}" {
		t.Errorf("missing = %v, want one unresolved placeholder", missing)
	}
}
