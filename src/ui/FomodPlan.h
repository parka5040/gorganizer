#pragma once

#include <QByteArray>
#include <QString>
#include <QList>
#include <optional>

namespace gorganizer {

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
    QList<FomodFile> requiredFiles;
    QList<FomodStep> steps;

    bool legacyInfoOnly = false;
    QString description;
    QByteArray screenshotData;
    QString version;
    QString author;

    bool isEmpty() const {
        return moduleName.isEmpty() && steps.isEmpty()
            && requiredFiles.isEmpty() && !legacyInfoOnly;
    }
};

class FomodParser {
public:
    static std::optional<FomodPlan> parseModuleConfig(const QByteArray& xml);
};

}
