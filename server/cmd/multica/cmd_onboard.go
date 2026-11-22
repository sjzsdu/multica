package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/presets"
	"github.com/multica-ai/multica/server/internal/squadworkflow"
)

var loadOnboardCatalog = presets.Load

var onboardCmd = &cobra.Command{
	Use:   "onboard",
	Short: "Install bundled agent, squad, and automation presets",
	Long: `Installs pre-built agent teams into the current workspace.

Presets ship with the CLI: skills, agents, squads, and scheduled automations.
You pick what to install, and every agent is bound to the runtime you choose.

Installation is idempotent: skills, agents, squads, and autopilots that already
exist by name are reused instead of duplicated, so re-running is safe.

Examples:
  multica onboard                       # interactive
  multica onboard --list                # show bundled presets
  multica onboard --preset solo-company --all
  multica onboard --preset solo-company --all --runtime-id <uuid> --yes
  multica onboard --preset solo-company --all --dry-run`,
	Args: cobra.NoArgs,
	RunE: runOnboard,
}

func init() {
	flags := onboardCmd.Flags()
	flags.Bool("list", false, "List bundled presets and exit")
	flags.String("preset", "", "Preset ids to install (comma-separated)")
	flags.Bool("all", false, "Install every preset and item without prompting")
	flags.String("runtime-id", "", "Runtime that new agents run on (prompts when omitted)")
	flags.String("model", "", "Override the model for every agent created")
	flags.Bool("yes", false, "Skip the confirmation prompt")
	flags.Bool("dry-run", false, "Print the plan without creating anything")
	flags.Bool("no-autopilots", false, "Skip bundled scheduled automations")
}

func runOnboard(cmd *cobra.Command, _ []string) error {
	if err := requireHumanLocalCommand("onboard"); err != nil {
		return err
	}

	catalog, err := loadOnboardCatalog()
	if err != nil {
		return err
	}
	if list, _ := cmd.Flags().GetBool("list"); list {
		printPresetCatalog(catalog)
		return nil
	}

	prompt := &onboardPrompt{in: bufio.NewReader(os.Stdin)}
	all, _ := cmd.Flags().GetBool("all")
	yes, _ := cmd.Flags().GetBool("yes")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	skipAutopilots, _ := cmd.Flags().GetBool("no-autopilots")

	selectedPresets, err := choosePresets(cmd, catalog, prompt, all)
	if err != nil {
		return err
	}

	selection, err := chooseItems(selectedPresets, prompt, all || yes, skipAutopilots)
	if err != nil {
		return err
	}
	if len(selection.agents) == 0 && len(selection.squads) == 0 && len(selection.autopilots) == 0 {
		fmt.Fprintln(os.Stderr, "Nothing selected.")
		return nil
	}

	skillDefs := collectSkills(selectedPresets)
	if dryRun {
		if err := validateOnboardSelection(selection, skillDefs, symbolicOnboardAgentIDs(selection.agents)); err != nil {
			return fmt.Errorf("validate onboarding selection: %w", err)
		}
		runner := &onboardRunner{
			skillIDs: map[string]string{}, agentIDs: map[string]string{},
			squadIDs: map[string]string{}, autopilotIDs: map[string]string{}, skillDefs: skillDefs,
		}
		fmt.Fprintln(os.Stderr, "\nRuntime: dry run (no runtime lookup)")
		runner.printPlan(selection)
		fmt.Fprintln(os.Stderr, "\nDry run: existing resources were not queried; plan counts show all items as new.")
		fmt.Fprintln(os.Stderr, "Dry run: nothing was created.")
		return nil
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	workspaceID, err := requireWorkspaceID(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	runtimeID, runtimeLabel, err := chooseRuntime(cmd, client, ctx, prompt, yes)
	if err != nil {
		return err
	}

	runner := &onboardRunner{
		ctx:           ctx,
		client:        client,
		workspaceID:   workspaceID,
		runtimeID:     runtimeID,
		modelOverride: mustFlag(cmd, "model"),
		skillIDs:      map[string]string{},
		agentIDs:      map[string]string{},
		squadIDs:      map[string]string{},
		autopilotIDs:  map[string]string{},
		createdInRun:  map[string]bool{},
		skillDefs:     skillDefs,
	}
	if err := runner.loadExisting(selection); err != nil {
		return err
	}
	// Reused agents have real IDs; selected agents not created yet need symbolic IDs for policy validation.
	validationAgentIDs := symbolicOnboardAgentIDs(selection.agents)
	for name, id := range runner.agentIDs {
		validationAgentIDs[name] = id
	}
	if err := validateOnboardSelection(selection, skillDefs, validationAgentIDs); err != nil {
		return fmt.Errorf("validate onboarding selection: %w", err)
	}

	fmt.Fprintf(os.Stderr, "\nRuntime: %s\n", runtimeLabel)
	runner.printPlan(selection)

	if !yes {
		ok, err := prompt.confirm("\nProceed? [Y/n] ", true)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(os.Stderr, "Aborted.")
			return nil
		}
	}

	runner.install(selection)

	fmt.Fprintln(os.Stderr, "\n== summary ==")
	fmt.Fprintf(os.Stderr, "agents created:     %d\n", runner.createdAgents)
	fmt.Fprintf(os.Stderr, "skills created:     %d\n", runner.createdSkills)
	fmt.Fprintf(os.Stderr, "squads created:     %d\n", runner.createdSquads)
	fmt.Fprintf(os.Stderr, "autopilots created: %d\n", runner.createdAutopilots)
	for _, note := range runner.notes {
		fmt.Fprintf(os.Stderr, "note: %s\n", note)
	}
	if len(runner.failures) > 0 {
		fmt.Fprintf(os.Stderr, "failures (%d):\n", len(runner.failures))
		for _, failure := range runner.failures {
			fmt.Fprintf(os.Stderr, "  - %s\n", failure)
		}
		return fmt.Errorf("onboarding finished with %d failure(s)", len(runner.failures))
	}
	fmt.Fprintln(os.Stderr, "done")
	return nil
}

func mustFlag(cmd *cobra.Command, name string) string {
	v, _ := cmd.Flags().GetString(name)
	return v
}

// ── selection ───────────────────────────────────────────────────────────────

type onboardSelection struct {
	agents     []presets.Agent
	squads     []presets.Squad
	autopilots []presets.Autopilot
}

func choosePresets(cmd *cobra.Command, catalog presets.Catalog, prompt *onboardPrompt, all bool) ([]presets.Preset, error) {
	if spec := mustFlag(cmd, "preset"); spec != "" {
		var chosen []presets.Preset
		for _, id := range strings.Split(spec, ",") {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			preset, ok := catalog.Find(id)
			if !ok {
				return nil, fmt.Errorf("unknown preset %q (available: %s)", id, strings.Join(catalog.IDs(), ", "))
			}
			chosen = append(chosen, preset)
		}
		if len(chosen) == 0 {
			return nil, errors.New("--preset did not name any preset")
		}
		return chosen, nil
	}
	if all {
		return catalog.Presets, nil
	}

	if len(catalog.Presets) == 1 {
		fmt.Fprintf(os.Stderr, "Using preset: %s\n", catalog.Presets[0].Name)
		return catalog.Presets, nil
	}

	printPresetCatalog(catalog)
	answer, err := prompt.ask(fmt.Sprintf("\nSelect presets (1-%d, comma-separated, Enter = all): ", len(catalog.Presets)))
	if err != nil {
		return nil, err
	}
	if answer == "" || answer == "all" {
		return catalog.Presets, nil
	}
	indexes, err := parseSelection(answer, len(catalog.Presets))
	if err != nil {
		return nil, err
	}
	chosen := make([]presets.Preset, 0, len(indexes))
	for _, idx := range indexes {
		chosen = append(chosen, catalog.Presets[idx])
	}
	return chosen, nil
}

func printPresetCatalog(catalog presets.Catalog) {
	fmt.Println("Bundled presets:")
	for i, preset := range catalog.Presets {
		fmt.Printf("  %d) %s  (%s)\n", i+1, preset.Name, preset.ID)
		fmt.Printf("     %s\n", preset.Description)
		fmt.Printf("     %d agents · %d skills · %d squads · %d autopilots\n",
			len(preset.Agents), len(preset.Skills), len(preset.Squads), len(preset.Autopilots))
	}
}

func chooseItems(chosen []presets.Preset, prompt *onboardPrompt, auto bool, skipAutopilots bool) (onboardSelection, error) {
	selection := onboardSelection{}
	seenAgents := map[string]bool{}
	seenSquads := map[string]bool{}
	seenAutopilots := map[string]bool{}

	for _, preset := range chosen {
		for _, agent := range preset.Agents {
			if !seenAgents[agent.Name] {
				seenAgents[agent.Name] = true
				selection.agents = append(selection.agents, agent)
			}
		}
		for _, squad := range preset.Squads {
			if !seenSquads[squad.Name] {
				seenSquads[squad.Name] = true
				selection.squads = append(selection.squads, squad)
			}
		}
		if skipAutopilots {
			continue
		}
		for _, autopilot := range preset.Autopilots {
			if !seenAutopilots[autopilot.Title] {
				seenAutopilots[autopilot.Title] = true
				selection.autopilots = append(selection.autopilots, autopilot)
			}
		}
	}

	if auto {
		return selection, nil
	}

	type entry struct {
		kind  string
		label string
		agent presets.Agent
		squad presets.Squad
		auto  presets.Autopilot
	}
	var entries []entry
	for _, agent := range selection.agents {
		entries = append(entries, entry{kind: "agent", label: agent.Name, agent: agent})
	}
	for _, squad := range selection.squads {
		entries = append(entries, entry{kind: "squad", label: squad.Name, squad: squad})
	}
	for _, autopilot := range selection.autopilots {
		entries = append(entries, entry{kind: "autopilot", label: autopilot.Title, auto: autopilot})
	}

	fmt.Println("\nInstall which items?")
	for i, e := range entries {
		fmt.Printf("  %2d) %-9s %s\n", i+1, e.kind, e.label)
	}
	answer, err := prompt.ask(fmt.Sprintf("\nSelect items (e.g. 1,3-5; \"all\"; Enter = all): "))
	if err != nil {
		return onboardSelection{}, err
	}
	if answer == "" || strings.EqualFold(answer, "all") {
		return selection, nil
	}
	indexes, err := parseSelection(answer, len(entries))
	if err != nil {
		return onboardSelection{}, err
	}

	picked := onboardSelection{}
	pickedAgents := map[string]bool{}
	pickedSquads := map[string]bool{}
	for _, idx := range indexes {
		e := entries[idx]
		switch e.kind {
		case "agent":
			if !pickedAgents[e.agent.Name] {
				pickedAgents[e.agent.Name] = true
				picked.agents = append(picked.agents, e.agent)
			}
		case "squad":
			if !pickedSquads[e.squad.Name] {
				pickedSquads[e.squad.Name] = true
				picked.squads = append(picked.squads, e.squad)
			}
		case "autopilot":
			picked.autopilots = append(picked.autopilots, e.auto)
		}
	}

	// Squads and autopilots need their agents; pull in members and assignees
	// that the user did not select explicitly.
	for _, squad := range picked.squads {
		for _, name := range squadAgentNames(squad) {
			if !pickedAgents[name] {
				if agent, ok := findAgent(selection.agents, name); ok {
					pickedAgents[name] = true
					picked.agents = append(picked.agents, agent)
				}
			}
		}
	}
	for _, autopilot := range picked.autopilots {
		if autopilot.Agent == "" || pickedAgents[autopilot.Agent] {
			continue
		}
		if agent, ok := findAgent(selection.agents, autopilot.Agent); ok {
			pickedAgents[autopilot.Agent] = true
			picked.agents = append(picked.agents, agent)
		}
	}
	return picked, nil
}

func squadAgentNames(squad presets.Squad) []string {
	seen := map[string]bool{}
	names := make([]string, 0, len(squad.Members)+1)
	add := func(name string) {
		if name != "" && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	add(squad.Leader)
	for _, member := range squad.Members {
		add(member.Name)
	}
	return names
}

func findAgent(agents []presets.Agent, name string) (presets.Agent, bool) {
	for _, agent := range agents {
		if agent.Name == name {
			return agent, true
		}
	}
	return presets.Agent{}, false
}

// parseSelection accepts "1,3-5" style input and returns zero-based indexes.
func parseSelection(input string, size int) ([]int, error) {
	seen := map[int]bool{}
	var indexes []int
	for _, part := range strings.Split(input, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "-") {
			bounds := strings.SplitN(part, "-", 2)
			start, err1 := strconv.Atoi(strings.TrimSpace(bounds[0]))
			end, err2 := strconv.Atoi(strings.TrimSpace(bounds[1]))
			if err1 != nil || err2 != nil || start > end {
				return nil, fmt.Errorf("invalid range %q", part)
			}
			for n := start; n <= end; n++ {
				if n < 1 || n > size {
					return nil, fmt.Errorf("selection %d is out of range 1-%d", n, size)
				}
				if !seen[n-1] {
					seen[n-1] = true
					indexes = append(indexes, n-1)
				}
			}
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 1 || n > size {
			return nil, fmt.Errorf("selection %q is out of range 1-%d", part, size)
		}
		if !seen[n-1] {
			seen[n-1] = true
			indexes = append(indexes, n-1)
		}
	}
	if len(indexes) == 0 {
		return nil, errors.New("no items selected")
	}
	return indexes, nil
}

// ── runtime ─────────────────────────────────────────────────────────────────

func chooseRuntime(cmd *cobra.Command, client *cli.APIClient, ctx context.Context, prompt *onboardPrompt, yes bool) (string, string, error) {
	if id := mustFlag(cmd, "runtime-id"); id != "" {
		return id, id, nil
	}

	var runtimes []map[string]any
	if err := client.GetJSON(ctx, "/api/runtimes", &runtimes); err != nil {
		return "", "", fmt.Errorf("list runtimes: %w", err)
	}
	if len(runtimes) == 0 {
		return "", "", errors.New("no runtime registered in this workspace: start 'multica daemon' on the target machine first")
	}
	if len(runtimes) == 1 {
		id := strVal(runtimes[0], "id")
		return id, runtimeLabel(runtimes[0]), nil
	}
	if yes {
		return "", "", errors.New("multiple runtimes are available; pass --runtime-id to choose one")
	}

	fmt.Println("\nRuntimes:")
	for i, runtime := range runtimes {
		fmt.Printf("  %d) %s\n", i+1, runtimeLabel(runtime))
	}
	answer, err := prompt.ask("Select runtime [1]: ")
	if err != nil {
		return "", "", err
	}
	if answer == "" {
		answer = "1"
	}
	n, err := strconv.Atoi(answer)
	if err != nil || n < 1 || n > len(runtimes) {
		return "", "", fmt.Errorf("invalid runtime selection %q", answer)
	}
	picked := runtimes[n-1]
	return strVal(picked, "id"), runtimeLabel(picked), nil
}

func runtimeLabel(runtime map[string]any) string {
	name := strVal(runtime, "name")
	if name == "" {
		name = "runtime"
	}
	return fmt.Sprintf("%s (%s)  %s", name, strVal(runtime, "id"), strVal(runtime, "status"))
}

// ── prompt ──────────────────────────────────────────────────────────────────

type onboardPrompt struct {
	in *bufio.Reader
}

func (p *onboardPrompt) ask(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	line, err := p.in.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if err != nil && line == "" {
		return "", errors.New("stdin closed; pass --all for non-interactive use")
	}
	return strings.TrimSpace(line), nil
}

func (p *onboardPrompt) confirm(prompt string, def bool) (bool, error) {
	answer, err := p.ask(prompt)
	if err != nil {
		return false, err
	}
	switch strings.ToLower(answer) {
	case "":
		return def, nil
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

// ── install ─────────────────────────────────────────────────────────────────

type onboardRunner struct {
	ctx           context.Context
	client        *cli.APIClient
	workspaceID   string
	runtimeID     string
	modelOverride string

	skillIDs     map[string]string
	agentIDs     map[string]string
	squadIDs     map[string]string
	autopilotIDs map[string]string
	skillDefs    map[string]presets.Skill
	createdInRun map[string]bool

	notes    []string
	failures []string

	createdAgents     int
	createdSkills     int
	createdSquads     int
	createdAutopilots int
}

func (r *onboardRunner) loadExisting(selection onboardSelection) error {
	var skills []map[string]any
	if err := r.client.GetJSON(r.ctx, "/api/skills", &skills); err != nil {
		return fmt.Errorf("list skills: %w", err)
	}
	for _, skill := range skills {
		r.skillIDs[strVal(skill, "name")] = strVal(skill, "id")
	}

	var agents []map[string]any
	path := "/api/agents?" + url.Values{"workspace_id": {r.workspaceID}}.Encode()
	if err := r.client.GetJSON(r.ctx, path, &agents); err != nil {
		return fmt.Errorf("list agents: %w", err)
	}
	for _, agent := range agents {
		r.agentIDs[strVal(agent, "name")] = strVal(agent, "id")
	}

	var squads []map[string]any
	if err := r.client.GetJSON(r.ctx, "/api/squads", &squads); err != nil {
		return fmt.Errorf("list squads: %w", err)
	}
	for _, squad := range squads {
		r.squadIDs[strVal(squad, "name")] = strVal(squad, "id")
	}

	if len(selection.autopilots) == 0 {
		return nil
	}
	var resp struct {
		Autopilots []map[string]any `json:"autopilots"`
	}
	if err := r.client.GetJSON(r.ctx, "/api/autopilots", &resp); err != nil {
		return fmt.Errorf("list autopilots: %w", err)
	}
	for _, autopilot := range resp.Autopilots {
		r.autopilotIDs[strVal(autopilot, "title")] = strVal(autopilot, "id")
	}
	return nil
}

func (r *onboardRunner) printPlan(selection onboardSelection) {
	newAgents, existingAgents := countExisting(selection.agents, func(a presets.Agent) string { return a.Name }, r.agentIDs)
	newSquads, existingSquads := countExisting(selection.squads, func(s presets.Squad) string { return s.Name }, r.squadIDs)
	newAutopilots, existingAutopilots := countExisting(selection.autopilots, func(a presets.Autopilot) string { return a.Title }, r.autopilotIDs)

	skillNames := r.skillNamesFor(selection)
	newSkills, existingSkills := countExisting(skillNames, func(name string) string { return name }, r.skillIDs)

	fmt.Fprintf(os.Stderr, "\nPlan:")
	fmt.Fprintf(os.Stderr, "\n  agents     %d new, %d existing", newAgents, existingAgents)
	fmt.Fprintf(os.Stderr, "\n  skills     %d new, %d existing", newSkills, existingSkills)
	fmt.Fprintf(os.Stderr, "\n  squads     %d new, %d existing", newSquads, existingSquads)
	fmt.Fprintf(os.Stderr, "\n  autopilots %d new, %d existing\n", newAutopilots, existingAutopilots)
}

func countExisting[T any](items []T, nameOf func(T) string, existing map[string]string) (int, int) {
	fresh, reused := 0, 0
	for _, item := range items {
		if _, ok := existing[nameOf(item)]; ok {
			reused++
		} else {
			fresh++
		}
	}
	return fresh, reused
}

func (r *onboardRunner) skillNamesFor(selection onboardSelection) []string {
	seen := map[string]bool{}
	var names []string
	for _, agent := range selection.agents {
		for _, name := range agent.Skills {
			if !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	sort.Strings(names)
	return names
}

func (r *onboardRunner) lookupMention(kind, name string) (string, bool) {
	switch kind {
	case "agent":
		id, ok := r.agentIDs[name]
		return id, ok
	case "squad":
		id, ok := r.squadIDs[name]
		return id, ok
	default:
		return "", false
	}
}

func (r *onboardRunner) install(selection onboardSelection) {
	fmt.Fprintln(os.Stderr, "\n== agents ==")
	for _, agent := range selection.agents {
		r.ensureAgent(agent)
	}

	// Squads come before skills so skill bodies can link to them.
	fmt.Fprintln(os.Stderr, "\n== squads ==")
	for _, squad := range selection.squads {
		r.ensureSquad(squad)
	}

	fmt.Fprintln(os.Stderr, "\n== skills ==")
	for _, skill := range r.skillsFor(selection) {
		r.ensureSkill(skill)
	}

	fmt.Fprintln(os.Stderr, "\n== skill assignments ==")
	for _, agent := range selection.agents {
		r.assignSkills(agent)
	}

	fmt.Fprintln(os.Stderr, "\n== agent instructions ==")
	for _, agent := range selection.agents {
		r.writeInstructions(agent)
	}

	fmt.Fprintln(os.Stderr, "\n== autopilots ==")
	for _, autopilot := range selection.autopilots {
		r.ensureAutopilot(autopilot)
	}
}

func (r *onboardRunner) skillsFor(selection onboardSelection) []presets.Skill {
	seen := map[string]bool{}
	var skills []presets.Skill
	for _, agent := range selection.agents {
		for _, name := range agent.Skills {
			if seen[name] {
				continue
			}
			seen[name] = true
			skill, ok := r.skillDefs[name]
			if !ok {
				r.notes = append(r.notes, fmt.Sprintf("skill %q is referenced by %q but missing from the preset", name, agent.Name))
				continue
			}
			skills = append(skills, skill)
		}
	}
	return skills
}

// collectSkills indexes every skill a preset can install by name.
func collectSkills(chosen []presets.Preset) map[string]presets.Skill {
	skills := map[string]presets.Skill{}
	for _, preset := range chosen {
		for _, skill := range preset.Skills {
			if _, ok := skills[skill.Name]; !ok {
				skills[skill.Name] = skill
			}
		}
	}
	return skills
}

var onboardAgentIDPlaceholder = regexp.MustCompile(`^\{\{agent-id:([^{}]+)\}\}$`)

func symbolicOnboardAgentIDs(agents []presets.Agent) map[string]string {
	ids := make(map[string]string, len(agents))
	for _, agent := range agents {
		ids[agent.Name] = uuid.NewSHA1(uuid.NameSpaceOID, []byte("multica/onboard/agent/"+agent.Name)).String()
	}
	return ids
}

func resolveOnboardWorkflowAgentIDs(workflow map[string]any, agentIDs map[string]string) (map[string]any, error) {
	var resolve func(any) (any, error)
	resolve = func(value any) (any, error) {
		switch typed := value.(type) {
		case string:
			match := onboardAgentIDPlaceholder.FindStringSubmatch(typed)
			if match == nil {
				return typed, nil
			}
			id, ok := agentIDs[match[1]]
			if !ok || id == "" {
				return nil, fmt.Errorf("workflow references unknown agent %q", match[1])
			}
			return id, nil
		case map[string]any:
			resolved := make(map[string]any, len(typed))
			for key, item := range typed {
				value, err := resolve(item)
				if err != nil {
					return nil, err
				}
				resolved[key] = value
			}
			return resolved, nil
		case []any:
			resolved := make([]any, len(typed))
			for i, item := range typed {
				value, err := resolve(item)
				if err != nil {
					return nil, err
				}
				resolved[i] = value
			}
			return resolved, nil
		default:
			return value, nil
		}
	}
	resolved, err := resolve(workflow)
	if err != nil {
		return nil, err
	}
	return resolved.(map[string]any), nil
}

func validateOnboardSelection(selection onboardSelection, skillDefs map[string]presets.Skill, agentIDs map[string]string) error {
	for _, agent := range selection.agents {
		for _, name := range agent.Skills {
			if _, ok := skillDefs[name]; !ok {
				return fmt.Errorf("agent %q references missing skill %q", agent.Name, name)
			}
		}
	}
	for _, squad := range selection.squads {
		for _, name := range squadAgentNames(squad) {
			if _, ok := agentIDs[name]; !ok {
				return fmt.Errorf("squad %q references missing agent %q", squad.Name, name)
			}
		}
		if len(squad.Workflow) == 0 {
			continue
		}
		workflow, err := resolveOnboardWorkflowAgentIDs(squad.Workflow, agentIDs)
		if err != nil {
			return fmt.Errorf("squad %q: %w", squad.Name, err)
		}
		raw, err := json.Marshal(workflow)
		if err != nil {
			return fmt.Errorf("squad %q workflow: %w", squad.Name, err)
		}
		if _, err := squadworkflow.Parse(raw); err != nil {
			return fmt.Errorf("squad %q workflow: %w", squad.Name, err)
		}
	}
	for _, autopilot := range selection.autopilots {
		if autopilot.Agent != "" {
			if _, ok := agentIDs[autopilot.Agent]; !ok {
				return fmt.Errorf("autopilot %q references missing agent %q", autopilot.Title, autopilot.Agent)
			}
		}
	}
	return nil
}

func (r *onboardRunner) ensureAgent(agent presets.Agent) {
	if id, ok := r.agentIDs[agent.Name]; ok {
		r.notes = append(r.notes, fmt.Sprintf("agent %q already exists (%s), left unchanged", agent.Name, id))
		fmt.Fprintf(os.Stderr, "  = %s\n", agent.Name)
		return
	}

	body := map[string]any{
		"name":                 agent.Name,
		"runtime_id":           r.runtimeID,
		"max_concurrent_tasks": agent.MaxConcurrentTasks,
	}
	if agent.Description != "" {
		body["description"] = agent.Description
	}
	if agent.Instructions != "" {
		// Forward references to agents/squads created later stay as
		// placeholders here and are rewritten by writeInstructions at the end.
		instructions, _ := presets.ResolveMentions(agent.Instructions, r.lookupMention)
		body["instructions"] = instructions
	}
	if agent.Permission == "workspace" {
		body["permission_mode"] = "public_to"
		body["invocation_targets"] = []map[string]any{{"target_type": "workspace"}}
	}
	model := agent.Model
	if r.modelOverride != "" {
		model = r.modelOverride
	}
	if model != "" {
		body["model"] = model
	}

	var result map[string]any
	err := r.client.PostJSON(r.ctx, "/api/agents", body, &result)
	if err != nil && model != "" {
		// Model catalogs are runtime-specific; fall back to the runtime default
		// instead of failing the whole install.
		delete(body, "model")
		var retry map[string]any
		if retryErr := r.client.PostJSON(r.ctx, "/api/agents", body, &retry); retryErr == nil {
			r.notes = append(r.notes, fmt.Sprintf("agent %q: model %q rejected, used the runtime default", agent.Name, model))
			result = retry
			err = nil
		}
	}
	if err != nil {
		r.failures = append(r.failures, fmt.Sprintf("agent %q: %v", agent.Name, err))
		fmt.Fprintf(os.Stderr, "  ! %s: failed\n", agent.Name)
		return
	}

	id := strVal(result, "id")
	r.agentIDs[agent.Name] = id
	r.createdInRun[agent.Name] = true
	r.createdAgents++
	fmt.Fprintf(os.Stderr, "  + %s (%s)\n", agent.Name, id)
}

func (r *onboardRunner) ensureSkill(skill presets.Skill) {
	if id, ok := r.skillIDs[skill.Name]; ok {
		fmt.Fprintf(os.Stderr, "  = %s\n", skill.Name)
		r.skillIDs[skill.Name] = id
		return
	}
	content, missing := presets.ResolveMentions(skill.Content, r.lookupMention)
	for _, placeholder := range missing {
		r.notes = append(r.notes, fmt.Sprintf("skill %q: unresolved %s", skill.Name, placeholder))
	}

	body := map[string]any{"name": skill.Name, "content": content}
	if skill.Description != "" {
		body["description"] = skill.Description
	}

	var result map[string]any
	if err := r.client.PostJSON(r.ctx, "/api/skills", body, &result); err != nil {
		r.failures = append(r.failures, fmt.Sprintf("skill %q: %v", skill.Name, err))
		fmt.Fprintf(os.Stderr, "  ! %s: failed\n", skill.Name)
		return
	}
	r.skillIDs[skill.Name] = strVal(result, "id")
	r.createdSkills++
	fmt.Fprintf(os.Stderr, "  + %s\n", skill.Name)
}

func (r *onboardRunner) assignSkills(agent presets.Agent) {
	agentID, ok := r.agentIDs[agent.Name]
	if !ok {
		return
	}
	// Existing agents keep their configuration; only agents created by this run
	// get the preset's skill assignments and final instructions.
	if !r.createdInRun[agent.Name] {
		return
	}

	var ids []string
	for _, name := range agent.Skills {
		id, ok := r.skillIDs[name]
		if !ok {
			r.notes = append(r.notes, fmt.Sprintf("agent %q: skill %q not found in the preset", agent.Name, name))
			continue
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return
	}
	var result map[string]any
	if err := r.client.PutJSON(r.ctx, "/api/agents/"+agentID+"/skills", map[string]any{"skill_ids": ids}, &result); err != nil {
		r.failures = append(r.failures, fmt.Sprintf("assign skills to %q: %v", agent.Name, err))
		fmt.Fprintf(os.Stderr, "  ! %s: skill assignment failed\n", agent.Name)
		return
	}
	fmt.Fprintf(os.Stderr, "  ~ %s: %d skill(s)\n", agent.Name, len(ids))
}

func (r *onboardRunner) ensureSquad(squad presets.Squad) {
	if id, ok := r.squadIDs[squad.Name]; ok {
		r.notes = append(r.notes, fmt.Sprintf("squad %q already exists (%s), left unchanged", squad.Name, id))
		fmt.Fprintf(os.Stderr, "  = %s\n", squad.Name)
		return
	}
	leaderID, ok := r.agentIDs[squad.Leader]
	if !ok {
		r.failures = append(r.failures, fmt.Sprintf("squad %q: leader %q is not part of the selection", squad.Name, squad.Leader))
		fmt.Fprintf(os.Stderr, "  ! %s: leader missing\n", squad.Name)
		return
	}
	body := map[string]any{"name": squad.Name, "leader_id": leaderID}
	if squad.Description != "" {
		body["description"] = squad.Description
	}
	if len(squad.Workflow) > 0 {
		workflow, err := resolveOnboardWorkflowAgentIDs(squad.Workflow, r.agentIDs)
		if err != nil {
			r.failures = append(r.failures, fmt.Sprintf("squad %q workflow: %v", squad.Name, err))
			fmt.Fprintf(os.Stderr, "  ! %s: invalid workflow\n", squad.Name)
			return
		}
		body["workflow"] = workflow
	}

	var result map[string]any
	if err := r.client.PostJSON(r.ctx, "/api/squads", body, &result); err != nil {
		r.failures = append(r.failures, fmt.Sprintf("squad %q: %v", squad.Name, err))
		fmt.Fprintf(os.Stderr, "  ! %s: failed\n", squad.Name)
		return
	}
	squadID := strVal(result, "id")
	r.squadIDs[squad.Name] = squadID
	r.createdSquads++
	fmt.Fprintf(os.Stderr, "  + %s (%s)\n", squad.Name, squadID)

	if squad.Instructions != "" {
		instructions, missing := presets.ResolveMentions(squad.Instructions, r.lookupMention)
		for _, placeholder := range missing {
			r.notes = append(r.notes, fmt.Sprintf("squad %q: unresolved %s", squad.Name, placeholder))
		}
		var patched map[string]any
		if err := r.client.PutJSON(r.ctx, "/api/squads/"+squadID, map[string]any{"instructions": instructions}, &patched); err != nil {
			r.failures = append(r.failures, fmt.Sprintf("squad %q instructions: %v", squad.Name, err))
		}
	}

	for _, member := range squad.Members {
		if member.Name == "" || member.Name == squad.Leader {
			continue
		}
		memberID, ok := r.agentIDs[member.Name]
		if !ok {
			r.notes = append(r.notes, fmt.Sprintf("squad %q: member %q is not part of the selection", squad.Name, member.Name))
			continue
		}
		role := member.Role
		if role == "" {
			role = "member"
		}
		body := map[string]any{"member_type": "agent", "member_id": memberID, "role": role}
		var added map[string]any
		if err := r.client.PostJSON(r.ctx, "/api/squads/"+squadID+"/members", body, &added); err != nil {
			r.failures = append(r.failures, fmt.Sprintf("squad %q member %q: %v", squad.Name, member.Name, err))
		}
	}
}

func (r *onboardRunner) writeInstructions(agent presets.Agent) {
	if agent.Instructions == "" {
		return
	}
	agentID, ok := r.agentIDs[agent.Name]
	if !ok {
		return
	}
	if !r.createdInRun[agent.Name] {
		return
	}
	instructions, missing := presets.ResolveMentions(agent.Instructions, r.lookupMention)
	for _, placeholder := range missing {
		r.notes = append(r.notes, fmt.Sprintf("agent %q: unresolved %s", agent.Name, placeholder))
	}
	var result map[string]any
	if err := r.client.PutJSON(r.ctx, "/api/agents/"+agentID, map[string]any{"instructions": instructions}, &result); err != nil {
		r.failures = append(r.failures, fmt.Sprintf("agent %q instructions: %v", agent.Name, err))
	}
}

func (r *onboardRunner) ensureAutopilot(autopilot presets.Autopilot) {
	if id, ok := r.autopilotIDs[autopilot.Title]; ok {
		r.notes = append(r.notes, fmt.Sprintf("autopilot %q already exists (%s), left unchanged", autopilot.Title, id))
		fmt.Fprintf(os.Stderr, "  = %s\n", autopilot.Title)
		return
	}
	agentID, ok := r.agentIDs[autopilot.Agent]
	if !ok {
		r.failures = append(r.failures, fmt.Sprintf("autopilot %q: agent %q is not part of the selection", autopilot.Title, autopilot.Agent))
		fmt.Fprintf(os.Stderr, "  ! %s: agent missing\n", autopilot.Title)
		return
	}
	mode := autopilot.Mode
	if mode == "" {
		mode = "run_only"
	}
	body := map[string]any{
		"title":          autopilot.Title,
		"assignee_id":    agentID,
		"execution_mode": mode,
	}
	if autopilot.Description != "" {
		body["description"] = autopilot.Description
	}
	var result map[string]any
	if err := r.client.PostJSON(r.ctx, "/api/autopilots", body, &result); err != nil {
		r.failures = append(r.failures, fmt.Sprintf("autopilot %q: %v", autopilot.Title, err))
		fmt.Fprintf(os.Stderr, "  ! %s: failed\n", autopilot.Title)
		return
	}
	autopilotID := strVal(result, "id")
	r.autopilotIDs[autopilot.Title] = autopilotID
	r.createdAutopilots++
	fmt.Fprintf(os.Stderr, "  + %s (%s)\n", autopilot.Title, autopilotID)

	for _, trigger := range autopilot.Triggers {
		if trigger.Cron == "" {
			continue
		}
		triggerBody := map[string]any{
			"kind":            "schedule",
			"cron_expression": trigger.Cron,
		}
		if trigger.Timezone != "" {
			triggerBody["timezone"] = trigger.Timezone
		}
		if trigger.Label != "" {
			triggerBody["label"] = trigger.Label
		}
		var created map[string]any
		if err := r.client.PostJSON(r.ctx, "/api/autopilots/"+autopilotID+"/triggers", triggerBody, &created); err != nil {
			r.failures = append(r.failures, fmt.Sprintf("autopilot %q trigger %q: %v", autopilot.Title, trigger.Cron, err))
		}
	}
}
