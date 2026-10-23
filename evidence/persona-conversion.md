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

## M4 judgment revision

The [starter changes](../docs/design/preset-work-methods.md#starter-records-and-persona-changes)
refine the converted working layers without replacing their characters. Names,
address terms, voice markers and examples remain; Arin's correction example now
uses the same historical honorifics as her other examples. Sion retains firm
declarative speech and explicitly distinguishes direct checks, estimates,
unchecked results and attributed reports. No fixed closing line is introduced.

Seen: the public conversion contained Arin's topic-dependent register switch in
default voice, and no fixed nothing-to-decide line in Sion's pair. The M4 contract
requires the switch removed from every layer. Inferred: updating the embedded
files uses the existing atomic preset revision path; filenames need no change.
Unknown: text checks and scripted-provider acceptance cannot establish model
response quality. The private replay evaluation remains separate under #590.

`TestM4StarterJudgmentAndVoice` checks the specified judgment concepts, catalog
names, character and voice markers, and forbidden instructions in both layers.
It does not snapshot full sentences. `TestM1ShippedPersonaInstallAndTools` loads
each embedded pair through installed Core, replaces stale layers, observes the
prepared request and exercises a real skill tool and its conversation result.
