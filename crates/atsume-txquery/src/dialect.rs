//! Rewriting internettools' extensions into standard XPath 3.1.
//!
//! Upstream reads module expressions in its XPath 4.0 mode, with extensions
//! for JSON. Over the node tree [`json`](crate::json) builds, each extension
//! has a standard form:
//!
//! | internettools                 | XPath 3.1                         |
//! |-------------------------------|-----------------------------------|
//! | `X.a.b` after `)`, `]`, `}`   | `X/a/b`                           |
//! | `X()`, a call on a value      | `X/*`, its members                |
//! | `X("key")`, `X(2)`            | `X/*[codepoint-equal(name(), "key")]`, `X/*[2]` |
//! | `X?a`, `X?*`, `X?2`           | `X/a`, `X/*`, `X/*[2]`            |
//! | `jn:keys(X)`, `jn:members(X)` | `((X)/*/name())`, `((X)/*)`       |
//! | `css("sel")`                  | the selector as a path            |
//! | `true`, `false`, `null`       | `true()`, `false()`, `"null"`     |
//! | `TotalPage`, a capital in a name test | `TotalPage` or `totalpage`, compared exactly |
//!
//! The last row exists because upstream matches HTML names ignoring case,
//! while html5ever stores them in lowercase and JSON keys keep their case.
//! Names are compared with `codepoint-equal`, because the engine's default
//! collation ignores case and upstream reads JSON keys exactly.

use crate::css;

/// What kind of token comes before the one being read, which decides whether
/// a `.`, `(` or `?` is internettools syntax.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum Before {
    /// The start of the expression, or an operator: nothing a value ends in.
    Operator,
    /// A closing `)`, `]` or `}`, or a value this rewrite produced: a dot or
    /// a call can apply to it.
    Value,
    /// A step or variable: a lookup can apply to it, but a `(` after it
    /// starts a function call.
    Step,
}

/// Rewrites `expression`'s internettools extensions into standard XPath.
pub(crate) fn rewrite(expression: &str) -> Result<String, String> {
    let tokens = tokenize(expression)?;
    Rewriter {
        tokens: &tokens,
        at: 0,
        out: String::new(),
        before: Before::Operator,
    }
    .run()
}

/// The kinds of token the rewrite tells apart.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum Kind {
    Space,
    String,
    Number,
    Name,
    Variable,
    Symbol,
}

#[derive(Clone, Copy, Debug)]
struct Token<'e> {
    kind: Kind,
    text: &'e str,
}

/// Operators of two characters, which must not be read as two of one.
const PAIRED_SYMBOLS: [&str; 11] = [
    "//", "::", "!=", "<=", ">=", "||", "=>", ":=", "..", "<<", ">>",
];

fn tokenize(expression: &str) -> Result<Vec<Token<'_>>, String> {
    let mut tokens = Vec::new();
    let mut rest = expression;
    while let Some(first) = rest.chars().next() {
        let (kind, length) = token_at(rest, first)?;
        tokens.push(Token {
            kind,
            text: &rest[..length],
        });
        rest = &rest[length..];
    }
    Ok(tokens)
}

/// Returns the kind and length of the token `rest` starts with.
fn token_at(rest: &str, first: char) -> Result<(Kind, usize), String> {
    let starts_number = first.is_ascii_digit()
        || (first == '.' && rest[1..].starts_with(|c: char| c.is_ascii_digit()));
    Ok(match first {
        c if c.is_whitespace() => (
            Kind::Space,
            rest.find(|ch: char| !ch.is_whitespace())
                .unwrap_or(rest.len()),
        ),
        '(' if rest.starts_with("(:") => (Kind::Space, comment_length(rest)?),
        '"' | '\'' => (Kind::String, string_length(rest, first)?),
        '$' => (Kind::Variable, 1 + qname_length(&rest[1..])),
        _ if starts_number => (Kind::Number, number_length(rest)),
        c if c.is_alphabetic() || c == '_' => (Kind::Name, qname_length(rest)),
        _ => (Kind::Symbol, symbol_length(rest, first)),
    })
}

fn is_name_char(c: char) -> bool {
    c.is_alphanumeric() || matches!(c, '_' | '-' | '.')
}

/// Measures a name, which may carry a prefix: `div`, `data.items`, `jn:keys`.
fn qname_length(text: &str) -> usize {
    let local = text.find(|c: char| !is_name_char(c)).unwrap_or(text.len());
    let after = &text[local..];
    let prefixed =
        after.starts_with(':') && after[1..].starts_with(|c: char| c.is_alphabetic() || c == '_');
    if !prefixed {
        return local;
    }
    local
        + 1
        + after[1..]
            .find(|c: char| !is_name_char(c))
            .unwrap_or(after.len() - 1)
}

fn number_length(text: &str) -> usize {
    let mut length = text
        .find(|c: char| !(c.is_ascii_digit() || c == '.'))
        .unwrap_or(text.len());
    let after = &text[length..];
    if after.starts_with(['e', 'E']) {
        let exponent = after[1..].trim_start_matches(['+', '-']);
        let digits = exponent
            .find(|c: char| !c.is_ascii_digit())
            .unwrap_or(exponent.len());
        if digits > 0 {
            length += after.len() - exponent.len() + digits;
        }
    }
    length
}

/// Measures a string literal; a doubled quote inside is an escaped quote.
fn string_length(text: &str, quote: char) -> Result<usize, String> {
    let mut chars = text.char_indices().skip(1).peekable();
    while let Some((at, c)) = chars.next() {
        if c != quote {
            continue;
        }
        if chars.peek().is_some_and(|&(_, next)| next == quote) {
            chars.next();
            continue;
        }
        return Ok(at + c.len_utf8());
    }
    Err("unterminated string literal".to_owned())
}

/// Measures an XPath comment, `(: … :)`, which may nest.
fn comment_length(text: &str) -> Result<usize, String> {
    let mut depth = 0;
    let mut at = 0;
    while at < text.len() {
        if text[at..].starts_with("(:") {
            depth += 1;
            at += 2;
        } else if text[at..].starts_with(":)") {
            depth -= 1;
            at += 2;
            if depth == 0 {
                return Ok(at);
            }
        } else {
            at += text[at..].chars().next().map_or(1, char::len_utf8);
        }
    }
    Err("unterminated comment".to_owned())
}

fn symbol_length(text: &str, first: char) -> usize {
    PAIRED_SYMBOLS
        .iter()
        .find(|pair| text.starts_with(**pair))
        .map_or(first.len_utf8(), |pair| pair.len())
}

/// Rewrites a run of tokens, writing standard XPath to `out`.
struct Rewriter<'t, 'e> {
    tokens: &'t [Token<'e>],
    at: usize,
    out: String,
    before: Before,
}

impl<'e> Rewriter<'_, 'e> {
    fn run(mut self) -> Result<String, String> {
        while self.at < self.tokens.len() {
            self.step()?;
        }
        Ok(self.out)
    }

    /// Rewrites the construct at the current token and moves past it.
    fn step(&mut self) -> Result<(), String> {
        let token = self.tokens[self.at];
        let handled = match (token.kind, token.text) {
            (Kind::Symbol, ".") => self.property_chain(),
            (Kind::Symbol, "?") => self.lookup(),
            (Kind::Symbol, "(") => self.call_on_value()?,
            (Kind::Name, "jn:keys" | "jn:members" | "css") => self.function(token.text)?,
            (Kind::Name, "true" | "false" | "null") => self.literal(token.text),
            (Kind::Name, name) if name.contains(char::is_uppercase) => self.capitalised_name(name),
            _ => false,
        };
        if !handled {
            self.copy(token);
        }
        Ok(())
    }

    fn copy(&mut self, token: Token<'_>) {
        self.out.push_str(token.text);
        self.at += 1;
        if token.kind != Kind::Space {
            self.before = self.classify(token);
        }
    }

    /// Decides what a copied token is to whatever follows it.
    fn classify(&self, token: Token<'_>) -> Before {
        match (token.kind, token.text) {
            (Kind::Symbol, ")" | "]" | "}") => Before::Value,
            (Kind::Name | Kind::Variable, _) | (Kind::Symbol, "." | "..") => Before::Step,
            // `*` after an operand multiplies; anywhere else it is a step.
            (Kind::Symbol, "*") if self.before == Before::Operator => Before::Step,
            _ => Before::Operator,
        }
    }

    /// Returns the token `offset` places after the current one, if any.
    fn peek(&self, offset: usize) -> Option<Token<'e>> {
        self.tokens.get(self.at + offset).copied()
    }

    /// Returns the next token that is not whitespace, and how far ahead it is.
    fn next_significant(&self) -> Option<(usize, Token<'e>)> {
        (1..)
            .map_while(|offset| self.peek(offset).map(|t| (offset, t)))
            .find(|(_, t)| t.kind != Kind::Space)
    }

    /// `X.a.b`: each dotted name after a value is a property read.
    fn property_chain(&mut self) -> bool {
        let Some(next) = self.peek(1) else {
            return false;
        };
        if self.before != Before::Value {
            return false;
        }
        let steps: Vec<String> = match next.kind {
            Kind::Name if !next.text.split('.').any(str::is_empty) => {
                next.text.split('.').map(property_step).collect()
            }
            // `X."key"` reads a property whose name is quoted.
            Kind::String => vec![named(next.text)],
            _ => return false,
        };
        for step in steps {
            self.out.push('/');
            self.out.push_str(&step);
        }
        self.at += 2;
        self.before = Before::Value;
        true
    }

    /// `X?key` and `?key`: a lookup is a child step. After a value or step it
    /// continues the path; at the start it selects from the context item.
    fn lookup(&mut self) -> bool {
        let Some(key) = self.peek(1) else {
            return false;
        };
        let step = match (key.kind, key.text) {
            (Kind::Name, name) => name.to_owned(),
            (Kind::Symbol, "*") => "*".to_owned(),
            (Kind::Number, index) if index.bytes().all(|b| b.is_ascii_digit()) => {
                format!("*[{index}]")
            }
            (Kind::String, name) => named(name),
            _ => return false,
        };
        if self.before != Before::Operator {
            self.out.push('/');
        }
        self.out.push_str(&step);
        self.at += 2;
        self.before = Before::Value;
        true
    }

    /// `X()`, `X("key")`, `X(2)`: calling a value reads its members, a
    /// property by name, or a member by position. A call after a name is an
    /// ordinary function call and is left alone.
    fn call_on_value(&mut self) -> Result<bool, String> {
        if self.before != Before::Value {
            return Ok(false);
        }
        let close = self.closing_parenthesis()?;
        let inner = significant(&self.tokens[self.at + 1..close]);
        let selection = match inner.as_slice() {
            [] => "/*".to_owned(),
            [key] if key.kind == Kind::String => format!("/{}", named(key.text)),
            [index] if index.kind == Kind::Number => format!("/*[{}]", index.text),
            _ => {
                let key = rewrite_tokens(&self.tokens[self.at + 1..close])?;
                format!(
                    "/*[let $atsume_key := ({key}) return if ($atsume_key instance of xs:numeric) \
                     then position() = $atsume_key else codepoint-equal(name(), string($atsume_key))]"
                )
            }
        };
        self.out.push_str(&selection);
        self.at = close + 1;
        self.before = Before::Value;
        Ok(true)
    }

    /// `jn:keys(X)`, `jn:members(X)` and `css("sel")`.
    fn function(&mut self, name: &str) -> Result<bool, String> {
        let Some((offset, open)) = self.next_significant() else {
            return Ok(false);
        };
        if open.text != "(" {
            return Ok(false);
        }
        self.at += offset;
        let close = self.closing_parenthesis()?;
        let argument = &self.tokens[self.at + 1..close];
        let rewritten = match name {
            "jn:keys" => format!("(({})/*/name())", rewrite_tokens(argument)?),
            "jn:members" => format!("(({})/*)", rewrite_tokens(argument)?),
            _ => format!("({})", css_argument(argument)?),
        };
        self.out.push_str(&rewritten);
        self.at = close + 1;
        self.before = Before::Value;
        Ok(true)
    }

    /// `true`, `false` and `null` are values, as in JSONiq, unless they are a
    /// path step or a function's name.
    fn literal(&mut self, word: &str) -> bool {
        let is_step =
            matches!(self.before, Before::Step) || self.out.trim_end().ends_with(['/', '@', ':']);
        let is_call = self
            .next_significant()
            .is_some_and(|(_, t)| t.text == "(" || t.text == "::");
        if is_step || is_call {
            return false;
        }
        self.out.push_str(match word {
            "true" => "true()",
            "false" => "false()",
            _ => "\"null\"",
        });
        self.at += 1;
        self.before = Before::Value;
        true
    }

    /// A name test with a capital matches the name as written, for a JSON key,
    /// and in lowercase, for an HTML name. A prefixed name, a function's name
    /// and an axis are left alone.
    fn capitalised_name(&mut self, name: &str) -> bool {
        let is_call_or_axis = self
            .next_significant()
            .is_some_and(|(_, t)| t.text == "(" || t.text == "::");
        if name.contains(':') || is_call_or_axis {
            return false;
        }
        let test = format!(
            "*[codepoint-equal(name(), \"{name}\") or codepoint-equal(name(), \"{}\")]",
            name.to_lowercase()
        );
        self.out.push_str(&test);
        self.at += 1;
        self.before = Before::Step;
        true
    }

    /// Returns the index of the `)` closing the `(` at the current token.
    fn closing_parenthesis(&self) -> Result<usize, String> {
        let mut depth = 0;
        for (index, token) in self.tokens.iter().enumerate().skip(self.at) {
            let change = match (token.kind, token.text) {
                (Kind::Symbol, "(") => 1,
                (Kind::Symbol, ")") => -1,
                _ => 0,
            };
            depth += change;
            if change < 0 && depth == 0 {
                return Ok(index);
            }
        }
        Err("unclosed parenthesis".to_owned())
    }
}

/// Returns the step reading the property `name`. A name XPath cannot write as
/// a step, such as one starting with a digit, is matched by `name()`.
fn property_step(name: &str) -> String {
    let is_step_name = name.starts_with(|c: char| c.is_alphabetic() || c == '_');
    if is_step_name {
        name.to_owned()
    } else {
        named(&format!("\"{name}\""))
    }
}

/// Returns a step to the child whose name is exactly the string literal
/// `quoted_name`.
fn named(quoted_name: &str) -> String {
    format!("*[codepoint-equal(name(), {quoted_name})]")
}

fn rewrite_tokens(tokens: &[Token<'_>]) -> Result<String, String> {
    Rewriter {
        tokens,
        at: 0,
        out: String::new(),
        before: Before::Operator,
    }
    .run()
}

fn significant<'e>(tokens: &[Token<'e>]) -> Vec<Token<'e>> {
    tokens
        .iter()
        .copied()
        .filter(|t| t.kind != Kind::Space)
        .collect()
}

/// Translates `css()`'s argument, which must be a single string literal.
fn css_argument(argument: &[Token<'_>]) -> Result<String, String> {
    let [selector] = significant(argument)[..] else {
        return Err("css() takes one string literal".to_owned());
    };
    if selector.kind != Kind::String {
        return Err("css() takes one string literal".to_owned());
    }
    let quote = &selector.text[..1];
    let inner = &selector.text[1..selector.text.len() - 1];
    css::to_xpath(&inner.replace(&format!("{quote}{quote}"), quote))
}

#[cfg(test)]
mod tests;
