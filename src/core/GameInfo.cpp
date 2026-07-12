#include "GameInfo.h"
#include <QDir>
#include <algorithm>

namespace gorganizer {

// Layout contract: internal/gamedef/consistency_test.go parses each entry's first line as `{appId, "Name", "shortName",` (positional, no designated initializers) and stops at the `};` line.
const std::vector<GameInfo>& GameInfo::knownGames()
{
    static const std::vector<GameInfo> games = {
        {22320, "The Elder Scrolls III: Morrowind", "morrowind",
         {}, {}, false, false, "", false,
         "Data Files", "Morrowind_Mods", {"Morrowind.exe", "Morrowind Launcher.exe"},
         {"Morrowind.esm"}, "", "", "",
         {"Morrowind.esm", "Tribunal.esm", "Bloodmoon.esm"}, {}},
        {22330, "The Elder Scrolls IV: Oblivion", "oblivion",
         {}, {}, false, false, "", false,
         "Data", "Oblivion_Mods", {"Oblivion.exe", "OblivionLauncher.exe"},
         {"Oblivion.esm"}, "obse", "OBSE", "obse_loader.exe",
         {"Oblivion.esm"}, {}},
        {72850, "The Elder Scrolls V: Skyrim", "skyrim",
         {}, {}, false, false, "", false,
         "Data", "Skyrim_Mods", {"TESV.exe", "SkyrimLauncher.exe"},
         {"Skyrim.esm"}, "skse", "SKSE", "skse_loader.exe",
         {"Skyrim.esm"}, {}},
        {489830, "The Elder Scrolls V: Skyrim Special Edition", "skyrimse",
         {}, {}, false, false, "", false,
         "Data", "SkyrimSE_Mods", {"SkyrimSE.exe", "SkyrimSELauncher.exe"},
         {"Skyrim.esm"}, "skse64", "SKSE64", "skse64_loader.exe",
         {"Skyrim.esm", "Update.esm", "Dawnguard.esm", "HearthFires.esm", "Dragonborn.esm"}, {}},
        {22370, "Fallout 3", "fallout3",
         {}, {}, false, false, "", false,
         "Data", "Fallout3_Mods", {"Fallout3.exe", "FalloutLauncher.exe"},
         {"Fallout3.esm"}, "fose", "FOSE", "fose_loader.exe",
         {"Fallout3.esm"},
         {"Anchorage.esm", "ThePitt.esm", "BrokenSteel.esm",
          "PointLookout.esm", "Zeta.esm"}},
        {22380, "Fallout: New Vegas", "falloutnv",
         {}, {}, false, false, "", false,
         "Data", "FalloutNV_Mods", {"FalloutNV.exe", "FalloutNVLauncher.exe"},
         {"FalloutNV.esm"}, "xnvse", "xNVSE", "nvse_loader.exe",
         {"FalloutNV.esm"},
         {"DeadMoney.esm", "HonestHearts.esm", "OldWorldBlues.esm",
          "LonesomeRoad.esm", "GunRunnersArsenal.esm",
          "ClassicPack.esm", "MercenaryPack.esm", "TribalPack.esm", "CaravanPack.esm"}},
        {377160, "Fallout 4", "fallout4",
         {}, {}, false, false, "", false,
         "Data", "Fallout4_Mods", {"Fallout4.exe", "Fallout4Launcher.exe"},
         {"Fallout4.esm"}, "f4se", "F4SE", "f4se_loader.exe",
         {"Fallout4.esm"}, {}},
        {1716740, "Starfield", "starfield",
         {}, {}, false, false, "", false,
         "Data", "Starfield_Mods", {"Starfield.exe"}, {"Starfield.esm"},
         "sfse", "SFSE", "sfse_loader.exe",
         {"Starfield.esm"}, {}},
        {2623190, "The Elder Scrolls IV: Oblivion Remastered", "oblivionremastered",
         {}, {}, false, false, "", false,
         "OblivionRemastered/Content/Dev/ObvData/Data", "OblivionRemastered_Mods",
         {"OblivionRemastered.exe",
          "OblivionRemastered/Binaries/Win64/OblivionRemastered-Win64-Shipping.exe"},
         {"Oblivion.esm"}, "obse64", "OBSE64", "obse64_loader.exe",
         {"Oblivion.esm"},
         {"DLCBattlehornCastle.esp", "DLCFrostcrag.esp", "DLCHorseArmor.esp",
          "DLCMehrunesRazor.esp", "DLCOrrery.esp", "DLCShiveringIsles.esp",
          "DLCSpellTomes.esp", "DLCThievesDen.esp", "DLCVileLair.esp",
          "Knights.esp", "AltarESPMain.esp", "AltarDeluxe.esp", "AltarESPLocal.esp"}},
        {0, "Tale of Two Wastelands", "ttw",
         {}, {}, false, true, "falloutnv", false,
         "Data", "TTW_Mods", {}, {}, "", "", "",
         {"FalloutNV.esm"},
         {"DeadMoney.esm", "HonestHearts.esm", "OldWorldBlues.esm",
          "LonesomeRoad.esm", "GunRunnersArsenal.esm",
          "ClassicPack.esm", "MercenaryPack.esm", "TribalPack.esm", "CaravanPack.esm",
          "Fallout3.esm",
          "Anchorage.esm", "ThePitt.esm", "BrokenSteel.esm",
          "PointLookout.esm", "Zeta.esm",
          "TaleOfTwoWastelands.esm"}},
    };
    return games;
}

std::optional<GameInfo> GameInfo::findIn(const std::vector<GameInfo>& games, uint32_t appId)
{
    if (appId == 0) {
        return std::nullopt;
    }
    auto it = std::find_if(games.begin(), games.end(),
                           [appId](const GameInfo& g) { return g.appId == appId; });
    if (it != games.end())
        return *it;
    return std::nullopt;
}

std::optional<GameInfo> GameInfo::findByShortName(const QString& shortName)
{
    const auto& games = knownGames();
    auto it = std::find_if(games.begin(), games.end(),
                           [&shortName](const GameInfo& g) { return g.shortName == shortName; });
    if (it != games.end())
        return *it;
    return std::nullopt;
}

std::optional<GameInfo> GameInfo::findByExeStem(const QString& stem)
{
    const auto& games = knownGames();
    auto it = std::find_if(games.begin(), games.end(),
                           [&stem](const GameInfo& g) {
        return std::any_of(g.executablePaths.begin(), g.executablePaths.end(),
                           [&stem](const QString& relPath) {
            const auto path = std::filesystem::path(relPath.toStdString());
            return QString::fromStdString(path.stem().string()).compare(
                       stem, Qt::CaseInsensitive) == 0;
        });
    });
    if (it != games.end())
        return *it;
    return std::nullopt;
}

// Absolute mods directory for a game: GORGANIZER_ROOT/<ModsDirName> when the override is set, else XDG data home layout.
QString GameInfo::modsDirPathFor(const QString& shortName)
{
    QByteArray root = qgetenv("GORGANIZER_ROOT");
    if (!root.isEmpty()) {
        QString name = shortName + "_Mods";
        if (auto game = findByShortName(shortName); game && !game->modsDirName.isEmpty())
            name = game->modsDirName;
        return QString::fromUtf8(root) + "/" + name;
    }
    QString dataHome = qEnvironmentVariable("XDG_DATA_HOME");
    if (dataHome.isEmpty())
        dataHome = QDir::homePath() + "/.local/share";
    return dataHome + "/gorganizer/" + shortName + "/mods";
}

QStringList GameInfo::mastersFor(const QString& shortName)
{
    if (auto game = findByShortName(shortName))
        return game->canonicalMasters;
    return {};
}

QStringList GameInfo::dlcOrderFor(const QString& shortName)
{
    if (auto game = findByShortName(shortName))
        return game->canonicalDlcOrder;
    return {};
}

}
