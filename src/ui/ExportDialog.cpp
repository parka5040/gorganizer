#include "ExportDialog.h"
#include "Dialogs.h"
#include "GrpcClient.h"
#include "ThemeManager.h"

#include <QCheckBox>
#include <QCloseEvent>
#include <QDir>
#include <QFileDialog>
#include <QGroupBox>
#include <QHBoxLayout>
#include <QLabel>
#include <QLineEdit>
#include <QListWidget>
#include <QListWidgetItem>
#include <QProgressBar>
#include <QPushButton>
#include <QStackedWidget>
#include <QVBoxLayout>

namespace gorganizer {

namespace {

constexpr int PAGE_CONFIG = 0;
constexpr int PAGE_PROGRESS = 1;

QString errHex() { return ThemeManager::currentPalette().errorFg.name(); }

QString humanBytes(int64_t b)
{
    if (b < 0) return "?";
    static const char* units[] = {"B", "KB", "MB", "GB", "TB"};
    int u = 0;
    double v = static_cast<double>(b);
    while (v >= 1024.0 && u + 1 < int(sizeof(units) / sizeof(units[0]))) {
        v /= 1024.0;
        ++u;
    }
    return QString("%1 %2").arg(v, 0, 'f', 1).arg(units[u]);
}

}

ExportDialog::ExportDialog(GrpcClient* grpc, const QString& gameId, QWidget* parent)
    : QDialog(parent)
    , m_grpc(grpc)
    , m_gameId(gameId)
{
    setWindowTitle(QString("Export Mods — %1").arg(m_gameId));
    resize(640, 560);
    setModal(true);

    auto* root = new QVBoxLayout(this);
    m_stack = new QStackedWidget;
    m_stack->addWidget(buildConfigPage());
    m_stack->addWidget(buildProgressPage());
    root->addWidget(m_stack, 1);

    connect(m_grpc, &GrpcClient::transferProgress, this, &ExportDialog::onTransferProgress);
    connect(m_grpc, &GrpcClient::transferCompleted, this, &ExportDialog::onTransferCompleted);
    connect(m_grpc, &GrpcClient::transferFailed, this, &ExportDialog::onTransferFailed);

    loadSelections();
    m_stack->setCurrentIndex(PAGE_CONFIG);
}

QWidget* ExportDialog::buildConfigPage()
{
    auto* page = new QWidget;
    auto* lay = new QVBoxLayout(page);

    auto* profileBox = new QGroupBox("Profiles");
    auto* profileLay = new QVBoxLayout(profileBox);
    m_profileList = new QListWidget;
    m_profileList->setSelectionMode(QAbstractItemView::NoSelection);
    profileLay->addWidget(m_profileList);
    lay->addWidget(profileBox, 1);

    auto* modBox = new QGroupBox("Mods");
    auto* modLay = new QVBoxLayout(modBox);
    m_modList = new QListWidget;
    m_modList->setSelectionMode(QAbstractItemView::NoSelection);
    modLay->addWidget(m_modList, 1);

    auto* modBtnRow = new QHBoxLayout;
    auto* allBtn = new QPushButton("Select All");
    auto* noneBtn = new QPushButton("Select None");
    m_selectionLabel = new QLabel;
    modBtnRow->addWidget(allBtn);
    modBtnRow->addWidget(noneBtn);
    modBtnRow->addStretch(1);
    modBtnRow->addWidget(m_selectionLabel);
    modLay->addLayout(modBtnRow);
    lay->addWidget(modBox, 2);

    m_overwriteCheck = new QCheckBox("Include Overwrite folder");
    m_overwriteCheck->setChecked(true);
    lay->addWidget(m_overwriteCheck);

    m_settingsCheck = new QCheckBox("Include game settings (profile INIs, load order)");
    m_settingsCheck->setChecked(true);
    lay->addWidget(m_settingsCheck);

    auto* destRow = new QHBoxLayout;
    destRow->addWidget(new QLabel("Destination:"));
    m_destinationEdit = new QLineEdit;
    m_destinationEdit->setText(QDir::homePath() + "/" + m_gameId
                               + "-export.gorganizer-export.tar.zst");
    auto* browseBtn = new QPushButton("Browse...");
    destRow->addWidget(m_destinationEdit, 1);
    destRow->addWidget(browseBtn);
    lay->addLayout(destRow);

    m_configErrorLabel = new QLabel;
    m_configErrorLabel->setWordWrap(true);
    m_configErrorLabel->setStyleSheet(QString("color: %1;").arg(errHex()));
    m_configErrorLabel->setVisible(false);
    lay->addWidget(m_configErrorLabel);

    auto* btnRow = new QHBoxLayout;
    m_closeConfigBtn = new QPushButton("Close");
    m_startBtn = new QPushButton("Export");
    m_startBtn->setDefault(true);
    btnRow->addWidget(m_closeConfigBtn);
    btnRow->addStretch(1);
    btnRow->addWidget(m_startBtn);
    lay->addLayout(btnRow);

    connect(allBtn, &QPushButton::clicked, this, [this] { setAllMods(true); });
    connect(noneBtn, &QPushButton::clicked, this, [this] { setAllMods(false); });
    connect(browseBtn, &QPushButton::clicked, this, &ExportDialog::onBrowseDestination);
    connect(m_destinationEdit, &QLineEdit::textChanged, this, &ExportDialog::updateStartEnabled);
    connect(m_closeConfigBtn, &QPushButton::clicked, this, &ExportDialog::reject);
    connect(m_startBtn, &QPushButton::clicked, this, &ExportDialog::onStartExport);
    connect(m_modList, &QListWidget::itemChanged, this, &ExportDialog::updateStartEnabled);
    connect(m_profileList, &QListWidget::itemChanged, this, &ExportDialog::updateStartEnabled);

    return page;
}

QWidget* ExportDialog::buildProgressPage()
{
    auto* page = new QWidget;
    auto* lay = new QVBoxLayout(page);

    m_stepLabel = new QLabel("Preparing export...");
    lay->addWidget(m_stepLabel);

    m_itemLabel = new QLabel;
    m_itemLabel->setWordWrap(true);
    lay->addWidget(m_itemLabel);

    m_progressBar = new QProgressBar;
    m_progressBar->setRange(0, 0);
    lay->addWidget(m_progressBar);

    m_bytesLabel = new QLabel;
    lay->addWidget(m_bytesLabel);

    m_resultLabel = new QLabel;
    m_resultLabel->setWordWrap(true);
    m_resultLabel->setTextInteractionFlags(Qt::TextSelectableByMouse);
    m_resultLabel->setVisible(false);
    lay->addWidget(m_resultLabel);

    lay->addStretch(1);

    auto* btnRow = new QHBoxLayout;
    m_backBtn = new QPushButton("Back");
    m_backBtn->setVisible(false);
    m_closeBtn = new QPushButton("Close");
    m_closeBtn->setVisible(false);
    m_cancelBtn = new QPushButton("Cancel");
    btnRow->addWidget(m_backBtn);
    btnRow->addStretch(1);
    btnRow->addWidget(m_closeBtn);
    btnRow->addWidget(m_cancelBtn);
    lay->addLayout(btnRow);

    connect(m_backBtn, &QPushButton::clicked, this, &ExportDialog::onBackToConfig);
    connect(m_closeBtn, &QPushButton::clicked, this, &ExportDialog::accept);
    connect(m_cancelBtn, &QPushButton::clicked, this, &ExportDialog::onCancelTransfer);

    return page;
}

// Fills the profile and mod checklists from synchronous daemon queries; everything starts checked.
void ExportDialog::loadSelections()
{
    QString err;
    std::vector<GrpcProfile> profiles;
    if (!m_grpc->listProfilesSync(m_gameId, profiles, err))
        m_loadError = QString("Could not list profiles: %1").arg(err);
    for (const auto& p : profiles) {
        auto* item = new QListWidgetItem(p.name, m_profileList);
        item->setFlags(item->flags() | Qt::ItemIsUserCheckable);
        item->setCheckState(Qt::Checked);
    }

    if (!m_grpc->listModsSync(m_gameId, m_mods, err))
        m_loadError = QString("Could not list mods: %1").arg(err);
    for (const auto& m : m_mods) {
        auto* item = new QListWidgetItem(
            QString("%1  (%2 files, %3)").arg(m.name).arg(m.fileCount).arg(humanBytes(m.totalSize)),
            m_modList);
        item->setData(Qt::UserRole, m.name);
        item->setFlags(item->flags() | Qt::ItemIsUserCheckable);
        item->setCheckState(Qt::Checked);
    }

    updateStartEnabled();
}

void ExportDialog::setAllMods(bool checked)
{
    for (int i = 0; i < m_modList->count(); ++i)
        m_modList->item(i)->setCheckState(checked ? Qt::Checked : Qt::Unchecked);
}

// Recomputes the selection summary and gates Export on a valid, expressible selection.
void ExportDialog::updateStartEnabled()
{
    int modsChecked = 0;
    int64_t bytesChecked = 0;
    for (int i = 0; i < m_modList->count(); ++i) {
        if (m_modList->item(i)->checkState() != Qt::Checked) continue;
        ++modsChecked;
        if (i < static_cast<int>(m_mods.size()))
            bytesChecked += m_mods[i].totalSize;
    }
    int profilesChecked = 0;
    for (int i = 0; i < m_profileList->count(); ++i)
        if (m_profileList->item(i)->checkState() == Qt::Checked) ++profilesChecked;

    m_selectionLabel->setText(QString("%1 of %2 mods (%3)")
                                  .arg(modsChecked)
                                  .arg(m_modList->count())
                                  .arg(humanBytes(bytesChecked)));

    const bool modsOk = modsChecked > 0 || m_modList->count() == 0;
    const bool profilesOk = profilesChecked > 0 || m_profileList->count() == 0;
    const bool destOk = !m_destinationEdit->text().trimmed().isEmpty();
    m_startBtn->setEnabled(m_loadError.isEmpty() && modsOk && profilesOk && destOk);
    if (!m_loadError.isEmpty()) {
        m_configErrorLabel->setText(m_loadError);
        m_configErrorLabel->setVisible(true);
    } else if (!modsOk || !profilesOk) {
        m_configErrorLabel->setText("Select at least one mod and one profile to export.");
        m_configErrorLabel->setVisible(true);
    } else {
        m_configErrorLabel->setVisible(false);
    }
}

QStringList ExportDialog::checkedItems(const QListWidget* list) const
{
    QStringList out;
    for (int i = 0; i < list->count(); ++i) {
        const auto* item = list->item(i);
        if (item->checkState() != Qt::Checked) continue;
        const QVariant folder = item->data(Qt::UserRole);
        out << (folder.isValid() ? folder.toString() : item->text());
    }
    return out;
}

void ExportDialog::onBrowseDestination()
{
    QString path = QFileDialog::getSaveFileName(
        this, "Export Destination", m_destinationEdit->text(),
        "Gorganizer export (*.gorganizer-export.tar.zst);;All files (*)");
    if (path.isEmpty()) return;
    if (!path.endsWith(".gorganizer-export.tar.zst"))
        path += ".gorganizer-export.tar.zst";
    m_destinationEdit->setText(path);
}

void ExportDialog::onStartExport()
{
    if (m_running) return;
    m_running = true;
    m_cancelRequested = false;

    m_stepLabel->setText("Starting export...");
    m_itemLabel->clear();
    m_bytesLabel->clear();
    m_resultLabel->setVisible(false);
    m_progressBar->setRange(0, 0);
    m_backBtn->setVisible(false);
    m_closeBtn->setVisible(false);
    m_cancelBtn->setVisible(true);
    m_cancelBtn->setEnabled(true);
    m_stack->setCurrentIndex(PAGE_PROGRESS);

    m_grpc->startExport(m_gameId, m_destinationEdit->text().trimmed(),
                        checkedItems(m_modList), checkedItems(m_profileList),
                        m_overwriteCheck->isChecked(), m_settingsCheck->isChecked());
}

void ExportDialog::onCancelTransfer()
{
    if (!m_running) return;
    m_cancelRequested = true;
    m_cancelBtn->setEnabled(false);
    m_stepLabel->setText("Cancelling...");
    m_grpc->cancelTransfer();
}

void ExportDialog::onBackToConfig()
{
    m_stack->setCurrentIndex(PAGE_CONFIG);
}

void ExportDialog::onTransferProgress(const GrpcTransferProgress& progress)
{
    if (!m_running) return;
    m_stepLabel->setText(QString("Step: %1").arg(progress.step));
    m_itemLabel->setText(progress.currentItem);
    if (progress.itemsTotal > 0) {
        m_progressBar->setRange(0, progress.itemsTotal);
        m_progressBar->setValue(progress.itemsDone);
    }
    if (progress.bytesTotal > 0)
        m_bytesLabel->setText(QString("%1 / %2").arg(humanBytes(progress.bytesDone),
                                                     humanBytes(progress.bytesTotal)));
    else if (progress.bytesDone > 0)
        m_bytesLabel->setText(humanBytes(progress.bytesDone));
}

void ExportDialog::onTransferCompleted(const GrpcTransferSummary& summary)
{
    if (!m_running) return;
    m_running = false;
    m_progressBar->setRange(0, 100);
    m_progressBar->setValue(100);
    m_stepLabel->setText("Export complete.");
    m_itemLabel->clear();
    m_resultLabel->setStyleSheet(QString());
    m_resultLabel->setText(QString("Exported %1 mod(s) and %2 profile(s) to:\n%3")
                               .arg(summary.modsExported)
                               .arg(summary.profilesTransferred)
                               .arg(summary.outputPath));
    m_resultLabel->setVisible(true);
    m_cancelBtn->setVisible(false);
    m_closeBtn->setVisible(true);
    m_closeBtn->setDefault(true);
}

void ExportDialog::onTransferFailed(const QString& error)
{
    if (!m_running) return;
    m_running = false;
    m_progressBar->setRange(0, 100);
    m_progressBar->setValue(0);
    if (m_cancelRequested) {
        m_stepLabel->setText("Export cancelled.");
        m_resultLabel->setStyleSheet(QString());
        m_resultLabel->setText("The export was cancelled. No archive was produced.");
    } else {
        m_stepLabel->setText("Export failed.");
        m_resultLabel->setStyleSheet(QString("color: %1;").arg(errHex()));
        m_resultLabel->setText(error);
    }
    m_resultLabel->setVisible(true);
    m_cancelBtn->setVisible(false);
    m_backBtn->setVisible(true);
    m_closeBtn->setVisible(true);
}

bool ExportDialog::confirmAbortWhileRunning()
{
    if (!m_running) return true;
    if (!dialogs::confirmWarn(this, "Cancel export?",
            "An export is still running. Close and cancel it?",
            QMessageBox::No))
        return false;
    m_cancelRequested = true;
    m_grpc->cancelTransfer();
    return true;
}

void ExportDialog::closeEvent(QCloseEvent* ev)
{
    if (!confirmAbortWhileRunning()) {
        ev->ignore();
        return;
    }
    QDialog::closeEvent(ev);
}

void ExportDialog::reject()
{
    if (!confirmAbortWhileRunning())
        return;
    QDialog::reject();
}

}
