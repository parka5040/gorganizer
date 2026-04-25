#include "VfsControlWidget.h"

#include <QHBoxLayout>
#include <filesystem>

namespace gorganizer {

VfsControlWidget::VfsControlWidget(GrpcClient* grpc, QWidget* parent)
    : QWidget(parent)
    , m_grpc(grpc)
{
    auto* layout = new QHBoxLayout(this);
    layout->setContentsMargins(0, 0, 0, 0);

    m_toggleBtn = new QPushButton("Mount VFS");
    m_toggleBtn->setEnabled(false);
    m_statusLabel = new QLabel("No game selected");

    layout->addWidget(m_toggleBtn);
    layout->addWidget(m_statusLabel);

    connect(m_toggleBtn, &QPushButton::clicked, this, &VfsControlWidget::onToggleClicked);
    connect(m_grpc, &GrpcClient::vfsMounted, this, &VfsControlWidget::onVfsMounted);
    connect(m_grpc, &GrpcClient::vfsUnmounted, this, &VfsControlWidget::onVfsUnmounted);
    connect(m_grpc, &GrpcClient::vfsStatusReceived, this, &VfsControlWidget::onVfsStatusReceived);
    connect(m_grpc, &GrpcClient::vfsStatusChanged, this, &VfsControlWidget::onVfsStatusChanged);
    connect(m_grpc, &GrpcClient::rpcError, this, &VfsControlWidget::onRpcError);
}

void VfsControlWidget::setGame(const GameInfo& game, const QString& profileName)
{
    m_game = game;
    m_profileName = profileName;

    if (!game.detected) {
        m_toggleBtn->setEnabled(false);
        m_statusLabel->setText("No game selected");
        return;
    }

    checkBackupState();

    if (!m_blocked)
        m_grpc->getVfsStatus(game.shortName);
}

void VfsControlWidget::checkBackupState()
{
    if (!m_game.detected) {
        m_blocked = false;
        return;
    }

    // Check if Data.orig/ exists on disk — indicates a stale backup from
    // a crash or another mount manager. The button should be disabled to
    // prevent the daemon from failing with ErrBackupExists.
    std::filesystem::path origPath = m_game.dataDir;
    origPath += ".orig";
    m_blocked = std::filesystem::exists(origPath) && std::filesystem::is_directory(origPath);

    if (m_blocked) {
        m_toggleBtn->setEnabled(false);
        m_toggleBtn->setText("Mount VFS");
        m_statusLabel->setText("Blocked: Data.orig/ exists. Start daemon to recover.");
    }
}

void VfsControlWidget::onToggleClicked()
{
    if (m_game.shortName.isEmpty() || m_blocked)
        return;

    if (m_mounted) {
        setOperating(true, "Unmounting...");
        m_grpc->unmountVfs(m_game.shortName);
    } else {
        setOperating(true, "Mounting...");
        m_grpc->mountVfs(m_game.shortName, m_profileName);
    }
}

void VfsControlWidget::onVfsMounted(const GrpcVFSStatus& status)
{
    updateDisplay(status);
}

void VfsControlWidget::onVfsUnmounted()
{
    m_mounted = false;
    m_blocked = false;
    m_toggleBtn->setEnabled(true);
    m_toggleBtn->setText("Mount VFS");
    m_statusLabel->setText("Unmounted successfully");
}

void VfsControlWidget::onVfsStatusReceived(const GrpcVFSStatus& status)
{
    updateDisplay(status);
}

void VfsControlWidget::onVfsStatusChanged(const GrpcVFSStatus& status)
{
    updateDisplay(status);
}

void VfsControlWidget::onRpcError(const QString& method, const QString& error)
{
    if (method != "MountVFS" && method != "UnmountVFS")
        return;

    m_toggleBtn->setEnabled(true);
    m_statusLabel->setText(error);

    // Re-check backup state in case mount failed because of it.
    checkBackupState();
}

void VfsControlWidget::updateDisplay(const GrpcVFSStatus& status)
{
    m_mounted = status.mounted;
    m_blocked = false;
    m_toggleBtn->setEnabled(true);

    if (m_mounted) {
        m_toggleBtn->setText("Unmount VFS");
        m_statusLabel->setText(QString("Mounted: %1 mods, %2 files")
                                   .arg(status.enabledModCount)
                                   .arg(status.totalFileCount));
    } else {
        m_toggleBtn->setText("Mount VFS");
        m_statusLabel->setText("Not mounted");
        checkBackupState();
    }
}

void VfsControlWidget::setOperating(bool busy, const QString& text)
{
    m_toggleBtn->setEnabled(!busy);
    m_statusLabel->setText(text);
}

} // namespace gorganizer
