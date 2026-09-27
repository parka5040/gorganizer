#include "ModInstallDialog.h"
#include "FomodInstallerDialog.h"
#include "GrpcClient.h"
#include "ErrorPresenter.h"
#include "InstallCollisionDialog.h"
#include "InstallErrorText.h"
#include "ThemeManager.h"

#include <QBrush>
#include <QCloseEvent>
#include <QDialogButtonBox>
#include <QLabel>
#include <QMap>
#include <QProgressBar>
#include <QPushButton>
#include <QTreeWidget>
#include <QVBoxLayout>
#include <utility>

namespace gorganizer {

namespace {

enum RootRole { RootPathRole = Qt::UserRole + 1 };

}

ModInstallDialog::ModInstallDialog(const QString& gameId, const QString& modName,
                                   GrpcClient* grpc, ArchiveSource source, QWidget* parent,
                                   InstallTarget target)
    : QDialog(parent)
    , m_gameId(gameId)
    , m_modName(modName)
    , m_grpc(grpc)
    , m_source(std::move(source))
    , m_target(std::move(target))
{
    setWindowTitle(m_target.mode != GrpcInstallAsNewMod
        ? "Update Mod: " + m_target.targetMod : "Install Mod: " + modName);
    setMinimumSize(500, 400);
    resize(600, 500);

    auto* layout = new QVBoxLayout(this);

    m_statusLabel = new QLabel("Reading archive…", this);
    m_statusLabel->setTextFormat(Qt::PlainText);
    m_statusLabel->setWordWrap(true);
    m_statusLabel->setStyleSheet("font-weight: bold;");
    layout->addWidget(m_statusLabel);

    m_progressBar = new QProgressBar(this);
    m_progressBar->setRange(0, 0);
    layout->addWidget(m_progressBar);

    m_treeLabel = new QLabel("Choose the folder containing the mod's game files:", this);
    m_treeLabel->setWordWrap(true);
    m_treeLabel->hide();
    layout->addWidget(m_treeLabel);

    m_treeWidget = new QTreeWidget(this);
    m_treeWidget->setHeaderLabels({"Folder"});
    m_treeWidget->hide();
    layout->addWidget(m_treeWidget, 1);

    m_buttons = new QDialogButtonBox(this);
    m_installBtn = m_buttons->addButton("Install", QDialogButtonBox::AcceptRole);
    m_installBtn->setEnabled(false);
    m_cancelBtn = m_buttons->addButton(QDialogButtonBox::Cancel);
    connect(m_installBtn, &QPushButton::clicked, this, &ModInstallDialog::onInstallClicked);
    connect(m_cancelBtn, &QPushButton::clicked, this, &ModInstallDialog::reject);
    connect(m_treeWidget, &QTreeWidget::currentItemChanged, this,
            [this](QTreeWidgetItem* item) {
                m_rootChosen = item && m_selectableRoots.contains(item->data(0, RootPathRole).toString())
                    && (item->flags() & Qt::ItemIsSelectable);
                if (m_rootChosen)
                    m_selectedRoot = item->data(0, RootPathRole).toString();
                m_installBtn->setEnabled(m_rootChosen);
            });
    layout->addWidget(m_buttons);

    connect(m_grpc, &GrpcClient::previewInstallCompleted,
            this, &ModInstallDialog::onPreviewCompleted);
    connect(m_grpc, &GrpcClient::previewInstallFailed,
            this, &ModInstallDialog::onPreviewFailed);
    connect(m_grpc, &GrpcClient::installRequestCompleted,
            this, &ModInstallDialog::onInstallCompleted);
    connect(m_grpc, &GrpcClient::installRequestFailed,
            this, &ModInstallDialog::onInstallFailed);
    m_previewRequestId = m_grpc->previewInstallAsync(m_gameId, m_source.archiveRelPath,
                                                       m_source.externalArchivePath);
}

QString ModInstallDialog::archivePath() const
{
    return m_source.archiveRelPath.isEmpty()
        ? m_source.externalArchivePath : m_source.archiveRelPath;
}

void ModInstallDialog::discardPreview()
{
    if (m_previewId.isEmpty())
        return;
    m_grpc->discardPreviewAsync(m_previewId);
    m_previewId.clear();
}

void ModInstallDialog::showFailure(const QString& message)
{
    m_phase = Done;
    m_statusLabel->setText(message);
    m_progressBar->hide();
    m_treeLabel->hide();
    m_treeWidget->hide();
    m_installBtn->hide();
    m_cancelBtn->setText("Close");
    m_cancelBtn->setEnabled(true);
}

void ModInstallDialog::onPreviewCompleted(quint64 requestId, const GrpcPreviewInstallResult& result)
{
    if (requestId != m_previewRequestId)
        return;
    m_previewId = result.previewId;
    if (m_phase == CancellingPreview) {
        discardPreview();
        QDialog::reject();
        return;
    }
    if (m_phase != Previewing)
        return;
    if (m_previewId.isEmpty()) {
        showFailure("This archive could not be read. Choose another archive.");
        return;
    }

    if (result.hasFomod) {
        FomodPlan plan;
        if (!result.plan.moduleConfigXml.isEmpty()) {
            auto parsed = FomodParser::parseModuleConfig(result.plan.moduleConfigXml);
            if (!parsed) {
                discardPreview();
                showFailure("This installer could not be read. Choose another archive.");
                return;
            }
            plan = std::move(*parsed);
        } else if (result.plan.legacyInfoOnly) {
            plan.legacyInfoOnly = true;
            plan.moduleName = result.plan.moduleName.isEmpty() ? m_modName : result.plan.moduleName;
            plan.description = result.plan.description;
            plan.version = result.plan.version;
            plan.author = result.plan.author;
        } else {
            discardPreview();
            showFailure("This installer could not be read. Choose another archive.");
            return;
        }
        plan.screenshotData = result.plan.screenshotData;
        m_phase = Choosing;
        m_statusLabel->setText("Choose your installation options…");
        emit fomodWizardOpened(archivePath(), m_modName);
        FomodInstallerDialog wizard(plan, this);
        const int code = wizard.exec();
        emit fomodWizardClosed(archivePath());
        if (code != QDialog::Accepted) {
            reject();
            return;
        }
        if (m_previewId.isEmpty())
            return;
        std::vector<GrpcFomodFile> files;
        for (const auto& file : wizard.selectedFiles())
            files.push_back({file.source, file.destination, file.isFolder, file.priority});
        beginInstall(true, files);
        return;
    }

    if (!result.rootAmbiguous) {
        beginInstall(false);
        return;
    }
    showRoots(result.selectableRoots);
}

void ModInstallDialog::onPreviewFailed(quint64 requestId, const QString& error)
{
    if (requestId != m_previewRequestId)
        return;
    if (m_phase == CancellingPreview) {
        QDialog::reject();
        return;
    }
    if (m_phase == Previewing) {
        showFailure(errorSummary("read this archive", error));
        presentError(this, "Archive Could Not Be Read", "read this archive", error);
    }
}

void ModInstallDialog::showRoots(const QStringList& selectableRoots)
{
    m_phase = Choosing;
    m_progressBar->hide();
    m_statusLabel->setText("Choose a folder to install from.");
    m_selectableRoots = selectableRoots;
    m_treeWidget->clear();
    QMap<QString, QTreeWidgetItem*> items;
    if (selectableRoots.contains(QString())) {
        auto* root = new QTreeWidgetItem({"(Archive root — use if files are directly here)"});
        root->setData(0, RootPathRole, QString());
        root->setForeground(0, QBrush(ThemeManager::currentPalette().accent));
        m_treeWidget->addTopLevelItem(root);
        items.insert(QString(), root);
    }
    for (const auto& path : selectableRoots) {
        if (path.isEmpty()) continue;
        QString prefix;
        QTreeWidgetItem* parent = items.value(QString());
        for (const auto& part : path.split('/', Qt::SkipEmptyParts)) {
            if (!prefix.isEmpty()) prefix += '/';
            prefix += part;
            auto* item = items.value(prefix, nullptr);
            if (!item) {
                item = new QTreeWidgetItem({part});
                item->setData(0, RootPathRole, prefix);
                if (!selectableRoots.contains(prefix))
                    item->setFlags(item->flags() & ~Qt::ItemIsSelectable);
                if (parent) parent->addChild(item);
                else m_treeWidget->addTopLevelItem(item);
                items.insert(prefix, item);
            }
            parent = item;
        }
    }
    m_treeWidget->expandToDepth(1);
    m_treeLabel->show();
    m_treeWidget->show();
}

void ModInstallDialog::onInstallClicked()
{
    if (m_phase != Choosing || !m_rootChosen)
        return;
    beginInstall(false, {}, m_selectedRoot);
}

void ModInstallDialog::beginInstall(bool fomodConfirmed,
                                    const std::vector<GrpcFomodFile>& files,
                                    const QString& selectedRoot)
{
    m_fomodConfirmed = fomodConfirmed;
    m_selectedFiles = files;
    m_installRoot = selectedRoot;
    m_phase = Installing;
    m_installBtn->setEnabled(false);
    m_cancelBtn->setEnabled(false);
    m_treeLabel->hide();
    m_treeWidget->hide();
    m_progressBar->show();
    m_statusLabel->setText(QString("Installing %1… please wait").arg(m_modName));
    const QString targetMod = m_target.targetMod.isEmpty() ? m_modName : m_target.targetMod;
    if (m_source.archiveRelPath.isEmpty()) {
        m_installRequestId = m_grpc->startInstallExternal(m_gameId, m_source.externalArchivePath,
            m_target.mode, targetMod, fomodConfirmed, selectedRoot, m_previewId, files);
    } else {
        m_installRequestId = m_grpc->startInstall(m_gameId, m_source.archiveRelPath,
            m_target.mode, targetMod, m_previewId, files, fomodConfirmed, selectedRoot);
    }
}

void ModInstallDialog::onInstallCompleted(quint64 requestId, const QString& modFolder, int fileCount)
{
    if (m_phase != Installing || requestId != m_installRequestId)
        return;
    discardPreview();
    m_modName = modFolder;
    m_fileCount = fileCount;
    m_phase = Done;
    accept();
}

void ModInstallDialog::onInstallFailed(quint64 requestId, const QString& error)
{
    if (m_phase != Installing || requestId != m_installRequestId)
        return;
    if (parseInstallError(error).token == QLatin1String("mod_collision")) {
        const auto choice = resolveInstallCollision(this, error,
            m_target.targetMod.isEmpty() ? m_modName : m_target.targetMod);
        if (choice) {
            m_target = {choice->mode, choice->targetMod};
            beginInstall(m_fomodConfirmed, m_selectedFiles, m_installRoot);
        } else {
            discardPreview();
            QDialog::reject();
        }
        return;
    }
    discardPreview();
    showFailure(errorSummary("install this mod", error, true));
    presentError(this, "Install Failed", "install this mod", error, true);
}

void ModInstallDialog::closeEvent(QCloseEvent* event)
{
    if (m_phase == Previewing || m_phase == CancellingPreview || m_phase == Installing) {
        reject();
        event->ignore();
        return;
    }
    discardPreview();
    QDialog::closeEvent(event);
}

void ModInstallDialog::reject()
{
    if (m_phase == Previewing) {
        m_phase = CancellingPreview;
        m_installBtn->setEnabled(false);
        m_cancelBtn->setEnabled(false);
        m_statusLabel->setText("Closing… please wait");
        return;
    }
    if (m_phase == CancellingPreview)
        return;
    if (m_phase == Installing) {
        m_statusLabel->setText("Installing… please wait");
        return;
    }
    discardPreview();
    QDialog::reject();
}

}
