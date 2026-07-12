#include "ModCatalog.h"

#include <QDir>
#include <QFile>
#include <QTextStream>

namespace gorganizer {

ModMetadata ModCatalog::readMetadata(const QString& yamlPath)
{
    ModMetadata meta;
    QFile f(yamlPath);
    if (!f.open(QIODevice::ReadOnly | QIODevice::Text))
        return meta;

    auto stripQuotes = [](QString s) {
        s = s.trimmed();
        if (s.startsWith('"') && s.endsWith('"'))
            s = s.mid(1, s.length() - 2);
        return s;
    };

    QTextStream in(&f);
    bool inSourceList = false;
    while (!in.atEnd()) {
        QString raw = in.readLine();
        QString line = raw.trimmed();
        if (line.startsWith('#') || line.isEmpty())
            continue;

        if (!raw.startsWith(' ') && line.endsWith(':')) {
            QString section = line.left(line.length() - 1).trimmed();
            inSourceList = (section == "source_archives");
            continue;
        }

        if (inSourceList) {
            if (line.startsWith("- path:")) {
                meta.sourceArchives.append(stripQuotes(line.mid(QString("- path:").length())));
            } else if (line.startsWith("path:")) {
                meta.sourceArchives.append(stripQuotes(line.mid(QString("path:").length())));
            } else if (line.startsWith("- ")) {
                QString rest = line.mid(2);
                if (!rest.contains(':'))
                    meta.sourceArchives.append(stripQuotes(rest));
            }
            continue;
        }

        int colon = line.indexOf(':');
        if (colon < 0)
            continue;
        QString key = line.left(colon).trimmed();
        QString val = stripQuotes(line.mid(colon + 1));

        if (key == "name")            meta.name = val;
        else if (key == "folder")     meta.folder = val;
        else if (key == "installed")  meta.installed = val;
        else if (key == "source_archive") meta.sourceArchive = val;
        else if (key == "nexus_url")  meta.nexusUrl = val;
        else if (key == "mod_page")   meta.nexusUrl = val;
        else if (key == "category")   meta.category = val;
        else if (key == "version")    meta.version = val;
        else if (key == "enabled")    meta.enabled = (val == "true");
        else if (key == "file_count") meta.fileCount = val.toInt();
        else if (key == "true_index")   meta.trueIndex = val;
        else if (key == "visual_index") meta.visualIndex = val;
        else if (key == "separator")    meta.separator = val;
    }

    if (meta.sourceArchives.isEmpty() && !meta.sourceArchive.isEmpty())
        meta.sourceArchives.append(meta.sourceArchive);

    return meta;
}

void ModCatalog::patchMetadataField(const QString& yamlPath, const QString& key,
                                    const QString& value)
{
    QFile f(yamlPath);
    if (!f.open(QIODevice::ReadOnly | QIODevice::Text))
        return;
    QString content = f.readAll();
    f.close();

    QStringList lines = content.split('\n');
    QStringList kept;
    bool matched = false;
    const QString prefix = key + ":";
    for (const auto& ln : lines) {
        QString trimmed = ln.trimmed();
        if (!trimmed.startsWith(prefix)) {
            kept.append(ln);
            continue;
        }
        if (!ln.startsWith(' ') && !ln.startsWith('\t')) {
            matched = true;
            if (!value.isEmpty())
                kept.append(QString("%1: \"%2\"").arg(key, value));
        } else {
            kept.append(ln);
        }
    }
    if (!matched && !value.isEmpty()) {
        int anchor = -1;
        for (int i = 0; i < kept.size(); ++i) {
            if (kept[i].trimmed() == "source_archives:") { anchor = i; break; }
        }
        QString newLine = QString("%1: \"%2\"").arg(key, value);
        if (anchor >= 0)
            kept.insert(anchor, newLine);
        else
            kept.append(newLine);
    }
    if (!f.open(QIODevice::WriteOnly | QIODevice::Text))
        return;
    f.write(kept.join('\n').toUtf8());
    f.close();
}

std::vector<ModMetadata> ModCatalog::scan(const QString& modsDir)
{
    std::vector<ModMetadata> mods;

    QDir dir(modsDir);
    if (!dir.exists())
        return mods;

    auto entries = dir.entryList(QDir::Dirs | QDir::NoDotAndDotDot, QDir::Name);
    for (const auto& dirName : entries) {
        if (dirName == "Downloads" || dirName.startsWith('.'))
            continue;
        if (dirName == kOverwriteModName)
            continue;
        QString metaPath = modsDir + "/" + dirName + "/metadata.yaml";
        ModMetadata meta;
        if (QFile::exists(metaPath)) {
            meta = readMetadata(metaPath);
        } else {
            meta.name = dirName;
            meta.folder = dirName;
            meta.enabled = true;
        }
        if (meta.folder.isEmpty())
            meta.folder = dirName;
        if (meta.name.isEmpty())
            meta.name = dirName;

        mods.push_back(meta);
    }
    return mods;
}

bool ModCatalog::isModEnabled(const QString& modDir)
{
    bool enabled = false;
    QFile metaFile(modDir + "/metadata.yaml");
    if (metaFile.open(QIODevice::ReadOnly | QIODevice::Text)) {
        while (!metaFile.atEnd()) {
            QString line = metaFile.readLine().trimmed();
            if (line.startsWith("enabled:")) {
                enabled = line.contains("true");
                break;
            }
        }
        metaFile.close();
    }
    return enabled;
}

}
