//! Translating `css("…")` selectors into XPath.
//!
//! internettools evaluates `css()` itself. The modules use plain selectors,
//! so this covers type, `*`, `#id`, `.class`, attribute tests, the four
//! combinators, and the structural pseudo-classes that count siblings.

/// Translates a selector list into an XPath expression selecting the
/// matching elements below the context node, in document order.
pub(crate) fn to_xpath(selector: &str) -> Result<String, String> {
    let paths = split_top_level(selector, ',')
        .into_iter()
        .map(|complex| complex_to_xpath(complex.trim()))
        .collect::<Result<Vec<_>, _>>()?;
    Ok(paths.join(" | "))
}

/// How one compound selector relates to the one before it.
#[derive(Clone, Copy)]
enum Combinator {
    Descendant,
    Child,
    Adjacent,
    Sibling,
}

impl Combinator {
    /// The XPath that leads from the previous element to this one.
    fn axis(self) -> &'static str {
        match self {
            Self::Descendant => "//",
            Self::Child => "/",
            Self::Adjacent => "/following-sibling::*[1]/self::",
            Self::Sibling => "/following-sibling::",
        }
    }

    fn from_char(c: char) -> Option<Self> {
        match c {
            '>' => Some(Self::Child),
            '+' => Some(Self::Adjacent),
            '~' => Some(Self::Sibling),
            _ => None,
        }
    }
}

/// Translates one complex selector, such as `div.list > a`.
fn complex_to_xpath(complex: &str) -> Result<String, String> {
    if complex.is_empty() {
        return Err("empty selector".to_owned());
    }
    let mut xpath = String::from(".");
    let mut combinator = Combinator::Descendant;
    let mut rest = complex;
    loop {
        let (compound, after) = split_compound(rest);
        xpath.push_str(combinator.axis());
        xpath.push_str(&compound_to_xpath(compound)?);
        let after = after.trim_start();
        if after.is_empty() {
            return Ok(xpath);
        }
        combinator = Combinator::from_char(after.chars().next().unwrap_or(' '))
            .unwrap_or(Combinator::Descendant);
        rest = if matches!(combinator, Combinator::Descendant) {
            after
        } else {
            after[1..].trim_start()
        };
    }
}

/// Splits off the first compound selector, which ends at whitespace or a
/// combinator outside brackets, parentheses and quotes.
fn split_compound(selector: &str) -> (&str, &str) {
    let mut depth = 0;
    let mut quote = None;
    for (at, c) in selector.char_indices() {
        match (quote, c) {
            (Some(q), _) if c == q => quote = None,
            (None, '"' | '\'') => quote = Some(c),
            (None, '[' | '(') => depth += 1,
            (None, ']' | ')') => depth -= 1,
            (None, c)
                if depth == 0 && (c.is_whitespace() || Combinator::from_char(c).is_some()) =>
            {
                return (&selector[..at], &selector[at..]);
            }
            _ => {}
        }
    }
    (selector, "")
}

/// Translates a compound selector, such as `li.chapter[data-id]`, into an
/// element test with its predicates.
fn compound_to_xpath(compound: &str) -> Result<String, String> {
    let type_end = compound
        .find(['#', '.', '[', ':'])
        .unwrap_or(compound.len());
    let element = match &compound[..type_end] {
        "" | "*" => "*".to_owned(),
        name => name.to_ascii_lowercase(),
    };
    let mut xpath = element;
    let mut rest = &compound[type_end..];
    while !rest.is_empty() {
        let (predicate, after) = simple_selector(rest)?;
        xpath.push('[');
        xpath.push_str(&predicate);
        xpath.push(']');
        rest = after;
    }
    Ok(xpath)
}

/// Translates the simple selector at the start of `rest` into a predicate,
/// returning it and what follows.
fn simple_selector(rest: &str) -> Result<(String, &str), String> {
    let marker = rest.chars().next().unwrap_or(' ');
    let body = &rest[1..];
    match marker {
        '#' => {
            let (id, after) = identifier(body);
            Ok((format!("@id = {}", quoted(id)), after))
        }
        '.' => {
            let (class, after) = identifier(body);
            Ok((has_word("@class", class), after))
        }
        '[' => {
            let end = body.find(']').ok_or("unclosed attribute selector")?;
            Ok((attribute_test(&body[..end])?, &body[end + 1..]))
        }
        ':' => pseudo_class(body),
        other => Err(format!("unexpected {other:?} in selector")),
    }
}

/// Reads a CSS identifier from the start of `text`.
fn identifier(text: &str) -> (&str, &str) {
    let end = text
        .find(|c: char| !(c.is_alphanumeric() || c == '-' || c == '_'))
        .unwrap_or(text.len());
    text.split_at(end)
}

/// Translates the inside of `[…]`: a bare attribute name, or a name, an
/// operator and a value.
fn attribute_test(inside: &str) -> Result<String, String> {
    let Some(op_start) = inside.find(['=', '~', '^', '$', '*', '|']) else {
        return Ok(format!("@{}", inside.trim().to_ascii_lowercase()));
    };
    let name = format!("@{}", inside[..op_start].trim().to_ascii_lowercase());
    let (operator, value) = inside[op_start..]
        .split_once('=')
        .ok_or("attribute operator without =")?;
    let value = value.trim().trim_matches(['"', '\'']);
    let test = match operator {
        "" => format!("{name} = {}", quoted(value)),
        // A value with whitespace in it can never be one word of the list.
        "~" if value.is_empty() || value.contains(char::is_whitespace) => "false()".to_owned(),
        "~" => has_word(&name, value),
        "^" => format!("starts-with({name}, {})", quoted(value)),
        "$" => format!("ends-with({name}, {})", quoted(value)),
        "*" => format!("contains({name}, {})", quoted(value)),
        "|" => format!(
            "{name} = {v} or starts-with({name}, {d})",
            v = quoted(value),
            d = quoted(&format!("{value}-"))
        ),
        other => return Err(format!("unknown attribute operator {other:?}=")),
    };
    Ok(test)
}

/// Translates a pseudo-class. Only the ones that count siblings are
/// supported; their argument must be a whole number.
fn pseudo_class(body: &str) -> Result<(String, &str), String> {
    let (name, after) = identifier(body);
    let (argument, after) = match after.strip_prefix('(') {
        Some(inner) => {
            let end = inner.find(')').ok_or("unclosed pseudo-class")?;
            (Some(inner[..end].trim()), &inner[end + 1..])
        }
        None => (None, after),
    };
    let position = |text: Option<&str>| -> Result<u64, String> {
        let n: u64 = text
            .unwrap_or("")
            .parse()
            .map_err(|_| format!(":{name} needs a whole number"))?;
        Ok(n.saturating_sub(1))
    };
    let predicate = match name {
        "first-child" => "not(preceding-sibling::*)".to_owned(),
        "last-child" => "not(following-sibling::*)".to_owned(),
        "nth-child" => format!("count(preceding-sibling::*) = {}", position(argument)?),
        "nth-last-child" => format!("count(following-sibling::*) = {}", position(argument)?),
        other => return Err(format!("unsupported pseudo-class :{other}")),
    };
    Ok((predicate, after))
}

/// Tests whether a space-separated attribute holds `word`, as `.class` and
/// `[attr~=word]` do.
fn has_word(attribute: &str, word: &str) -> String {
    format!(
        "contains(concat(' ', normalize-space({attribute}), ' '), {})",
        quoted(&format!(" {word} "))
    )
}

/// Writes `text` as an XPath string literal.
fn quoted(text: &str) -> String {
    format!("\"{}\"", text.replace('"', "\"\""))
}

/// Splits `text` at `separator`, except inside brackets, parentheses and
/// quotes.
fn split_top_level(text: &str, separator: char) -> Vec<&str> {
    let mut parts = Vec::new();
    let mut depth = 0;
    let mut quote = None;
    let mut start = 0;
    for (at, c) in text.char_indices() {
        match (quote, c) {
            (Some(q), _) if c == q => quote = None,
            (None, '"' | '\'') => quote = Some(c),
            (None, '[' | '(') => depth += 1,
            (None, ']' | ')') => depth -= 1,
            (None, c) if c == separator && depth == 0 => {
                parts.push(&text[start..at]);
                start = at + c.len_utf8();
            }
            _ => {}
        }
    }
    parts.push(&text[start..]);
    parts
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn selectors_the_modules_use_translate() {
        let class =
            |c: &str| format!("contains(concat(' ', normalize-space(@class), ' '), \" {c} \")");
        assert_eq!(
            to_xpath("div#info > h1").unwrap(),
            ".//div[@id = \"info\"]/h1"
        );
        assert_eq!(
            to_xpath("div.chapter_list > ul > li > .ch_top > a").unwrap(),
            format!(
                ".//div[{}]/ul/li/*[{}]/a",
                class("chapter_list"),
                class("ch_top")
            )
        );
        assert_eq!(
            to_xpath("div#chapters_list div.chapters_list > a").unwrap(),
            format!(
                ".//div[@id = \"chapters_list\"]//div[{}]/a",
                class("chapters_list")
            )
        );
        assert_eq!(
            to_xpath("table.ptt>tbody>tr>td:nth-last-child(2)>a").unwrap(),
            format!(
                ".//table[{}]/tbody/tr/td[count(following-sibling::*) = 1]/a",
                class("ptt")
            )
        );
    }

    #[test]
    fn attribute_tests_and_lists_translate() {
        assert_eq!(
            to_xpath("a[href^='/manga']").unwrap(),
            ".//a[starts-with(@href, \"/manga\")]"
        );
        assert_eq!(
            to_xpath("img[data-src], h1 + p").unwrap(),
            ".//img[@data-src] | .//h1/following-sibling::*[1]/self::p"
        );
        assert_eq!(to_xpath("[title~='a b']").unwrap(), ".//*[false()]");
    }

    #[test]
    fn unsupported_selectors_are_refused() {
        assert!(to_xpath("a:hover").is_err());
        assert!(to_xpath("").is_err());
        assert!(to_xpath("li:nth-child(odd)").is_err());
    }
}
