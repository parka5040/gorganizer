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

// Walks the user through a FOMOD installer one step at a time.
class FomodInstallerDialog : public QDialog {
    Q_OBJECT
public:
    FomodInstallerDialog(const FomodPlan& plan, QWidget* parent = nullptr);

    // Source/destination copy ops with sources relative to plan.modulePath.
    QList<FomodFile> selectedFiles() const { return m_selectedFiles; }

private slots:
    void onNext();
    void onBack();

private:
    struct StepWidgets {
        QList<QButtonGroup*> groupButtons;
        QList<QList<QAbstractButton*>> pluginButtons;
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
