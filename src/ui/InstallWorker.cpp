#include "InstallWorker.h"

#include <QDebug>
#include <QDir>
#include <QDirIterator>
#include <QFile>
#include <QFileInfo>
#include <algorithm>

namespace gorganizer {

InstallWorker::InstallWorker(QObject* parent)
    : QObject(parent)
{
    m_cancel.store(false);
}

void InstallWorker::configureRecursive(const QString& src, const QString& dst)
{
    m_mode = Recursive;
    m_src = src;
    m_dst = dst;
}

void InstallWorker::configureFomodSelections(const QString& modulePath,
                                             const QList<FomodFile>& selections,
                                             const QString& destDir)
{
    m_mode = FomodSelections;
    m_modulePath = modulePath;
    m_selections = selections;
    m_dst = destDir;
}

void InstallWorker::configureLegacyFomod(const QString& modulePath, const QString& destDir)
{
    m_mode = LegacyFomod;
    m_modulePath = modulePath;
    m_dst = destDir;
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
        // Catch-all so a non-std exception (foreign library, asm) can't
        // skip the finished() emission and leave the dialog stuck on
        // "Installing… please wait" forever.
        ok = false;
        err = QStringLiteral("install worker: unknown exception");
    }
    // Wrap the finished() emission so an exception in any connected
    // slot (slot calls happen inline for direct connections) cannot
    // tear out of run() without unwinding the QThread cleanly. A
    // QThread that exits via exception terminates the whole process.
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
    QDirIterator it(m_src, QDir::Files | QDir::Dirs | QDir::NoDotAndDotDot,
                    QDirIterator::Subdirectories);
    while (it.hasNext()) {
        if (m_cancel.load())
            return count;
        it.next();
        QString rel = QDir(m_src).relativeFilePath(it.filePath());
        QString destPath = m_dst + "/" + rel;

        if (it.fileInfo().isDir()) {
            QDir().mkpath(destPath);
        } else {
            QDir().mkpath(QFileInfo(destPath).path());
            QFile::copy(it.filePath(), destPath);
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

        QString normSource = f.source;
        normSource.replace('\\', '/');
        QString absSource = m_modulePath + "/" + normSource;

        if (f.isFolder) {
            QDir srcDir(absSource);
            if (!srcDir.exists()) continue;
            QString destRoot = m_dst;
            QString destSub = normalizeDest(f);
            if (!destSub.isEmpty())
                destRoot = m_dst + "/" + destSub;
            QDir().mkpath(destRoot);

            QDirIterator it(absSource, QDir::Files | QDir::Dirs | QDir::NoDotAndDotDot,
                            QDirIterator::Subdirectories);
            while (it.hasNext()) {
                if (m_cancel.load())
                    return count;
                it.next();
                QString rel = srcDir.relativeFilePath(it.filePath());
                QString target = destRoot + "/" + rel;
                if (it.fileInfo().isDir()) {
                    QDir().mkpath(target);
                } else {
                    QDir().mkpath(QFileInfo(target).absolutePath());
                    QFile::remove(target);
                    if (QFile::copy(it.filePath(), target))
                        ++count;
                }
            }
        } else {
            if (!QFile::exists(absSource)) continue;
            QString destSub = normalizeDest(f);
            QString target = destSub.isEmpty() ? m_dst + "/" + QFileInfo(absSource).fileName()
                                               : m_dst + "/" + destSub;
            QDir().mkpath(QFileInfo(target).absolutePath());
            QFile::remove(target);
            if (QFile::copy(absSource, target))
                ++count;
        }
    }
    return count;
}

int InstallWorker::doLegacyFomod()
{
    int count = 0;
    QDir base(m_modulePath);
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

        QString target = m_dst + "/" + rel;
        if (it.fileInfo().isDir()) {
            QDir().mkpath(target);
        } else {
            QDir().mkpath(QFileInfo(target).absolutePath());
            QFile::remove(target);
            if (QFile::copy(it.filePath(), target))
                ++count;
        }
    }
    return count;
}

} // namespace gorganizer
