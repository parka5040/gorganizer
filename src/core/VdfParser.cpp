#include "VdfParser.h"
#include <QFile>
#include <QTextStream>

namespace gorganizer {

VdfParser::Tokenizer::Tokenizer(const QString& input)
    : m_input(input)
{
}

void VdfParser::Tokenizer::skipWhitespaceAndComments()
{
    while (m_pos < m_input.size()) {
        QChar c = m_input[m_pos];
        if (c.isSpace()) {
            ++m_pos;
            continue;
        }
        if (m_pos + 1 < m_input.size() && c == '/' && m_input[m_pos + 1] == '/') {
            while (m_pos < m_input.size() && m_input[m_pos] != '\n')
                ++m_pos;
            continue;
        }
        break;
    }
}

VdfParser::Token VdfParser::Tokenizer::next()
{
    skipWhitespaceAndComments();

    if (m_pos >= m_input.size())
        return {Token::EndOfInput, {}};

    QChar c = m_input[m_pos];

    if (c == '{') {
        ++m_pos;
        return {Token::BraceOpen, {}};
    }
    if (c == '}') {
        ++m_pos;
        return {Token::BraceClose, {}};
    }
    if (c == '"') {
        ++m_pos;
        QString value;
        while (m_pos < m_input.size()) {
            QChar ch = m_input[m_pos];
            if (ch == '\\' && m_pos + 1 < m_input.size()) {
                QChar escaped = m_input[m_pos + 1];
                if (escaped == '"' || escaped == '\\') {
                    value += escaped;
                    m_pos += 2;
                    continue;
                }
            }
            if (ch == '"') {
                ++m_pos;
                return {Token::String, value};
            }
            value += ch;
            ++m_pos;
        }
        return {Token::String, value};
    }

    QString value;
    while (m_pos < m_input.size() && !m_input[m_pos].isSpace()
           && m_input[m_pos] != '{' && m_input[m_pos] != '}') {
        value += m_input[m_pos];
        ++m_pos;
    }
    return {Token::String, value};
}

std::optional<QVariantMap> VdfParser::parseObject(Tokenizer& tok)
{
    QVariantMap map;
    for (;;) {
        Token key = tok.next();
        if (key.type == Token::BraceClose || key.type == Token::EndOfInput)
            return map;
        if (key.type != Token::String)
            return std::nullopt;

        Token valueOrBrace = tok.next();
        if (valueOrBrace.type == Token::BraceOpen) {
            auto sub = parseObject(tok);
            if (!sub)
                return std::nullopt;
            map[key.value] = *sub;
        } else if (valueOrBrace.type == Token::String) {
            map[key.value] = valueOrBrace.value;
        } else {
            return std::nullopt;
        }
    }
}

std::optional<QVariantMap> VdfParser::parse(const QString& content)
{
    Tokenizer tok(content);

    Token rootKey = tok.next();
    if (rootKey.type != Token::String)
        return std::nullopt;

    Token brace = tok.next();
    if (brace.type != Token::BraceOpen)
        return std::nullopt;

    auto rootObj = parseObject(tok);
    if (!rootObj)
        return std::nullopt;

    QVariantMap result;
    result[rootKey.value] = *rootObj;
    return result;
}

std::optional<QVariantMap> VdfParser::parseFile(const std::filesystem::path& filepath)
{
    QFile file(QString::fromStdString(filepath.string()));
    if (!file.open(QIODevice::ReadOnly | QIODevice::Text))
        return std::nullopt;

    QTextStream stream(&file);
    QString content = stream.readAll();
    return parse(content);
}

}
