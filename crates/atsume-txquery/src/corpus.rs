//! The expressions upstream's modules pass to TXQuery, for measuring how many
//! this crate compiles.
//!
//! The modules are GPL-2.0-only and never committed, so the expressions are
//! read from a checkout at test time, never stored.

use std::path::{Path, PathBuf};

use crate::engine::{Engine, json_variable};
use crate::{dialect, json};

/// The TXQuery methods whose first argument is an expression. Longer names
/// come first, so `XPathStringAll` is not read as `XPathString`.
const METHODS: [&str; 6] = [
    "XPathStringAll",
    "XPathHREFTitleAll",
    "XPathHREFAll",
    "XPathString",
    "XPathCount",
    "XPath",
];

/// Stands in for a Lua value spliced into an expression, so the expression
/// is still one.
const PLACEHOLDER: &str = "PH";

/// One expression a module passes to TXQuery.
pub(crate) struct Expression {
    pub(crate) text: String,
    pub(crate) file: PathBuf,
}

/// Reads every expression passed to a TXQuery method in the `.lua` files
/// under `lua_dir`.
pub(crate) fn expressions(lua_dir: &Path) -> std::io::Result<Vec<Expression>> {
    let mut found = Vec::new();
    for file in lua_files(lua_dir)? {
        let source = std::fs::read_to_string(&file)?;
        for text in expressions_in(&source) {
            found.push(Expression {
                text,
                file: file.clone(),
            });
        }
    }
    Ok(found)
}

fn lua_files(dir: &Path) -> std::io::Result<Vec<PathBuf>> {
    let mut files = Vec::new();
    for entry in std::fs::read_dir(dir)? {
        let path = entry?.path();
        if path.is_dir() {
            files.extend(lua_files(&path)?);
        } else if path.extension().is_some_and(|ext| ext == "lua") {
            files.push(path);
        }
    }
    files.sort();
    Ok(files)
}

/// Finds each `.Method(` call in a Lua file and returns its first argument
/// as an expression. An argument with no string literal at all, such as a
/// variable, carries nothing to check and is skipped.
fn expressions_in(source: &str) -> Vec<String> {
    let mut found = Vec::new();
    let mut at = 0;
    while let Some(offset) = source[at..].find('.') {
        let start = at + offset + 1;
        at = start;
        let Some(method) = METHODS.iter().find(|m| source[start..].starts_with(**m)) else {
            continue;
        };
        let after = &source[start + method.len()..];
        let Some(open) = after
            .find(|c: char| !c.is_whitespace())
            .filter(|&i| after[i..].starts_with('('))
        else {
            continue;
        };
        let argument = first_argument(&after[open + 1..]);
        if let Some(text) = concatenation(argument).filter(|t| !t.trim().is_empty()) {
            found.push(text);
        }
    }
    found
}

/// Returns the first argument of a Lua call whose `(` has been read.
fn first_argument(after_open: &str) -> &str {
    let mut depth = 0;
    let mut chars = after_open.char_indices().peekable();
    while let Some((at, c)) = chars.next() {
        match c {
            '\'' | '"' => skip_lua_string(&mut chars, c),
            '[' if after_open[at..].starts_with("[[") => skip_long_string(&mut chars),
            '(' | '[' | '{' => depth += 1,
            ')' | ']' | '}' | ',' if depth == 0 => return &after_open[..at],
            ')' | ']' | '}' => depth -= 1,
            _ => {}
        }
    }
    after_open
}

fn skip_lua_string(chars: &mut std::iter::Peekable<std::str::CharIndices<'_>>, quote: char) {
    while let Some((_, c)) = chars.next() {
        match c {
            '\\' => {
                chars.next();
            }
            c if c == quote => return,
            _ => {}
        }
    }
}

fn skip_long_string(chars: &mut std::iter::Peekable<std::str::CharIndices<'_>>) {
    let mut previous = ' ';
    for (_, c) in chars.by_ref() {
        if previous == ']' && c == ']' {
            return;
        }
        previous = c;
    }
}

/// Collapses a Lua concatenation into one string: literals as written, and
/// [`PLACEHOLDER`] for each other term. Returns `None` if no term is a
/// literal.
fn concatenation(expression: &str) -> Option<String> {
    let mut text = String::new();
    let mut saw_literal = false;
    for term in split_concatenation(expression) {
        let term = term.trim();
        match lua_literal(term) {
            Some(literal) => {
                text.push_str(&literal);
                saw_literal = true;
            }
            None if term.is_empty() => {}
            None => text.push_str(PLACEHOLDER),
        }
    }
    saw_literal.then_some(text)
}

/// Splits at each `..` outside brackets and string literals.
fn split_concatenation(expression: &str) -> Vec<&str> {
    let mut terms = Vec::new();
    let mut depth = 0;
    let mut start = 0;
    let mut chars = expression.char_indices().peekable();
    while let Some((at, c)) = chars.next() {
        match c {
            '\'' | '"' => skip_lua_string(&mut chars, c),
            '[' if expression[at..].starts_with("[[") => skip_long_string(&mut chars),
            '(' | '[' | '{' => depth += 1,
            ')' | ']' | '}' => depth -= 1,
            '.' if depth == 0
                && expression[at..].starts_with("..")
                && !expression[at..].starts_with("...") =>
            {
                terms.push(&expression[start..at]);
                chars.next();
                start = at + 2;
            }
            _ => {}
        }
    }
    terms.push(&expression[start..]);
    terms
}

/// Reads a term that is entirely one Lua string literal.
fn lua_literal(term: &str) -> Option<String> {
    if let Some(inner) = term.strip_prefix("[[").and_then(|t| t.strip_suffix("]]")) {
        return Some(inner.to_owned());
    }
    let quote = term.chars().next().filter(|c| matches!(c, '\'' | '"'))?;
    let inner = term.strip_prefix(quote)?.strip_suffix(quote)?;
    let mut text = String::new();
    let mut chars = inner.chars();
    while let Some(c) = chars.next() {
        if c != '\\' {
            text.push(c);
            continue;
        }
        match chars.next() {
            Some('n') => text.push('\n'),
            Some('r') => text.push('\r'),
            Some('t') => text.push('\t'),
            Some(other) => text.push(other),
            None => {}
        }
    }
    Some(text)
}

/// Compiles `expression` the way evaluation does: each `json()` argument on
/// its own, then the rest with the calls bound to variables.
fn compiles(engine: &Engine, expression: &str) -> Result<(), String> {
    let calls = json::calls(expression)?;
    let mut substituted = String::new();
    let mut last = 0;
    for (index, call) in calls.iter().enumerate() {
        compiles(engine, &expression[call.argument.clone()])?;
        substituted.push_str(&expression[last..call.span.start]);
        substituted.push_str("($");
        substituted.push_str(&json_variable(index));
        substituted.push(')');
        last = call.span.end;
    }
    substituted.push_str(&expression[last..]);
    let standard = dialect::rewrite(&substituted)?;
    engine
        .compile(&standard, calls.len())
        .map(|_| ())
        .map_err(|e| e.to_string())
}

/// The share of upstream's expressions that must compile.
const MIN_COMPILE_RATE: f64 = 0.99;

#[test]
#[ignore = "needs an FMD2 checkout in ATSUME_FMD2_DIR"]
fn upstream_expressions_compile() {
    let checkout =
        std::env::var_os("ATSUME_FMD2_DIR").expect("set ATSUME_FMD2_DIR to an FMD2 checkout");
    let found = expressions(&PathBuf::from(checkout).join("lua")).unwrap();
    assert!(
        found.len() > 1000,
        "only {} expressions found; the checkout looks wrong",
        found.len()
    );

    let engine = Engine::new();
    let failures: Vec<(&Expression, String)> = found
        .iter()
        .filter_map(|e| compiles(&engine, &e.text).err().map(|why| (e, why)))
        .collect();
    for (expression, why) in &failures {
        let file = expression
            .file
            .file_name()
            .unwrap_or_default()
            .to_string_lossy();
        eprintln!("  {file}: {}\n      {why}", expression.text);
    }
    let compiled = found.len() - failures.len();
    #[expect(
        clippy::cast_precision_loss,
        reason = "expression counts are far below 2^52"
    )]
    let rate = compiled as f64 / found.len() as f64;
    eprintln!(
        "{compiled}/{} expressions compile ({:.2}%)",
        found.len(),
        rate * 100.0
    );
    assert!(
        rate >= MIN_COMPILE_RATE,
        "compile rate below {:.0}%",
        MIN_COMPILE_RATE * 100.0
    );
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn lua_arguments_collapse_into_expressions() {
        let source = r#"
            local a = x.XPathString('//h1')
            x.XPathStringAll('//a[@id="' .. id .. '"]', LINKS)
            x.XPath([[//div[@class="c"] ]], node)
            local count = x.XPathCount(expr)
            v.XPathString("concat(a, ', ')")
        "#;
        let found = expressions_in(source);
        assert_eq!(
            found,
            [
                "//h1",
                r#"//a[@id="PH"]"#,
                r#"//div[@class="c"] "#,
                "concat(a, ', ')"
            ]
        );
    }
}
