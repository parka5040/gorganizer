#pragma once

#include <QString>

class QMessageBox;
class QWidget;

namespace gorganizer {

QString errorSummary(const QString& operation, const QString& rawError);
QString errorSummary(const QString& operation, const QString& rawError, bool mutating);
void presentError(QWidget* parent, const QString& title, const QString& operation, const QString& rawError);
void presentError(QWidget* parent, const QString& title, const QString& operation, const QString& rawError,
                  bool mutating);
void presentError(QWidget* parent, const QString& title, const QString& operation, const QString& rawError,
                  bool mutating, const QString& extraDetails);
void attachErrorDetails(QMessageBox* box, const QString& title, const QString& operation, const QString& rawError);

}
