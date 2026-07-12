#pragma once

#include <filesystem>

namespace gorganizer {

namespace Paths {

std::filesystem::path configHome();
std::filesystem::path dataHome();

std::filesystem::path appConfigDir();
std::filesystem::path appDataDir();

std::filesystem::path steamRoot();

}

}
