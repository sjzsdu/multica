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
	Ref         string `json:"ref,omitempty"`
	Description string `json:"description"`
	File        string `json:"file"`
	Content     string `json:"-"`
}

// Agent is an agent definition; Instructions is read from InstructionsFile.
type Agent struct {
	Name               string   `json:"name"`
	Ref                string   `json:"ref,omitempty"`
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
			if preset.Skills[j].Ref != "" {
				continue
			}
			content, err := readAsset(preset.ID, preset.Skills[j].File)
			if err != nil {
				return Catalog{}, fmt.Errorf("skill %q: %w", preset.Skills[j].Name, err)
			}
			preset.Skills[j].Content = content
		}
		for j := range preset.Agents {
			if preset.Agents[j].Ref != "" {
				continue
			}
			content, err := readAsset(preset.ID, preset.Agents[j].InstructionsFile)
			if err != nil {
				return Catalog{}, fmt.Errorf("agent %q: %w", preset.Agents[j].Name, err)
			}
			preset.Agents[j].Instructions = content
		}
	}
	if err := resolveReferences(&catalog); err != nil {
		return Catalog{}, err
	}
	return catalog, nil
}

type referenceKey struct {
	presetID string
	name     string
}

func resolveReferences(catalog *Catalog) error {
	skills := map[referenceKey]*Skill{}
	agents := map[referenceKey]*Agent{}
	for i := range catalog.Presets {
		preset := &catalog.Presets[i]
		for j := range preset.Skills {
			skill := &preset.Skills[j]
			skills[referenceKey{presetID: preset.ID, name: skill.Name}] = skill
		}
		for j := range preset.Agents {
			agent := &preset.Agents[j]
			agents[referenceKey{presetID: preset.ID, name: agent.Name}] = agent
		}
	}

	// Validate the original graph before copying anything so a resolved reference
	// cannot make a later chained reference look like a local definition.
	for i := range catalog.Presets {
		preset := &catalog.Presets[i]
		for j := range preset.Skills {
			skill := &preset.Skills[j]
			if skill.Ref == "" {
				continue
			}
			if err := validateReferenceLocation("skill", preset.ID, skill.Name, skill.Ref); err != nil {
				return err
			}
			if skill.Description != "" || skill.File != "" || skill.Content != "" {
				return fmt.Errorf("preset %q skill %q: reference item may set only ref and name", preset.ID, skill.Name)
			}
			source, ok := skills[referenceKey{presetID: skill.Ref, name: skill.Name}]
			if !ok {
				return fmt.Errorf("preset %q skill %q: unknown reference %q", preset.ID, skill.Name, skill.Ref)
			}
			if source == skill {
				return fmt.Errorf("preset %q skill %q: self reference", preset.ID, skill.Name)
			}
			if source.Ref != "" {
				return fmt.Errorf("preset %q skill %q: chained reference through %q", preset.ID, skill.Name, skill.Ref)
			}
		}
		for j := range preset.Agents {
			agent := &preset.Agents[j]
			if agent.Ref == "" {
				continue
			}
			if err := validateReferenceLocation("agent", preset.ID, agent.Name, agent.Ref); err != nil {
				return err
			}
			if agent.Description != "" || agent.InstructionsFile != "" || agent.Instructions != "" ||
				agent.Model != "" || agent.MaxConcurrentTasks != 0 || agent.Permission != "" || len(agent.Skills) != 0 {
				return fmt.Errorf("preset %q agent %q: reference item may set only ref and name", preset.ID, agent.Name)
			}
			source, ok := agents[referenceKey{presetID: agent.Ref, name: agent.Name}]
			if !ok {
				return fmt.Errorf("preset %q agent %q: unknown reference %q", preset.ID, agent.Name, agent.Ref)
			}
			if source == agent {
				return fmt.Errorf("preset %q agent %q: self reference", preset.ID, agent.Name)
			}
			if source.Ref != "" {
				return fmt.Errorf("preset %q agent %q: chained reference through %q", preset.ID, agent.Name, agent.Ref)
			}
		}
	}

	for i := range catalog.Presets {
		preset := &catalog.Presets[i]
		for j := range preset.Skills {
			if preset.Skills[j].Ref == "" {
				continue
			}
			source := skills[referenceKey{presetID: preset.Skills[j].Ref, name: preset.Skills[j].Name}]
			preset.Skills[j] = *source
		}
		for j := range preset.Agents {
			if preset.Agents[j].Ref == "" {
				continue
			}
			source := agents[referenceKey{presetID: preset.Agents[j].Ref, name: preset.Agents[j].Name}]
			resolved := *source
			if source.Skills != nil {
				resolved.Skills = make([]string, len(source.Skills))
				copy(resolved.Skills, source.Skills)
			}
			preset.Agents[j] = resolved
		}
	}
	return nil
}

func validateReferenceLocation(kind, presetID, name, ref string) error {
	validPart := func(value string) bool {
		return value != "" && value != "." && value != ".." &&
			!strings.ContainsAny(value, `/\\`) && path.Clean(value) == value
	}
	if !validPart(ref) || !validPart(name) {
		return fmt.Errorf("preset %q %s %q: reference path must use a plain preset id and name", presetID, kind, name)
	}
	return nil
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
