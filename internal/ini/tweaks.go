package ini

// TweakEntry is one (section, key, value) triple a tweak writes.
type TweakEntry struct {
	Section string
	Key     string
	Value   string
}

// Tweak is a named preset — a human-readable bundle of INI edits that map
// to a well-known modding configuration (archive invalidation, papyrus
// logging, face-gen loading, etc.). All tweaks target the game's
// {Game}Custom.ini so the primary INI stays untouched; for engines that
// don't read Custom.ini natively the daemon merges it on push.
type Tweak struct {
	ID          string
	Name        string
	Description string
	Entries     []TweakEntry
}

// Canonical archive-invalidation blocks per engine family. These mirror
// what MO2's "Toggle Archive Invalidation" writes.
var tweakArchiveInvalidationOblivion = Tweak{
	ID:   "archive_invalidation",
	Name: "Archive Invalidation",
	Description: "Enables BSA override so loose files and modded BSAs replace vanilla assets. " +
		"Required for most Oblivion texture/mesh mods to appear in-game.",
	Entries: []TweakEntry{
		{Section: "Archive", Key: "bInvalidateOlderFiles", Value: "1"},
		{Section: "Archive", Key: "SInvalidationFile", Value: ""},
	},
}

var tweakArchiveInvalidationFallout = Tweak{
	ID:   "archive_invalidation",
	Name: "Archive Invalidation",
	Description: "Enables BSA override so loose mod files take precedence. " +
		"Fixes pink textures, missing meshes, and body-replacer glitches in Fallout 3 / New Vegas.",
	Entries: []TweakEntry{
		{Section: "Archive", Key: "bInvalidateOlderFiles", Value: "1"},
		{Section: "Archive", Key: "SInvalidationFile", Value: ""},
	},
}

var tweakArchiveInvalidationSkyrimLE = Tweak{
	ID:   "archive_invalidation",
	Name: "Archive Invalidation",
	Description: "Forces Skyrim LE to honor loose-file mods by clearing the final resource " +
		"dirs list. Equivalent to MO2's built-in archive invalidation toggle.",
	Entries: []TweakEntry{
		{Section: "Archive", Key: "bInvalidateOlderFiles", Value: "1"},
		{Section: "Archive", Key: "sResourceDataDirsFinal", Value: ""},
	},
}

var tweakFaceGenLoad = Tweak{
	ID:   "facegen_load",
	Name: "Load FaceGen Head Files",
	Description: "Makes Fallout 3 / New Vegas load head EGT/NIF files. Required by many " +
		"body replacers and race overhauls.",
	Entries: []TweakEntry{
		{Section: "General", Key: "bLoadFaceGenHeadEGTFiles", Value: "1"},
		{Section: "General", Key: "bLoadFaceGenHeadNIFFiles", Value: "1"},
	},
}

var tweakDisableIntroFallout = Tweak{
	ID:          "disable_intro",
	Name:        "Disable Intro Videos",
	Description: "Skips the Bethesda/publisher logos and the opening FMV on launch.",
	Entries: []TweakEntry{
		{Section: "General", Key: "SIntroSequence", Value: ""},
	},
}

var tweakDisableIntroSkyrim = Tweak{
	ID:          "disable_intro",
	Name:        "Disable Intro Videos",
	Description: "Skips the Bethesda logo and opening cinematic.",
	Entries: []TweakEntry{
		{Section: "General", Key: "sIntroSequence", Value: ""},
	},
}

var tweakPapyrusLog = Tweak{
	ID:   "papyrus_log",
	Name: "Enable Papyrus Logging",
	Description: "Turns on the Papyrus debug log (Documents/My Games/Skyrim*/Logs/Script/). " +
		"Useful when diagnosing script-heavy mods; noticeable runtime cost.",
	Entries: []TweakEntry{
		{Section: "Papyrus", Key: "bEnableLogging", Value: "1"},
		{Section: "Papyrus", Key: "bEnableTrace", Value: "1"},
		{Section: "Papyrus", Key: "bLoadDebugInformation", Value: "1"},
	},
}

var tweakAllowConsole = Tweak{
	ID:          "allow_console",
	Name:        "Allow Console",
	Description: "Permits opening the developer console in-game. Useful for mod testing.",
	Entries: []TweakEntry{
		{Section: "General", Key: "bAllowConsole", Value: "1"},
	},
}

// gameTweaks maps gameID → curated preset list. Each game only offers
// tweaks that apply to its engine generation.
var gameTweaks = map[string][]Tweak{
	"oblivion":  {tweakArchiveInvalidationOblivion},
	"fallout3":  {tweakArchiveInvalidationFallout, tweakFaceGenLoad, tweakDisableIntroFallout, tweakAllowConsole},
	"falloutnv": {tweakArchiveInvalidationFallout, tweakFaceGenLoad, tweakDisableIntroFallout, tweakAllowConsole},
	"skyrim":    {tweakArchiveInvalidationSkyrimLE, tweakDisableIntroSkyrim, tweakPapyrusLog},
	"skyrimse":  {tweakDisableIntroSkyrim, tweakPapyrusLog},
	// Fallout 4 and Starfield handle archive invalidation in-engine; no
	// tweak presets offered here (by request). Morrowind omitted — its INI
	// layout predates the archive concept entirely.
}

// AvailableTweaks returns the preset list for a game, or nil when none are
// defined. Never returns nil entries, so a `range` on the result is safe.
func AvailableTweaks(gameID string) []Tweak {
	return gameTweaks[gameID]
}

// FindTweak looks up a tweak by ID for a game.
func FindTweak(gameID, tweakID string) (Tweak, bool) {
	for _, t := range gameTweaks[gameID] {
		if t.ID == tweakID {
			return t, true
		}
	}
	return Tweak{}, false
}

// IsApplied returns true when every entry of the tweak is present in the
// target INI document with its exact value. A single missing or mismatched
// entry marks the tweak as not applied.
func (t Tweak) IsApplied(doc *Document) bool {
	if doc == nil {
		return false
	}
	for _, e := range t.Entries {
		got, ok := doc.Get(e.Section, e.Key)
		if !ok || got != e.Value {
			return false
		}
	}
	return true
}

// Apply writes every tweak entry into the document.
func (t Tweak) Apply(doc *Document) {
	for _, e := range t.Entries {
		doc.Set(e.Section, e.Key, e.Value)
	}
}

// Unapply removes every tweak entry from the document.
func (t Tweak) Unapply(doc *Document) {
	for _, e := range t.Entries {
		doc.Remove(e.Section, e.Key)
	}
}
