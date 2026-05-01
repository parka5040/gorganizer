#include "FomodPlan.h"

#include <QFile>
#include <QXmlStreamReader>
#include <QFileInfo>
#include <QDebug>
#include <QProcess>
#include <QTextStream>
#include <QStringDecoder>

namespace gorganizer {

namespace {

// Locate a direct child by name, case-insensitively.
QString findChildCI(const QString& parent, const QString& target)
{
    QDir dir(parent);
    if (!dir.exists()) return {};
    QString lowered = target.toLower();
    for (const auto& entry : dir.entryList(QDir::AllEntries | QDir::NoDotAndDotDot)) {
        if (entry.toLower() == lowered)
            return dir.filePath(entry);
    }
    return {};
}

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

// Read a <files>/<folder>+<file> block until the matching EndElement.
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

} // namespace

// Best-effort parse of a legacy fomod/info.xml (handles UTF-16 BOMs).
namespace {
void readLegacyInfo(const QString& fomodDir, FomodPlan& plan)
{
    QString infoPath = findChildCI(fomodDir, "info.xml");
    if (infoPath.isEmpty())
        return;
    QFile f(infoPath);
    if (!f.open(QIODevice::ReadOnly))
        return;
    QByteArray raw = f.readAll();
    f.close();

    QStringDecoder dec;
    if (raw.size() >= 2 && (uchar)raw[0] == 0xFF && (uchar)raw[1] == 0xFE)
        dec = QStringDecoder(QStringDecoder::Utf16LE);
    else if (raw.size() >= 2 && (uchar)raw[0] == 0xFE && (uchar)raw[1] == 0xFF)
        dec = QStringDecoder(QStringDecoder::Utf16BE);
    else
        dec = QStringDecoder(QStringDecoder::Utf8);
    QString text = dec.decode(raw);

    QXmlStreamReader xml(text);
    while (!xml.atEnd() && !xml.hasError()) {
        xml.readNext();
        if (!xml.isStartElement()) continue;
        const auto tag = xml.name().toString();
        if (tag == "Name")
            plan.moduleName = xml.readElementText().trimmed();
        else if (tag == "Description")
            plan.description = xml.readElementText().trimmed();
        else if (tag == "Version")
            plan.version = xml.readElementText().trimmed();
        else if (tag == "Author")
            plan.author = xml.readElementText().trimmed();
    }

    QDir dir(fomodDir);
    QStringList preferred = {"screenshot.png", "screenshot.jpg"};
    for (const auto& p : preferred) {
        QString c = findChildCI(fomodDir, p);
        if (!c.isEmpty()) { plan.screenshotPath = c; return; }
    }
    for (const auto& e : dir.entryList(QDir::Files)) {
        QString lower = e.toLower();
        if (lower.endsWith(".png") || lower.endsWith(".jpg") || lower.endsWith(".jpeg")) {
            plan.screenshotPath = dir.filePath(e);
            return;
        }
    }
}
} // namespace

void FomodParser::expandNestedFomods(const QString& extractRoot)
{
    auto visit = [](const QString& dir) {
        QDir d(dir);
        for (const auto& fi : d.entryInfoList(QDir::Files)) {
            if (fi.suffix().compare("fomod", Qt::CaseInsensitive) != 0)
                continue;
            QString outDir = d.filePath(fi.completeBaseName());
            QDir().mkpath(outDir);
            QProcess p;
            p.start("7z", {"x", "-o" + outDir, "-y", fi.absoluteFilePath()});
            if (!p.waitForFinished(60000) || p.exitCode() != 0) {
                qWarning() << "FomodParser: failed to expand nested .fomod:"
                           << fi.absoluteFilePath();
                QDir(outDir).removeRecursively();
                continue;
            }
            QFile::remove(fi.absoluteFilePath());
        }
    };
    visit(extractRoot);
    QDir root(extractRoot);
    for (const auto& sub : root.entryList(QDir::Dirs | QDir::NoDotAndDotDot))
        visit(root.filePath(sub));
}

std::optional<FomodPlan> FomodParser::parse(const QString& extractRoot)
{
    expandNestedFomods(extractRoot);

    enum class Kind { None, ModuleConfig, LegacyInfo };
    auto findFomod = [](const QString& dir) -> std::tuple<QString, QString, Kind> {
        QString fomod = findChildCI(dir, "fomod");
        if (fomod.isEmpty()) return {QString(), QString(), Kind::None};
        QString cfg = findChildCI(fomod, "ModuleConfig.xml");
        if (!cfg.isEmpty()) return {fomod, cfg, Kind::ModuleConfig};
        QString info = findChildCI(fomod, "info.xml");
        if (!info.isEmpty()) return {fomod, info, Kind::LegacyInfo};
        return {QString(), QString(), Kind::None};
    };

    QString fomodDir, configPath, modulePath;
    Kind kind = Kind::None;
    {
        auto [fd, cp, k] = findFomod(extractRoot);
        if (k != Kind::None) { fomodDir = fd; configPath = cp; modulePath = extractRoot; kind = k; }
    }
    if (kind == Kind::None) {
        QDir root(extractRoot);
        for (const auto& d1 : root.entryList(QDir::Dirs | QDir::NoDotAndDotDot)) {
            QString l1 = root.filePath(d1);
            auto [fd, cp, k] = findFomod(l1);
            if (k != Kind::None) {
                fomodDir = fd; configPath = cp; modulePath = l1; kind = k; break;
            }
            QDir l1dir(l1);
            for (const auto& d2 : l1dir.entryList(QDir::Dirs | QDir::NoDotAndDotDot)) {
                QString l2 = l1dir.filePath(d2);
                auto [fd2, cp2, k2] = findFomod(l2);
                if (k2 != Kind::None) {
                    fomodDir = fd2; configPath = cp2; modulePath = l2; kind = k2; break;
                }
            }
            if (kind != Kind::None) break;
        }
    }
    if (kind == Kind::None) return std::nullopt;

    FomodPlan plan;
    plan.modulePath = modulePath;

    if (kind == Kind::LegacyInfo) {
        plan.legacyInfoOnly = true;
        plan.moduleName = QFileInfo(modulePath).fileName();
        readLegacyInfo(fomodDir, plan);
        return plan;
    }

    QFile f(configPath);
    if (!f.open(QIODevice::ReadOnly)) return std::nullopt;

    QXmlStreamReader xml(&f);
    while (!xml.atEnd() && !xml.hasError()) {
        xml.readNext();
        if (!xml.isStartElement()) continue;
        const auto tag = xml.name();
        if (tag == QLatin1String("moduleName")) {
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
    if (xml.hasError()) {
        qWarning() << "FOMOD parse error:" << xml.errorString() << "at" << configPath;
    }
    return plan;
}

} // namespace gorganizer
