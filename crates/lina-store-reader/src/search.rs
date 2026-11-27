use postgres::Transaction;
use serde::{Deserialize, Serialize};
use sha2::{Digest as _, Sha256};

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
pub(crate) struct Request {
    pub(crate) agent_id: String,
    pub(crate) query: String,
    pub(crate) sources: Vec<String>,
    pub(crate) limit: i64,
    pub(crate) authors: Option<Vec<String>>,
    speakers: Option<Vec<String>>,
    #[serde(default = "closed_sources")]
    disabled_sources: Vec<String>,
    // The Python lexical search accepts all sessions, including the current one.
    #[serde(rename = "session_id")]
    _session_id: Option<String>,
}

fn closed_sources() -> Vec<String> {
    vec![String::from("mail"), String::from("slack")]
}

#[derive(Serialize)]
pub(crate) struct Hit {
    uid: String,
    content_hash: String,
}

const SKILL_PREFIX: &str = "[IMPORTANT: The user has invoked the ";
const INJECTED_TURN_RE: &str = concat!(
    r"^\s*\[(?:(?:Recent|Session Arc|Durable|Depth-[0-9]+) Summary \(d[0-9]+, node [0-9]+\)\]",
    r"|Current user objective preserved from compacted history\]",
    r"|IMPORTANT: (?:The user has invoked |Background process|[0-9]+ background process|You are running as a )",
    r"|System: |System note:|Runtime note:|CONTEXT COMPACTION|CONTEXT SUMMARY\]|PRIOR CONTEXT",
    r"|ASYNC DELEGATION|Planning state preserved)"
);

// Python's str.split and str.strip also recognize the ASCII information separators.
const fn python_whitespace(character: char) -> bool {
    character.is_whitespace() || matches!(character, '\u{1c}'..='\u{1f}')
}

pub(crate) fn patterns(query: &str) -> Vec<String> {
    let mut terms = Vec::new();
    for term in query
        .split(python_whitespace)
        .filter(|term| !term.is_empty())
    {
        if !terms.contains(&term) {
            terms.push(term);
            if terms.len() == 8 {
                break;
            }
        }
    }
    terms
        .into_iter()
        .map(|term| {
            let escaped = term
                .replace('\\', r"\\")
                .replace('%', r"\%")
                .replace('_', r"\_");
            format!("%{escaped}%")
        })
        .collect()
}

fn skill_instruction(content: &str) -> Option<&str> {
    if !content.starts_with(SKILL_PREFIX) {
        return Some(content);
    }
    let (instruction, stop) = if content.contains(" skill bundle,") {
        (
            content.split_once("\nUser instruction: ")?.1,
            "\n\n[Loaded as part of the ",
        )
    } else if content.contains("The full skill content is loaded below.]") {
        (
            content.rsplit_once(
                "The user has provided the following instruction alongside the skill invocation: ",
            )?.1,
            "\n\n[Runtime note:",
        )
    } else {
        return None;
    };
    let instruction = instruction
        .split_once(stop)
        .map_or(instruction, |(before, _)| before)
        .trim_matches(python_whitespace);
    (!instruction.is_empty()).then_some(instruction)
}

pub(crate) fn search(
    transaction: &mut Transaction<'_>,
    request: &Request,
    patterns: &[String],
) -> Result<Vec<Hit>, postgres::Error> {
    let scaffolds = transaction.query(
        "SELECT message_id, content FROM memory.messages
         WHERE source = 'agent' AND role = 'user' AND forgotten_at IS NULL AND content LIKE $1",
        &[&format!("{SKILL_PREFIX}%")],
    )?;
    let mut ids = Vec::<i64>::with_capacity(scaffolds.len());
    let mut instructions = Vec::with_capacity(scaffolds.len());
    for row in &scaffolds {
        ids.push(row.try_get(0)?);
        instructions.push(skill_instruction(row.try_get(1)?));
    }

    let hits = transaction.query(
        include_str!("search.sql"),
        &[
            &ids,
            &instructions,
            &request.query,
            &patterns,
            &request.sources,
            &INJECTED_TURN_RE,
            &request.authors,
            &request.speakers,
            &request.limit,
            &request.disabled_sources,
        ],
    )?;
    hits.into_iter()
        .map(|row| {
            let uid = row.try_get(0)?;
            let content: &str = row.try_get(1)?;
            Ok(Hit {
                uid,
                content_hash: format!("{:x}", Sha256::digest(content.as_bytes())),
            })
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use super::{patterns, skill_instruction};

    #[test]
    fn substring_matching_uses_first_eight_distinct_literal_terms() {
        assert_eq!(
            patterns("one one\u{1c}two %a_b\\ c d e f g h"),
            [
                "%one%",
                "%two%",
                r"%\%a\_b\\%",
                "%c%",
                "%d%",
                "%e%",
                "%f%",
                "%g%"
            ]
        );
    }

    #[test]
    fn only_the_user_instruction_survives_skill_projection() {
        let single = "[IMPORTANT: The user has invoked the \"fixture\" skill. The full skill content is loaded below.]\n\
                      The user has provided the following instruction alongside the skill invocation: quoted marker\n\
                      The user has provided the following instruction alongside the skill invocation: 부탁해 \n\n\
                      [Runtime note: generated text]";
        let bundle = "[IMPORTANT: The user has invoked the \"fixture\" skill bundle, synthetic]\n\
                      User instruction: first\n\n[Loaded as part of the bundle]\nUser instruction: second";
        assert_eq!(skill_instruction(single), Some("부탁해"));
        assert_eq!(skill_instruction(bundle), Some("first"));
        assert_eq!(
            skill_instruction("[IMPORTANT: The user has invoked the \"fixture\" skill.]"),
            None
        );
    }
}
