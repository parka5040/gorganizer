#pragma once

#include <QWidget>
#include <QPushButton>
#include <QLabel>
#include "GrpcClient.h"
#include "GameInfo.h"

namespace gorganizer {

class VfsControlWidget : public QWidget {
    Q_OBJECT
public:
    explicit VfsControlWidget(GrpcClient* grpc, QWidget* parent = nullptr);

    void setGame(const GameInfo& game, const QString& profileName);

private slots:
    void onToggleClicked();
    void onVfsMounted(const GrpcVFSStatus& status);
    void onVfsUnmounted();
    void onVfsStatusReceived(const GrpcVFSStatus& status);
    void onVfsStatusChanged(const GrpcVFSStatus& status);
    void onRpcError(const QString& method, const QString& error);

private:
    void updateDisplay(const GrpcVFSStatus& status);
    void setOperating(bool busy, const QString& text);
    void checkBackupState();

    GrpcClient* m_grpc;
    QPushButton* m_toggleBtn;
    QLabel* m_statusLabel;
    GameInfo m_game;
    QString m_profileName;
    bool m_mounted = false;
    bool m_blocked = false;
};

} // namespace gorganizer
