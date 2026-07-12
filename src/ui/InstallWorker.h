#pragma once

#include <QObject>
#include <QString>
#include <QList>
#include <atomic>
#include "FomodPlan.h"

namespace gorganizer {

class InstallWorker : public QObject {
    Q_OBJECT
public:
    enum Mode { Recursive, FomodSelections, LegacyFomod };

    explicit InstallWorker(QObject* parent = nullptr);

    void configureRecursive(const QString& src, const QString& dst, const QString& gameId);
    void configureFomodSelections(const QString& modulePath,
                                  const QList<FomodFile>& selections,
                                  const QString& destDir, const QString& gameId);
    void configureLegacyFomod(const QString& modulePath, const QString& destDir,
                              const QString& gameId);

    void cancel();
    bool isCancelled() const { return m_cancel.load(); }

public slots:
    void run();

signals:
    void finished(bool ok, bool cancelled, int fileCount, const QString& err);

private:
    int doRecursive();
    int doFomodSelections();
    int doLegacyFomod();

    Mode m_mode = Recursive;
    QString m_src;
    QString m_dst;
    QString m_modulePath;
    QString m_gameId;
    QList<FomodFile> m_selections;
    std::atomic<bool> m_cancel;
};

}
