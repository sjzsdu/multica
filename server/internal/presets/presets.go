// Package presets ships the built-in agent fleet presets that `multica onboard`
// installs into a workspace.
//
// A preset bundles skills, agents, squads, and autopilots. Content lives in
// assets/ as real files so prompts stay reviewable and diffable; catalog.json
// is the index. Agent and squad references inside skill bodies use
// {{agent:Name}} / {{squad:Name}} placeholders because ids only exist after
// installation.
package presets

import (
	"embed"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strings"
)

//go:embed assets/catalog.json assets/*/skills/*.md assets/*/agents/*.md
var assets embed.FS

// Catalog is the top-level index embedded in the CLI.
type Catalog struct {
	Presets []Preset `json:"presets"`
}

// Preset is one installable team.
type Preset struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Skills      []Skill     `json:"skills"`
	Agents      []Agent     `json:"agents"`
	Squads      []Squad     `json:"squads"`
	Autopilots  []Autopilot `json:"autopilots"`
}

// Skill is a workspace skill plus the body read from its file.
type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	File        string `json:"file"`
	Content     string `json:"-"`
}

// Agent is an agent definition; Instructions is read from InstructionsFile.
type Agent struct {
	Name               string   `json:"name"`
	Description        string   `json:"description"`
	InstructionsFile   string   `json:"instructions_file"`
	Instructions       string   `json:"-"`
	Model              string   `json:"model"`
	MaxConcurrentTasks int      `json:"max_concurrent_tasks"`
	Permission         string   `json:"permission"`
	Skills             []string `json:"skills"`
}

// Squad groups agents under a leader.
type Squad struct {
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	Instructions string         `json:"instructions"`
	Leader       string         `json:"leader"`
	Members      []SquadMember  `json:"members"`
	Workflow     map[string]any `json:"workflow"`
}

// SquadMember is one squad membership entry.
type SquadMember struct {
	Name string `json:"name"`
	Role string `json:"role"`
}

// Autopilot is a scheduled automation bound to one agent.
type Autopilot struct {
	Title       string            `json:"title"`
	Description string            `json:"description"`
	Agent       string            `json:"agent"`
	Mode        string            `json:"mode"`
	Triggers    []ScheduleTrigger `json:"triggers"`
}

// ScheduleTrigger is a cron schedule attached to an autopilot.
type ScheduleTrigger struct {
	Cron     string `json:"cron"`
	Timezone string `json:"timezone"`
	Label    string `json:"label"`
}

// Load parses the embedded catalog and reads every referenced content file.
func Load() (Catalog, error) {
	raw, err := assets.ReadFile("assets/catalog.json")
	if err != nil {
		return Catalog{}, fmt.Errorf("read preset catalog: %w", err)
	}
	var catalog Catalog
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return Catalog{}, fmt.Errorf("parse preset catalog: %w", err)
	}

	for i := range catalog.Presets {
		preset := &catalog.Presets[i]
		if preset.ID == "" {
			return Catalog{}, fmt.Errorf("preset %d: missing id", i)
		}
		for j := range preset.Skills {
			content, err := readAsset(preset.ID, preset.Skills[j].File)
			if err != nil {
				return Catalog{}, fmt.Errorf("skill %q: %w", preset.Skills[j].Name, err)
			}
			preset.Skills[j].Content = content
		}
		for j := range preset.Agents {
			content, err := readAsset(preset.ID, preset.Agents[j].InstructionsFile)
			if err != nil {
				return Catalog{}, fmt.Errorf("agent %q: %w", preset.Agents[j].Name, err)
			}
			preset.Agents[j].Instructions = content
		}
	}
	return catalog, nil
}

func readAsset(presetID, file string) (string, error) {
	if file == "" {
		return "", nil
	}
	// path.Join also rejects traversal in practice: ".." segments would leave
	// the embedded assets tree and fail the read.
	raw, err := assets.ReadFile(path.Join("assets", presetID, file))
	if err != nil {
		return "", fmt.Errorf("read %s: %w", file, err)
	}
	return string(raw), nil
}

// Find returns the preset with the given id.
func (c Catalog) Find(id string) (Preset, bool) {
	for _, preset := range c.Presets {
		if preset.ID == id {
			return preset, true
		}
	}
	return Preset{}, false
}

// IDs lists preset ids in catalog order.
func (c Catalog) IDs() []string {
	ids := make([]string, 0, len(c.Presets))
	for _, preset := range c.Presets {
		ids = append(ids, preset.ID)
	}
	return ids
}

var mentionPlaceholder = regexp.MustCompile(`\{\{(agent|squad):([^{}]+)\}\}`)

// ResolveMentions rewrites {{agent:Name}} / {{squad:Name}} placeholders into
// mention URLs using lookup. Unresolvable placeholders are left untouched and
// returned so callers can warn instead of installing a broken link.
func ResolveMentions(text string, lookup func(kind, name string) (id string, ok bool)) (string, []string) {
	var missing []string
	resolved := mentionPlaceholder.ReplaceAllStringFunc(text, func(match string) string {
		parts := mentionPlaceholder.FindStringSubmatch(match)
		kind, name := parts[1], strings.TrimSpace(parts[2])
		id, ok := lookup(kind, name)
		if !ok {
			missing = append(missing, match)
			return match
		}
		return "mention://" + kind + "/" + id
	})
	return resolved, missing
}
