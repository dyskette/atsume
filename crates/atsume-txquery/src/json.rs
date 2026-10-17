//! internettools' `json()`: reading JSON leniently, as a tree of nodes.
//!
//! Upstream gives `json()` a liberal parser, because sites embed JavaScript
//! object literals as page data: single-quoted strings, unquoted keys,
//! trailing commas, `TRUE` in any case, `NaN`, and several values in a row.
//!
//! The result is a node tree, so internettools' path steps on JSON work as
//! ordinary steps: an object's property is a child element named after its
//! key, so `obj/name` reads it and `obj/*` gives every value in order; an
//! array's members are `_` children; a scalar is text, and `null` is the text
//! `"null"`, as upstream renders it. A plain step through an array without
//! unboxing it first (`?*`, `()` or `/*`) selects nothing here, where
//! upstream applies it to each member.

use std::ops::Range;

use xot::{Node, Xot};

/// The element name for an array member, and for an empty key.
pub(crate) const MEMBER: &str = "_";

/// How deeply values may nest, so a hostile document cannot exhaust the
/// stack.
const MAX_DEPTH: usize = 256;

/// A JSON value, with numbers kept as internettools prints them.
#[derive(Clone, Debug, PartialEq)]
pub(crate) enum Json {
    Object(Vec<(String, Json)>),
    Array(Vec<Json>),
    String(String),
    Number(String),
    Bool(bool),
    Null,
}

/// Parses every value in `text`, which may hold none, one or several.
pub(crate) fn parse(text: &str) -> Result<Vec<Json>, String> {
    let mut reader = Reader {
        chars: text.char_indices().peekable(),
        text,
        depth: 0,
    };
    let mut values = Vec::new();
    loop {
        reader.skip_separators();
        if reader.chars.peek().is_none() {
            return Ok(values);
        }
        values.push(reader.value()?);
    }
}

struct Reader<'t> {
    chars: std::iter::Peekable<std::str::CharIndices<'t>>,
    text: &'t str,
    depth: usize,
}

impl Reader<'_> {
    fn peek(&mut self) -> Option<char> {
        self.chars.peek().map(|&(_, c)| c)
    }

    fn skip_whitespace(&mut self) {
        while self.peek().is_some_and(|c| c.is_whitespace() || c == '\0') {
            self.chars.next();
        }
    }

    /// Skips what may separate top-level values: whitespace and commas.
    fn skip_separators(&mut self) {
        while self
            .peek()
            .is_some_and(|c| c.is_whitespace() || c == '\0' || c == ',')
        {
            self.chars.next();
        }
    }

    fn expect(&mut self, wanted: char) -> Result<(), String> {
        self.skip_whitespace();
        match self.chars.next() {
            Some((_, c)) if c == wanted => Ok(()),
            Some((at, c)) => Err(format!("expected {wanted:?} at {at}, found {c:?}")),
            None => Err(format!("expected {wanted:?}, found the end")),
        }
    }

    fn value(&mut self) -> Result<Json, String> {
        self.skip_whitespace();
        match self.peek() {
            Some('{') => self.nested(Self::object),
            Some('[') => self.nested(Self::array),
            Some(quote @ ('"' | '\'')) => self.string(quote).map(Json::String),
            Some(c) if c == '-' || c == '.' || c.is_ascii_digit() => self.number(),
            Some(c) if c.is_alphabetic() || c == '_' => self.word(),
            Some(c) => Err(format!("unexpected {c:?}")),
            None => Err("unexpected end".to_owned()),
        }
    }

    fn nested(&mut self, read: fn(&mut Self) -> Result<Json, String>) -> Result<Json, String> {
        self.depth += 1;
        if self.depth > MAX_DEPTH {
            return Err(format!("nested deeper than {MAX_DEPTH} levels"));
        }
        let value = read(self);
        self.depth -= 1;
        value
    }

    fn object(&mut self) -> Result<Json, String> {
        self.expect('{')?;
        let mut properties = Vec::new();
        loop {
            self.skip_whitespace();
            match self.peek() {
                Some('}') => break,
                Some(quote @ ('"' | '\'')) => {
                    properties.push((self.string(quote)?, self.after_key()?));
                }
                Some(_) => properties.push((self.identifier(), self.after_key()?)),
                None => return Err("unterminated object".to_owned()),
            }
            if !self.comma_before('}')? {
                break;
            }
        }
        self.expect('}')?;
        Ok(Json::Object(properties))
    }

    fn after_key(&mut self) -> Result<Json, String> {
        self.expect(':')?;
        self.value()
    }

    fn array(&mut self) -> Result<Json, String> {
        self.expect('[')?;
        let mut members = Vec::new();
        loop {
            self.skip_whitespace();
            if self.peek() == Some(']') {
                break;
            }
            members.push(self.value()?);
            if !self.comma_before(']')? {
                break;
            }
        }
        self.expect(']')?;
        Ok(Json::Array(members))
    }

    /// Reads the comma after a member, and reports whether another member
    /// may follow. A comma right before `close` is allowed and ends the list.
    fn comma_before(&mut self, close: char) -> Result<bool, String> {
        self.skip_whitespace();
        match self.peek() {
            Some(',') => {
                self.chars.next();
                Ok(true)
            }
            Some(c) if c == close => Ok(false),
            Some(c) => Err(format!("expected ',' or {close:?}, found {c:?}")),
            None => Err(format!("expected {close:?}, found the end")),
        }
    }

    fn string(&mut self, quote: char) -> Result<String, String> {
        self.chars.next();
        let mut out = String::new();
        loop {
            match self.chars.next() {
                Some((_, c)) if c == quote => return Ok(out),
                Some((_, '\\')) => self.escape(&mut out)?,
                Some((_, c)) => out.push(c),
                None => return Err("unterminated string".to_owned()),
            }
        }
    }

    fn escape(&mut self, out: &mut String) -> Result<(), String> {
        let Some((_, c)) = self.chars.next() else {
            return Err("unterminated escape".to_owned());
        };
        let unescaped = match c {
            'n' => '\n',
            'r' => '\r',
            't' => '\t',
            'b' => '\u{8}',
            'f' => '\u{c}',
            'u' => return self.unicode_escape(out),
            other => other,
        };
        out.push(unescaped);
        Ok(())
    }

    /// Reads the four hex digits after `\u` and adds the character they
    /// encode.
    ///
    /// JSON writes a character beyond the Basic Multilingual Plane as two
    /// escapes, a high surrogate then a low one, and they are joined here. A
    /// surrogate without its partner becomes U+FFFD, and whatever follows it
    /// is read as usual.
    fn unicode_escape(&mut self, out: &mut String) -> Result<(), String> {
        let unit = self.hex_digits()?;
        if !HIGH_SURROGATES.contains(&unit) {
            out.push(char::from_u32(unit).unwrap_or(char::REPLACEMENT_CHARACTER));
            return Ok(());
        }
        match self.low_surrogate_next() {
            Some(low) => out.push(join_surrogates(unit, low)),
            None => out.push(char::REPLACEMENT_CHARACTER),
        }
        Ok(())
    }

    /// Reads a `\uXXXX` escape holding a low surrogate if one comes next,
    /// and otherwise reads nothing.
    fn low_surrogate_next(&mut self) -> Option<u32> {
        let mut ahead = self.chars.clone();
        let starts_escape =
            ahead.next().map(|(_, c)| c) == Some('\\') && ahead.next().map(|(_, c)| c) == Some('u');
        if !starts_escape {
            return None;
        }
        let digits: String = ahead.take(ESCAPE_DIGITS).map(|(_, c)| c).collect();
        let unit = u32::from_str_radix(&digits, 16)
            .ok()
            .filter(|u| LOW_SURROGATES.contains(u))?;
        self.chars.nth(ESCAPE_DIGITS + 1);
        Some(unit)
    }

    /// Reads the hex digits of a `\u` escape.
    fn hex_digits(&mut self) -> Result<u32, String> {
        let digits: String = (0..ESCAPE_DIGITS)
            .filter_map(|_| self.chars.next().map(|(_, c)| c))
            .collect();
        u32::from_str_radix(&digits, 16).map_err(|_| format!("invalid \\u escape {digits:?}"))
    }

    fn number(&mut self) -> Result<Json, String> {
        let start = self.chars.peek().map_or(self.text.len(), |&(at, _)| at);
        let mut end = start;
        while let Some(&(at, c)) = self.chars.peek() {
            if !(c.is_ascii_digit() || matches!(c, '-' | '+' | '.' | 'e' | 'E')) {
                break;
            }
            end = at + c.len_utf8();
            self.chars.next();
        }
        number_text(&self.text[start..end]).map(Json::Number)
    }

    /// Reads a bare word: `true`, `false` or `null` in any case, or `NaN` or
    /// `Infinity`.
    fn word(&mut self) -> Result<Json, String> {
        let word = self.identifier();
        match word.to_ascii_lowercase().as_str() {
            "true" => Ok(Json::Bool(true)),
            "false" => Ok(Json::Bool(false)),
            "null" => Ok(Json::Null),
            "nan" => Ok(Json::Number("NaN".to_owned())),
            "inf" | "infinity" => Ok(Json::Number("INF".to_owned())),
            _ => Err(format!("unexpected word {word:?}")),
        }
    }

    fn identifier(&mut self) -> String {
        let mut word = String::new();
        while let Some(c) = self
            .peek()
            .filter(|c| c.is_alphanumeric() || *c == '_' || *c == '$')
        {
            word.push(c);
            self.chars.next();
        }
        word
    }
}

/// How many hex digits a `\u` escape has.
const ESCAPE_DIGITS: usize = 4;

/// The UTF-16 code units that start a surrogate pair.
const HIGH_SURROGATES: Range<u32> = 0xD800..0xDC00;

/// The UTF-16 code units that end a surrogate pair.
const LOW_SURROGATES: Range<u32> = 0xDC00..0xE000;

/// The first character beyond the Basic Multilingual Plane. A surrogate pair
/// encodes a character as its distance from this one.
const FIRST_SUPPLEMENTARY: u32 = 0x10000;

/// How many bits of that distance each surrogate carries: the high one the
/// top ten, the low one the bottom ten.
const SURROGATE_BITS: u32 = 10;

/// Joins a high and a low surrogate into the character they encode.
fn join_surrogates(high: u32, low: u32) -> char {
    let top = (high - HIGH_SURROGATES.start) << SURROGATE_BITS;
    let bottom = low - LOW_SURROGATES.start;
    char::from_u32(FIRST_SUPPLEMENTARY + (top | bottom)).unwrap_or(char::REPLACEMENT_CHARACTER)
}

/// Renders a number as internettools prints it: an integer as its digits, and
/// anything else as the shortest decimal, switching to exponent form outside
/// `[0.000001, 1000000)`.
fn number_text(lexeme: &str) -> Result<String, String> {
    if !lexeme.contains(['.', 'e', 'E']) {
        if let Ok(n) = lexeme.parse::<i64>() {
            return Ok(n.to_string());
        }
        let digits = lexeme.trim_start_matches('-').trim_start_matches('0');
        let sign = if lexeme.starts_with('-') { "-" } else { "" };
        if !digits.is_empty() && digits.bytes().all(|b| b.is_ascii_digit()) {
            return Ok(format!("{sign}{digits}"));
        }
    }
    let value: f64 = lexeme
        .parse()
        .map_err(|_| format!("invalid number {lexeme:?}"))?;
    let magnitude = value.abs();
    if value == 0.0 || (1e-6..1e6).contains(&magnitude) {
        Ok(value.to_string())
    } else {
        Ok(format!("{value:E}"))
    }
}

/// Builds each value as a document of its own and returns the document
/// nodes. A document's children are the value: an object's properties, an
/// array's members, or a scalar's text.
pub(crate) fn build(xot: &mut Xot, values: &[Json]) -> Vec<Node> {
    values
        .iter()
        .map(|value| {
            let document = xot.new_document();
            append(xot, document, value);
            document
        })
        .collect()
}

fn append(xot: &mut Xot, parent: Node, value: &Json) {
    let text = match value {
        Json::Object(properties) => {
            for (key, property) in properties {
                append_element(
                    xot,
                    parent,
                    if key.is_empty() { MEMBER } else { key },
                    property,
                );
            }
            return;
        }
        Json::Array(members) => {
            for member in members {
                append_element(xot, parent, MEMBER, member);
            }
            return;
        }
        Json::String(text) | Json::Number(text) => text.as_str(),
        Json::Bool(true) => "true",
        Json::Bool(false) => "false",
        Json::Null => "null",
    };
    if !text.is_empty() {
        let node = xot.new_text(text);
        tolerate(xot.append(parent, node));
    }
}

fn append_element(xot: &mut Xot, parent: Node, name: &str, value: &Json) {
    let name = xot.add_name(name);
    let element = xot.new_element(name);
    append(xot, element, value);
    tolerate(xot.append(parent, element));
}

fn tolerate(result: Result<(), xot::Error>) {
    if let Err(error) = result {
        tracing::debug!(%error, "a JSON value was left out of its tree");
    }
}

#[cfg(test)]
mod tests;
