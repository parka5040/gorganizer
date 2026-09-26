#include "ModDependencyText.h"
#include "ModCatalog.h"

#include <QSet>
#include <algorithm>

namespace gorganizer {

namespace {

QString sentence(const QString& text)
{
    QString trimmed = text.trimmed();
    if (trimmed.isEmpty())
        return trimmed;
    trimmed[0] = trimmed[0].toUpper();
    if (trimmed.endsWith(QLatin1Char('.')) || trimmed.endsWith(QLatin1Char('!')) || trimmed.endsWith(QLatin1Char('?')))
        return trimmed;
    return trimmed + QLatin1Char('.');
}

QString issueDetailText(const GrpcModIssue& issue)
{
    if (issue.detail.isEmpty())
        return QString();
    switch (issue.kind) {
    case GrpcModIssueDuplicateId:
        return sentence(QStringLiteral("Folders: %1").arg(issue.detail));
    case GrpcModIssueFolderCollision:
        return sentence(QStringLiteral("UniqueIDs: %1").arg(issue.detail));
    case GrpcModIssueCircular:
        return sentence(QStringLiteral("Chain: %1").arg(issue.detail));
    default:
        return sentence(issue.detail);
    }
}

QString minimumSuffix(const QString& version)
{
    return version.isEmpty() ? QString() : QStringLiteral(" %1 or newer").arg(version);
}

QString providerSuffix(const QStringList& providers)
{
    return providers.isEmpty() ? QString() : QStringLiteral(" (in %1)").arg(providers.join(QStringLiteral(", ")));
}

QString installedVersionText(const QString& version)
{
    return version.isEmpty() ? QStringLiteral("unknown") : version;
}

}

bool showsModDependencies(const GameInfo& game)
{
    return game.detected && game.capabilitiesKnown && game.capabilities.manifestDependencies;
}

DependencySeverity issueSeverity(GrpcModIssueKind kind)
{
    switch (kind) {
    case GrpcModIssueDuplicateId:
    case GrpcModIssueNeedsNewerLoader:
    case GrpcModIssueNeedsNewerGame:
    case GrpcModIssueCircular:
    case GrpcModIssueDependencyFailed:
        return DependencySeverityError;
    default:
        return DependencySeverityWarning;
    }
}

DependencySeverity componentSeverity(const GrpcModComponent& component)
{
    DependencySeverity worst = DependencySeverityNone;
    for (const auto& issue : component.issues)
        worst = std::max(worst, issueSeverity(issue.kind));
    if (component.failed && component.issues.empty())
        worst = DependencySeverityError;
    return worst;
}

int issueRank(GrpcModIssueKind kind)
{
    switch (kind) {
    case GrpcModIssueDuplicateId: return 0;
    case GrpcModIssueNeedsNewerLoader: return 1;
    case GrpcModIssueNeedsNewerGame: return 2;
    case GrpcModIssueCircular: return 3;
    case GrpcModIssueDependencyFailed: return 4;
    case GrpcModIssueInvalidManifest: return 5;
    case GrpcModIssueMissing: return 6;
    case GrpcModIssueDisabled: return 7;
    case GrpcModIssueVersionTooLow: return 8;
    case GrpcModIssueFolderCollision: return 9;
    default: return 10;
    }
}

QString issueStatusText(GrpcModIssueKind kind)
{
    switch (kind) {
    case GrpcModIssueMissing: return QStringLiteral("Missing dependency");
    case GrpcModIssueDisabled: return QStringLiteral("Disabled dependency");
    case GrpcModIssueVersionTooLow: return QStringLiteral("Needs newer version");
    case GrpcModIssueDuplicateId: return QStringLiteral("Duplicate ID");
    case GrpcModIssueInvalidManifest: return QStringLiteral("Invalid manifest");
    case GrpcModIssueNeedsNewerLoader: return QStringLiteral("Needs newer SMAPI");
    case GrpcModIssueNeedsNewerGame: return QStringLiteral("Needs newer game");
    case GrpcModIssueCircular: return QStringLiteral("Circular");
    case GrpcModIssueFolderCollision: return QStringLiteral("Folder collision");
    case GrpcModIssueDependencyFailed: return QStringLiteral("Dependency failed");
    default: return QStringLiteral("Problem");
    }
}

QString componentStatusText(const GrpcModComponent& component)
{
    if (component.issues.empty())
        return component.failed ? QStringLiteral("Will not load") : QStringLiteral("OK");
    GrpcModIssueKind worst = component.issues.front().kind;
    for (const auto& issue : component.issues) {
        if (issueRank(issue.kind) < issueRank(worst))
            worst = issue.kind;
    }
    return issueStatusText(worst);
}

QString issueDescription(const GrpcModIssue& issue, const QHash<QString, QString>& names)
{
    const QString target = dependencyDisplayName(issue.targetId, names);
    const QString providers = issue.providers.join(QStringLiteral(", "));
    QString text;
    switch (issue.kind) {
    case GrpcModIssueMissing:
        text = QStringLiteral("Requires %1%2, which is not installed or not enabled.")
                   .arg(target, minimumSuffix(issue.requiredVersion));
        break;
    case GrpcModIssueDisabled:
        text = QStringLiteral("Requires %1%2, which is installed but disabled%3.")
                   .arg(target, minimumSuffix(issue.requiredVersion), providerSuffix(issue.providers));
        break;
    case GrpcModIssueVersionTooLow:
        text = QStringLiteral("Requires %1%2, but version %3 is installed%4.")
                   .arg(target, minimumSuffix(issue.requiredVersion), installedVersionText(issue.foundVersion),
                        providerSuffix(issue.providers));
        break;
    case GrpcModIssueDuplicateId:
        text = QStringLiteral("Another mod folder uses the same UniqueID %1, so SMAPI loads neither.").arg(target);
        break;
    case GrpcModIssueInvalidManifest:
        text = QStringLiteral("Its manifest.json is invalid.");
        break;
    case GrpcModIssueNeedsNewerLoader:
        text = QStringLiteral("Needs SMAPI%1; the installed version is %2.")
                   .arg(minimumSuffix(issue.requiredVersion), installedVersionText(issue.foundVersion));
        break;
    case GrpcModIssueNeedsNewerGame:
        text = QStringLiteral("Needs game version%1; the installed version is %2.")
                   .arg(minimumSuffix(issue.requiredVersion), installedVersionText(issue.foundVersion));
        break;
    case GrpcModIssueCircular:
        text = QStringLiteral("Its dependency on %1 is circular.").arg(target);
        break;
    case GrpcModIssueFolderCollision:
        text = QStringLiteral("Its folder is also provided with a different UniqueID by %1; only this copy is deployed.")
                   .arg(providers.isEmpty() ? QStringLiteral("another mod") : providers);
        break;
    case GrpcModIssueDependencyFailed:
        text = QStringLiteral("Requires %1, which will not load itself%2.").arg(target, providerSuffix(issue.providers));
        break;
    default:
        text = QStringLiteral("SMAPI reports a problem with this mod.");
        break;
    }
    const QString detail = issueDetailText(issue);
    if (!detail.isEmpty())
        text += QLatin1Char(' ') + detail;
    return text;
}

QString componentKindText(GrpcModComponentKind kind)
{
    switch (kind) {
    case GrpcModComponentCode: return QStringLiteral("Code");
    case GrpcModComponentContentPack: return QStringLiteral("Content pack");
    case GrpcModComponentInvalid: return QStringLiteral("Invalid");
    default: return QString();
    }
}

QString componentProviderText(const GrpcModComponent& component)
{
    if (component.providerMod.isEmpty())
        return QStringLiteral("(game Mods folder)");
    return component.providerMod;
}

QHash<QString, QString> dependencyNames(const GrpcModDependencyReport& report)
{
    QHash<QString, QString> names;
    for (const auto& component : report.components) {
        if (!component.uniqueId.isEmpty() && !component.name.isEmpty())
            names.insert(component.uniqueId.toCaseFolded(), component.name);
    }
    for (const auto& dep : report.missing) {
        if (!dep.uniqueId.isEmpty() && !dep.name.isEmpty() && !names.contains(dep.uniqueId.toCaseFolded()))
            names.insert(dep.uniqueId.toCaseFolded(), dep.name);
    }
    return names;
}

QString dependencyDisplayName(const QString& uniqueId, const QHash<QString, QString>& names)
{
    const QString name = names.value(uniqueId.toCaseFolded());
    if (name.isEmpty() || name.compare(uniqueId, Qt::CaseInsensitive) == 0)
        return uniqueId;
    return QStringLiteral("%1 (%2)").arg(name, uniqueId);
}

QString dependencyFetchReasonCode(const QString& reason)
{
    const qsizetype sep = reason.indexOf(QLatin1Char(':'));
    return (sep < 0 ? reason : reason.left(sep)).trimmed();
}

QString dependencyFetchReasonText(const QString& reason)
{
    const QString code = dependencyFetchReasonCode(reason);
    const qsizetype sep = reason.indexOf(QLatin1Char(':'));
    const QString detail = sep < 0 ? QString() : reason.mid(sep + 1).trimmed();
    QString text;
    if (code == QLatin1String("provided"))
        text = detail.isEmpty() ? QStringLiteral("It is already installed and enabled.")
                                : QStringLiteral("It is already provided by %1.").arg(detail);
    else if (code == QLatin1String("disabled"))
        text = detail.isEmpty() ? QStringLiteral("It is installed but disabled.")
                                : QStringLiteral("It is installed but disabled (%1).").arg(detail);
    else if (code == QLatin1String("not_missing"))
        text = QStringLiteral("It is no longer missing.");
    else if (code == QLatin1String("no_nexus_page"))
        text = QStringLiteral("smapi.io knows no Nexus Mods page for it; download it manually.");
    else if (code == QLatin1String("no_api_key"))
        text = QStringLiteral("No Nexus API key is set (Tools → Settings), so it cannot be downloaded automatically.");
    else if (code == QLatin1String("downloads_unavailable"))
        text = QStringLiteral("The daemon's download manager is not available.");
    else if (code == QLatin1String("premium_check_failed"))
        text = QStringLiteral("Your Nexus Premium status could not be checked.");
    else if (code == QLatin1String("not_premium"))
        text = QStringLiteral("Automatic downloads need a Nexus Premium account.");
    else if (code == QLatin1String("file_list_failed"))
        text = QStringLiteral("The mod's file list on Nexus Mods could not be read.");
    else if (code == QLatin1String("ambiguous_main_file"))
        text = QStringLiteral("The mod has several main files on Nexus Mods; pick the right one yourself.");
    else if (code == QLatin1String("no_main_file"))
        text = QStringLiteral("The mod has no main file on Nexus Mods.");
    else if (code == QLatin1String("queue_failed"))
        text = QStringLiteral("Queueing the download failed.");
    else if (code == QLatin1String("pending_enable"))
        text = detail.isEmpty()
            ? QStringLiteral("It is already installed and waiting to be enabled; gorganizer enables it shortly.")
            : QStringLiteral("It is already installed as \"%1\" and waiting to be enabled; gorganizer enables it "
                             "shortly.").arg(detail);
    else if (code == QLatin1String("in_flight"))
        text = QStringLiteral("It is already being downloaded.");
    else if (code == QLatin1String("installing"))
        text = QStringLiteral("It is already downloaded and being installed.");
    else if (code == QLatin1String("lookup_failed"))
        text = QStringLiteral("Couldn't reach smapi.io — try again.");
    else if (code == QLatin1String("bundled"))
        text = QStringLiteral("It is provided by SMAPI's bundled mods — repair SMAPI if it is missing.");
    else if (code == QLatin1String("premium_file_mismatch"))
        text = QStringLiteral("The main Nexus file didn't contain this mod — opening the page so you can pick the "
                              "right file.");
    else if (code.isEmpty())
        return QString();
    else
        return detail.isEmpty() ? sentence(code) : QStringLiteral("%1: %2").arg(code, sentence(detail));
    return text;
}

QStringList disabledProvidersToEnable(const std::vector<GrpcMissingDependency>& missing)
{
    QStringList names;
    QSet<QString> seen;
    for (const auto& dep : missing) {
        if (dep.disabledProviders.isEmpty())
            continue;
        const QString& provider = dep.disabledProviders.front();
        if (provider.isEmpty() || provider == QLatin1String(kOverwriteModName) || seen.contains(provider))
            continue;
        seen.insert(provider);
        names.append(provider);
    }
    return names;
}

std::vector<GrpcDependencyRequestIssue> relevantRecentFailures(const GrpcModDependencyReport& report)
{
    QSet<QString> missing;
    for (const auto& dep : report.missing) {
        if (dep.disabledProviders.isEmpty())
            missing.insert(dep.uniqueId.toCaseFolded());
    }
    std::vector<GrpcDependencyRequestIssue> newest = report.recentFailures;
    std::stable_sort(newest.begin(), newest.end(),
                     [](const GrpcDependencyRequestIssue& a, const GrpcDependencyRequestIssue& b) {
                         return a.updatedAt > b.updatedAt;
                     });
    std::vector<GrpcDependencyRequestIssue> failures;
    QSet<QString> seen;
    for (const auto& issue : newest) {
        const QString key = issue.uniqueId.toCaseFolded();
        if (key.isEmpty() || !missing.contains(key) || seen.contains(key))
            continue;
        seen.insert(key);
        failures.push_back(issue);
    }
    return failures;
}

QString dependencyRequestIssueText(const GrpcDependencyRequestIssue& issue)
{
    const QString code = dependencyFetchReasonCode(issue.detail);
    const qsizetype sep = issue.detail.indexOf(QLatin1Char(':'));
    const QString detail = sep < 0 ? QString() : issue.detail.mid(sep + 1).trimmed();
    if (issue.state == QLatin1String("expired")) {
        const QString why = dependencyFetchReasonText(issue.detail);
        const QString text = QStringLiteral("Its Nexus Mods page was opened, but no matching download arrived "
                                            "within a day.");
        return why.isEmpty() ? text : QStringLiteral("%1 (%2)").arg(text, why);
    }
    if (code == QLatin1String("archive_mismatch"))
        return QStringLiteral("The downloaded archive did not contain this mod.");
    if (code == QLatin1String("download_failed"))
        return detail.isEmpty() ? QStringLiteral("The download failed.")
                                : QStringLiteral("The download failed: %1").arg(sentence(detail));
    if (code == QLatin1String("download_lost"))
        return QStringLiteral("The download disappeared before its archive arrived.");
    if (code == QLatin1String("install_failed"))
        return detail.isEmpty() ? QStringLiteral("Installing it failed.")
                                : QStringLiteral("Installing it failed: %1").arg(sentence(detail));
    if (code == QLatin1String("install_interrupted"))
        return QStringLiteral("The daemon stopped while installing it.");
    if (issue.detail.trimmed().isEmpty())
        return QStringLiteral("The download failed.");
    return sentence(issue.detail);
}

QString plainToolTip(const QString& text)
{
    if (text.isEmpty())
        return QString();
    QString escaped = text.toHtmlEscaped();
    escaped.replace(QLatin1Char('\n'), QLatin1String("<br>"));
    return QStringLiteral("<qt>%1</qt>").arg(escaped);
}

}
