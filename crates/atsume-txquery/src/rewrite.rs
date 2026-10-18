//! Rewrites internettools' extensions into standard XPath 3.1.
//!
//! Upstream reads module expressions in its XPath 4.0 mode, which adds syntax
//! for JSON. Each addition has a standard XPath equivalent over the node tree
//! that [`json`](crate::json) builds:
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
//! | `json(X)`, `parse-json(X)`    | `($json0)`, with `X` evaluated first |
//! | `TotalPage`, a capital in a name test | `TotalPage` or `totalpage`, compared exactly |
//!
//! The last row is needed because upstream matches HTML names ignoring case,
//! while html5ever stores HTML names in lowercase and JSON keys keep their
//! case. Names are compared with `codepoint-equal` because the engine's
//! default collation ignores case, and upstream reads JSON keys exactly.

use crate::css;

/// The kind of token that came before the one being read. It decides whether a
/// `.`, `(` or `?` is internettools syntax.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum PreviousToken {
    /// The start of the expression, or an operator. Neither can end a value.
    Operator,
    /// A closing `)`, `]` or `}`, or a value that this rewrite produced. A dot
    /// or a call after it applies to that value.
    Value,
    /// A step or a variable. A lookup after it applies to it, but a `(` after
    /// it starts a function call.
    Step,
}

/// An expression in standard XPath, with the `json()` arguments it needs.
#[derive(Debug, PartialEq, Eq)]
pub(crate) struct RewrittenExpression {
    /// The expression in standard XPath. The `n`th `json()` call in it has
    /// become the variable that [`json_variable_name`]`(n)` names.
    pub(crate) xpath: String,
    /// Each `json()` call's argument as the module wrote it. Every argument is
    /// evaluated before the expression runs.
    pub(crate) json_arguments: Vec<String>,
}

/// Rewrites `expression`'s internettools extensions into standard XPath.
pub(crate) fn to_xpath(expression: &str) -> Result<RewrittenExpression, String> {
    let tokens = tokenize(expression)?;
    Rewriter::new(&tokens, Vec::new()).rewrite_all()
}

/// Returns the name of the variable that holds the tree of the `index`th
/// `json()` call.
pub(crate) fn json_variable_name(index: usize) -> String {
    format!("json{index}")
}

/// The characters that can come right before a path step. A `json(` after one
/// of them would run once for each node, so its argument cannot be evaluated
/// in advance.
const STEP_MARKERS: [char; 3] = ['/', '!', '['];

/// The kinds of token the rewrite tells apart.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum TokenKind {
    /// Whitespace or a comment. The rewrite copies it unchanged and never
    /// interprets it.
    Trivia,
    String,
    Number,
    Name,
    Variable,
    Symbol,
}

#[derive(Clone, Copy, Debug)]
struct Token<'e> {
    kind: TokenKind,
    text: &'e str,
}

/// Operators made of two characters. The tokenizer reads each of them as one
/// token, rather than as two one-character symbols.
const TWO_CHAR_OPERATORS: [&str; 11] = [
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

/// Returns the kind and length of the token at the start of `rest`. The caller
/// passes the first character of `rest` as `first`, because it has already
/// read that character to check that `rest` is not empty.
fn token_at(rest: &str, first: char) -> Result<(TokenKind, usize), String> {
    let starts_number = first.is_ascii_digit()
        || (first == '.' && rest[1..].starts_with(|c: char| c.is_ascii_digit()));
    Ok(match first {
        c if c.is_whitespace() => (
            TokenKind::Trivia,
            rest.find(|ch: char| !ch.is_whitespace())
                .unwrap_or(rest.len()),
        ),
        '(' if rest.starts_with("(:") => (TokenKind::Trivia, comment_length(rest)?),
        '"' | '\'' => (TokenKind::String, string_length(rest, first)?),
        '$' => (TokenKind::Variable, 1 + qname_length(&rest[1..])),
        _ if starts_number => (TokenKind::Number, number_length(rest)),
        c if c.is_alphabetic() || c == '_' => (TokenKind::Name, qname_length(rest)),
        _ => (TokenKind::Symbol, symbol_length(rest, first)),
    })
}

fn is_name_char(c: char) -> bool {
    c.is_alphanumeric() || matches!(c, '_' | '-' | '.')
}

/// Measures a name, which may have a prefix, such as `div`, `data.items` or
/// `jn:keys`.
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

/// Measures a string literal. Inside the literal, a doubled quote stands for
/// one quote character.
fn string_length(text: &str, quote: char) -> Result<usize, String> {
    let mut chars = text.char_indices().skip(1).peekable();
    while let Some((pos, c)) = chars.next() {
        if c != quote {
            continue;
        }
        if chars.peek().is_some_and(|&(_, next)| next == quote) {
            chars.next();
            continue;
        }
        return Ok(pos + c.len_utf8());
    }
    Err("unterminated string literal".to_owned())
}

/// Measures an XPath comment, `(: … :)`. Comments may nest.
fn comment_length(text: &str) -> Result<usize, String> {
    let mut depth = 0;
    let mut pos = 0;
    while pos < text.len() {
        if text[pos..].starts_with("(:") {
            depth += 1;
            pos += 2;
        } else if text[pos..].starts_with(":)") {
            depth -= 1;
            pos += 2;
            if depth == 0 {
                return Ok(pos);
            }
        } else {
            pos += text[pos..].chars().next().map_or(1, char::len_utf8);
        }
    }
    Err("unterminated comment".to_owned())
}

fn symbol_length(text: &str, first: char) -> usize {
    TWO_CHAR_OPERATORS
        .iter()
        .find(|pair| text.starts_with(**pair))
        .map_or(first.len_utf8(), |pair| pair.len())
}

/// Rewrites a sequence of tokens and writes the standard XPath to `out`.
struct Rewriter<'t, 'e> {
    tokens: &'t [Token<'e>],
    pos: usize,
    out: String,
    previous: PreviousToken,
    /// The `json()` arguments found so far. Nested rewrites share this list,
    /// so the variables are numbered across the whole expression.
    json_arguments: Vec<String>,
}

impl<'t, 'e> Rewriter<'t, 'e> {
    fn new(tokens: &'t [Token<'e>], json_arguments: Vec<String>) -> Self {
        Self {
            tokens,
            pos: 0,
            out: String::new(),
            previous: PreviousToken::Operator,
            json_arguments,
        }
    }

    fn rewrite_all(mut self) -> Result<RewrittenExpression, String> {
        while self.pos < self.tokens.len() {
            self.rewrite_next_construct()?;
        }
        Ok(RewrittenExpression {
            xpath: self.out,
            json_arguments: self.json_arguments,
        })
    }

    /// Rewrites the tokens of a nested part of the expression, such as a
    /// function's argument. The nested rewrite adds its `json()` arguments to
    /// the same list.
    fn rewrite_nested(&mut self, tokens: &[Token<'_>]) -> Result<String, String> {
        let json_arguments = std::mem::take(&mut self.json_arguments);
        let nested = Rewriter::new(tokens, json_arguments).rewrite_all()?;
        self.json_arguments = nested.json_arguments;
        Ok(nested.xpath)
    }

    /// Rewrites the construct that starts at the current token, and moves past
    /// it.
    fn rewrite_next_construct(&mut self) -> Result<(), String> {
        let token = self.tokens[self.pos];
        let handled = match (token.kind, token.text) {
            (TokenKind::Symbol, ".") => self.property_chain(),
            (TokenKind::Symbol, "?") => self.lookup(),
            (TokenKind::Symbol, "(") => self.call_on_value()?,
            (TokenKind::Name, name @ ("json" | "parse-json")) => self.json_call(name)?,
            (TokenKind::Name, "jn:keys" | "jn:members" | "css") => {
                self.jsoniq_or_css_call(token.text)?
            }
            (TokenKind::Name, "true" | "false" | "null") => self.json_literal(token.text),
            (TokenKind::Name, name) if name.contains(char::is_uppercase) => {
                self.capitalized_name(name)
            }
            _ => false,
        };
        if !handled {
            self.copy(token);
        }
        Ok(())
    }

    fn copy(&mut self, token: Token<'_>) {
        self.out.push_str(token.text);
        self.pos += 1;
        if token.kind != TokenKind::Trivia {
            self.previous = self.classify(token);
        }
    }

    /// Decides how a copied token affects the reading of the token after it.
    fn classify(&self, token: Token<'_>) -> PreviousToken {
        match (token.kind, token.text) {
            (TokenKind::Symbol, ")" | "]" | "}") => PreviousToken::Value,
            (TokenKind::Name | TokenKind::Variable, _) | (TokenKind::Symbol, "." | "..") => {
                PreviousToken::Step
            }
            // A `*` after an operand multiplies. Anywhere else, it is a step
            // that selects every element.
            (TokenKind::Symbol, "*") if self.previous == PreviousToken::Operator => {
                PreviousToken::Step
            }
            _ => PreviousToken::Operator,
        }
    }

    /// Returns the token `offset` places after the current one, if any.
    fn peek(&self, offset: usize) -> Option<Token<'e>> {
        self.tokens.get(self.pos + offset).copied()
    }

    /// Returns the next token that is not whitespace or a comment, together
    /// with how far ahead it is.
    fn next_significant(&self) -> Option<(usize, Token<'e>)> {
        (1..)
            .map_while(|offset| self.peek(offset).map(|t| (offset, t)))
            .find(|(_, t)| t.kind != TokenKind::Trivia)
    }

    /// Rewrites `X.a.b`, where each dotted name after a value reads a property.
    fn property_chain(&mut self) -> bool {
        let Some(next) = self.peek(1) else {
            return false;
        };
        if self.previous != PreviousToken::Value {
            return false;
        }
        let steps: Vec<String> = match next.kind {
            TokenKind::Name if !next.text.split('.').any(str::is_empty) => {
                next.text.split('.').map(property_step).collect()
            }
            // `X."key"` reads a property whose name is quoted.
            TokenKind::String => vec![child_named(next.text)],
            _ => return false,
        };
        for step in steps {
            self.out.push('/');
            self.out.push_str(&step);
        }
        self.pos += 2;
        self.previous = PreviousToken::Value;
        true
    }

    /// Rewrites the lookups `X?key` and `?key` as child steps. After a value or
    /// a step, a lookup continues the path. At the start of an expression, it
    /// selects from the context item.
    fn lookup(&mut self) -> bool {
        let Some(key) = self.peek(1) else {
            return false;
        };
        let step = match (key.kind, key.text) {
            (TokenKind::Name, name) => name.to_owned(),
            (TokenKind::Symbol, "*") => "*".to_owned(),
            (TokenKind::Number, index) if index.bytes().all(|b| b.is_ascii_digit()) => {
                format!("*[{index}]")
            }
            (TokenKind::String, name) => child_named(name),
            _ => return false,
        };
        if self.previous != PreviousToken::Operator {
            self.out.push('/');
        }
        self.out.push_str(&step);
        self.pos += 2;
        self.previous = PreviousToken::Value;
        true
    }

    /// Rewrites a call on a value. `X()` reads the value's members, `X("key")`
    /// reads a property by name, and `X(2)` reads a member by position. A call
    /// after a name is an ordinary function call, so it is left alone.
    fn call_on_value(&mut self) -> Result<bool, String> {
        if self.previous != PreviousToken::Value {
            return Ok(false);
        }
        let close = self.closing_parenthesis()?;
        let inner = without_trivia(&self.tokens[self.pos + 1..close]);
        let selection = match inner.as_slice() {
            [] => "/*".to_owned(),
            [key] if key.kind == TokenKind::String => format!("/{}", child_named(key.text)),
            [index] if index.kind == TokenKind::Number => format!("/*[{}]", index.text),
            _ => {
                let tokens = self.tokens;
                let key = self.rewrite_nested(&tokens[self.pos + 1..close])?;
                format!(
                    "/*[let $atsume_key := ({key}) return if ($atsume_key instance of xs:numeric) \
                     then position() = $atsume_key else codepoint-equal(name(), string($atsume_key))]"
                )
            }
        };
        self.out.push_str(&selection);
        self.pos = close + 1;
        self.previous = PreviousToken::Value;
        Ok(true)
    }

    /// Rewrites the calls `jn:keys(X)`, `jn:members(X)` and `css("sel")`.
    fn jsoniq_or_css_call(&mut self, name: &str) -> Result<bool, String> {
        let Some((offset, open)) = self.next_significant() else {
            return Ok(false);
        };
        if open.text != "(" {
            return Ok(false);
        }
        self.pos += offset;
        let close = self.closing_parenthesis()?;
        let tokens = self.tokens;
        let argument = &tokens[self.pos + 1..close];
        let rewritten = match name {
            "jn:keys" => format!("(({})/*/name())", self.rewrite_nested(argument)?),
            "jn:members" => format!("(({})/*)", self.rewrite_nested(argument)?),
            _ => format!("({})", css_argument(argument)?),
        };
        self.out.push_str(&rewritten);
        self.pos = close + 1;
        self.previous = PreviousToken::Value;
        Ok(true)
    }

    /// Rewrites `json(X)` or `parse-json(X)`, the function `name`. The
    /// argument is evaluated before the expression runs, and the call is
    /// replaced by the variable that holds the tree of the JSON it read.
    ///
    /// Upstream reads both functions' results through the same lookups, so
    /// both become trees. A `parse-json` call with a second argument, the
    /// options map of standard XPath, is left as written.
    fn json_call(&mut self, function_name: &str) -> Result<bool, String> {
        let Some((offset, open)) = self.next_significant() else {
            return Ok(false);
        };
        if open.text != "(" {
            return Ok(false);
        }
        let open_index = self.pos + offset;
        let close = self.closing_parenthesis_from(open_index)?;
        let argument_tokens = &self.tokens[open_index + 1..close];
        if function_name == "parse-json" && has_several_arguments(argument_tokens) {
            return Ok(false);
        }
        if self.out.trim_end().ends_with(STEP_MARKERS) {
            return Err(format!(
                "{function_name}() inside a path step is not supported"
            ));
        }
        let argument_text: String = argument_tokens.iter().map(|t| t.text).collect();
        let variable = json_variable_name(self.json_arguments.len());
        self.json_arguments.push(argument_text);
        // The variable is parenthesized so that a dot after it reads a
        // property, instead of becoming part of the variable's name.
        self.out.push_str("($");
        self.out.push_str(&variable);
        self.out.push(')');
        self.pos = close + 1;
        self.previous = PreviousToken::Value;
        Ok(true)
    }

    /// Rewrites `true`, `false` and `null` as values, as JSONiq reads them. A
    /// word used as a path step or as a function's name is left alone.
    fn json_literal(&mut self, word: &str) -> bool {
        let is_step = matches!(self.previous, PreviousToken::Step)
            || self.out.trim_end().ends_with(['/', '@', ':']);
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
        self.pos += 1;
        self.previous = PreviousToken::Value;
        true
    }

    /// Rewrites a name test that contains a capital letter, so that it matches
    /// both the name as written, for a JSON key, and its lowercase form, for an
    /// HTML name. Prefixed names, function names and axes are left alone.
    fn capitalized_name(&mut self, name: &str) -> bool {
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
        self.pos += 1;
        self.previous = PreviousToken::Step;
        true
    }

    /// Returns the index of the `)` closing the `(` at the current token.
    fn closing_parenthesis(&self) -> Result<usize, String> {
        self.closing_parenthesis_from(self.pos)
    }

    /// Returns the index of the `)` closing the `(` at index `open`.
    fn closing_parenthesis_from(&self, open: usize) -> Result<usize, String> {
        let mut depth = 0;
        for (index, token) in self.tokens.iter().enumerate().skip(open) {
            let change = match (token.kind, token.text) {
                (TokenKind::Symbol, "(") => 1,
                (TokenKind::Symbol, ")") => -1,
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

/// Reports whether `tokens`, the arguments of a call, hold more than one
/// argument, that is a comma outside any nested bracket.
fn has_several_arguments(tokens: &[Token<'_>]) -> bool {
    let mut depth = 0_i32;
    for token in tokens
        .iter()
        .filter(|token| token.kind == TokenKind::Symbol)
    {
        match token.text {
            "(" | "[" | "{" => depth += 1,
            ")" | "]" | "}" => depth -= 1,
            "," if depth == 0 => return true,
            _ => {}
        }
    }
    false
}

/// Returns the step that reads the property `name`. Some names, such as one
/// that starts with a digit, cannot be written as an XPath step, so the step
/// matches them with `name()` instead.
fn property_step(name: &str) -> String {
    let is_step_name = name.starts_with(|c: char| c.is_alphabetic() || c == '_');
    if is_step_name {
        name.to_owned()
    } else {
        child_named(&format!("\"{name}\""))
    }
}

/// Returns a step to the child whose name is exactly the string literal
/// `quoted_name`.
fn child_named(quoted_name: &str) -> String {
    format!("*[codepoint-equal(name(), {quoted_name})]")
}

fn without_trivia<'e>(tokens: &[Token<'e>]) -> Vec<Token<'e>> {
    tokens
        .iter()
        .copied()
        .filter(|t| t.kind != TokenKind::Trivia)
        .collect()
}

/// Translates `css()`'s argument, which must be a single string literal.
fn css_argument(argument: &[Token<'_>]) -> Result<String, String> {
    let [selector] = without_trivia(argument)[..] else {
        return Err("css() takes one string literal".to_owned());
    };
    if selector.kind != TokenKind::String {
        return Err("css() takes one string literal".to_owned());
    }
    let quote = &selector.text[..1];
    let inner = &selector.text[1..selector.text.len() - 1];
    css::to_xpath(&inner.replace(&format!("{quote}{quote}"), quote))
}

#[cfg(test)]
mod tests;
