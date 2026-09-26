#pragma once

#include <QMap>
#include <QString>

class QWidget;

namespace gorganizer {

struct InstallError {
    QString token;
    QMap<QString, QString> fields;
};

// Splits a daemon error into its leading token and key=value fields, percent-decoding the values only for tokens whose values the daemon escapes.
InstallError parseInstallError(const QString& error);

// Reports whether the daemon percent-escapes the field values of token, so a parser must decode them; legacy tokens carry raw values.
bool tokenValuesPercentEscaped(const QString& token);

// Returns a plain-text explanation of a daemon install error, or the error itself when it carries no token.
QString installErrorMessage(const QString& error);

// Shows a daemon install error in a warning box that renders it as plain text.
void showInstallError(QWidget* parent, const QString& title, const QString& error);

// Returns a plain-text explanation of a daemon mod-loader error, or the error itself when it carries no token.
QString modLoaderErrorMessage(const QString& error);

// Shows a daemon mod-loader error in a warning box that renders it as plain text.
void showModLoaderError(QWidget* parent, const QString& title, const QString& error);

// Returns a plain-text explanation of a failed SMAPI dependency RPC, or the error itself when it carries no token.
QString modDependencyErrorMessage(const QString& error);

// Returns a plain-text explanation of a daemon error whose token gorganizer recognises, or the error itself otherwise.
QString daemonErrorMessage(const QString& error);

// Describes a modloader_unavailable reason as the state SMAPI is in, for "SMAPI is <text>." sentences.
QString modLoaderUnavailableReasonText(const QString& reason);

// Returns why a mod name cannot be sent as an install target, or an empty string when it is acceptable.
QString modNameProblem(const QString& name);

}
