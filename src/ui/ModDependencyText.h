#pragma once

#include <QHash>
#include <QString>
#include <QStringList>
#include "GameInfo.h"
#include "GrpcTypes.h"

namespace gorganizer {

enum DependencySeverity {
    DependencySeverityNone = 0,
    DependencySeverityWarning = 1,
    DependencySeverityError = 2,
};

// Reports whether game has the daemon-sent manifestDependencies capability that turns on SMAPI dependency features.
bool showsModDependencies(const GameInfo& game);

// Returns the severity one issue kind contributes to a mod's dependency indicator.
DependencySeverity issueSeverity(GrpcModIssueKind kind);

// Returns the worst severity among a component's issues, treating a failed component without issues as an error.
DependencySeverity componentSeverity(const GrpcModComponent& component);

// Returns the position of an issue kind in the worst-first order used for a component's status.
int issueRank(GrpcModIssueKind kind);

// Returns the short status words for one issue kind.
QString issueStatusText(GrpcModIssueKind kind);

// Returns the short status words for a component: its worst issue, or OK.
QString componentStatusText(const GrpcModComponent& component);

// Returns a plain-text sentence describing one issue, naming its target through names when known.
QString issueDescription(const GrpcModIssue& issue, const QHash<QString, QString>& names);

// Returns the display name of a component kind.
QString componentKindText(GrpcModComponentKind kind);

// Returns the gorganizer mod that provides a component, or a placeholder for the game's own Mods folder.
QString componentProviderText(const GrpcModComponent& component);

// Maps case-folded UniqueIDs to display names taken from a report's components and missing dependencies.
QHash<QString, QString> dependencyNames(const GrpcModDependencyReport& report);

// Returns "Name (UniqueID)" when a name is known for uniqueId, otherwise the ID itself.
QString dependencyDisplayName(const QString& uniqueId, const QHash<QString, QString>& names);

// Returns the machine-readable code of a fetch result reason without its detail.
QString dependencyFetchReasonCode(const QString& reason);

// Returns a plain-text explanation of a FetchModDependencies reason, keeping any detail after the code.
QString dependencyFetchReasonText(const QString& reason);

// Returns the first provider of each dependency that is installed but disabled, as mod folder names without duplicates.
QStringList disabledProvidersToEnable(const std::vector<GrpcMissingDependency>& missing);

// Returns the report's failed or expired dependency downloads whose dependency is still missing, newest first and one per UniqueID.
std::vector<GrpcDependencyRequestIssue> relevantRecentFailures(const GrpcModDependencyReport& report);

// Returns a plain-text sentence explaining why a dependency download failed or expired.
QString dependencyRequestIssueText(const GrpcDependencyRequestIssue& issue);

// Returns text as a rich-text tooltip with every character HTML-escaped and its line breaks kept.
QString plainToolTip(const QString& text);

}
