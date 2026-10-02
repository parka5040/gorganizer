#include "InstallLayout.h"

#include <QCoreApplication>
#include <QDir>
#include <QFile>
#include <QFileInfo>
#include <QRegularExpression>
#include <array>
#include <cerrno>
#include <dirent.h>
#include <sys/stat.h>
#include <unistd.h>

namespace gorganizer {

namespace {

const QRegularExpression& basePattern()
{
    static const QRegularExpression pattern(QStringLiteral("\\A[0-9]{1,9}\\.[0-9]{1,9}\\.[0-9]{1,9}\\z"));
    return pattern;
}

const QRegularExpression& buildPattern()
{
    static const QRegularExpression pattern(
        QStringLiteral("\\A[0-9]{1,9}\\.[0-9]{1,9}\\.[0-9]{1,9}(\\+[0-9A-Za-z.\\-]{1,64})?\\z"));
    return pattern;
}

bool metadata(const QString& path, struct stat* out)
{
    const QByteArray encoded = QFile::encodeName(path);
    return ::lstat(encoded.constData(), out) == 0;
}

bool regularFile(const QString& path)
{
    struct stat st;
    return metadata(path, &st) && S_ISREG(st.st_mode);
}

bool secureBundleFile(const QString& path)
{
    struct stat st;
    return metadata(path, &st) && S_ISREG(st.st_mode) && st.st_uid == getuid()
        && (st.st_mode & (0022 | S_ISUID | S_ISGID)) == 0;
}

bool secureAncestors(const QString& bin, const QString& managedRoot, const QString& releasesRoot)
{
    if (QDir::cleanPath(bin) != bin)
        return false;
    QString path = QStringLiteral("/");
    const QStringList components = bin.split(QLatin1Char('/'), Qt::SkipEmptyParts);
    for (const QString& component : components) {
        path += (path == QLatin1String("/") ? QString() : QStringLiteral("/")) + component;
        struct stat st;
        if (!metadata(path, &st) || !S_ISDIR(st.st_mode)
            || (st.st_uid != 0 && st.st_uid != getuid())
            || ((st.st_mode & 0022) && !(st.st_uid == 0 && (st.st_mode & S_ISVTX)))
            || ((path.startsWith(managedRoot + QLatin1Char('/')) || path == managedRoot)
                && st.st_uid != getuid())
            || (path == releasesRoot && (st.st_mode & 07777) != 0755))
            return false;
    }
    struct stat root;
    return metadata(QStringLiteral("/"), &root) && S_ISDIR(root.st_mode)
        && root.st_uid == 0 && (!(root.st_mode & 0022) || (root.st_mode & S_ISVTX));
}

bool secureBundle(const QString& bundle)
{
    QStringList pending{bundle};
    while (!pending.isEmpty()) {
        const QString dir = pending.takeLast();
        struct stat directory;
        if (!metadata(dir, &directory) || !S_ISDIR(directory.st_mode)
            || directory.st_uid != getuid() || (directory.st_mode & (0022 | S_ISUID | S_ISGID)))
            return false;
        DIR* entries = ::opendir(QFile::encodeName(dir).constData());
        if (!entries)
            return false;
        bool valid = true;
        for (;;) {
            errno = 0;
            dirent* entry = ::readdir(entries);
            if (!entry) {
                valid = errno == 0;
                break;
            }
            const QByteArray name(entry->d_name);
            if (name == "." || name == "..")
                continue;
            const QString decoded = QFile::decodeName(name);
            if (QFile::encodeName(decoded) != name) {
                valid = false;
                break;
            }
            const QString path = dir + QLatin1Char('/') + decoded;
            struct stat st;
            if (!metadata(path, &st)) {
                valid = false;
                break;
            }
            if (S_ISLNK(st.st_mode)) {
                std::array<char, 4096> target{};
                const ssize_t size = ::readlink(QFile::encodeName(path).constData(), target.data(), target.size());
                if (size <= 0 || static_cast<size_t>(size) >= target.size()) {
                    valid = false;
                    break;
                }
                const QString link = QFile::decodeName(QByteArray(target.data(), size));
                const QString destination = QDir::cleanPath(dir + QLatin1Char('/') + link);
                if (QDir::isAbsolutePath(link)
                    || (destination != bundle && !destination.startsWith(bundle + QLatin1Char('/')))) {
                    valid = false;
                    break;
                }
            } else if (st.st_uid != getuid() || (st.st_mode & (0022 | S_ISUID | S_ISGID))) {
                valid = false;
                break;
            } else if (S_ISDIR(st.st_mode)) {
                pending.append(path);
            } else if (!S_ISREG(st.st_mode)) {
                valid = false;
                break;
            }
        }
        if (::closedir(entries) != 0)
            valid = false;
        if (!valid)
            return false;
    }
    return true;
}

bool secureExecutable(const QString& path)
{
    struct stat st;
    return metadata(path, &st) && S_ISREG(st.st_mode) && st.st_uid == getuid()
        && (st.st_mode & (0022 | S_ISUID | S_ISGID)) == 0
        && (st.st_mode & 0111) != 0 && ::access(QFile::encodeName(path).constData(), X_OK) == 0;
}

bool plainVersion(const QString& version)
{
    return basePattern().match(version).hasMatch();
}

}

AppInstallLayout detectInstallLayout()
{
    AppInstallLayout layout;
    layout.runningVersion = QCoreApplication::applicationVersion();
    if (!buildPattern().match(layout.runningVersion).hasMatch()) {
        layout.kind = InstallKind::Development;
        return layout;
    }
    layout.runningBase = layout.runningVersion.section(QLatin1Char('+'), 0, 0);

    const QString xdg = QString::fromLocal8Bit(qgetenv("XDG_DATA_HOME"));
    layout.dataHome = !xdg.isEmpty() && QDir::isAbsolutePath(xdg)
        ? xdg : QDir::homePath() + QStringLiteral("/.local/share");
    layout.releasesRoot = layout.dataHome + QStringLiteral("/gorganizer/releases");
    const QString appDir = QCoreApplication::applicationDirPath();
    const QString bundle = layout.releasesRoot + QLatin1Char('/') + layout.runningBase;
    if (secureAncestors(bundle + QStringLiteral("/bin"),
                        layout.dataHome + QStringLiteral("/gorganizer"), layout.releasesRoot)
        && secureBundle(bundle)
        && QFileInfo(appDir + QStringLiteral("/..")).canonicalFilePath() == bundle
        && secureExecutable(bundle + QStringLiteral("/gorganizer.sh"))
        && secureExecutable(bundle + QStringLiteral("/bin/gorganizerctl"))
        && secureBundleFile(bundle + QStringLiteral("/release.json"))) {
        layout.kind = InstallKind::Prebuilt;
        layout.bundleDir = bundle;
        layout.scriptPath = bundle + QStringLiteral("/gorganizer.sh");
        return layout;
    }

    const QString source = QFileInfo(appDir + QStringLiteral("/../..")).canonicalFilePath();
    struct stat gitStat;
    if (!source.isEmpty() && regularFile(source + QStringLiteral("/gorganizer.sh"))
        && metadata(source + QStringLiteral("/.git"), &gitStat) && S_ISDIR(gitStat.st_mode)) {
        layout.kind = InstallKind::Source;
        layout.scriptPath = source + QStringLiteral("/gorganizer.sh");
    }
    return layout;
}

bool readInstalledVersion(const QString& releasesRoot, QString* version)
{
    struct stat rootStat;
    if (!metadata(releasesRoot, &rootStat) || !S_ISDIR(rootStat.st_mode))
        return false;
    const QByteArray path = QFile::encodeName(releasesRoot + QStringLiteral("/current"));
    std::array<char, 4096> target{};
    const ssize_t size = ::readlink(path.constData(), target.data(), target.size());
    if (size <= 0 || static_cast<size_t>(size) >= target.size())
        return false;
    const QString name = QString::fromUtf8(target.data(), static_cast<qsizetype>(size));
    if (!plainVersion(name))
        return false;
    struct stat selected;
    if (!metadata(releasesRoot + QLatin1Char('/') + name, &selected)
        || !S_ISDIR(selected.st_mode) || selected.st_uid != getuid())
        return false;
    if (version)
        *version = name;
    return true;
}

int compareVersions(const QString& a, const QString& b)
{
    if (!plainVersion(a) || !plainVersion(b))
        return 0;
    const QStringList left = a.split(QLatin1Char('.'));
    const QStringList right = b.split(QLatin1Char('.'));
    for (int i = 0; i < 3; ++i) {
        const int l = left[i].toInt();
        const int r = right[i].toInt();
        if (l != r)
            return l < r ? -1 : 1;
    }
    return 0;
}

QString shellQuote(const QString& path)
{
    for (QChar c : path) {
        if (c.unicode() < 0x20 || c.unicode() == 0x7f)
            return {};
    }
    QString quoted = path;
    quoted.replace(QLatin1Char('\''), QStringLiteral("'\\''"));
    return QLatin1Char('\'') + quoted + QLatin1Char('\'');
}

}
