#pragma once

#include <QWidget>
#include <QHash>
#include <QTimer>

#include "GrpcClient.h"

class QLabel;
class QProgressBar;
class QPushButton;

namespace gorganizer {

// Slim persistent strip at the top of the main window surfacing install activity.
class InstallStatusBanner : public QWidget {
    Q_OBJECT
public:
    explicit InstallStatusBanner(GrpcClient* grpc, QWidget* parent = nullptr);

    // Synthetic pending state for client-side flows that bypass daemon InstallProgress.
    void showFomodPending(const QString& archiveRelPath, const QString& modName);
    void clearUiNotice(const QString& archiveRelPath);

signals:
    void bringFomodToFront();

private slots:
    void onInstallProgress(const GrpcInstallProgress& p);

private:
    struct ActiveInstall {
        QString archiveRelPath;
        QString modName;
        int step = 0;
        int pct = -1;
        qint64 filesDone = 0;
        qint64 filesTotal = 0;
        QString currentFile;
        QString error;
    };

    void redraw();
    void setActive(bool visible);

    GrpcClient* m_grpc = nullptr;
    QHash<QString, ActiveInstall> m_active;
    QString m_focusedKey;
    QTimer* m_autoHide = nullptr;

    QLabel* m_titleLabel = nullptr;
    QLabel* m_detailLabel = nullptr;
    QProgressBar* m_bar = nullptr;
    QPushButton* m_fomodBtn = nullptr;
    QLabel* m_extraBadge = nullptr;
};

} // namespace gorganizer
