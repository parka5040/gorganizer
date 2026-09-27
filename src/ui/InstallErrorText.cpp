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
        return text + QStringLiteral(". Try another archive.");
    return QStringLiteral("%1: \"%2\". Try another archive.").arg(text, detail);
}

QString notAModMessage(const QString& reason, const QString& detail)
{
    if (reason == QLatin1String("no_manifest"))
        return QStringLiteral("This archive does not contain a mod this game can use. Try another archive.");
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
        return withDetail(QStringLiteral("This archive contains several copies of the same mod, so the game "
                                         "cannot load them together"), detail);
    if (reason == QLatin1String("nested_mods"))
        return withDetail(QStringLiteral("This archive contains a mod folder nested inside another mod folder, "
                                         "so it cannot be installed safely"),
                          detail);
    return QStringLiteral("This archive is not a mod this game can use. Try another archive.");
}

QString genericTokenMessage(const QString& lead)
{
    return QStringLiteral("%1. Show details for more information.").arg(lead);
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
        return QStringLiteral("Another SMAPI change is in progress for %1. Wait for it to finish, then try again.")
            .arg(subject);
    if (operation == QLatin1String("mounted"))
        return QStringLiteral("%1 has active mods. Choose Tools → Unmount Mods, then try again.").arg(subject);
    if (operation == QLatin1String("running") || operation == QLatin1String("launch"))
        return QStringLiteral("%1 is running. Close the game, then try again.").arg(subjectTitle);
    if (operation == QLatin1String("tool"))
        return QStringLiteral("A tool started from gorganizer is running for %1. Close it, then try again.")
            .arg(subject);
    if (operation == QLatin1String("root_deployment"))
        return QStringLiteral("%1 has active mods in its game folder. Choose Tools → Unmount Mods, then try again.")
            .arg(subject);
    if (operation == QLatin1String("mount") || operation == QLatin1String("unmount")
        || operation == QLatin1String("apply"))
        return QStringLiteral("The mod folder of %1 is being rebuilt. Try again when that finishes.").arg(subject);
    if (operation == QLatin1String("configure"))
        return QStringLiteral("%1 is being reconfigured. Try again when that finishes.").arg(subjectTitle);
    if (operation == QLatin1String("script_extender"))
        return QStringLiteral("A script extender is being installed for %1. Try again when that finishes.").arg(subject);
    if (operation == QLatin1String("import"))
        return QStringLiteral("A backup is being imported for %1. Try again when it finishes.").arg(subject);
    if (operation == QLatin1String("reinstall"))
        return QStringLiteral("A mod of %1 is being reinstalled. Try again when that finishes.").arg(subject);
    return QStringLiteral("%1 is busy. Try again when it is idle.").arg(subjectTitle);
}

QString modLoaderBusyMessage(const QString& operation, const QString& gameId, const QString& holderId)
{
    const bool otherHolder = !holderId.isEmpty() && holderId != gameId;
    const QString busyId = otherHolder ? holderId : gameId;
    const QString subject = gameName(busyId, otherHolder ? QStringLiteral("the other game") : QStringLiteral("this game"));
    const QString subjectTitle = gameName(busyId, otherHolder ? QStringLiteral("The other game") : QStringLiteral("The game"));
    QString text = modLoaderBusyReason(operation, subject, subjectTitle);
    if (otherHolder)
        text += QStringLiteral(" (%1 shares its game folder with %2.)")
                    .arg(subjectTitle, gameName(gameId, QStringLiteral("this game")));
    return text;
}

QString modLoaderFailedMessage(const QString& reason)
{
    if (reason == QLatin1String("interrupted"))
        return QStringLiteral("A previous SMAPI change was interrupted. Restart Gorganizer to finish it, "
                              "then try again.");
    if (reason == QLatin1String("game_changed"))
        return QStringLiteral("Steam changed the game while SMAPI was installing. Try again.");
    if (reason == QLatin1String("no_vanilla_launcher"))
        return QStringLiteral("The game's original launcher could not be found. Verify the game files in "
                              "Steam, then repair SMAPI.");
    if (reason == QLatin1String("unsafe_target"))
        return QStringLiteral("A file in the game folder points to an unsafe location. Check the game files, "
                              "then try again.");
    if (reason == QLatin1String("farm_mounted"))
        return QStringLiteral("This game has active mods. Choose Tools → Unmount Mods, then try again.");
    if (reason == QLatin1String("stage_incomplete"))
        return QStringLiteral("The SMAPI installer did not finish preparing all its files. Try again later.");
    if (reason == QLatin1String("stage_unexpected"))
        return QStringLiteral("The SMAPI installer created unexpected files. Try again later.");
    if (reason == QLatin1String("cross_device"))
        return QStringLiteral("Parts of the game folder are on different drives. Move the game to one drive, "
                              "then try again.");
    if (reason == QLatin1String("rename_unsupported"))
        return QStringLiteral("This drive cannot safely update SMAPI. Move the game to another drive, then try again.");
    if (reason == QLatin1String("digest_mismatch"))
        return QStringLiteral("The SMAPI download did not match the published version. Try again later.");
    if (reason == QLatin1String("not_stable"))
        return QStringLiteral("The newest SMAPI version is not ready to install. Try again later.");
    if (reason == QLatin1String("no_digest"))
        return QStringLiteral("The SMAPI download could not be verified. Try again later.");
    if (reason == QLatin1String("no_previous"))
        return QStringLiteral("No previous SMAPI version is available. Install or update SMAPI instead.");
    if (reason == QLatin1String("no_artifact"))
        return QStringLiteral("The saved SMAPI installer is missing. Install or update SMAPI instead.");
    return QStringLiteral("The SMAPI change failed. Try again or show details for more information.");
}

QString modLoaderUnavailableMessage(const QString& reason, const QString& gameId)
{
    if (reason == QLatin1String("unsupported_build"))
        return QStringLiteral("This %1 installation is not the Linux Steam version. Install the Linux version "
                              "from Steam to use SMAPI here.").arg(gameName(gameId, QStringLiteral("game")));
    const QString text = QStringLiteral("SMAPI is %1.").arg(modLoaderUnavailableReasonText(reason));
    if (reason == QLatin1String("not_installed"))
        return text + QStringLiteral(" Install it from Tools → SMAPI.");
    if (reason == QLatin1String("interrupted"))
        return text + QStringLiteral(" Restart Gorganizer to finish the change, or repair SMAPI from Tools → SMAPI.");
    return text + QStringLiteral(" Repair it from Tools → SMAPI.");
}

// Returns the plain-language explanation for an archive refused by the daemon.
QString archiveRejectedMessage(const QString& reason)
{
    if (reason == QLatin1String("nested_installer"))
        return QStringLiteral("This archive contains an installer that cannot be opened safely. "
                              "Nothing was installed. Try another archive.");
    if (reason == QLatin1String("limit"))
        return QStringLiteral("This archive is too large or contains too many files to install safely. "
                              "Nothing was installed. Try a smaller archive.");
    if (reason == QLatin1String("destination"))
        return QStringLiteral("This download has an unsafe saved location. Download it again from Nexus Mods.");
    if (reason == QLatin1String("unsupported"))
        return QStringLiteral("This archive uses a format Gorganizer cannot open safely (for example a "
                              "multi-part or encrypted RAR). Try a ZIP or 7z version of the mod.");
    return QStringLiteral("This archive contains unsafe file names or links, so it was not installed. "
                          "Try another archive.");
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
            return QStringLiteral("A mod with this name is already installed. Choose a different name.");
        return QStringLiteral("A mod named \"%1\" is already installed. Choose a different name.").arg(name);
    }
    if (token == QLatin1String("mod_registration_failed"))
        return QStringLiteral("The files were installed, but the mod could not be added to your profiles. "
                              "Refresh before trying again.");
    if (token == QLatin1String("manifest_layout_invalid"))
        return QStringLiteral("\"%1\" has a folder layout this game cannot load. Install a compatible archive.")
            .arg(field("mod").isEmpty() ? QStringLiteral("This mod") : field("mod"));
    if (token == QLatin1String("not_a_mod"))
        return notAModMessage(field("reason"), field("detail"));
    if (token == QLatin1String("invalid_target_mod"))
        return QStringLiteral("\"%1\" cannot be used as a mod name. Choose a name that does not start with a "
                              "dot, contain slashes, or match \"Overwrite\" or \"Downloads\".")
            .arg(field("name").isEmpty() ? QStringLiteral("This name") : field("name"));
    if (token == QLatin1String("layout_unsupported"))
        return QStringLiteral("This install option does not work with this game. Choose another install option.");
    if (token == QLatin1String("fomod_unsupported"))
        return QStringLiteral("This game's mods cannot use this archive's installer. Try a different archive.");
    if (token == QLatin1String("fomod_required"))
        return QStringLiteral("This archive needs its FOMOD installer, which is not available for this game.");
    if (token == QLatin1String("mod_mounted"))
        return QStringLiteral("\"%1\" is part of the active mods. Choose \"Unmount Mods\", then try again.")
            .arg(field("mod"));
    if (token == QLatin1String("fomod_reinstall_unsupported"))
        return QStringLiteral("\"%1\" was installed through a FOMOD installer and cannot be reinstalled "
                              "automatically. Install it again from its archive instead.")
            .arg(field("mod"));
    if (token == QLatin1String("reinstall_source_missing"))
        return QStringLiteral("\"%1\" cannot be reinstalled because its original archive is missing or "
                              "unreadable. Download it again, then try again.")
            .arg(field("mod").isEmpty() ? QStringLiteral("This mod") : field("mod"));
    if (token == QLatin1String("archive_missing"))
        return QStringLiteral("This archive is no longer in Downloads. Download it again, then try again.");
    if (token == QLatin1String("unsafe_path"))
        return QStringLiteral("This file location is unsafe. Choose a different location, then try again.");
    if (token == QLatin1String("modloader_busy"))
        return modLoaderBusyMessage(field("operation"), field("game"), field("holder"));
    if (token == QLatin1String("modloader_unavailable"))
        return modLoaderUnavailableMessage(field("reason"), field("game"));
    if (token == QLatin1String("modloader_unsupported"))
        return QStringLiteral("Gorganizer cannot set up this game's mod support. Choose a supported game.");
    if (token == QLatin1String("modloader_failed"))
        return modLoaderFailedMessage(field("reason"));
    if (token == QLatin1String("loader_missing"))
        return QStringLiteral("The script extender is missing or needs repair.");
    if (token == QLatin1String("mod_dependencies_unsupported"))
        return QStringLiteral("%1 does not use SMAPI mod requirements. Check the mod's page for what it needs.")
            .arg(gameName(field("game"), QStringLiteral("This game")));
    if (token == QLatin1String("game_running")) {
        if (field("operation") == QLatin1String("unmount"))
            return QStringLiteral("%1 is still running, or was started less than two minutes ago, so its active "
                                  "mods cannot be turned off. Close the game, then try again.")
                .arg(gameName(field("game"), QStringLiteral("The game")));
        return QStringLiteral("%1 is still running, or was started less than two minutes ago, so gorganizer "
                              "cannot apply your pending mod changes. Close the game, then press Run (or Apply) "
                              "again.")
            .arg(gameName(field("game"), QStringLiteral("The game")));
    }
    if (token == QLatin1String("daemon_shutting_down"))
        return QStringLiteral("Gorganizer is closing. Start it again to continue.");
    if (token == QLatin1String("archive_rejected"))
        return archiveRejectedMessage(field("reason"));
    if (token == QLatin1String("bundle_rejected")) {
        if (field("reason") == QLatin1String("limit"))
            return QStringLiteral("This backup is too large to import safely. Ask for a smaller backup.");
        return QStringLiteral("This backup contains unsafe names or file links. Ask for a new backup.");
    }
    if (token == QLatin1String("plugin_state_failed"))
        return QStringLiteral("Gorganizer could not prepare the plugin list for %1, so the game was not started. "
                              "Check that the disk is not full, then try again.")
            .arg(gameName(field("game"), QStringLiteral("this game")));
    if (token == QLatin1String("farm_recovery_deferred"))
        return QStringLiteral("%1 still has an unfinished mod change from before. Gorganizer will finish it "
                              "after the game closes. Close the game, then try again.")
            .arg(gameName(field("game"), QStringLiteral("This game")));
    if (token == QLatin1String("install_record_failed"))
        return QStringLiteral("\"%1\" could not be installed because its install information could not be saved. "
                              "Check that the disk is not full, then try again.")
            .arg(field("mod").isEmpty() ? QStringLiteral("This mod") : field("mod"));
    if (token == QLatin1String("recovery_stale"))
        return QStringLiteral("The unfinished change for %1 changed before your choice was applied. "
                              "Nothing was restored. Review the new message, then choose again.")
            .arg(gameName(field("game"), QStringLiteral("this game")));
    if (token == QLatin1String("install_selection_empty"))
        return QStringLiteral("No files are selected. Go back and choose at least one option to install.");
    if (token == QLatin1String("profile_identity_invalid"))
        return QStringLiteral("The profile \"%1\" has a name or folder that cannot be used. "
                              "Choose a different profile name.")
            .arg(field("name").isEmpty() ? QStringLiteral("this profile") : field("name"));
    if (token == QLatin1String("mod_not_found"))
        return QStringLiteral("This mod is no longer installed. Refresh the list.");
    if (token == QLatin1String("mod_in_use"))
        return QStringLiteral("This mod is used by other profiles. Review them before removing it.");
    if (token == QLatin1String("nxm_expired"))
        return QStringLiteral("This download link has expired. Download the file again from Nexus Mods.");
    if (token == QLatin1String("download_not_found"))
        return QStringLiteral("This download is no longer available. Refresh Downloads.");
    if (token == QLatin1String("preview_not_found"))
        return QStringLiteral("The installer preview has expired. Open the installer again.");
    if (token == QLatin1String("vfs_mutex"))
        return QStringLiteral("Another game sharing these files has active mods. Close that game before switching.");
    if (token == QLatin1String("linked_parent_missing"))
        return QStringLiteral("Add the required base game before using this game.");
    if (token == QLatin1String("ttw_drift"))
        return QStringLiteral("The Tale of Two Wastelands installation needs attention before it can launch.");
    if (token == QLatin1String("prefix_missing"))
        return QStringLiteral("Start this game once from Steam, close it, then try again.");
    if (token == QLatin1String("steam_not_running"))
        return QStringLiteral("Open Steam, then try again.");
    if (token == QLatin1String("ttw_requires_vanilla_fnv"))
        return QStringLiteral("Deactivate Fallout: New Vegas mods before installing Tale of Two Wastelands.");
    if (token == QLatin1String("xnvse_missing_for_ttw"))
        return QStringLiteral("Install xNVSE from the menu beside Run, then try again.");
    if (token == QLatin1String("fnv4gb_not_applied_for_ttw"))
        return QStringLiteral("Choose Tools → Patch Fallout to 4GB, then try again.");
    if (token == QLatin1String("transfer_game_mismatch"))
        return QStringLiteral("This backup belongs to a different game.");
    if (token == QLatin1String("transfer_schema"))
        return QStringLiteral("This backup uses an unsupported format. Update Gorganizer and try again.");
    if (token == QLatin1String("transfer_path"))
        return QStringLiteral("This backup contains an unsafe file location and cannot be imported.");
    if (token == QLatin1String("transfer_collision"))
        return QStringLiteral("An item with this name already exists. Choose Rename or Skip.");
    if (token == QLatin1String("transfer_overwrite_mounted"))
        return QStringLiteral("Deactivate mods before replacing items used by the active profile.");
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
        QStringLiteral("plugin_state_failed"),
        QStringLiteral("farm_recovery_deferred"),
        QStringLiteral("recovery_stale"),
        QStringLiteral("install_record_failed"),
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
    return genericTokenMessage(QStringLiteral("The install failed"));
}

QString modLoaderErrorMessage(const QString& error)
{
    const InstallError parsed = parseInstallError(error);
    if (parsed.token.isEmpty())
        return error;
    const QString known = knownTokenMessage(parsed);
    if (!known.isEmpty())
        return known;
    return genericTokenMessage(QStringLiteral("The SMAPI operation failed"));
}

QString modDependencyErrorMessage(const QString& error)
{
    const InstallError parsed = parseInstallError(error);
    if (parsed.token.isEmpty())
        return error;
    const QString known = knownTokenMessage(parsed);
    if (!known.isEmpty())
        return known;
    return genericTokenMessage(QStringLiteral("The SMAPI dependency request failed"));
}

QString daemonErrorMessage(const QString& error)
{
    const InstallError parsed = parseInstallError(error);
    if (parsed.token.isEmpty())
        return error;
    const QString known = knownTokenMessage(parsed);
    return known.isEmpty() ? genericTokenMessage(QStringLiteral("The request failed")) : known;
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
        return QStringLiteral("not supported for this version of the game");
    if (reason == QLatin1String("interrupted"))
        return QStringLiteral("unavailable after an interrupted change");
    return QStringLiteral("not ready");
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
