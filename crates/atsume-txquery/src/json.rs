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

    /// Reads `\uXXXX`, joining a surrogate pair into one character.
    fn unicode_escape(&mut self, out: &mut String) -> Result<(), String> {
        let high = self.hex4()?;
        if !(0xD800..0xDC00).contains(&high) {
            out.push(char::from_u32(high).unwrap_or('\u{fffd}'));
            return Ok(());
        }
        let rest: String = self.chars.clone().take(6).map(|(_, c)| c).collect();
        if !rest.starts_with("\\u") {
            out.push('\u{fffd}');
            return Ok(());
        }
        self.chars.next();
        self.chars.next();
        let low = self.hex4()?;
        let joined = 0x10000 + ((high - 0xD800) << 10) + (low.wrapping_sub(0xDC00) & 0x3FF);
        out.push(char::from_u32(joined).unwrap_or('\u{fffd}'));
        Ok(())
    }

    fn hex4(&mut self) -> Result<u32, String> {
        let digits: String = (0..4)
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

/// A call to `json()` in an expression.
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct Call {
    /// The whole call, from `json` to its closing parenthesis.
    pub(crate) span: Range<usize>,
    /// The argument between the parentheses.
    pub(crate) argument: Range<usize>,
}

/// Finds the `json()` calls in `expression`, outermost only, in order.
///
/// A call nested inside another's argument is found when that argument is
/// evaluated. `parse-json()` and names that merely end in `json` are not
/// calls. A call inside a path step, which upstream evaluates once per node,
/// is refused, since its argument cannot be evaluated beforehand.
pub(crate) fn calls(expression: &str) -> Result<Vec<Call>, String> {
    let bytes = expression.as_bytes();
    let mut found = Vec::new();
    let mut i = 0;
    while i < bytes.len() {
        match bytes[i] {
            quote @ (b'"' | b'\'') => i = skip_string(bytes, i, quote),
            b'j' if starts_call(bytes, i) => {
                let call = call_at(expression, i)?;
                i = call.span.end;
                found.push(call);
            }
            _ => i += 1,
        }
    }
    Ok(found)
}

/// Reports whether a `json(` call starts at `i`, rather than a longer name.
fn starts_call(bytes: &[u8], i: usize) -> bool {
    let is_name = |b: u8| b.is_ascii_alphanumeric() || matches!(b, b'-' | b'_' | b'.' | b':');
    let after = &bytes[i..];
    let preceded_by_name = i > 0 && is_name(bytes[i - 1]);
    if preceded_by_name || !after.starts_with(b"json") {
        return false;
    }
    after[4..].iter().find(|b| !b.is_ascii_whitespace()) == Some(&b'(')
}

fn call_at(expression: &str, start: usize) -> Result<Call, String> {
    let bytes = expression.as_bytes();
    let before = expression[..start].trim_end();
    if before.ends_with(['/', '!', '[']) {
        return Err("json() inside a path step is not supported".to_owned());
    }
    let open = start + bytes[start..].iter().position(|&b| b == b'(').unwrap_or(0);
    let mut depth = 0;
    let mut i = open;
    while i < bytes.len() {
        match bytes[i] {
            quote @ (b'"' | b'\'') => {
                i = skip_string(bytes, i, quote);
                continue;
            }
            b'(' => depth += 1,
            b')' => {
                depth -= 1;
                if depth == 0 {
                    return Ok(Call {
                        span: start..i + 1,
                        argument: open + 1..i,
                    });
                }
            }
            _ => {}
        }
        i += 1;
    }
    Err("json( has no closing parenthesis".to_owned())
}

/// Returns the index just past the string literal starting at `start`. A
/// doubled quote inside is an escaped quote, as XPath writes it.
fn skip_string(bytes: &[u8], start: usize, quote: u8) -> usize {
    let mut i = start + 1;
    while i < bytes.len() {
        if bytes[i] == quote {
            if bytes.get(i + 1) == Some(&quote) {
                i += 2;
                continue;
            }
            return i + 1;
        }
        i += 1;
    }
    bytes.len()
}

#[cfg(test)]
mod tests;
