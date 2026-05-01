#pragma once

#include <QString>
#include <QList>
#include <QDir>
#include <optional>

namespace gorganizer {

// One source-to-destination copy operation in a FOMOD install plan.
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
    QString modulePath;
    QList<FomodFile> requiredFiles;
    QList<FomodStep> steps;

    bool legacyInfoOnly = false;
    QString description;
    QString screenshotPath;
    QString version;
    QString author;

    bool isEmpty() const {
        return moduleName.isEmpty() && steps.isEmpty()
            && requiredFiles.isEmpty() && !legacyInfoOnly;
    }
};

// Parses a FOMOD installer at the extract root.
class FomodParser {
public:
    static std::optional<FomodPlan> parse(const QString& extractRoot);

    // Idempotently extract any *.fomod files at the root or one level deep.
    static void expandNestedFomods(const QString& extractRoot);
};

} // namespace gorganizer
