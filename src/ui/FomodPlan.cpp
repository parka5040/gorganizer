#include "FomodPlan.h"

#include <QXmlStreamReader>

namespace gorganizer {

namespace {

FomodGroupType parseGroupType(const QString& s)
{
    QString v = s.trimmed();
    if (v == "SelectAny")          return FomodGroupType::SelectAny;
    if (v == "SelectAtMostOne")    return FomodGroupType::SelectAtMostOne;
    if (v == "SelectExactlyOne")   return FomodGroupType::SelectExactlyOne;
    if (v == "SelectAtLeastOne")   return FomodGroupType::SelectAtLeastOne;
    if (v == "SelectAll")          return FomodGroupType::SelectAll;
    return FomodGroupType::SelectAny;
}

FomodPluginState parsePluginState(const QString& s)
{
    QString v = s.trimmed();
    if (v == "Required")      return FomodPluginState::Required;
    if (v == "Recommended")   return FomodPluginState::Recommended;
    if (v == "Optional")      return FomodPluginState::Optional;
    if (v == "CouldBeUsable") return FomodPluginState::CouldBeUsable;
    if (v == "NotUsable")     return FomodPluginState::NotUsable;
    return FomodPluginState::Optional;
}

QList<FomodFile> readFilesBlock(QXmlStreamReader& xml)
{
    QList<FomodFile> files;
    const auto startName = xml.name().toString();
    while (!xml.atEnd()) {
        xml.readNext();
        if (xml.isEndElement() && xml.name() == startName) break;
        if (!xml.isStartElement()) continue;
        QString name = xml.name().toString();
        if (name == "file" || name == "folder") {
            FomodFile f;
            const auto attrs = xml.attributes();
            f.source = attrs.value("source").toString();
            f.destination = attrs.value("destination").toString();
            if (attrs.hasAttribute("priority"))
                f.priority = attrs.value("priority").toInt();
            f.isFolder = (name == "folder");
            if (!f.source.isEmpty())
                files.append(std::move(f));
            xml.skipCurrentElement();
        }
    }
    return files;
}

void readPlugin(QXmlStreamReader& xml, FomodPlugin& plugin)
{
    plugin.name = xml.attributes().value("name").toString();
    while (!xml.atEnd()) {
        xml.readNext();
        if (xml.isEndElement() && xml.name() == QLatin1String("plugin")) break;
        if (!xml.isStartElement()) continue;
        const auto tag = xml.name();
        if (tag == QLatin1String("description")) {
            plugin.description = xml.readElementText().trimmed();
        } else if (tag == QLatin1String("image")) {
            plugin.imagePath = xml.attributes().value("path").toString();
            xml.skipCurrentElement();
        } else if (tag == QLatin1String("files")) {
            plugin.files = readFilesBlock(xml);
        } else if (tag == QLatin1String("typeDescriptor")) {
            while (!xml.atEnd()) {
                xml.readNext();
                if (xml.isEndElement() && xml.name() == QLatin1String("typeDescriptor")) break;
                if (xml.isStartElement() && xml.name() == QLatin1String("type")) {
                    plugin.defaultState = parsePluginState(xml.attributes().value("name").toString());
                    xml.skipCurrentElement();
                } else if (xml.isStartElement() && xml.name() == QLatin1String("defaultType")) {
                    plugin.defaultState = parsePluginState(xml.attributes().value("name").toString());
                    xml.skipCurrentElement();
                }
            }
        } else {
            xml.skipCurrentElement();
        }
    }
}

void readGroup(QXmlStreamReader& xml, FomodGroup& group)
{
    group.name = xml.attributes().value("name").toString();
    group.type = parseGroupType(xml.attributes().value("type").toString());
    while (!xml.atEnd()) {
        xml.readNext();
        if (xml.isEndElement() && xml.name() == QLatin1String("group")) break;
        if (!xml.isStartElement()) continue;
        if (xml.name() == QLatin1String("plugins")) {
            while (!xml.atEnd()) {
                xml.readNext();
                if (xml.isEndElement() && xml.name() == QLatin1String("plugins")) break;
                if (xml.isStartElement() && xml.name() == QLatin1String("plugin")) {
                    FomodPlugin p;
                    readPlugin(xml, p);
                    group.plugins.append(std::move(p));
                }
            }
        } else {
            xml.skipCurrentElement();
        }
    }
}

void readInstallStep(QXmlStreamReader& xml, FomodStep& step)
{
    step.name = xml.attributes().value("name").toString();
    while (!xml.atEnd()) {
        xml.readNext();
        if (xml.isEndElement() && xml.name() == QLatin1String("installStep")) break;
        if (!xml.isStartElement()) continue;
        if (xml.name() == QLatin1String("optionalFileGroups")) {
            while (!xml.atEnd()) {
                xml.readNext();
                if (xml.isEndElement() && xml.name() == QLatin1String("optionalFileGroups")) break;
                if (xml.isStartElement() && xml.name() == QLatin1String("group")) {
                    FomodGroup g;
                    readGroup(xml, g);
                    step.groups.append(std::move(g));
                }
            }
        } else {
            xml.skipCurrentElement();
        }
    }
}

}

std::optional<FomodPlan> FomodParser::parseModuleConfig(const QByteArray& bytes)
{
    QXmlStreamReader xml(bytes);
    FomodPlan plan;
    bool foundConfig = false;
    while (!xml.atEnd()) {
        xml.readNext();
        if (!xml.isStartElement()) continue;
        const auto tag = xml.name();
        if (tag == QLatin1String("config")) {
            foundConfig = true;
        } else if (tag == QLatin1String("moduleName")) {
            plan.moduleName = xml.readElementText().trimmed();
        } else if (tag == QLatin1String("requiredInstallFiles")) {
            plan.requiredFiles = readFilesBlock(xml);
        } else if (tag == QLatin1String("installSteps")) {
            while (!xml.atEnd()) {
                xml.readNext();
                if (xml.isEndElement() && xml.name() == QLatin1String("installSteps")) break;
                if (xml.isStartElement() && xml.name() == QLatin1String("installStep")) {
                    FomodStep step;
                    readInstallStep(xml, step);
                    plan.steps.append(std::move(step));
                }
            }
        }
    }
    if (xml.hasError() || !foundConfig)
        return std::nullopt;
    return plan;
}

}
