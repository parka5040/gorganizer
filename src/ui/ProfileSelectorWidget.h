#pragma once

#include <QWidget>
#include <QComboBox>
#include <QToolButton>
#include <vector>
#include "GrpcClient.h"

namespace gorganizer {

// MO2-style profile selector: combo box + create/delete/copy buttons.
// Layout: [Profile: ▼ combo] [+] [-] [copy]
class ProfileSelectorWidget : public QWidget {
    Q_OBJECT
public:
    explicit ProfileSelectorWidget(GrpcClient* grpc, QWidget* parent = nullptr);

    void loadForGame(const QString& gameId);
    // Same as loadForGame but asks the selector to pick `preferred` once the
    // daemon's profile list arrives, if that profile still exists.
    void loadForGame(const QString& gameId, const QString& preferred);
    QString currentProfile() const;

signals:
    void profileChanged(const QString& profileName);

private slots:
    void onProfilesListed(const std::vector<GrpcProfile>& profiles);
    void onProfileCreated(const GrpcProfile& profile);
    void onProfileDeleted();
    void onComboChanged(int index);
    void onCreateClicked();
    void onDeleteClicked();
    void onCopyClicked();

private:
    GrpcClient* m_grpc;
    QComboBox* m_combo;
    QToolButton* m_createBtn;
    QToolButton* m_deleteBtn;
    QToolButton* m_copyBtn;
    QString m_gameId;
    QString m_pendingPreferred;
};

} // namespace gorganizer
