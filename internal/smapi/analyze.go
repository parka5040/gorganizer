package smapi

import (
	"fmt"
	"sort"
	"strings"
)

type IssueKind uint8

const (
	IssueNone IssueKind = iota
	IssueMissing
	IssueDisabled
	IssueVersionTooLow
	IssueDuplicateID
	IssueInvalidManifest
	IssueNeedsNewerLoader
	IssueNeedsNewerGame
	IssueCircular
	IssueFolderCollision
	IssueDependencyFailed
)

type Provider struct {
	Folder          string
	Provider        string
	Manifest        *Manifest
	ParseErr        error
	EntryDllPresent *bool
}

type ProjectedFolder struct {
	Provider
	Competing []Provider
}

type AnalyzeInput struct {
	Active        []ProjectedFolder
	Disabled      []Provider
	LoaderVersion *Version
	GameVersion   *Version
	BundledIDs    []string
}

type Issue struct {
	Kind            IssueKind
	TargetID        string
	RequiredVersion string
	FoundVersion    string
	Providers       []string
	Detail          string
}

type ComponentResult struct {
	Folder   string
	Provider string
	UniqueID string
	Name     string
	Version  string
	Kind     ModKind
	Bundled  bool
	Failed   bool
	Issues   []Issue
}

type MissingDependency struct {
	UniqueID          string
	MinimumVersion    string
	RequiredBy        []string
	DisabledProviders []string
}

type Report struct {
	Components []ComponentResult
	Missing    []MissingDependency
}

type dependencyState uint8

const (
	stateQueued dependencyState = iota
	stateChecking
	stateSorted
	stateFailed
)

type failReason uint8

const (
	failNone failReason = iota
	failInvalidManifest
	failIncompatible
	failDuplicate
	failMissingDependencies
)

type analysisNode struct {
	folder ProjectedFolder
	result ComponentResult
	state  dependencyState
	reason failReason
}

type resolvedDependency struct {
	dep   Dependency
	found *analysisNode
}

type analyzer struct {
	nodes    []*analysisNode
	byID     map[string]*analysisNode
	disabled []Provider
}

// Analyze ports ModResolver.ValidateManifests and ProcessDependencies over a projected SMAPI Mods folder.
func Analyze(in AnalyzeInput) Report {
	a := newAnalyzer(in)
	a.validateManifests(in.LoaderVersion, in.GameVersion)
	a.markDuplicates()
	a.markFolderCollisions()
	for _, node := range a.nodes {
		a.processDependencies(node, nil)
	}
	return a.report()
}

// newAnalyzer builds one sorted analysis node per active folder and indexes them by mod ID.
func newAnalyzer(in AnalyzeInput) *analyzer {
	a := &analyzer{byID: map[string]*analysisNode{}, disabled: in.Disabled}
	for _, folder := range in.Active {
		node := &analysisNode{folder: folder}
		node.result.Folder = folder.Folder
		node.result.Provider = folder.Provider.Provider
		if m := folder.Manifest; m != nil {
			node.result.UniqueID = m.UniqueID
			node.result.Name = m.Name
			if !m.Version.IsZero() {
				node.result.Version = m.Version.String()
			}
		}
		node.result.Kind = folder.Manifest.Kind()
		node.result.Bundled = containsID(in.BundledIDs, node.result.UniqueID)
		a.nodes = append(a.nodes, node)
	}
	sort.SliceStable(a.nodes, func(i, j int) bool {
		if c := compareFold(a.nodes[i].result.Folder, a.nodes[j].result.Folder); c != 0 {
			return c < 0
		}
		return compareFold(a.nodes[i].result.Provider, a.nodes[j].result.Provider) < 0
	})
	for _, node := range a.nodes {
		key := idKey(node.result.UniqueID)
		if node.folder.Manifest == nil || key == "" {
			continue
		}
		if _, exists := a.byID[key]; !exists {
			a.byID[key] = node
		}
	}
	return a
}

// validateManifests ports the per-mod checks of ModResolver.ValidateManifests, stopping at the first failure per mod.
func (a *analyzer) validateManifests(loaderVersion, gameVersion *Version) {
	for _, node := range a.nodes {
		m := node.folder.Manifest
		switch {
		case node.folder.ParseErr != nil:
			node.fail(failInvalidManifest, Issue{Kind: IssueInvalidManifest, Detail: node.folder.ParseErr.Error()})
		case m == nil:
			node.fail(failInvalidManifest, Issue{Kind: IssueInvalidManifest, Detail: "manifest is missing."})
		case loaderVersion != nil && m.MinimumApiVersion != nil && m.MinimumApiVersion.IsNewerThan(*loaderVersion):
			node.fail(failIncompatible, Issue{Kind: IssueNeedsNewerLoader, RequiredVersion: m.MinimumApiVersion.String(), FoundVersion: loaderVersion.String()})
		case gameVersion != nil && m.MinimumGameVersion != nil && m.MinimumGameVersion.IsNewerThan(*gameVersion):
			node.fail(failIncompatible, Issue{Kind: IssueNeedsNewerGame, RequiredVersion: m.MinimumGameVersion.String(), FoundVersion: gameVersion.String()})
		case m.validationProblem() != "":
			node.fail(failInvalidManifest, Issue{Kind: IssueInvalidManifest, Detail: m.validationProblem()})
		case node.folder.entryDllMissing():
			node.fail(failInvalidManifest, Issue{Kind: IssueInvalidManifest, Detail: fmt.Sprintf("its DLL '%s' doesn't exist.", m.EntryDll)})
		}
	}
}

// markDuplicates ports the unique-ID check of ModResolver.ValidateManifests.
func (a *analyzer) markDuplicates() {
	groups := map[string][]*analysisNode{}
	var order []string
	for _, node := range a.nodes {
		key := idKey(node.result.UniqueID)
		if node.folder.Manifest == nil || key == "" {
			continue
		}
		if _, exists := groups[key]; !exists {
			order = append(order, key)
		}
		groups[key] = append(groups[key], node)
	}
	for _, key := range order {
		group := groups[key]
		if len(group) < 2 {
			continue
		}
		folders := make([]string, 0, len(group))
		for _, member := range group {
			folders = append(folders, member.result.Folder)
		}
		sortFold(folders)
		for _, member := range group {
			if member.state == stateFailed && member.reason != failInvalidManifest && member.reason != failMissingDependencies {
				continue
			}
			var others []string
			for _, other := range group {
				if other != member {
					others = append(others, other.label())
				}
			}
			member.fail(failDuplicate, Issue{Kind: IssueDuplicateID, TargetID: member.result.UniqueID, Providers: others, Detail: strings.Join(folders, ", ")})
		}
	}
}

// markFolderCollisions warns when a shadowed provider of the same folder carries a different mod ID.
func (a *analyzer) markFolderCollisions() {
	for _, node := range a.nodes {
		var providers, ids []string
		for _, competitor := range node.folder.Competing {
			if competitor.Manifest == nil || SameID(competitor.Manifest.UniqueID, node.result.UniqueID) {
				continue
			}
			providers = append(providers, competitor.label())
			ids = append(ids, competitor.Manifest.UniqueID)
		}
		if len(providers) != 0 {
			node.warn(Issue{Kind: IssueFolderCollision, Providers: providers, Detail: strings.Join(uniqueSortedFold(ids), ", ")})
		}
	}
}

// processDependencies ports the recursive ModResolver.ProcessDependencies state machine for one mod.
func (a *analyzer) processDependencies(node *analysisNode, chain []*analysisNode) dependencyState {
	if node.state != stateQueued {
		return node.state
	}
	var deps []resolvedDependency
	for _, dep := range node.folder.Manifest.allDependencies() {
		deps = append(deps, resolvedDependency{dep: dep, found: a.byID[idKey(dep.UniqueID)]})
	}
	if len(deps) == 0 {
		node.state = stateSorted
		return node.state
	}
	missing := false
	for _, entry := range deps {
		if entry.dep.IsRequired && entry.found == nil {
			issue := Issue{Kind: IssueMissing, TargetID: entry.dep.UniqueID}
			if entry.dep.MinimumVersion != nil {
				issue.RequiredVersion = entry.dep.MinimumVersion.String()
			}
			node.mergeRequirement(issue)
			missing = true
		}
	}
	if missing {
		for i := range node.result.Issues {
			if node.result.Issues[i].Kind == IssueMissing {
				a.classifyMissing(&node.result.Issues[i])
			}
		}
		node.fail(failMissingDependencies)
		return node.state
	}
	tooLow := false
	for _, entry := range deps {
		if entry.found == nil || entry.dep.MinimumVersion == nil || !entry.dep.MinimumVersion.IsNewerThan(entry.found.folder.Manifest.Version) {
			continue
		}
		node.mergeRequirement(Issue{Kind: IssueVersionTooLow, TargetID: entry.dep.UniqueID, RequiredVersion: entry.dep.MinimumVersion.String(), FoundVersion: entry.found.result.Version, Providers: []string{entry.found.label()}})
		tooLow = true
	}
	if tooLow {
		node.fail(failMissingDependencies)
		return node.state
	}
	node.state = stateChecking
	subchain := append(chain[:len(chain):len(chain)], node)
	for _, entry := range deps {
		required := entry.found
		if required == nil {
			continue
		}
		if required.state == stateChecking {
			node.fail(failMissingDependencies, Issue{Kind: IssueCircular, TargetID: entry.dep.UniqueID, Providers: []string{required.label()}, Detail: chainText(append(subchain, required))})
			return node.state
		}
		status := a.processDependencies(required, subchain)
		if status == stateFailed && entry.dep.IsRequired {
			node.fail(failMissingDependencies, Issue{Kind: IssueDependencyFailed, TargetID: entry.dep.UniqueID, Providers: []string{required.label()}})
			return node.state
		}
	}
	node.state = stateSorted
	return node.state
}

// classifyMissing turns a Missing issue into a Disabled issue when a disabled provider could satisfy it once enabled.
func (a *analyzer) classifyMissing(issue *Issue) {
	var suitable, unsuitable []string
	for _, provider := range a.disabled {
		if provider.Manifest == nil || idKey(provider.Manifest.UniqueID) == "" || !SameID(provider.Manifest.UniqueID, issue.TargetID) {
			continue
		}
		if provider.satisfies(issue.RequiredVersion) {
			suitable = append(suitable, provider.label())
		} else {
			unsuitable = append(unsuitable, provider.label())
		}
	}
	switch {
	case len(suitable) != 0:
		issue.Kind = IssueDisabled
		issue.Providers = suitable
	case len(unsuitable) != 0:
		issue.Detail = "disabled providers that can't satisfy it: " + strings.Join(uniqueSortedFold(unsuitable), ", ")
	}
}

// satisfies reports whether a disabled provider has a valid manifest whose version meets minimum, which may be empty.
func (p Provider) satisfies(minimum string) bool {
	if p.ParseErr != nil || p.Manifest.validationProblem() != "" || p.entryDllMissing() {
		return false
	}
	if minimum == "" {
		return true
	}
	required, err := ParseVersion(minimum, true)
	return err == nil && !required.IsNewerThan(p.Manifest.Version)
}

// entryDllMissing reports whether the provider's code mod DLL was checked and found absent.
func (p Provider) entryDllMissing() bool {
	return p.Manifest != nil && p.Manifest.EntryDll != "" && p.EntryDllPresent != nil && !*p.EntryDllPresent
}

// report assembles the deterministic analysis result.
func (a *analyzer) report() Report {
	report := Report{Components: make([]ComponentResult, 0, len(a.nodes))}
	for _, node := range a.nodes {
		result := node.result
		for i := range result.Issues {
			result.Issues[i].Providers = uniqueSortedFold(result.Issues[i].Providers)
		}
		sort.SliceStable(result.Issues, func(i, j int) bool {
			if result.Issues[i].Kind != result.Issues[j].Kind {
				return result.Issues[i].Kind < result.Issues[j].Kind
			}
			return compareFold(result.Issues[i].TargetID, result.Issues[j].TargetID) < 0
		})
		report.Components = append(report.Components, result)
	}
	report.Missing = aggregateMissing(report.Components)
	return report
}

// aggregateMissing merges every Missing and Disabled issue into one entry per dependency ID, keeping only providers that satisfy every requirer.
func aggregateMissing(components []ComponentResult) []MissingDependency {
	type aggregate struct {
		item    MissingDependency
		minimum *Version
	}
	byID := map[string]*aggregate{}
	var order []string
	for _, component := range components {
		requiredBy := component.UniqueID
		if strings.TrimSpace(requiredBy) == "" {
			requiredBy = component.Folder
		}
		for _, issue := range component.Issues {
			if issue.Kind != IssueMissing && issue.Kind != IssueDisabled {
				continue
			}
			key := idKey(issue.TargetID)
			entry := byID[key]
			if entry == nil {
				entry = &aggregate{item: MissingDependency{UniqueID: issue.TargetID, DisabledProviders: append([]string(nil), issue.Providers...)}}
				byID[key] = entry
				order = append(order, key)
			} else {
				entry.item.DisabledProviders = intersectFold(entry.item.DisabledProviders, issue.Providers)
			}
			entry.item.RequiredBy = append(entry.item.RequiredBy, requiredBy)
			if minimum, err := ParseVersion(issue.RequiredVersion, true); err == nil && (entry.minimum == nil || minimum.IsNewerThan(*entry.minimum)) {
				entry.minimum = &minimum
			}
		}
	}
	result := make([]MissingDependency, 0, len(order))
	for _, key := range order {
		entry := byID[key]
		entry.item.RequiredBy = uniqueSortedFold(entry.item.RequiredBy)
		entry.item.DisabledProviders = uniqueSortedFold(entry.item.DisabledProviders)
		if entry.minimum != nil {
			entry.item.MinimumVersion = entry.minimum.String()
		}
		result = append(result, entry.item)
	}
	sort.SliceStable(result, func(i, j int) bool { return compareFold(result[i].UniqueID, result[j].UniqueID) < 0 })
	return result
}

// intersectFold returns the values of a that also appear in b, ignoring case.
func intersectFold(a, b []string) []string {
	var result []string
	for _, value := range a {
		for _, other := range b {
			if strings.EqualFold(value, other) {
				result = append(result, value)
				break
			}
		}
	}
	return result
}

// fail marks the node as failed for reason and records any issues.
func (n *analysisNode) fail(reason failReason, issues ...Issue) {
	n.state = stateFailed
	n.reason = reason
	n.result.Failed = true
	n.result.Issues = append(n.result.Issues, issues...)
}

// warn records an issue that does not fail the node.
func (n *analysisNode) warn(issue Issue) {
	n.result.Issues = append(n.result.Issues, issue)
}

// mergeRequirement records a dependency issue, keeping only the highest required version per kind and target.
func (n *analysisNode) mergeRequirement(issue Issue) {
	for i := range n.result.Issues {
		existing := &n.result.Issues[i]
		if existing.Kind != issue.Kind || !SameID(existing.TargetID, issue.TargetID) {
			continue
		}
		if versionTextNewer(issue.RequiredVersion, existing.RequiredVersion) {
			existing.RequiredVersion = issue.RequiredVersion
		}
		return
	}
	n.result.Issues = append(n.result.Issues, issue)
}

// label returns the provider name of the node, falling back to its folder.
func (n *analysisNode) label() string {
	return n.folder.Provider.label()
}

// label returns the provider name, falling back to its folder.
func (p Provider) label() string {
	if p.Provider != "" {
		return p.Provider
	}
	return p.Folder
}

// chainText formats a dependency chain by mod ID, falling back to folder names.
func chainText(chain []*analysisNode) string {
	parts := make([]string, 0, len(chain))
	for _, node := range chain {
		if strings.TrimSpace(node.result.UniqueID) != "" {
			parts = append(parts, node.result.UniqueID)
		} else {
			parts = append(parts, node.result.Folder)
		}
	}
	return strings.Join(parts, " => ")
}

// versionTextNewer reports whether version string a parses to a newer version than b.
func versionTextNewer(a, b string) bool {
	va, errA := ParseVersion(a, true)
	if errA != nil {
		return false
	}
	vb, errB := ParseVersion(b, true)
	return errB != nil || va.IsNewerThan(vb)
}

// idKey returns the case-folded, trimmed lookup key for a mod ID.
func idKey(id string) string {
	return strings.ToLower(strings.TrimSpace(id))
}

// containsID reports whether ids contains id, ignoring case and surrounding whitespace.
func containsID(ids []string, id string) bool {
	if idKey(id) == "" {
		return false
	}
	for _, candidate := range ids {
		if SameID(candidate, id) {
			return true
		}
	}
	return false
}

// compareFold orders strings case-insensitively, breaking ties by byte order.
func compareFold(a, b string) int {
	if c := strings.Compare(strings.ToLower(a), strings.ToLower(b)); c != 0 {
		return c
	}
	return strings.Compare(a, b)
}

// sortFold sorts values case-insensitively in place.
func sortFold(values []string) {
	sort.SliceStable(values, func(i, j int) bool { return compareFold(values[i], values[j]) < 0 })
}

// uniqueSortedFold returns a sorted copy of values without case-insensitive duplicates, or nil when empty.
func uniqueSortedFold(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	sorted := append([]string(nil), values...)
	sortFold(sorted)
	result := sorted[:0]
	for _, value := range sorted {
		if len(result) == 0 || !strings.EqualFold(result[len(result)-1], value) {
			result = append(result, value)
		}
	}
	return result
}
