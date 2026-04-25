#pragma once

#include <QString>
#include <QVariantMap>
#include <filesystem>
#include <optional>

namespace gorganizer {

class VdfParser {
public:
    static std::optional<QVariantMap> parseFile(const std::filesystem::path& filepath);
    static std::optional<QVariantMap> parse(const QString& content);

private:
    struct Token {
        enum Type { String, BraceOpen, BraceClose, EndOfInput };
        Type type;
        QString value;
    };

    class Tokenizer {
    public:
        explicit Tokenizer(const QString& input);
        Token next();

    private:
        QString m_input;
        int m_pos = 0;
        void skipWhitespaceAndComments();
    };

    static std::optional<QVariantMap> parseObject(Tokenizer& tok);
};

} // namespace gorganizer
