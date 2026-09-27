#include "InstallErrorText.h"
#include "Dialogs.h"
#include "GameInfo.h"

#include <QRegularExpression>
#include <QSet>
#include <QStringList>
#include <QUrl>

namespace gorganizer {

namespace {

const QRegularExpression& identifierPattern()
{
    static const QRegularExpression pattern(QStringLiteral("^[a-z][a-z0-9_]*$"));
    return pattern;
}

QString withDetail(const QString& text, const QString& detail)
{
    if (detail.isEmpty())
        return text + QLatin1Char('.');
    return QStringLiteral("%1: \"%2\".").arg(text, detail);
}

QString notAModMessage(const QString& reason, const QString& detail)
{
    if (reason == QLatin1String("no_manifest"))
        return QStringLiteral("This archive does not contain a SMAPI mod (no manifest.json).");
    if (reason == QLatin1String("loader_installer"))
        return QStringLiteral("This is the SMAPI installer, not a mod. Install SMAPI from Tools → SMAPI.");
    if (reason == QLatin1String("unsafe_destination"))
        return withDetail(QStringLiteral("This archive contains a mod folder whose name cannot be installed safely"),
                          detail);
    if (reason == QLatin1String("folder_collision"))
        return withDetail(QStringLiteral("This archive contains two mod folders whose names differ only in "
                                         "letter case, so they would install over each other"),
                          detail);
    if (reason == QLatin1String("duplicate_ids"))
        return withDetail(QStringLiteral("This archive contains several mod folders with the same SMAPI UniqueID "
                                         "(usually alternative versions of one mod), so SMAPI would load none of them"),
                          detail);
    if (reason == QLatin1String("nested_mods"))
        return withDetail(QStringLiteral("This archive contains a mod folder nested inside another mod folder, "
                                         "so it cannot be installed safely"),
                          detail);
    QString text = QStringLiteral("This archive is not a mod this game can use");
    if (!reason.isEmpty())
        text += QStringLiteral(" (%1)").arg(reason);
    return withDetail(text, detail);
}

QString genericTokenMessage(const QString& lead, const InstallError& parsed)
{
    QString text = QStringLiteral("%1 (%2).").arg(lead, parsed.token);
    for (auto it = parsed.fields.cbegin(); it != parsed.fields.cend(); ++it)
        text += QStringLiteral("\n%1: %2").arg(it.key(), it.value());
    return text;
}

QString gameName(const QString& gameId, const QString& fallback)
{
    if (const auto game = GameInfo::findByShortName(gameId); game && !game->name.isEmpty())
        return game->name;
    return fallback;
}

QString modLoaderBusyReason(const QString& operation, const QString& subject, const QString& subjectTitle)
{
    if (operation == QLatin1String("modloader") || operation == QLatin1String("transaction"))
        return QStringLiteral("Another SMAPI operation is in progress for %1. Wait for it to finish, then try again.")
            .arg(subject);
    if (operation == QLatin1String("mounted"))
        return QStringLiteral("The mods of %1 are mounted. Unmount them with Tools → Unmount Mods, then try again.")
            .arg(subject);
    if (operation == QLatin1String("running") || operation == QLatin1String("launch"))
        return QStringLiteral("%1 is running. Close it, then try again; gorganizer never changes SMAPI while the "
                              "game may be running.").arg(subjectTitle);
    if (operation == QLatin1String("tool"))
        return QStringLiteral("A tool started from gorganizer is running for %1. Close it, then try again.")
            .arg(subject);
    if (operation == QLatin1String("root_deployment"))
        return QStringLiteral("Root files of %1 are deployed into its game folder. Unmount the mods, then try again.")
            .arg(subject);
    if (operation == QLatin1String("mount") || operation == QLatin1String("unmount")
        || operation == QLatin1String("apply"))
        return QStringLiteral("The mod folder of %1 is being rebuilt. Try again when that finishes.").arg(subject);
    if (operation == QLatin1String("configure"))
        return QStringLiteral("%1 is being reconfigured. Try again when that finishes.").arg(subjectTitle);
    if (operation == QLatin1String("script_extender"))
        return QStringLiteral("A script extender is being installed for %1. Try again when that finishes.").arg(subject);
    if (operation == QLatin1String("import"))
        return QStringLiteral("An instance import is running for %1. Try again when it finishes.").arg(subject);
    if (operation == QLatin1String("reinstall"))
        return QStringLiteral("A mod of %1 is being reinstalled. Try again when that finishes.").arg(subject);
    if (operation.isEmpty())
        return QStringLiteral("%1 is busy. Try again when it is idle.").arg(subjectTitle);
    return QStringLiteral("%1 is busy (%2). Try again when that finishes.").arg(subjectTitle, operation);
}

QString modLoaderBusyMessage(const QString& operation, const QString& gameId, const QString& holderId)
{
    const bool otherHolder = !holderId.isEmpty() && holderId != gameId;
    const QString busyId = otherHolder ? holderId : gameId;
    const QString subject = gameName(busyId, otherHolder ? holderId : QStringLiteral("this game"));
    const QString subjectTitle = gameName(busyId, otherHolder ? holderId : QStringLiteral("The game"));
    QString text = modLoaderBusyReason(operation, subject, subjectTitle);
    if (otherHolder)
        text += QStringLiteral(" (%1 shares its game folder with %2.)")
                    .arg(subjectTitle, gameName(gameId, QStringLiteral("this game")));
    return text;
}

QString modLoaderFailedMessage(const QString& reason)
{
    if (reason == QLatin1String("interrupted"))
        return QStringLiteral("A previous SMAPI operation was interrupted. Restart gorganizer so it can "
                              "recover, then try again.");
    if (reason == QLatin1String("game_changed"))
        return QStringLiteral("Steam changed the game while SMAPI was installing. Try again.");
    if (reason == QLatin1String("no_vanilla_launcher"))
        return QStringLiteral("The game's original launcher could not be found. Verify the game files in "
                              "Steam, then repair SMAPI.");
    if (reason == QLatin1String("unsafe_target"))
        return QStringLiteral("SMAPI would have written to an unsafe location in the game folder (for "
                              "example through a symbolic link), so gorganizer refused the change.");
    if (reason == QLatin1String("farm_mounted"))
        return QStringLiteral("The game's Mods folder is still a mounted gorganizer mod view. Unmount the "
                              "mods, then try again.");
    if (reason == QLatin1String("stage_incomplete"))
        return QStringLiteral("The SMAPI installer did not produce a complete install, so gorganizer did "
                              "not apply it to the game.");
    if (reason == QLatin1String("stage_unexpected"))
        return QStringLiteral("The SMAPI installer produced files gorganizer did not expect, so gorganizer "
                              "did not apply them to the game.");
    if (reason == QLatin1String("cross_device"))
        return QStringLiteral("Part of the game folder is on a different filesystem mount, so SMAPI cannot "
                              "be changed safely there.");
    if (reason == QLatin1String("rename_unsupported"))
        return QStringLiteral("The game's filesystem does not support the safe no-replace renames "
                              "gorganizer needs, so SMAPI cannot be changed there.");
    if (reason == QLatin1String("digest_mismatch"))
        return QStringLiteral("The downloaded SMAPI release does not match its published SHA-256 checksum, "
                              "so gorganizer discarded it. Try again later.");
    if (reason == QLatin1String("not_stable"))
        return QStringLiteral("The newest SMAPI release is not a stable release, so gorganizer did not "
                              "install it.");
    if (reason == QLatin1String("no_digest"))
        return QStringLiteral("The SMAPI release does not publish a SHA-256 checksum, so gorganizer cannot "
                              "verify it and did not install it.");
    if (reason == QLatin1String("no_previous"))
        return QStringLiteral("No usable previous SMAPI version is kept, so there is nothing to roll back to.");
    if (reason == QLatin1String("no_artifact"))
        return QStringLiteral("gorganizer has no retained SMAPI installer to repair from. Use Install or Update "
                              "SMAPI instead.");
    return QStringLiteral("The SMAPI operation failed (%1).").arg(reason);
}

QString modLoaderUnavailableMessage(const QString& reason, const QString& gameId)
{
    if (reason == QLatin1String("unsupported_build"))
        return QStringLiteral("This %1 install is not the native Linux Steam build (for example a Windows "
                              "build run through Proton). gorganizer manages SMAPI only for the native "
                              "Linux build.").arg(gameName(gameId, QStringLiteral("game")));
    const QString text = QStringLiteral("SMAPI is %1.").arg(modLoaderUnavailableReasonText(reason));
    if (reason == QLatin1String("not_installed"))
        return text + QStringLiteral(" Install it from Tools → SMAPI.");
    if (reason == QLatin1String("interrupted"))
        return text + QStringLiteral(" Restart gorganizer so it can recover, or repair SMAPI from Tools → SMAPI.");
    return text + QStringLiteral(" Repair it from Tools → SMAPI.");
}

// Returns the plain-language explanation for an archive refused by the daemon.
QString archiveRejectedMessage(const QString& reason)
{
    if (reason == QLatin1String("nested_installer"))
        return QStringLiteral("This archive contains an unsafe nested installer. Nothing was installed.");
    if (reason == QLatin1String("limit"))
        return QStringLiteral("This archive is too large or contains too many files to install safely. "
                              "Nothing was installed.");
    if (reason == QLatin1String("destination"))
        return QStringLiteral("This download has an unsafe saved location. Download it again from Nexus Mods.");
    if (reason == QLatin1String("unsupported"))
        return QStringLiteral("This archive uses a format Gorganizer cannot open safely (for example a "
                              "multi-part or encrypted RAR). Try a ZIP or 7z version of the mod.");
    return QStringLiteral("This archive contains unsafe file names or links, so it was not installed. "
                          "Nothing was changed.");
}

QString knownTokenMessage(const InstallError& parsed)
{
    const QString& token = parsed.token;
    const auto field = [&parsed](const char* key) {
        return parsed.fields.value(QString::fromLatin1(key));
    };

    if (token == QLatin1String("mod_collision")) {
        const QString name = field("name");
        if (name.isEmpty())
            return QStringLiteral("A mod with this name is already installed.");
        return QStringLiteral("A mod named \"%1\" is already installed.").arg(name);
    }
    if (token == QLatin1String("not_a_mod"))
        return notAModMessage(field("reason"), field("detail"));
    if (token == QLatin1String("invalid_target_mod"))
        return QStringLiteral("\"%1\" cannot be used as a mod name. Mod names must not be empty, start with "
                              "a dot, contain slashes, or be \"Overwrite\" or \"Downloads\".")
            .arg(field("name"));
    if (token == QLatin1String("layout_unsupported"))
        return QStringLiteral("This install option is not supported for this game's mod layout (%1).")
            .arg(field("layout"));
    if (token == QLatin1String("fomod_unsupported"))
        return QStringLiteral("This archive is a FOMOD installer, which this game's mod layout does not support.");
    if (token == QLatin1String("fomod_required"))
        return QStringLiteral("This archive needs its FOMOD installer, which is not available for this game.");
    if (token == QLatin1String("mod_mounted"))
        return QStringLiteral("\"%1\" is enabled in the mounted profile. Unmount the game before reinstalling it.")
            .arg(field("mod"));
    if (token == QLatin1String("fomod_reinstall_unsupported"))
        return QStringLiteral("\"%1\" was installed through a FOMOD installer and cannot be reinstalled "
                              "automatically. Install it again from its archive instead.")
            .arg(field("mod"));
    if (token == QLatin1String("reinstall_source_missing"))
        return QStringLiteral("\"%1\" cannot be reinstalled because its source archive \"%2\" is missing or "
                              "unreadable. The mod was not changed.")
            .arg(field("mod"), field("path"));
    if (token == QLatin1String("archive_missing"))
        return QStringLiteral("The archive \"%1\" is no longer in the Downloads folder.").arg(field("path"));
    if (token == QLatin1String("unsafe_path"))
        return QStringLiteral("The daemon refused an unsafe path (%1).").arg(field("field"));
    if (token == QLatin1String("modloader_busy"))
        return modLoaderBusyMessage(field("operation"), field("game"), field("holder"));
    if (token == QLatin1String("modloader_unavailable"))
        return modLoaderUnavailableMessage(field("reason"), field("game"));
    if (token == QLatin1String("modloader_unsupported"))
        return QStringLiteral("%1 has no mod loader that gorganizer manages.")
            .arg(gameName(field("game"), QStringLiteral("This game")));
    if (token == QLatin1String("modloader_failed"))
        return modLoaderFailedMessage(field("reason"));
    if (token == QLatin1String("mod_dependencies_unsupported"))
        return QStringLiteral("%1 does not use SMAPI mod dependencies, so gorganizer cannot check them.")
            .arg(gameName(field("game"), QStringLiteral("This game")));
    if (token == QLatin1String("game_running"))
        return QStringLiteral("%1 is still running, or was started less than two minutes ago, so gorganizer "
                              "cannot apply your pending mod changes. Close the game, then press Run (or Apply) "
                              "again.")
            .arg(gameName(field("game"), QStringLiteral("The game")));
    if (token == QLatin1String("daemon_shutting_down"))
        return QStringLiteral("The gorganizer daemon is shutting down. Start gorganizer again to continue.");
    if (token == QLatin1String("archive_rejected"))
        return archiveRejectedMessage(field("reason"));
    if (token == QLatin1String("bundle_rejected")) {
        if (field("reason") == QLatin1String("limit"))
            return QStringLiteral("This backup is larger than Gorganizer's safety limits, so nothing was imported. "
                                  "Ask for a smaller export bundle.");
        return QStringLiteral("This backup contains unsafe names or file links, so nothing was imported.");
    }
    if (token == QLatin1String("install_selection_empty"))
        return QStringLiteral("No files are selected. Go back and choose at least one option to install.");
    if (token == QLatin1String("profile_identity_invalid"))
        return QStringLiteral("The profile \"%1\" has an invalid name or folder, so it was not changed.")
            .arg(field("name"));
    return QString();
}

}

InstallError parseInstallError(const QString& error)
{
    InstallError out;
    const int colon = error.indexOf(QLatin1Char(':'));
    const QString head = colon < 0 ? error : error.left(colon);
    if (!identifierPattern().match(head).hasMatch())
        return out;
    out.token = head;
    if (colon < 0)
        return out;

    const QString rest = error.mid(colon + 1);
    const QStringList parts = rest.split(QLatin1Char(':'));
    QString lastKey;
    qsizetype offset = 0;
    for (const QString& part : parts) {
        const qsizetype eq = part.indexOf(QLatin1Char('='));
        const QString key = eq > 0 ? part.left(eq) : QString();
        if (key == QLatin1String("detail")) {
            out.fields.insert(key, rest.mid(offset + eq + 1));
            break;
        }
        if (!key.isEmpty() && identifierPattern().match(key).hasMatch()) {
            lastKey = key;
            out.fields.insert(key, part.mid(eq + 1));
        } else if (!lastKey.isEmpty()) {
            out.fields[lastKey] += QLatin1Char(':') + part;
        }
        offset += part.size() + 1;
    }
    if (!tokenValuesPercentEscaped(out.token))
        return out;
    for (auto it = out.fields.begin(); it != out.fields.end(); ++it)
        it.value() = QUrl::fromPercentEncoding(it.value().toUtf8());
    return out;
}

bool tokenValuesPercentEscaped(const QString& token)
{
    static const QSet<QString> escaped = {
        QStringLiteral("not_a_mod"),
        QStringLiteral("manifest_layout_invalid"),
        QStringLiteral("mod_mounted"),
        QStringLiteral("fomod_reinstall_unsupported"),
        QStringLiteral("reinstall_source_missing"),
        QStringLiteral("mod_registration_failed"),
        QStringLiteral("invalid_target_mod"),
        QStringLiteral("modloader_busy"),
        QStringLiteral("modloader_unavailable"),
        QStringLiteral("modloader_unsupported"),
        QStringLiteral("modloader_failed"),
        QStringLiteral("mod_dependencies_unsupported"),
        QStringLiteral("game_running"),
        QStringLiteral("daemon_shutting_down"),
        QStringLiteral("archive_rejected"),
        QStringLiteral("bundle_rejected"),
        QStringLiteral("profile_identity_invalid"),
        QStringLiteral("install_selection_empty"),
    };
    return escaped.contains(token);
}

QString installErrorMessage(const QString& error)
{
    const InstallError parsed = parseInstallError(error);
    if (parsed.token.isEmpty())
        return error;
    const QString known = knownTokenMessage(parsed);
    if (!known.isEmpty())
        return known;
    return genericTokenMessage(QStringLiteral("The install failed"), parsed);
}

QString modLoaderErrorMessage(const QString& error)
{
    const InstallError parsed = parseInstallError(error);
    if (parsed.token.isEmpty())
        return error;
    const QString known = knownTokenMessage(parsed);
    if (!known.isEmpty())
        return known;
    if (parsed.fields.isEmpty())
        return QStringLiteral("The SMAPI operation failed: %1").arg(error);
    return genericTokenMessage(QStringLiteral("The SMAPI operation failed"), parsed);
}

QString modDependencyErrorMessage(const QString& error)
{
    const InstallError parsed = parseInstallError(error);
    if (parsed.token.isEmpty())
        return error;
    const QString known = knownTokenMessage(parsed);
    if (!known.isEmpty())
        return known;
    if (parsed.fields.isEmpty())
        return QStringLiteral("The SMAPI dependency request failed: %1").arg(error);
    return genericTokenMessage(QStringLiteral("The SMAPI dependency request failed"), parsed);
}

QString daemonErrorMessage(const QString& error)
{
    const InstallError parsed = parseInstallError(error);
    if (parsed.token.isEmpty())
        return error;
    const QString known = knownTokenMessage(parsed);
    return known.isEmpty() ? error : known;
}

QString modLoaderUnavailableReasonText(const QString& reason)
{
    if (reason == QLatin1String("not_installed"))
        return QStringLiteral("not installed");
    if (reason == QLatin1String("launcher_reverted"))
        return QStringLiteral("no longer started by the game's launcher (a Steam update or file verification "
                              "restored the original launcher)");
    if (reason == QLatin1String("incomplete"))
        return QStringLiteral("incomplete");
    if (reason == QLatin1String("unsupported_build"))
        return QStringLiteral("not supported on this game build");
    if (reason == QLatin1String("interrupted"))
        return QStringLiteral("unusable until an interrupted SMAPI operation is recovered");
    QString text = reason;
    return text.replace(QLatin1Char('_'), QLatin1Char(' '));
}

void showInstallError(QWidget* parent, const QString& title, const QString& error)
{
    dialogs::plainWarn(parent, title, installErrorMessage(error));
}

void showModLoaderError(QWidget* parent, const QString& title, const QString& error)
{
    dialogs::plainWarn(parent, title, modLoaderErrorMessage(error));
}

QString modNameProblem(const QString& name)
{
    if (name.isEmpty())
        return QStringLiteral("Enter a mod name.");
    if (name.startsWith(QLatin1Char('.')))
        return QStringLiteral("Mod names cannot start with a dot.");
    if (name.contains(QLatin1Char('/')) || name.contains(QLatin1Char('\\')))
        return QStringLiteral("Mod names cannot contain \"/\" or \"\\\".");
    if (name.compare(QLatin1String("Overwrite"), Qt::CaseInsensitive) == 0
        || name.compare(QLatin1String("Downloads"), Qt::CaseInsensitive) == 0)
        return QStringLiteral("\"%1\" is reserved by gorganizer. Choose a different mod name.").arg(name);
    return QString();
}

}
