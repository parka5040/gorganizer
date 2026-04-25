#pragma once

#include <QString>
#include <QList>
#include <QDir>
#include <optional>

namespace gorganizer {

// FomodFile is a single source→destination copy operation. `source` is
// relative to the FOMOD project root (i.e. the directory that contains
// fomod/). `destination` is relative to the target mod folder; empty means
// "mirror the source path underneath the mod root".
struct FomodFile {
    QString source;
    QString destination;
    bool isFolder = false;
    int priority = 0;
};

enum class FomodGroupType {
    SelectAny,
    SelectAtMostOne,
    SelectExactlyOne,
    SelectAtLeastOne,
    SelectAll,
};

enum class FomodPluginState {
    Required,
    Recommended,
    Optional,
    CouldBeUsable,
    NotUsable,
};

struct FomodPlugin {
    QString name;
    QString description;
    QString imagePath;
    QList<FomodFile> files;
    FomodPluginState defaultState = FomodPluginState::Optional;
};

struct FomodGroup {
    QString name;
    FomodGroupType type = FomodGroupType::SelectAny;
    QList<FomodPlugin> plugins;
};

struct FomodStep {
    QString name;
    QList<FomodGroup> groups;
};

struct FomodPlan {
    QString moduleName;
    QString modulePath;                 // root directory that holds fomod/
    QList<FomodFile> requiredFiles;     // always installed
    QList<FomodStep> steps;

    // Legacy NMM-style FOMOD: fomod/info.xml only, no ModuleConfig.xml.
    // The wizard renders an info-only popup; the install path falls back
    // to a flat copy of everything outside fomod/ (excluding *.cs).
    bool legacyInfoOnly = false;
    QString description;
    QString screenshotPath;             // absolute path or empty
    QString version;
    QString author;

    bool isEmpty() const {
        return moduleName.isEmpty() && steps.isEmpty()
            && requiredFiles.isEmpty() && !legacyInfoOnly;
    }
};

// Parses a FOMOD installer. Pass the root extract directory — the parser
// first extracts any nested *.fomod archive (NMM convention) in place,
// then locates fomod/ModuleConfig.xml or, failing that, fomod/info.xml.
class FomodParser {
public:
    static std::optional<FomodPlan> parse(const QString& extractRoot);

    // Extract any *.fomod files at the extract root (or one level deep)
    // in place. Idempotent. Public so ModInstallDialog can call it before
    // its own walk.
    static void expandNestedFomods(const QString& extractRoot);
};

} // namespace gorganizer
