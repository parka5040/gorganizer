#pragma once

#include <QDialog>
#include <QList>
#include <QButtonGroup>
#include <QPointer>
#include "FomodPlan.h"

class QStackedWidget;
class QLabel;
class QTextEdit;
class QPushButton;

namespace gorganizer {

// FomodInstallerDialog walks the user through a FOMOD installer: one page
// per installStep with the group/plugin controls (radio or checkboxes based
// on group type). On accept, selectedFiles() returns the flattened list of
// FomodFile operations (required files + user-picked plugin files).
class FomodInstallerDialog : public QDialog {
    Q_OBJECT
public:
    FomodInstallerDialog(const FomodPlan& plan, QWidget* parent = nullptr);

    // Flat list of source/destination copy operations. Paths in `source`
    // are relative to plan.modulePath (caller resolves against extractRoot).
    QList<FomodFile> selectedFiles() const { return m_selectedFiles; }

private slots:
    void onNext();
    void onBack();

private:
    struct StepWidgets {
        QList<QButtonGroup*> groupButtons;            // one per group (radio groups)
        QList<QList<QAbstractButton*>> pluginButtons; // [group][plugin]
    };

    void buildPages();
    void buildStepPage(const FomodStep& step, int stepIdx);
    void showStep(int idx);
    void collectSelections();
    void updateButtons();
    void renderDescription(const QString& name, const QString& description);

    FomodPlan m_plan;
    QStackedWidget* m_stack;
    QPushButton* m_backBtn;
    QPushButton* m_nextBtn;
    QPushButton* m_cancelBtn;
    QLabel* m_titleLabel;
    QTextEdit* m_descriptionText;

    QList<StepWidgets> m_stepWidgets;
    QList<FomodFile> m_selectedFiles;
    int m_currentStep = 0;
};

} // namespace gorganizer
