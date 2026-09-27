#pragma once

#include <filesystem>
#include <vector>

namespace gorganizer {

namespace Paths {

std::filesystem::path configHome();
std::filesystem::path dataHome();

std::filesystem::path appConfigDir();
std::filesystem::path appDataDir();

std::vector<std::filesystem::path> steamRoots();
std::filesystem::path steamRoot();

}

}
