//! Pascal's `TStringList`, which modules use for links, names, page lists,
//! headers and cookies.
//!
//! This is the plain list the host reads; `upstream::string_list` is how
//! modules see it.

/// An ordered list of strings.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub(crate) struct StringList {
    items: Vec<String>,
}

impl StringList {
    /// Returns the items.
    #[must_use]
    pub(crate) fn items(&self) -> &[String] {
        &self.items
    }

    /// Appends an item.
    pub(crate) fn add(&mut self, item: impl Into<String>) {
        self.items.push(item.into());
    }

    /// Removes every item.
    pub(crate) fn clear(&mut self) {
        self.items.clear();
    }

    /// Replaces every item.
    pub(crate) fn set(&mut self, items: Vec<String>) {
        self.items = items;
    }

    /// Returns the item at a 0-based index, or `""` when out of range.
    #[must_use]
    pub(crate) fn get(&self, index: i64) -> &str {
        usize::try_from(index)
            .ok()
            .and_then(|i| self.items.get(i))
            .map_or("", String::as_str)
    }

    /// Writes the item at a 0-based index, growing the list as needed.
    pub(crate) fn put(&mut self, index: i64, item: String) {
        let Ok(index) = usize::try_from(index) else {
            return;
        };
        if self.items.len() <= index {
            self.items.resize(index + 1, String::new());
        }
        self.items[index] = item;
    }

    /// Removes the item at a 0-based index, if there is one.
    pub(crate) fn delete(&mut self, index: i64) {
        if let Some(i) = usize::try_from(index)
            .ok()
            .filter(|&i| i < self.items.len())
        {
            self.items.remove(i);
        }
    }

    /// Returns the 0-based index of the first item equal to `item`, or -1.
    #[must_use]
    pub(crate) fn index_of(&self, item: &str) -> i64 {
        let position = self.items.iter().position(|it| it == item);
        position.map_or(-1, |i| i64::try_from(i).unwrap_or(i64::MAX))
    }

    /// Reads the value of the `name=value` pair named `name`, ignoring case.
    #[must_use]
    pub(crate) fn value(&self, name: &str) -> &str {
        self.position_of(name)
            .and_then(|i| self.items[i].split_once('='))
            .map_or("", |(_, value)| value)
    }

    /// Writes a `name=value` pair, replacing any with the same name. An empty
    /// value removes the pair.
    pub(crate) fn set_value(&mut self, name: &str, value: &str) {
        let position = self.position_of(name);
        match (position, value.is_empty()) {
            (Some(i), true) => {
                self.items.remove(i);
            }
            (Some(i), false) => self.items[i] = format!("{name}={value}"),
            (None, false) => self.items.push(format!("{name}={value}")),
            (None, true) => {}
        }
    }

    /// Returns the `name=value` pairs as separate names and values.
    pub(crate) fn pairs(&self) -> impl Iterator<Item = (&str, &str)> {
        self.items
            .iter()
            .filter_map(|item| item.split_once('='))
            .map(|(name, value)| (name.trim(), value))
    }

    fn position_of(&self, name: &str) -> Option<usize> {
        self.items.iter().position(|item| {
            item.split_once('=')
                .is_some_and(|(key, _)| key.trim().eq_ignore_ascii_case(name))
        })
    }

    /// Reverses the items' order.
    pub(crate) fn reverse(&mut self) {
        self.items.reverse();
    }

    /// Returns how many items there are, as Lua reads a count.
    pub(crate) fn count(&self) -> i64 {
        i64::try_from(self.items.len()).unwrap_or(i64::MAX)
    }

    /// Renders the list as Pascal's `CommaText`: comma-separated, with an item
    /// quoted when it holds a comma, a quote or a space.
    #[must_use]
    pub(crate) fn comma_text(&self) -> String {
        let quote = |item: &String| {
            if item.contains([',', '"', ' ']) {
                format!("\"{}\"", item.replace('"', "\"\""))
            } else {
                item.clone()
            }
        };
        self.items.iter().map(quote).collect::<Vec<_>>().join(",")
    }

    /// Replaces the items by parsing a `CommaText` value.
    pub(crate) fn set_comma_text(&mut self, text: &str) {
        self.items.clear();
        let mut current = String::new();
        let mut quoted = false;
        let mut chars = text.chars().peekable();
        while let Some(c) = chars.next() {
            match c {
                '"' if quoted && chars.peek() == Some(&'"') => {
                    current.push('"');
                    chars.next();
                }
                '"' => quoted = !quoted,
                ',' if !quoted => self.items.push(std::mem::take(&mut current)),
                _ => current.push(c),
            }
        }
        if !current.is_empty() || !self.items.is_empty() {
            self.items.push(current);
        }
    }
}

/// Splits text into lines, accepting both LF and CRLF line endings.
pub(crate) fn split_lines(text: &str) -> Vec<String> {
    text.lines().map(str::to_owned).collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn comma_text_round_trips_quoted_items() {
        let mut list = StringList::default();
        list.set_comma_text(r#"a.jpg,b.jpg,"c,with,commas.jpg""#);
        assert_eq!(list.items(), ["a.jpg", "b.jpg", "c,with,commas.jpg"]);
        assert_eq!(list.comma_text(), r#"a.jpg,b.jpg,"c,with,commas.jpg""#);
    }

    #[test]
    fn values_are_name_value_pairs() {
        let mut list = StringList::default();
        list.set_value("Referer", "https://a.example");
        list.set_value("referer", "https://b.example");
        assert_eq!(list.items(), ["referer=https://b.example"]);
        assert_eq!(list.value("REFERER"), "https://b.example");
        list.set_value("Referer", "");
        assert_eq!(list.items(), [] as [String; 0]);
    }
}
