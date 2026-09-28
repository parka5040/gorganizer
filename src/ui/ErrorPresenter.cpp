#include "ErrorPresenter.h"
#include "InstallErrorText.h"

#include <QApplication>
#include <QClipboard>
#include <QMessageBox>
#include <QPushButton>
#include <QRegularExpression>

namespace gorganizer {

namespace {

QString redacted(const QString& text)
{
    QString result = text;
    static const QRegularExpression urls(QStringLiteral(R"((\b[a-z][a-z0-9+.-]*://[^\s?<>"']+)\?[^\s#<>"']*(#[^\s<>"']*)?)"),
                                         QRegularExpression::CaseInsensitiveOption);
    static const QRegularExpression encodedUrls(QStringLiteral(R"((\b[a-z][a-z0-9+.-]*%3a%2f%2f[^\s:<>'"]*?)%3f[^\s:<>'"]*)"),
                                                QRegularExpression::CaseInsensitiveOption);
    static const QRegularExpression authorization(QStringLiteral(R"((\bAuthorization[ \t]*:[ \t]*)[^\r\n]*)"),
                                                  QRegularExpression::CaseInsensitiveOption);
    static const QRegularExpression encodedAuthorization(
        QStringLiteral(R"((\bAuthorization%3a(?:%20|[ \t])*)(?:(?!%0a|%0d)[^\r\n])+)"),
        QRegularExpression::CaseInsensitiveOption);
    static const QRegularExpression keys(
        QStringLiteral(R"(\b(apikey|key)([ \t]*(?:=|%3d)[ \t]*)(?:(?!%26|%3b)[^\s&;:'"\r\n])+)"),
        QRegularExpression::CaseInsensitiveOption);
    result.replace(urls, QStringLiteral("\\1?…redacted\\2"));
    result.replace(encodedUrls, QStringLiteral("\\1%3F…redacted"));
    result.replace(authorization, QStringLiteral("\\1[redacted]"));
    result.replace(encodedAuthorization, QStringLiteral("\\1[redacted]"));
    result.replace(keys, QStringLiteral("\\1\\2[redacted]"));
    return result;
}

bool isTimeout(const QString& error)
{
    return error.contains(QLatin1String("deadline exceeded"), Qt::CaseInsensitive)
        || error.contains(QLatin1String("timed out"), Qt::CaseInsensitive)
        || error.contains(QLatin1String("timeout"), Qt::CaseInsensitive);
}

bool isTransportFailure(const QString& error)
{
    return isTimeout(error) || error.contains(QLatin1String("failed to connect"), Qt::CaseInsensitive)
        || error.contains(QLatin1String("socket closed"), Qt::CaseInsensitive)
        || error.contains(QLatin1String("not connected"), Qt::CaseInsensitive)
        || error.contains(QLatin1String("connection reset"), Qt::CaseInsensitive)
        || error.contains(QLatin1String("connection refused"), Qt::CaseInsensitive)
        || error.contains(QLatin1String("unavailable: connection"), Qt::CaseInsensitive);
}

}

QString errorSummary(const QString& operation, const QString& rawError)
{
    return errorSummary(operation, rawError, false);
}

QString errorSummary(const QString& operation, const QString& rawError, bool mutating)
{
    return errorSummary(operation, GrpcError{0, QString(), rawError}, mutating);
}

QString errorSummary(const QString& operation, const GrpcError& error, bool changesSomething)
{
    if (changesSomething && error.outcomeUnknown())
        return QStringLiteral("The result is unknown. Reconnect to check before trying again.");
    if (!changesSomething && error.unavailable())
        return QStringLiteral("Gorganizer's background service is not responding. Try again in a moment.");
    const QString token = parseInstallError(error.message).token;
    if (!token.isEmpty() && token != QLatin1String("timeout"))
        return redacted(daemonErrorMessage(error.message));
    if (error.ok() && isTimeout(error.message) && changesSomething)
        return QStringLiteral("The result is unknown. Reconnect to check before trying again.");
    if (isTransportFailure(error.message))
        return QStringLiteral("Gorganizer's background service is not responding. Check that Gorganizer is still "
                              "running, then try again.");
    return QStringLiteral("Couldn't %1.").arg(redacted(operation));
}

void presentError(QWidget* parent, const QString& title, const QString& operation, const QString& rawError)
{
    presentError(parent, title, operation, rawError, false);
}

void presentError(QWidget* parent, const QString& title, const QString& operation, const QString& rawError,
                  bool mutating)
{
    presentError(parent, title, operation, rawError, mutating, QString());
}

void presentError(QWidget* parent, const QString& title, const QString& operation, const QString& rawError,
                  bool mutating, const QString& extraDetails)
{
    presentError(parent, title, operation, GrpcError{0, QString(), rawError}, mutating, extraDetails);
}

void presentError(QWidget* parent, const QString& title, const QString& operation, const GrpcError& error,
                  bool changesSomething)
{
    presentError(parent, title, operation, error, changesSomething, QString());
}

void presentError(QWidget* parent, const QString& title, const QString& operation, const GrpcError& error,
                  bool changesSomething, const QString& extraDetails)
{
    QMessageBox box(parent);
    box.setIcon(QMessageBox::Warning);
    box.setWindowTitle(redacted(title));
    box.setTextFormat(Qt::PlainText);
    box.setText(errorSummary(operation, error, changesSomething));
    box.setStandardButtons(QMessageBox::Ok);
    attachErrorDetails(&box, title, operation, extraDetails.isEmpty()
                           ? error.message : error.message + QStringLiteral("\n\n") + extraDetails);
    box.exec();
}

void attachErrorDetails(QMessageBox* box, const QString& title, const QString& operation, const QString& rawError)
{
    const QString safeTitle = redacted(title);
    const QString safeOperation = redacted(operation);
    const QString details = redacted(rawError);
    box->setDetailedText(details);
    QPushButton* copy = box->addButton(QStringLiteral("Copy details"), QMessageBox::ActionRole);
    QObject::connect(copy, &QPushButton::clicked, box, [safeTitle, safeOperation, details] {
        QApplication::clipboard()->setText(QStringLiteral("%1\nOperation: %2\n%3")
                                               .arg(safeTitle, safeOperation, details));
    });
}

}
