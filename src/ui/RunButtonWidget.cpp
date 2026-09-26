#include "RunButtonWidget.h"

#include <QHBoxLayout>
#include <QStandardItemModel>
#include <QVariant>
#include <filesystem>

namespace gorganizer {

namespace {

struct ToolEntry {
    QString gameShortName;
    QString toolId;
    QString displayName;
    QString loaderExe;
};

QList<ToolEntry> toolsFor(const QString& gameShortName)
{
    QList<ToolEntry> out;
    auto game = GameInfo::findByShortName(gameShortName);
    if (game && !game->seToolId.isEmpty())
        out.append({gameShortName, game->seToolId, game->seDisplayName, game->seLoaderExe});
    return out;
}

bool toolInstalled(const std::filesystem::path& installDir, const ToolEntry& t)
{
    if (installDir.empty())
        return false;
    std::error_code ec;
    return std::filesystem::exists(installDir / t.loaderExe.toStdString(), ec);
}

const QString kInstallModLoaderId = QStringLiteral("smapi-install");
const QString kRepairModLoaderId = QStringLiteral("smapi-repair");
const QString kUnsupportedBuildTip = QStringLiteral(
    "This install is not the native Linux Steam build (for example a Windows build run through Proton). "
    "gorganizer manages SMAPI only for the native Linux build.");

}

RunButtonWidget::RunButtonWidget(QWidget* parent)
    : QWidget(parent)
{
    auto* layout = new QHBoxLayout(this);
    layout->setContentsMargins(0, 0, 0, 0);
    layout->setSpacing(0);

    m_combo = new QComboBox;
    m_combo->setMinimumWidth(160);
    m_combo->setToolTip(
        "Choose what the Run button launches — the game directly, or a "
        "script extender (xNVSE/SKSE64/F4SE/…).");
    layout->addWidget(m_combo);

    m_runBtn = new QToolButton;
    m_runBtn->setText("Run");
    m_runBtn->setMinimumWidth(140);
    layout->addWidget(m_runBtn);

    connect(m_runBtn, &QToolButton::clicked, this, [this]() { emit runRequested(); });
    connect(m_combo, &QComboBox::currentIndexChanged, this, [this](int) {
        syncRunLabel();
        auto t = currentTarget();
        emit targetChanged(t.toolId);
    });

    rebuildCombo(QString());
}

void RunButtonWidget::setGame(const GameInfo& game, const QString& preferredToolId)
{
    if (game.shortName != m_game.shortName || !managesSmapi(game)) {
        m_modLoaderStatus = GrpcModLoaderStatus{};
        m_hasModLoaderStatus = false;
    }
    m_game = game;
    m_lastPreferredToolId = preferredToolId;
    rebuildCombo(preferredToolId);
}

void RunButtonWidget::setFourGBPatched(bool patched)
{
    if (m_fourGBPatched == patched) return;
    m_fourGBPatched = patched;
    rebuildCombo(m_lastPreferredToolId);
}

void RunButtonWidget::setTTWVfsActive(bool active)
{
    if (m_ttwVfsActive == active) return;
    m_ttwVfsActive = active;
    rebuildCombo(m_lastPreferredToolId);
}

void RunButtonWidget::setModLoaderStatus(const GrpcModLoaderStatus& status)
{
    if (!managesSmapi(m_game) || status.gameId != m_game.shortName)
        return;
    m_modLoaderStatus = status;
    m_hasModLoaderStatus = true;
    rebuildKeepingSelection();
}

void RunButtonWidget::clearModLoaderStatus()
{
    if (!m_hasModLoaderStatus)
        return;
    m_modLoaderStatus = GrpcModLoaderStatus{};
    m_hasModLoaderStatus = false;
    rebuildKeepingSelection();
}

void RunButtonWidget::rebuildKeepingSelection()
{
    const QString selected = currentTarget().toolId;
    rebuildCombo(selected.isEmpty() ? m_lastPreferredToolId : selected);
}

bool RunButtonWidget::modLoaderStateIs(GrpcModLoaderState state) const
{
    return m_hasModLoaderStatus && managesSmapi(m_game) && m_modLoaderStatus.gameId == m_game.shortName
        && m_modLoaderStatus.state == state;
}

void RunButtonWidget::rebuildCombo(const QString& preferredToolId)
{
    m_combo->blockSignals(true);
    m_combo->clear();

    QString gameLabel = m_game.detected ? m_game.name : QString("Game");
    auto* itemModel = qobject_cast<QStandardItemModel*>(m_combo->model());

    if (m_game.synthetic) {
        bool nvseInstalled = false;
        if (!m_game.installDir.empty()) {
            std::error_code ec;
            nvseInstalled = std::filesystem::exists(m_game.installDir / "nvse_loader.exe", ec);
        }
        if (nvseInstalled) {
            Target t;
            t.type = TargetTool;
            t.toolId = "xnvse";
            t.label = "Run nvse_loader.exe";
            m_combo->addItem(t.label, t.toolId);
            m_combo->setItemData(0, int(TargetTool), TargetTypeRole);
        } else {
            Target t;
            t.type = TargetInstallTool;
            t.toolId = "xnvse";
            t.label = "Install xNVSE...";
            m_combo->addItem(t.label, t.toolId);
            m_combo->setItemData(0, int(TargetInstallTool), TargetTypeRole);
        }
        m_combo->blockSignals(false);
        syncRunLabel();
        return;
    }

    {
        Target t;
        t.type = TargetGame;
        t.label = modLoaderStateIs(GrpcModLoaderStateOk) ? QString("Launch %1 (SMAPI)").arg(gameLabel)
                                                         : QString("Launch %1").arg(gameLabel);
        t.toolId = "";
        m_combo->addItem(t.label, QVariant::fromValue(t.toolId));
        m_combo->setItemData(0, int(TargetGame), TargetTypeRole);
        if (modLoaderStateIs(GrpcModLoaderStateUnsupportedBuild))
            m_combo->setItemData(0, kUnsupportedBuildTip, Qt::ToolTipRole);

        if (m_game.shortName == "falloutnv" && m_game.vfsActive == false
            && m_ttwVfsActive && itemModel) {
            if (auto* item = itemModel->item(0)) {
                item->setFlags(item->flags() & ~Qt::ItemIsEnabled);
                item->setToolTip(
                    "Tale of Two Wastelands is currently active. Switch "
                    "active game to deactivate it first.");
            }
        }
    }

    if (modLoaderStateIs(GrpcModLoaderStateNotInstalled)) {
        m_combo->addItem("Install SMAPI…", kInstallModLoaderId);
        m_combo->setItemData(m_combo->count() - 1, int(TargetInstallModLoader), TargetTypeRole);
    } else if (modLoaderStateIs(GrpcModLoaderStateLauncherReverted)
               || modLoaderStateIs(GrpcModLoaderStateIncomplete)
               || modLoaderStateIs(GrpcModLoaderStateInterrupted)) {
        m_combo->addItem("Repair SMAPI…", kRepairModLoaderId);
        m_combo->setItemData(m_combo->count() - 1, int(TargetRepairModLoader), TargetTypeRole);
    }

    for (const auto& t : toolsFor(m_game.shortName)) {
        Target target;
        target.toolId = t.toolId;
        if (toolInstalled(m_game.installDir, t)) {
            target.type = TargetTool;
            target.label = QString("Run %1").arg(t.displayName);
        } else {
            target.type = TargetInstallTool;
            target.label = QString("Install %1...").arg(t.displayName);
        }
        int row = m_combo->count();
        m_combo->addItem(target.label, target.toolId);
        m_combo->setItemData(row, int(target.type), TargetTypeRole);

        if (m_fourGBPatched
            && m_game.shortName == "falloutnv"
            && target.toolId == "xnvse"
            && itemModel != nullptr) {
            if (auto* item = itemModel->item(row)) {
                item->setFlags(item->flags() & ~Qt::ItemIsEnabled);
                item->setToolTip(QStringLiteral("Launch through Launcher"));
            }
        }
    }

    auto rowIsEnabled = [&](int row) -> bool {
        if (!itemModel) return true;
        auto* item = itemModel->item(row);
        return item == nullptr || (item->flags() & Qt::ItemIsEnabled);
    };

    if (!preferredToolId.isEmpty()) {
        int idx = m_combo->findData(preferredToolId);
        if (idx >= 0)
            m_combo->setCurrentIndex(idx);
    } else {
        for (int i = 0; i < m_combo->count(); ++i) {
            auto type = static_cast<TargetType>(
                m_combo->itemData(i, TargetTypeRole).toInt());
            if (type == TargetTool && rowIsEnabled(i)) {
                m_combo->setCurrentIndex(i);
                break;
            }
        }
    }

    if (!rowIsEnabled(m_combo->currentIndex()))
        m_combo->setCurrentIndex(0);

    m_combo->blockSignals(false);
    syncRunLabel();
}

void RunButtonWidget::syncRunLabel()
{
    auto t = currentTarget();
    switch (t.type) {
        case TargetGame:
            if (!m_game.detected)
                m_runBtn->setText("Run");
            else if (modLoaderStateIs(GrpcModLoaderStateOk))
                m_runBtn->setText(QString("Run %1 (SMAPI)").arg(m_game.name));
            else
                m_runBtn->setText(QString("Run %1").arg(m_game.name));
            if (modLoaderStateIs(GrpcModLoaderStateUnsupportedBuild))
                m_runBtn->setToolTip(kUnsupportedBuildTip);
            else if (managesSmapi(m_game))
                m_runBtn->setToolTip("Launch through Steam; enabled mods are deployed into the game's Mods folder first.");
            else
                m_runBtn->setToolTip("Launch through Steam; the mod hardlink farm + plugins.txt are deployed first.");
            break;
        case TargetTool:
            m_runBtn->setText(t.label);
            m_runBtn->setToolTip("Launch the selected script extender directly via Proton.");
            break;
        case TargetInstallTool:
            m_runBtn->setText(t.label);
            m_runBtn->setToolTip(
                "Fetches the script extender from Nexus Mods and installs it "
                "into the game's folder. Requires a Nexus API key.");
            break;
        case TargetInstallModLoader:
            m_runBtn->setText(t.label);
            m_runBtn->setToolTip(
                "Downloads the newest stable SMAPI release from GitHub, verifies its SHA-256 "
                "checksum, and installs it into the game's folder.");
            break;
        case TargetRepairModLoader:
            m_runBtn->setText(t.label);
            m_runBtn->setToolTip(
                "Reinstalls SMAPI's launcher and files, which a Steam update or file "
                "verification can revert.");
            break;
    }
}

RunButtonWidget::Target RunButtonWidget::currentTarget() const
{
    Target t;
    int idx = m_combo->currentIndex();
    if (idx < 0) return t;
    t.toolId = m_combo->itemData(idx).toString();
    t.type = static_cast<TargetType>(m_combo->itemData(idx, TargetTypeRole).toInt());
    t.label = m_combo->itemText(idx);
    return t;
}

bool RunButtonWidget::useToolEnabled() const
{
    return currentTarget().type == TargetTool;
}

}
