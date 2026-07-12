#pragma once

#include <QString>
#include <QStringList>
#include <vector>

namespace gorganizer {

inline constexpr const char* kOverwriteModName = "Overwrite";

struct ModMetadata {
    QString name;
    QString folder;
    QString installed;
    QString sourceArchive;
    QStringList sourceArchives;
    QString nexusUrl;
    QString category;
    QString version;
    bool enabled = true;
    int fileCount = 0;
    QString trueIndex;
    QString visualIndex;
    QString separator;
};

class ModCatalog {
public:
    // Simple line-based YAML parser for metadata.yaml — no external library required.
    static ModMetadata readMetadata(const QString& yamlPath);

    // Sets/unsets a single top-level key in metadata.yaml without disturbing other lines; empty value removes the key.
    static void patchMetadataField(const QString& yamlPath, const QString& key,
                                   const QString& value);

    // Scans modsDir for mod folders (skipping Downloads, dotfiles, and the Overwrite pseudo-mod) and reads each metadata.yaml.
    static std::vector<ModMetadata> scan(const QString& modsDir);

    // Reads the first enabled: line of modDir/metadata.yaml; false when absent or unreadable.
    static bool isModEnabled(const QString& modDir);
};

}
