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

The content comparison against the M4 contract covers each pair:

| Preset | Judgment changes in core | Voice retained |
| --- | --- | --- |
| system | No extra barriers around personal tools beyond credentials, deletion, money and publishing; execute available steps; ask before making an exception to an all-agent rule | Selene's dry informal speech and short endings |
| lina | Read original sources before listing unfinished work or asking about completion; finish legal drafts before requesting Kanna's review | Lina's reserved manner, address term, formal conclusions and softer explanations |
| dev | Keep peer reports as claims until reproduced; completion refers to execution, not merge | Sion's address term and declarative register, with direct checks, estimates and unchecked results distinguished |
| knowledge | Give one concrete risk flag instead of leading with warnings; read available sources or records before asking | Rumi's librarian character, informal speech and sparse old-fashioned endings |
| life | Keep the same voice with explicit safety content; consult recorded dislikes and fixed choices; check owned-device facts before quoting them | Arin's self-reference, address term and historical honorifics, including the correction example |
| creative | Compare the favourite with stated taste; report handoffs only after sending; rerun reversible work; narrow vague rejection to one question | Cleo's address term, modern informal speech and enthusiast expressions |
| investing | Finish with stated assumptions; source and date every figure; check that a quoted close is on a trading day | Kai's relaxed informal speech and address term |
| legal | Flag an unasked legal risk once; round toward stated bounds; use the person's document labels | Kanna's lively informal speech and precise document language |

Both layers were read for each preset: none prescribes a topic-dependent
register switch or a fixed nothing-to-decide closing. The six default voices
other than Sion's and Arin's are unchanged. Character introductions and all
other examples are retained. This is a content comparison, not a model score.

`TestShippedStarterRevisionPreservesIdentityAndAgentLayers` tests all eight
presets as opaque text: the current embedded core, voice and work method must
reach the next revision together, retaining identity, agent-owned voice and
growth, prepared history and idempotence. It permits equivalent wording without
frozen vocabulary. `TestM1ShippedPersonaInstallAndTools` loads each embedded pair
through installed Core, replaces stale layers, observes the prepared request
and exercises a real skill tool and its conversation result. These tests prove
publication and preparation; the content comparison above establishes fidelity.

The persona tests check the concrete M4 requirements as well as the revision
against the layer the #584 conversion shipped (`persona/testdata/m4-baseline`):

- `TestM4StarterChangesAreCarried` checks 24 requirements across the eight
  presets, in the core or default voice named by the design. Concept groups
  cover each core change, Sion's evidence labels and Arin's correction register.
  Core and voice rules cannot be supplied by examples. Arin's correction check
  reads the quoted example, excluding its caption and surrounding voice rules.
  Kai's figure, source and as-of date must occur together, so another source
  reference elsewhere cannot hide a missing citation.
- `TestM4EachDocumentedChangeIsRequired` independently removes each requirement
  from the shipped layer while leaving the other changes in place. It also
  removes individual parts of compound requirements: the four personal-tool
  boundaries, evidence labels, explicit safety, dislikes and fixed choices,
  source and date, trading-day verification, and both rounding bounds.
- `TestM4StarterRevisionCarriesTheDocumentedChanges` retains the structural
  comparison with #584: each core and the dev/life default voices must have a
  new rule, and removed paragraphs must have replacements.
- `TestM4StarterIdentityAndVoice` retains name, character, address terms,
  register markers, examples and the line for agent exchanges for every preset,
  including the six default voices the design leaves unchanged.
- `TestM4StarterLayersHaveNoTopicSwitchOrClosing` checks both layers for every
  preset, including dev's fixed-closing prohibition and life's topic-switch
  prohibition. Existing helper regression tests retain allowed prohibitions,
  stable voice, open decisions and meaning-preserving rewrite examples.

The concept alternatives are focused textual checks, not a semantic evaluator
or a snapshot of the whole persona. A legitimate rewrite may require updating
an alternative and its removal fixture; that alone does not change the design.
Model-response quality remains a separate replay evaluation under #590.

Takeover red/green evidence: replacing only Arin's correction example with the
#584 example made `TestM4StarterChangesAreCarried/life` fail with
`voice layer does not carry the listed change "the correction example keeps
the honorific register"`. Restoring the shipped example made all `TestM4`
tests pass. The new removal matrix also exposed an initial false positive when
Kai's figure lost its source but another sentence still mentioned sources;
requiring the citation alongside the figure made that regression fail.
