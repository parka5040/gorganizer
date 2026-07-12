#include "ImportDialog.h"
#include "Dialogs.h"
#include "GrpcClient.h"
#include "ThemeManager.h"

#include <QApplication>
#include <QCloseEvent>
#include <QDir>
#include <QFileDialog>
#include <QGroupBox>
#include <QHBoxLayout>
#include <QLabel>
#include <QLineEdit>
#include <QProgressBar>
#include <QPushButton>
#include <QRadioButton>
#include <QStackedWidget>
#include <QTreeWidget>
#include <QTreeWidgetItem>
#include <QVBoxLayout>

namespace gorganizer {

namespace {

constexpr int PAGE_ARCHIVE = 0;
constexpr int PAGE_SELECTION = 1;
constexpr int PAGE_PROGRESS = 2;

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

ImportDialog::ImportDialog(GrpcClient* grpc, const QString& gameId, QWidget* parent)
    : QDialog(parent)
    , m_grpc(grpc)
    , m_gameId(gameId)
{
    setWindowTitle(QString("Import Mods — %1").arg(m_gameId));
    resize(680, 600);
    setModal(true);

    auto* root = new QVBoxLayout(this);
    m_stack = new QStackedWidget;
    m_stack->addWidget(buildArchivePage());
    m_stack->addWidget(buildSelectionPage());
    m_stack->addWidget(buildProgressPage());
    root->addWidget(m_stack, 1);

    connect(m_grpc, &GrpcClient::transferProgress, this, &ImportDialog::onTransferProgress);
    connect(m_grpc, &GrpcClient::transferCompleted, this, &ImportDialog::onTransferCompleted);
    connect(m_grpc, &GrpcClient::transferFailed, this, &ImportDialog::onTransferFailed);

    m_stack->setCurrentIndex(PAGE_ARCHIVE);
}

QWidget* ImportDialog::buildArchivePage()
{
    auto* page = new QWidget;
    auto* lay = new QVBoxLayout(page);

    auto* intro = new QLabel(
        "Pick a gorganizer export archive to import into this game. "
        "Nothing is written until you review the contents and start the import.");
    intro->setWordWrap(true);
    lay->addWidget(intro);

    auto* row = new QHBoxLayout;
    row->addWidget(new QLabel("Archive:"));
    m_archiveEdit = new QLineEdit;
    auto* browseBtn = new QPushButton("Browse...");
    row->addWidget(m_archiveEdit, 1);
    row->addWidget(browseBtn);
    lay->addLayout(row);

    m_archiveErrorLabel = new QLabel;
    m_archiveErrorLabel->setWordWrap(true);
    m_archiveErrorLabel->setStyleSheet(QString("color: %1;").arg(errHex()));
    m_archiveErrorLabel->setVisible(false);
    lay->addWidget(m_archiveErrorLabel);

    lay->addStretch(1);

    auto* btnRow = new QHBoxLayout;
    auto* closeBtn = new QPushButton("Close");
    m_previewBtn = new QPushButton("Preview");
    m_previewBtn->setDefault(true);
    m_previewBtn->setEnabled(false);
    btnRow->addWidget(closeBtn);
    btnRow->addStretch(1);
    btnRow->addWidget(m_previewBtn);
    lay->addLayout(btnRow);

    connect(browseBtn, &QPushButton::clicked, this, &ImportDialog::onBrowseArchive);
    connect(m_archiveEdit, &QLineEdit::textChanged, this, [this](const QString& text) {
        m_previewBtn->setEnabled(!text.trimmed().isEmpty());
    });
    connect(closeBtn, &QPushButton::clicked, this, &ImportDialog::reject);
    connect(m_previewBtn, &QPushButton::clicked, this, &ImportDialog::onPreview);

    return page;
}

QWidget* ImportDialog::buildSelectionPage()
{
    auto* page = new QWidget;
    auto* lay = new QVBoxLayout(page);

    m_manifestLabel = new QLabel;
    m_manifestLabel->setWordWrap(true);
    lay->addWidget(m_manifestLabel);

    m_tree = new QTreeWidget;
    m_tree->setHeaderHidden(true);
    m_tree->setSelectionMode(QAbstractItemView::NoSelection);
    lay->addWidget(m_tree, 1);

    auto* policyBox = new QGroupBox("When a mod or profile already exists");
    auto* policyLay = new QVBoxLayout(policyBox);
    m_renameRadio = new QRadioButton("Rename the imported copy (keeps both)");
    m_renameRadio->setChecked(true);
    m_skipRadio = new QRadioButton("Skip it (keep what you have)");
    m_overwriteRadio = new QRadioButton("Overwrite it (replace with the archive's copy)");
    policyLay->addWidget(m_renameRadio);
    policyLay->addWidget(m_skipRadio);
    policyLay->addWidget(m_overwriteRadio);
    lay->addWidget(policyBox);

    m_selectionErrorLabel = new QLabel;
    m_selectionErrorLabel->setWordWrap(true);
    m_selectionErrorLabel->setStyleSheet(QString("color: %1;").arg(errHex()));
    m_selectionErrorLabel->setVisible(false);
    lay->addWidget(m_selectionErrorLabel);

    auto* btnRow = new QHBoxLayout;
    auto* backBtn = new QPushButton("Back");
    m_startBtn = new QPushButton("Start Import");
    m_startBtn->setDefault(true);
    btnRow->addWidget(backBtn);
    btnRow->addStretch(1);
    btnRow->addWidget(m_startBtn);
    lay->addLayout(btnRow);

    connect(backBtn, &QPushButton::clicked, this, [this] {
        m_stack->setCurrentIndex(PAGE_ARCHIVE);
    });
    connect(m_startBtn, &QPushButton::clicked, this, &ImportDialog::onStartImport);
    connect(m_tree, &QTreeWidget::itemChanged, this, &ImportDialog::updateStartEnabled);

    return page;
}

QWidget* ImportDialog::buildProgressPage()
{
    auto* page = new QWidget;
    auto* lay = new QVBoxLayout(page);

    m_stepLabel = new QLabel("Preparing import...");
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

    connect(m_backBtn, &QPushButton::clicked, this, &ImportDialog::onBackToSelection);
    connect(m_closeBtn, &QPushButton::clicked, this, &ImportDialog::accept);
    connect(m_cancelBtn, &QPushButton::clicked, this, &ImportDialog::onCancelTransfer);

    return page;
}

void ImportDialog::onBrowseArchive()
{
    QString path = QFileDialog::getOpenFileName(
        this, "Import Archive", QDir::homePath(),
        "Gorganizer export (*.gorganizer-export.tar.zst);;All files (*)");
    if (path.isEmpty()) return;
    m_archiveEdit->setText(path);
}

// Runs the synchronous manifest preview and advances to the selection page on success.
void ImportDialog::onPreview()
{
    const QString path = m_archiveEdit->text().trimmed();
    if (path.isEmpty()) return;

    m_archiveErrorLabel->setVisible(false);
    m_previewBtn->setEnabled(false);
    QApplication::setOverrideCursor(Qt::WaitCursor);
    GrpcImportPreview preview;
    QString err;
    const bool ok = m_grpc->previewImport(m_gameId, path, preview, err);
    QApplication::restoreOverrideCursor();
    m_previewBtn->setEnabled(true);

    if (!ok) {
        m_archiveErrorLabel->setText(friendlyTransferError(err));
        m_archiveErrorLabel->setVisible(true);
        return;
    }

    m_preview = preview;
    populatePreview();
    m_stack->setCurrentIndex(PAGE_SELECTION);
}

// Rebuilds the manifest header and the checkable mods/profiles tree from the preview.
void ImportDialog::populatePreview()
{
    QString header = QString("<b>Game:</b> %1 &nbsp; <b>Gorganizer:</b> %2")
                         .arg(m_preview.gameId, m_preview.gorganizerVersion);
    if (!m_preview.exportedAt.isEmpty())
        header += QString(" &nbsp; <b>Exported:</b> %1").arg(m_preview.exportedAt);
    QStringList extras;
    if (m_preview.includesOverwrite) extras << "Overwrite folder";
    if (m_preview.includesGameSettings) extras << "game settings";
    if (!extras.isEmpty())
        header += QString("<br>Also includes: %1 (imported automatically).").arg(extras.join(", "));
    m_manifestLabel->setText(header);

    m_tree->blockSignals(true);
    m_tree->clear();

    m_modsRoot = new QTreeWidgetItem(m_tree, {QString("Mods (%1)").arg(m_preview.mods.size())});
    m_modsRoot->setFlags(m_modsRoot->flags() | Qt::ItemIsUserCheckable | Qt::ItemIsAutoTristate);
    for (const auto& m : m_preview.mods) {
        QString text = QString("%1  (%2 files, %3)")
                           .arg(m.name.isEmpty() ? m.folder : m.name)
                           .arg(m.fileCount)
                           .arg(humanBytes(m.totalBytes));
        if (m.collision) text += "  — exists";
        auto* item = new QTreeWidgetItem(m_modsRoot, {text});
        item->setData(0, Qt::UserRole, m.folder);
        item->setFlags(item->flags() | Qt::ItemIsUserCheckable);
        item->setCheckState(0, Qt::Checked);
        if (m.collision) item->setForeground(0, ThemeManager::currentPalette().warningFg);
    }

    m_profilesRoot = new QTreeWidgetItem(m_tree, {QString("Profiles (%1)").arg(m_preview.profiles.size())});
    m_profilesRoot->setFlags(m_profilesRoot->flags() | Qt::ItemIsUserCheckable | Qt::ItemIsAutoTristate);
    for (const auto& p : m_preview.profiles) {
        QString text = p.name;
        if (p.collision) text += "  — exists";
        auto* item = new QTreeWidgetItem(m_profilesRoot, {text});
        item->setData(0, Qt::UserRole, p.name);
        item->setFlags(item->flags() | Qt::ItemIsUserCheckable);
        item->setCheckState(0, Qt::Checked);
        if (p.collision) item->setForeground(0, ThemeManager::currentPalette().warningFg);
    }

    m_tree->expandAll();
    m_tree->blockSignals(false);
    updateStartEnabled();
}

// Gates Start Import: each non-empty section needs at least one checked item.
void ImportDialog::updateStartEnabled()
{
    if (!m_modsRoot || !m_profilesRoot) return;
    const int modsChecked = checkedChildren(m_modsRoot).size();
    const int profilesChecked = checkedChildren(m_profilesRoot).size();
    const bool modsOk = modsChecked > 0 || m_modsRoot->childCount() == 0;
    const bool profilesOk = profilesChecked > 0 || m_profilesRoot->childCount() == 0;
    m_startBtn->setEnabled(modsOk && profilesOk);
    if (!modsOk || !profilesOk) {
        m_selectionErrorLabel->setText(
            "Select at least one item in each section, or go Back to import nothing.");
        m_selectionErrorLabel->setVisible(true);
    } else {
        m_selectionErrorLabel->setVisible(false);
    }
}

GrpcTransferPolicy ImportDialog::selectedPolicy() const
{
    if (m_skipRadio->isChecked()) return GrpcTransferPolicySkip;
    if (m_overwriteRadio->isChecked()) return GrpcTransferPolicyOverwrite;
    return GrpcTransferPolicyRename;
}

QStringList ImportDialog::checkedChildren(const QTreeWidgetItem* root) const
{
    QStringList out;
    for (int i = 0; i < root->childCount(); ++i) {
        const auto* child = root->child(i);
        if (child->checkState(0) == Qt::Checked)
            out << child->data(0, Qt::UserRole).toString();
    }
    return out;
}

void ImportDialog::onStartImport()
{
    if (m_running) return;
    m_running = true;
    m_cancelRequested = false;

    m_stepLabel->setText("Starting import...");
    m_itemLabel->clear();
    m_bytesLabel->clear();
    m_resultLabel->setVisible(false);
    m_progressBar->setRange(0, 0);
    m_backBtn->setVisible(false);
    m_closeBtn->setVisible(false);
    m_cancelBtn->setVisible(true);
    m_cancelBtn->setEnabled(true);
    m_stack->setCurrentIndex(PAGE_PROGRESS);

    m_grpc->startImport(m_gameId, m_archiveEdit->text().trimmed(), selectedPolicy(),
                        QMap<QString, int>(), checkedChildren(m_modsRoot),
                        checkedChildren(m_profilesRoot));
}

void ImportDialog::onCancelTransfer()
{
    if (!m_running) return;
    m_cancelRequested = true;
    m_cancelBtn->setEnabled(false);
    m_stepLabel->setText("Cancelling...");
    m_grpc->cancelTransfer();
}

void ImportDialog::onBackToSelection()
{
    m_stack->setCurrentIndex(PAGE_SELECTION);
}

void ImportDialog::onTransferProgress(const GrpcTransferProgress& progress)
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

void ImportDialog::onTransferCompleted(const GrpcTransferSummary& summary)
{
    if (!m_running) return;
    m_running = false;
    m_progressBar->setRange(0, 100);
    m_progressBar->setValue(100);
    m_stepLabel->setText("Import complete.");
    m_itemLabel->clear();

    QStringList lines;
    lines << QString("Imported %1 mod(s) and %2 profile(s).")
                 .arg(summary.modsImported)
                 .arg(summary.profilesTransferred);
    if (!summary.renamed.isEmpty()) {
        lines << QString("Renamed %1 item(s):").arg(summary.renamed.size());
        for (auto it = summary.renamed.constBegin(); it != summary.renamed.constEnd(); ++it)
            lines << QString("    %1 → %2").arg(it.key(), it.value());
    }
    if (!summary.skipped.isEmpty())
        lines << QString("Skipped %1 item(s): %2").arg(summary.skipped.size())
                     .arg(summary.skipped.join(", "));
    m_resultLabel->setStyleSheet(QString());
    m_resultLabel->setText(lines.join("\n"));
    m_resultLabel->setVisible(true);

    m_cancelBtn->setVisible(false);
    m_closeBtn->setVisible(true);
    m_closeBtn->setDefault(true);

    emit importCompleted();
}

void ImportDialog::onTransferFailed(const QString& error)
{
    if (!m_running) return;
    m_running = false;
    m_progressBar->setRange(0, 100);
    m_progressBar->setValue(0);
    if (m_cancelRequested) {
        m_stepLabel->setText("Import cancelled.");
        m_resultLabel->setStyleSheet(QString());
        m_resultLabel->setText("The import was cancelled. Items already imported remain in place.");
    } else {
        m_stepLabel->setText("Import failed.");
        m_resultLabel->setStyleSheet(QString("color: %1;").arg(errHex()));
        m_resultLabel->setText(friendlyTransferError(error));
    }
    m_resultLabel->setVisible(true);
    m_cancelBtn->setVisible(false);
    m_backBtn->setVisible(true);
    m_closeBtn->setVisible(true);
}

// Maps the daemon's machine-readable transfer errors to user-facing text.
QString ImportDialog::friendlyTransferError(const QString& error)
{
    if (error.startsWith("transfer_game_mismatch"))
        return QString("This archive was exported from a different game and cannot be "
                       "imported here.\n\n(%1)").arg(error);
    if (error.startsWith("transfer_schema"))
        return QString("This archive uses an export format this version of gorganizer "
                       "does not understand.\n\n(%1)").arg(error);
    if (error.startsWith("transfer_overwrite_mounted"))
        return QString("A mod cannot be overwritten while the game's mod view is mounted. "
                       "Unmount mods first, or choose Rename/Skip.\n\n(%1)").arg(error);
    if (error.startsWith("transfer_collision"))
        return QString("An item in the archive already exists in this instance and the "
                       "chosen policy aborts on collisions.\n\n(%1)").arg(error);
    return error;
}

bool ImportDialog::confirmAbortWhileRunning()
{
    if (!m_running) return true;
    if (!dialogs::confirmWarn(this, "Cancel import?",
            "An import is still running. Close and cancel it?",
            QMessageBox::No))
        return false;
    m_cancelRequested = true;
    m_grpc->cancelTransfer();
    return true;
}

void ImportDialog::closeEvent(QCloseEvent* ev)
{
    if (!confirmAbortWhileRunning()) {
        ev->ignore();
        return;
    }
    QDialog::closeEvent(ev);
}

void ImportDialog::reject()
{
    if (!confirmAbortWhileRunning())
        return;
    QDialog::reject();
}

}
