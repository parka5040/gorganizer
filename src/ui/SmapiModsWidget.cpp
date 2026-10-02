#include "SmapiModsWidget.h"
#include "ModDependencyText.h"
#include "SmapiComponentModel.h"
#include "ThemeManager.h"
#include "SafeLinks.h"

#include <QHBoxLayout>
#include <QHeaderView>
#include <QLabel>
#include <QLocale>
#include <QPushButton>
#include <QTreeView>
#include <QTreeWidget>
#include <QVBoxLayout>

namespace gorganizer {

namespace {

enum FailureColumn { FailureColDependency = 0, FailureColState = 1, FailureColWhen = 2, FailureColProblem = 3 };

enum FailureItemRole { FailureUniqueIdRole = Qt::UserRole + 1 };

QLabel* makeNoteLabel()
{
    auto* label = new QLabel;
    label->setWordWrap(true);
    label->setTextFormat(Qt::PlainText);
    label->setTextInteractionFlags(Qt::TextSelectableByMouse);
    label->hide();
    return label;
}

}

SmapiModsWidget::SmapiModsWidget(QWidget* parent)
    : QWidget(parent)
{
    auto* layout = new QVBoxLayout(this);

    m_versionLabel = new QLabel(QStringLiteral("SMAPI mods of the selected profile."));
    m_versionLabel->setTextFormat(Qt::PlainText);
    m_versionLabel->setWordWrap(true);
    layout->addWidget(m_versionLabel);

    m_remoteLabel = new QLabel;
    m_remoteLabel->setTextFormat(Qt::PlainText);
    m_remoteLabel->setWordWrap(true);
    m_remoteLabel->setObjectName("hintLabel");
    layout->addWidget(m_remoteLabel);

    m_rootManifestLabel = makeNoteLabel();
    layout->addWidget(m_rootManifestLabel);

    m_errorLabel = makeNoteLabel();
    layout->addWidget(m_errorLabel);

    auto* buttons = new QHBoxLayout;
    m_refreshButton = new QPushButton(QStringLiteral("Refresh"));
    m_refreshButton->setToolTip(QStringLiteral("Re-check the SMAPI mods of this profile without going online."));
    m_checkButton = new QPushButton(QStringLiteral("Check for Updates"));
    m_checkButton->setToolTip(QStringLiteral("Ask smapi.io for mod names, Nexus pages and updates."));
    m_fetchButton = new QPushButton(QStringLiteral("Fetch Missing"));
    m_enableButton = new QPushButton(QStringLiteral("Enable Required"));
    buttons->addWidget(m_refreshButton);
    buttons->addWidget(m_checkButton);
    buttons->addWidget(m_fetchButton);
    buttons->addWidget(m_enableButton);
    buttons->addStretch();
    layout->addLayout(buttons);

    m_onlineChecksHint = makeNoteLabel();
    m_onlineChecksHint->setObjectName("hintLabel");
    m_onlineChecksHint->setText(QStringLiteral("Online checks are off. Use Check for Updates to look up your mods now."));
    layout->addWidget(m_onlineChecksHint);

    m_summaryLabel = new QLabel;
    m_summaryLabel->setTextFormat(Qt::PlainText);
    m_summaryLabel->setObjectName("hintLabel");
    layout->addWidget(m_summaryLabel);

    m_waitingBox = new QWidget;
    auto* waitingLayout = new QHBoxLayout(m_waitingBox);
    waitingLayout->setContentsMargins(0, 0, 0, 0);
    m_waitingLabel = new QLabel;
    m_waitingLabel->setTextFormat(Qt::PlainText);
    m_waitingLabel->setWordWrap(true);
    m_waitingLabel->setTextInteractionFlags(Qt::TextSelectableByMouse);
    m_waitingButton = new QPushButton(QStringLiteral("Enable Now"));
    m_waitingButton->setToolTip(QStringLiteral("Enable these downloaded dependencies in this profile."));
    waitingLayout->addWidget(m_waitingLabel, 1);
    waitingLayout->addWidget(m_waitingButton, 0, Qt::AlignTop);
    m_waitingBox->hide();
    layout->addWidget(m_waitingBox);

    m_failuresBox = new QWidget;
    auto* failuresLayout = new QVBoxLayout(m_failuresBox);
    failuresLayout->setContentsMargins(0, 0, 0, 0);
    auto* failuresTitle = new QLabel(QStringLiteral("Recent dependency downloads that failed or expired:"));
    failuresTitle->setTextFormat(Qt::PlainText);
    failuresLayout->addWidget(failuresTitle);
    m_failuresTree = new QTreeWidget;
    m_failuresTree->setColumnCount(4);
    m_failuresTree->setHeaderLabels({QStringLiteral("Dependency"), QStringLiteral("State"), QStringLiteral("When"),
                                     QStringLiteral("Problem")});
    m_failuresTree->setRootIsDecorated(false);
    m_failuresTree->setUniformRowHeights(true);
    m_failuresTree->setSelectionMode(QAbstractItemView::SingleSelection);
    m_failuresTree->setEditTriggers(QAbstractItemView::NoEditTriggers);
    m_failuresTree->setMaximumHeight(120);
    failuresLayout->addWidget(m_failuresTree);
    auto* retryRow = new QHBoxLayout;
    m_retryButton = new QPushButton(QStringLiteral("Retry Download…"));
    m_retryButton->setToolTip(QStringLiteral("Fetch the selected dependency again."));
    retryRow->addWidget(m_retryButton);
    retryRow->addStretch();
    failuresLayout->addLayout(retryRow);
    m_failuresBox->hide();
    layout->addWidget(m_failuresBox);

    m_model = new SmapiComponentModel(this);
    m_view = new QTreeView;
    m_view->setModel(m_model);
    m_view->setRootIsDecorated(false);
    m_view->setUniformRowHeights(true);
    m_view->setAlternatingRowColors(true);
    m_view->setSelectionBehavior(QAbstractItemView::SelectRows);
    m_view->setSelectionMode(QAbstractItemView::SingleSelection);
    m_view->setEditTriggers(QAbstractItemView::NoEditTriggers);
    m_view->header()->setStretchLastSection(false);
    m_view->header()->setSectionResizeMode(QHeaderView::ResizeToContents);
    m_view->header()->setSectionResizeMode(SmapiColName, QHeaderView::Stretch);
    layout->addWidget(m_view, 1);

    connect(m_refreshButton, &QPushButton::clicked, this, &SmapiModsWidget::refreshRequested);
    connect(m_checkButton, &QPushButton::clicked, this, &SmapiModsWidget::checkUpdatesRequested);
    connect(m_fetchButton, &QPushButton::clicked, this, &SmapiModsWidget::fetchMissingRequested);
    connect(m_enableButton, &QPushButton::clicked, this, &SmapiModsWidget::enableRequiredRequested);
    connect(m_view, &QTreeView::clicked, this, &SmapiModsWidget::onItemClicked);
    connect(m_waitingButton, &QPushButton::clicked, this, &SmapiModsWidget::waitingEnablesRequested);
    connect(m_retryButton, &QPushButton::clicked, this, &SmapiModsWidget::onRetryClicked);
    connect(m_failuresTree, &QTreeWidget::itemSelectionChanged, this, &SmapiModsWidget::updateButtons);
    connect(ThemeManager::instance(), &ThemeManager::themeChanged, this, [this](const Palette& pal) {
        m_rootManifestLabel->setStyleSheet(QStringLiteral("color: %1;").arg(pal.warningFg.name()));
        m_errorLabel->setStyleSheet(QStringLiteral("color: %1;").arg(pal.errorFg.name()));
        m_view->viewport()->update();
    });
    const Palette& pal = ThemeManager::currentPalette();
    m_rootManifestLabel->setStyleSheet(QStringLiteral("color: %1;").arg(pal.warningFg.name()));
    m_errorLabel->setStyleSheet(QStringLiteral("color: %1;").arg(pal.errorFg.name()));

    clearReport();
}

void SmapiModsWidget::setReport(const GrpcModDependencyReport& report)
{
    m_haveReport = true;
    m_model->setReport(report);

    const QString loader = report.loaderVersion.isEmpty() ? QStringLiteral("not installed or unknown")
                                                          : report.loaderVersion;
    const QString game = report.gameVersion.isEmpty() ? QStringLiteral("unknown") : report.gameVersion;
    m_versionLabel->setText(QStringLiteral("SMAPI %1 · game version %2 · profile \"%3\"")
                                .arg(loader, game, report.profileName));

    if (report.rootManifestMods.isEmpty()) {
        m_rootManifestLabel->hide();
    } else {
        m_rootManifestLabel->setText(
            QStringLiteral("These mods have manifest.json at their root and are not deployed; reinstall them: %1")
                .arg(report.rootManifestMods.join(QStringLiteral(", "))));
        m_rootManifestLabel->show();
    }

    int problems = 0;
    int updates = 0;
    for (const auto& component : report.components) {
        if (componentSeverity(component) != DependencySeverityNone)
            ++problems;
        if (!component.updateVersion.isEmpty())
            ++updates;
    }
    m_fetchableCount = 0;
    for (const auto& dep : report.missing) {
        if (dep.disabledProviders.isEmpty())
            ++m_fetchableCount;
    }
    m_enableCount = static_cast<int>(disabledProvidersToEnable(report.missing).size());
    m_summaryLabel->setText(QStringLiteral("%1 mods deployed · %2 with problems · %3 missing dependencies · %4 updates")
                                .arg(static_cast<int>(report.components.size()))
                                .arg(problems)
                                .arg(static_cast<int>(report.missing.size()))
                                .arg(updates));
    showRecentFailures(report);
    updateButtons();
}

void SmapiModsWidget::showRecentFailures(const GrpcModDependencyReport& report)
{
    const QHash<QString, QString> names = dependencyNames(report);
    QString selectedId;
    if (const QList<QTreeWidgetItem*> selected = m_failuresTree->selectedItems(); !selected.isEmpty())
        selectedId = selected.front()->data(FailureColDependency, FailureUniqueIdRole).toString();
    m_failuresTree->clear();
    for (const auto& issue : relevantRecentFailures(report)) {
        auto* item = new QTreeWidgetItem;
        item->setText(FailureColDependency, dependencyDisplayName(issue.uniqueId, names));
        item->setData(FailureColDependency, FailureUniqueIdRole, issue.uniqueId);
        item->setText(FailureColState, issue.state == QLatin1String("expired") ? QStringLiteral("Expired")
                                                                               : QStringLiteral("Failed"));
        if (issue.updatedAt.isValid())
            item->setText(FailureColWhen, QLocale().toString(issue.updatedAt.toLocalTime(), QLocale::ShortFormat));
        const QString problem = dependencyRequestIssueText(issue);
        item->setText(FailureColProblem, problem);
        const QString tip = issue.detail.isEmpty() ? problem : QStringLiteral("%1\n\n%2").arg(problem, issue.detail);
        for (int c = 0; c <= FailureColProblem; ++c)
            item->setToolTip(c, plainToolTip(tip));
        m_failuresTree->addTopLevelItem(item);
        if (!selectedId.isEmpty() && issue.uniqueId.compare(selectedId, Qt::CaseInsensitive) == 0)
            item->setSelected(true);
    }
    for (int c = 0; c < FailureColProblem; ++c)
        m_failuresTree->resizeColumnToContents(c);
    m_failuresBox->setVisible(m_failuresTree->topLevelItemCount() > 0);
}

void SmapiModsWidget::setWaitingEnables(const QStringList& lines)
{
    if (lines.isEmpty()) {
        m_waitingBox->hide();
        m_waitingLabel->clear();
        return;
    }
    QStringList bullets;
    for (const auto& line : lines)
        bullets.append(QStringLiteral("• %1").arg(line));
    m_waitingLabel->setText(QStringLiteral("%1 downloaded dependencies are waiting to be enabled:\n%2")
                                .arg(lines.size())
                                .arg(bullets.join(QLatin1Char('\n'))));
    m_waitingBox->show();
    updateButtons();
}

void SmapiModsWidget::onRetryClicked()
{
    const QList<QTreeWidgetItem*> selected = m_failuresTree->selectedItems();
    if (selected.isEmpty())
        return;
    const QString uniqueId = selected.front()->data(FailureColDependency, FailureUniqueIdRole).toString();
    if (!uniqueId.isEmpty())
        emit retryFetchRequested(uniqueId);
}

void SmapiModsWidget::clearReport()
{
    m_haveReport = false;
    m_model->clear();
    m_fetchableCount = 0;
    m_enableCount = 0;
    m_versionLabel->setText(QStringLiteral("Checking the SMAPI mods of this profile…"));
    m_rootManifestLabel->hide();
    m_summaryLabel->clear();
    m_failuresTree->clear();
    m_failuresBox->hide();
    setWaitingEnables({});
    setReportError(QString());
    updateButtons();
}

void SmapiModsWidget::setReportError(const QString& error)
{
    if (error.isEmpty()) {
        m_errorLabel->hide();
        m_errorLabel->clear();
        return;
    }
    m_errorLabel->setText(QStringLiteral("Could not check the SMAPI mods: %1").arg(error));
    m_errorLabel->show();
}

void SmapiModsWidget::setRemoteState(const QDateTime& lastChecked, const QString& lastError, bool checking)
{
    m_checking = checking;
    QString text;
    if (checking)
        text = QStringLiteral("Checking smapi.io…");
    else if (lastChecked.isValid())
        text = QStringLiteral("Checked online %1.")
                   .arg(QLocale().toString(lastChecked.time(), QLocale::ShortFormat));
    else
        text = QStringLiteral("Not checked online yet; names, Nexus pages and updates may be missing.");
    if (!lastError.isEmpty() && !checking)
        text += QStringLiteral(" The last online check failed (%1); showing cached information.").arg(lastError);
    m_remoteLabel->setText(text);
    updateButtons();
}

void SmapiModsWidget::setOnlineChecksHint(bool off)
{
    m_onlineChecksHint->setVisible(off);
}

void SmapiModsWidget::setActionsBusy(bool busy)
{
    m_actionsBusy = busy;
    updateButtons();
}

void SmapiModsWidget::updateButtons()
{
    m_checkButton->setEnabled(!m_checking);
    m_fetchButton->setText(QStringLiteral("Fetch Missing (%1)").arg(m_fetchableCount));
    m_fetchButton->setEnabled(m_haveReport && !m_actionsBusy && m_fetchableCount > 0);
    m_fetchButton->setToolTip(QStringLiteral("Download the missing dependencies from Nexus Mods; they are "
                                             "installed and enabled when the downloads finish."));
    m_enableButton->setText(QStringLiteral("Enable Required (%1)").arg(m_enableCount));
    m_enableButton->setEnabled(m_haveReport && !m_actionsBusy && m_enableCount > 0);
    m_enableButton->setToolTip(QStringLiteral("Enable installed but disabled mods that other mods require."));
    m_waitingButton->setEnabled(m_haveReport && !m_actionsBusy);
    m_retryButton->setEnabled(m_haveReport && !m_actionsBusy && !m_failuresTree->selectedItems().isEmpty());
}

void SmapiModsWidget::onItemClicked(const QModelIndex& index)
{
    if (!index.isValid() || index.column() != SmapiColUpdate)
        return;
    const QString url = index.data(SmapiComponentModel::UpdateUrlRole).toString();
    if (url.startsWith(QLatin1String("https://")))
        openWebLink(this, url);
}

}
