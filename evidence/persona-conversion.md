# Working persona conversion

The eight authorized source personas were read before conversion for #584.
Their working instructions are split between `core-v1.md` (role, character and
judgment) and `default-voice-v1.md` (register, situational voice and examples).
Examples describe hypothetical situations; completed-action wording requires
actual evidence. The source documents are not release inputs.

| Key | Name | Character and judgment retained | Voice and example coverage |
| --- | --- | --- | --- |
| system | Selene | Dry, slightly weary administrator; inconvenience never lowers work quality; measured facts, verified repairs and clear objections to unsafe changes | Short informal Korean; restart, diagnosis, network exposure, unmeasured results and thanks |
| lina | Lina | Reserved, meticulous secretary who likes connecting scattered work; accuracy, fair judgment, prior decisions and the person's attention matter | Formal conclusions and promises, softer explanations, rare understated care; workload, dates, changed decisions and casual conversation |
| dev | Sion | Calm development manager; delegates implementation, owns verification, rejects cosmetic fixes, controls scope and protects others' work | Firm declarative Korean; unverified worker reports, rewrite decisions, conflicting work, infrastructure failures and completion evidence |
| knowledge | Rumi | Fussy librarian with an old soul; listens before offering advice, distinguishes evidence from interpretation, preserves uncertainty and source history | Modern informal Korean with sparse old-fashioned endings and library metaphors; identity ambiguity, copied sources, wiki conflicts and listening |
| life | Arin | Bright life curator; accuracy under warmth, buying nothing as a valid choice, care without scolding, preferences learned from actual use | Light historical honorifics with modern figures on every topic, including health, money and mistakes; equipment, travel and correction examples |
| creative | Cleo | Blunt enthusiast and creative colleague; one strong direction, justified taste, criticism of artifacts, revision and original-output verification | Fast modern informal Korean, occasional artifact-directed profanity and fan expressions; composition, hooks, references and revision mistakes |
| investing | Kai | Relaxed adviser with firm loss limits; cost-aware evidence, invalidation conditions, abstention and point-in-time review; no execution | Calm informal Korean without market hype; missing evidence, backtest bias, loss limits and date corrections |
| legal | Kanna | Friendly practical adviser; clause-specific risk, source dates, precise figures and useful alternatives; drafts only | Lively informal Korean with exact document language; contract scope, settlement discrepancies, rights and deadline corrections |

Seen: the source contains these distinct characters, voices, judgment rules and
examples, alongside private material. Each public pair was checked against its
source for those five dimensions. The public catalog retains the eight specified
keys and display names. No generic role description replaces a character.

Inferred conversion decisions: role and judgment belong in core; register and
examples belong in default voice. Private names and situations in examples are
replaced with fictional, general situations. Address terms are voice choices,
not assertions about the person's identity or relationships. Existing operational
grants are replaced with the product's authority boundaries. A short plain
register for agent exchanges is included in every voice layer.

Excluded and retained only in the private source: owner identity and personal
work context; private relationships; account, host, path and internal-tool details;
biographical, family and world history. No source excerpt describing that material
is reproduced here. World material can enter through material recall, never as a
fallback persona layer.

Automated evidence uses synthetic fixtures for pair validation, publication,
failed publication, migration, preparation and installed Core conversations. It
checks state and tool results rather than incidental persona prose. The real
shipped pairs also pass structural and budget validation. These checks do not
establish subjective model fidelity; the source-to-layer comparison is separate
conversion evidence, and no live model evaluation is claimed.

## M4 persona revision

The [starter changes](../docs/design/preset-work-methods.md#starter-records-and-persona-changes)
refine the converted working layers without replacing their characters. The
M4-4 replay (#590) measured that change set and rejected it, so the shipped
layers keep the #584 conversion's text and the per-preset subsections remain the
design target rather than a description of what ships. One correction ships
because the [presets contract](../docs/design/conversation-and-memory.md#presets)
requires it independently of the replay: Arin's default voice keeps its
historical honorific register on every topic, including health, money and
mistakes, and her correction example uses that same register. No layer carries a
fixed nothing-to-decide closing, and names, characters and voice markers are
unchanged.

Seen: the #584 conversion placed a topic-dependent register switch in Arin's
default voice, and no layer carried a fixed nothing-to-decide closing. The
presets contract forbids both in a persona layer. Inferred: the switch is a
voice rule, not one of the judgment-rule changes the replay measured, so removing
it satisfies the presets contract without re-shipping the rejected tailoring.
Unknown: text checks and scripted-provider acceptance cannot establish model
response quality; the private replay remains under #590.

The shipped layers were read for every preset: none prescribes a topic-dependent
register switch or a fixed nothing-to-decide closing, and each keeps its name,
character, address terms and voice markers. The six default voices other than
Sion's and Arin's are unchanged.

`TestM4StarterLayersAreShippedRevisions` checks the structure of every pair:
both versioned revision files are present and usable, each layer has one title
heading, the core keeps at least four rule paragraphs and the default voice at
least four examples. `TestM4StarterIdentityAndVoice` checks the markers the issue
fixes one token at a time: each name, each character word, each address term,
register word and speech ending. Whitespace, punctuation and symbols are stripped
from the layer and from the token before the comparison, so `~야/~어/~지`,
`~야, ~어, ~지` and `야 어 지` are the same three markers and a layer may reword,
reorder or reseparate them. `TestM4StarterLayersHaveNoTopicSwitchOrClosing`
checks both layers of every preset for the two prohibitions, matching the topic,
register and emptiness tokens the same way, so a rewrite of a prohibition passes
and an instruction to switch or to close fails. `TestM4ChecksCatchRegressions`
proves each check fails on a violation and accepts a legitimate rewrite,
including a layer that states the prohibition itself ("do not switch register",
"do not end with nothing to decide").

None of these requires a phrase. The limit of a token check is stated rather than
hidden: a single-syllable marker such as `지` matches wherever that syllable
occurs, so the check catches a layer that drops the marker entirely, not a list
edit that removes one ending while the syllable survives elsewhere. The meaning
of the text, including Arin's correction example staying in her register, is
content review and the #590 replay's subject, not a text assertion.

The checks do not pin the judgment-rule wording: the design does not ship those
changes, and a required-phrase list fails a legitimate rewrite. A rewrite of a
shipped layer is a design change and updates this document with it. Model-response
quality remains a separate replay evaluation under #590.
