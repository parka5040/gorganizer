#include "ProfileSelectorWidget.h"
#include "Dialogs.h"

#include <QHBoxLayout>
#include <QLabel>
#include <QInputDialog>

namespace gorganizer {

ProfileSelectorWidget::ProfileSelectorWidget(GrpcClient* grpc, QWidget* parent)
    : QWidget(parent)
    , m_grpc(grpc)
{
    auto* layout = new QHBoxLayout(this);
    layout->setContentsMargins(0, 0, 0, 0);
    layout->setSpacing(2);

    layout->addWidget(new QLabel("Profile:"));

    m_combo = new QComboBox;
    m_combo->setMinimumWidth(120);
    layout->addWidget(m_combo);

    m_createBtn = new QToolButton;
    m_createBtn->setText("+");
    m_createBtn->setToolTip("Create new profile");
    m_createBtn->setFixedWidth(28);
    layout->addWidget(m_createBtn);

    m_deleteBtn = new QToolButton;
    m_deleteBtn->setText("-");
    m_deleteBtn->setToolTip("Delete current profile");
    m_deleteBtn->setFixedWidth(28);
    layout->addWidget(m_deleteBtn);

    m_copyBtn = new QToolButton;
    m_copyBtn->setText("Copy");
    m_copyBtn->setToolTip("Copy current profile");
    layout->addWidget(m_copyBtn);

    connect(m_combo, &QComboBox::currentIndexChanged, this, &ProfileSelectorWidget::onComboChanged);
    connect(m_createBtn, &QToolButton::clicked, this, &ProfileSelectorWidget::onCreateClicked);
    connect(m_deleteBtn, &QToolButton::clicked, this, &ProfileSelectorWidget::onDeleteClicked);
    connect(m_copyBtn, &QToolButton::clicked, this, &ProfileSelectorWidget::onCopyClicked);

    connect(m_grpc, &GrpcClient::profilesListed, this, &ProfileSelectorWidget::onProfilesListed);
    connect(m_grpc, &GrpcClient::profileCreated, this, &ProfileSelectorWidget::onProfileCreated);
    connect(m_grpc, &GrpcClient::profileDeleted, this, &ProfileSelectorWidget::onProfileDeleted);
}

void ProfileSelectorWidget::loadForGame(const QString& gameId)
{
    loadForGame(gameId, QString());
}

void ProfileSelectorWidget::loadForGame(const QString& gameId, const QString& preferred)
{
    m_gameId = gameId;
    m_pendingPreferred = preferred;
    m_grpc->listProfiles(gameId);
}

QString ProfileSelectorWidget::currentProfile() const
{
    return m_combo->currentData().toString();
}

void ProfileSelectorWidget::onProfilesListed(const std::vector<GrpcProfile>& profiles)
{
    m_combo->blockSignals(true);
    QString previous = m_combo->currentData().toString();
    m_combo->clear();
    for (const auto& p : profiles)
        m_combo->addItem(p.name, p.name);
    if (m_combo->count() == 0)
        m_combo->addItem("Default", "Default");

    QString target = m_pendingPreferred.isEmpty() ? previous : m_pendingPreferred;
    m_pendingPreferred.clear();
    int idx = m_combo->findData(target);
    if (idx >= 0)
        m_combo->setCurrentIndex(idx);
    m_combo->blockSignals(false);

    m_deleteBtn->setEnabled(m_combo->count() > 1);

    emit profileChanged(m_combo->currentData().toString());
}

void ProfileSelectorWidget::onProfileCreated(const GrpcProfile&)
{
    if (!m_gameId.isEmpty())
        m_grpc->listProfiles(m_gameId);
}

void ProfileSelectorWidget::onProfileDeleted()
{
    if (!m_gameId.isEmpty())
        m_grpc->listProfiles(m_gameId);
}

void ProfileSelectorWidget::onComboChanged(int)
{
    emit profileChanged(m_combo->currentData().toString());
}

void ProfileSelectorWidget::onCreateClicked()
{
    if (m_gameId.isEmpty())
        return;

    bool ok = false;
    QString name = QInputDialog::getText(this, "New Profile",
                                          "Profile name:", QLineEdit::Normal,
                                          "", &ok);
    if (!ok || name.trimmed().isEmpty())
        return;

    m_grpc->createProfile(m_gameId, name.trimmed());
}

void ProfileSelectorWidget::onDeleteClicked()
{
    if (m_gameId.isEmpty())
        return;

    QString current = currentProfile();
    if (current.isEmpty())
        return;

    if (!dialogs::confirm(this, "Delete Profile",
                          QString("Delete profile \"%1\"?\n\nThis cannot be undone.").arg(current)))
        return;

    m_grpc->deleteProfile(m_gameId, current);
}

void ProfileSelectorWidget::onCopyClicked()
{
    if (m_gameId.isEmpty())
        return;

    QString source = currentProfile();
    if (source.isEmpty())
        return;

    bool ok = false;
    QString name = QInputDialog::getText(this, "Copy Profile",
                                          "New profile name:",
                                          QLineEdit::Normal,
                                          source + " (Copy)", &ok);
    if (!ok || name.trimmed().isEmpty())
        return;

    m_grpc->createProfile(m_gameId, name.trimmed());
}

}
