package ini

type TweakEntry struct {
	Section string
	Key     string
	Value   string
}

type Tweak struct {
	ID          string
	Name        string
	Description string
	Entries     []TweakEntry
}

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

var tweakSets = map[string][]Tweak{
	"oblivion": {tweakArchiveInvalidationOblivion},
	"fallout":  {tweakArchiveInvalidationFallout, tweakFaceGenLoad, tweakDisableIntroFallout, tweakAllowConsole},
	"skyrimle": {tweakArchiveInvalidationSkyrimLE, tweakDisableIntroSkyrim, tweakPapyrusLog},
	"skyrimse": {tweakDisableIntroSkyrim, tweakPapyrusLog},
}

// AvailableTweaks returns the preset list for a game, or nil when none defined.
func AvailableTweaks(gameID string) []Tweak {
	spec, ok := SpecFor(gameID)
	if !ok || spec.TweakSet == "" {
		return nil
	}
	return tweakSets[spec.TweakSet]
}

func FindTweak(gameID, tweakID string) (Tweak, bool) {
	for _, t := range AvailableTweaks(gameID) {
		if t.ID == tweakID {
			return t, true
		}
	}
	return Tweak{}, false
}

// IsApplied returns true when every entry is present with its exact value.
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

func (t Tweak) Apply(doc *Document) {
	for _, e := range t.Entries {
		doc.Set(e.Section, e.Key, e.Value)
	}
}

func (t Tweak) Unapply(doc *Document) {
	for _, e := range t.Entries {
		doc.Remove(e.Section, e.Key)
	}
}
