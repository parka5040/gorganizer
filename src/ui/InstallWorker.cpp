#include "InstallWorker.h"

#include <QDebug>
#include <QDir>
#include <QDirIterator>
#include <QFile>
#include <QFileInfo>
#include <algorithm>
#include <stdexcept>

namespace gorganizer {

InstallWorker::InstallWorker(QObject* parent)
    : QObject(parent)
{
    m_cancel.store(false);
}

namespace {

bool cleanRelativePath(const QString& raw, QString& clean, bool allowDot = false)
{
    clean = raw;
    clean.replace('\\', '/');
    clean = QDir::cleanPath(clean);
    return !clean.isEmpty() && !QDir::isAbsolutePath(clean)
        && clean != ".." && !clean.startsWith("../")
        && (allowDot || clean != ".");
}

bool pathInside(const QString& root, const QString& candidate)
{
    QString rel = QDir(root).relativeFilePath(candidate);
    rel.replace('\\', '/');
    return rel != ".." && !rel.startsWith("../") && !QDir::isAbsolutePath(rel);
}

QString routeOblivionRemasteredPath(QString relative)
{
    relative.replace('\\', '/');
    relative = QDir::cleanPath(relative);
    const auto parts = relative.split('/', Qt::SkipEmptyParts);
    const QList<QStringList> dataPrefixes = {
        {"OblivionRemastered", "Content", "Dev", "ObvData", "Data"},
        {"Content", "Dev", "ObvData", "Data"},
        {"ObvData", "Data"},
    };
    for (const auto& prefix : dataPrefixes) {
        if (parts.size() < prefix.size())
            continue;
        bool matches = true;
        for (int i = 0; i < prefix.size(); ++i) {
            if (parts[i].compare(prefix[i], Qt::CaseInsensitive) != 0) {
                matches = false;
                break;
            }
        }
        if (!matches)
            continue;
        if (parts.size() == prefix.size())
            return ".";
        return parts.mid(prefix.size()).join('/');
    }
    if (!parts.isEmpty()
        && (parts.first().compare("Engine", Qt::CaseInsensitive) == 0
            || parts.first().compare("OblivionRemastered", Qt::CaseInsensitive) == 0))
        return ".gorganizer-root/" + relative;
    if (relative.compare("OblivionRemastered.exe", Qt::CaseInsensitive) == 0)
        return ".gorganizer-root/" + relative;
    return relative;
}

QString routedPath(const QString& gameId, const QString& relative)
{
    if (gameId.compare("oblivionremastered", Qt::CaseInsensitive) == 0)
        return routeOblivionRemasteredPath(relative);
    return relative;
}

[[noreturn]] void unsafePath(const char* kind, const QString& path)
{
    throw std::runtime_error(
        QString("unsafe FOMOD %1 path: %2").arg(QString::fromLatin1(kind), path).toStdString());
}

void ensureDirectory(const QString& path)
{
    if (!QDir().mkpath(path))
        throw std::runtime_error(QString("could not create install directory: %1").arg(path).toStdString());
}

void copyReplacing(const QString& source, const QString& target)
{
    ensureDirectory(QFileInfo(target).absolutePath());
    if (QFileInfo::exists(target) && !QFile::remove(target))
        throw std::runtime_error(QString("could not replace install file: %1").arg(target).toStdString());
    if (!QFile::copy(source, target))
        throw std::runtime_error(QString("could not copy %1 to %2").arg(source, target).toStdString());
}

}

void InstallWorker::configureRecursive(const QString& src, const QString& dst,
                                       const QString& gameId)
{
    m_mode = Recursive;
    m_src = src;
    m_dst = dst;
    m_gameId = gameId;
}

void InstallWorker::configureFomodSelections(const QString& modulePath,
                                             const QList<FomodFile>& selections,
                                             const QString& destDir, const QString& gameId)
{
    m_mode = FomodSelections;
    m_modulePath = modulePath;
    m_selections = selections;
    m_dst = destDir;
    m_gameId = gameId;
}

void InstallWorker::configureLegacyFomod(const QString& modulePath, const QString& destDir,
                                         const QString& gameId)
{
    m_mode = LegacyFomod;
    m_modulePath = modulePath;
    m_dst = destDir;
    m_gameId = gameId;
}

void InstallWorker::cancel()
{
    m_cancel.store(true);
}

void InstallWorker::run()
{
    int count = 0;
    QString err;
    bool ok = true;
    try {
        switch (m_mode) {
        case Recursive:        count = doRecursive();        break;
        case FomodSelections:  count = doFomodSelections();  break;
        case LegacyFomod:      count = doLegacyFomod();      break;
        }
    } catch (const std::exception& e) {
        ok = false;
        err = QString::fromUtf8(e.what());
    } catch (...) {
        ok = false;
        err = QStringLiteral("install worker: unknown exception");
    }
    try {
        emit finished(ok && !m_cancel.load(), m_cancel.load(), count, err);
    } catch (const std::exception& e) {
        qCritical("InstallWorker::run: finished() slot threw: %s", e.what());
    } catch (...) {
        qCritical("InstallWorker::run: finished() slot threw unknown exception");
    }
}

int InstallWorker::doRecursive()
{
    int count = 0;
    const QString sourceCanonical = QFileInfo(m_src).canonicalFilePath();
    if (sourceCanonical.isEmpty())
        unsafePath("source", m_src);
    QDirIterator it(m_src, QDir::Files | QDir::Dirs | QDir::NoDotAndDotDot,
                    QDirIterator::Subdirectories);
    while (it.hasNext()) {
        if (m_cancel.load())
            return count;
        it.next();
        QString rel = QDir(m_src).relativeFilePath(it.filePath());
        QString routed = routedPath(m_gameId, rel);
        QString clean;
        if (!cleanRelativePath(routed, clean, true))
            unsafePath("destination", routed);
        QString destPath = QDir(m_dst).absoluteFilePath(clean);
        if (!pathInside(QDir(m_dst).absolutePath(), QDir::cleanPath(destPath)))
            unsafePath("destination", routed);

        if (it.fileInfo().isDir()) {
            ensureDirectory(destPath);
        } else {
            const QString fileCanonical = it.fileInfo().canonicalFilePath();
            if (fileCanonical.isEmpty() || !pathInside(sourceCanonical, fileCanonical))
                unsafePath("source", rel);
            copyReplacing(fileCanonical, destPath);
            ++count;
        }
    }
    return count;
}

int InstallWorker::doFomodSelections()
{
    auto ops = m_selections;
    std::stable_sort(ops.begin(), ops.end(),
        [](const FomodFile& a, const FomodFile& b) { return a.priority < b.priority; });

    auto normalizeDest = [&](const FomodFile& f) -> QString {
        QString dest = f.destination;
        dest.replace('\\', '/');
        if (dest.isEmpty()) {
            if (f.isFolder)
                return QString();
            return QFileInfo(f.source).fileName();
        }
        while (dest.startsWith('/')) dest.remove(0, 1);
        while (dest.endsWith('/'))   dest.chop(1);
        return dest;
    };

    int count = 0;
    for (const auto& f : ops) {
        if (m_cancel.load())
            return count;

        QString normSource;
        if (!cleanRelativePath(f.source, normSource))
            unsafePath("source", f.source);
        QString absSource = QDir(m_modulePath).absoluteFilePath(normSource);
        QString moduleCanonical = QFileInfo(m_modulePath).canonicalFilePath();
        QString sourceCanonical = QFileInfo(absSource).canonicalFilePath();
        if (moduleCanonical.isEmpty() || sourceCanonical.isEmpty()
            || !pathInside(moduleCanonical, sourceCanonical))
            unsafePath("source", f.source);
        absSource = sourceCanonical;

        if (f.isFolder) {
            QDir srcDir(absSource);
            if (!srcDir.exists()) continue;
            QString destRoot = m_dst;
            QString destSub = routedPath(m_gameId, normalizeDest(f));
            QString cleanDest;
            if (!destSub.isEmpty() && !cleanRelativePath(destSub, cleanDest, true))
                unsafePath("destination", f.destination);
            if (!destSub.isEmpty())
                destRoot = QDir(m_dst).absoluteFilePath(cleanDest);
            if (!pathInside(QDir(m_dst).absolutePath(), QDir::cleanPath(destRoot)))
                unsafePath("destination", f.destination);
            ensureDirectory(destRoot);

            QDirIterator it(absSource, QDir::Files | QDir::Dirs | QDir::NoDotAndDotDot,
                            QDirIterator::Subdirectories);
            while (it.hasNext()) {
                if (m_cancel.load())
                    return count;
                it.next();
                QString rel = srcDir.relativeFilePath(it.filePath());
                QString target = destRoot + "/" + rel;
                if (it.fileInfo().isDir()) {
                    ensureDirectory(target);
                } else {
                    const QString fileCanonical = it.fileInfo().canonicalFilePath();
                    if (fileCanonical.isEmpty() || !pathInside(moduleCanonical, fileCanonical))
                        unsafePath("source", f.source);
                    copyReplacing(fileCanonical, target);
                    ++count;
                }
            }
        } else {
            if (!QFile::exists(absSource)) continue;
            QString destSub = routedPath(m_gameId, normalizeDest(f));
            QString cleanDest;
            if (!cleanRelativePath(destSub, cleanDest, true))
                unsafePath("destination", f.destination);
            QString target = destSub.isEmpty() ? m_dst + "/" + QFileInfo(absSource).fileName()
                                               : QDir(m_dst).absoluteFilePath(cleanDest);
            if (!pathInside(QDir(m_dst).absolutePath(), QDir::cleanPath(target)))
                unsafePath("destination", f.destination);
            copyReplacing(absSource, target);
            ++count;
        }
    }
    return count;
}

int InstallWorker::doLegacyFomod()
{
    int count = 0;
    QDir base(m_modulePath);
    const QString moduleCanonical = QFileInfo(m_modulePath).canonicalFilePath();
    if (moduleCanonical.isEmpty())
        unsafePath("source", m_modulePath);
    QDirIterator it(m_modulePath, QDir::Files | QDir::Dirs | QDir::NoDotAndDotDot,
                    QDirIterator::Subdirectories);
    while (it.hasNext()) {
        if (m_cancel.load())
            return count;
        it.next();
        QString rel = base.relativeFilePath(it.filePath());
        QString lowerRel = rel.toLower();
        if (lowerRel == "fomod" || lowerRel.startsWith("fomod/"))
            continue;
        if (lowerRel.endsWith(".cs"))
            continue;

        QString routed = routedPath(m_gameId, rel);
        QString clean;
        if (!cleanRelativePath(routed, clean, true))
            unsafePath("destination", routed);
        QString target = QDir(m_dst).absoluteFilePath(clean);
        if (!pathInside(QDir(m_dst).absolutePath(), QDir::cleanPath(target)))
            unsafePath("destination", routed);
        if (it.fileInfo().isDir()) {
            ensureDirectory(target);
        } else {
            const QString fileCanonical = it.fileInfo().canonicalFilePath();
            if (fileCanonical.isEmpty() || !pathInside(moduleCanonical, fileCanonical))
                unsafePath("source", rel);
            copyReplacing(fileCanonical, target);
            ++count;
        }
    }
    return count;
}

}
