# Synthetic memory questions

`questions.jsonl` and `corpus.jsonl` are the retrieval set for `lina memory eval-search`. The expected values in that set are source references. `rule_questions.json` holds the companion standing-rule questions. Their expected values are answers to an owner question or decisions at the approval prompt.

Run the rule set with a disposable store:

```sh
scripts/run_tests.sh tests/plugins/memory/lina/test_rule_questions.py -- --junitxml=rule-results.xml
```

Each JUnit case names its answering agent in brackets. Each agent hears the other agent's rules in turn. The fixed responder chooses the narrowest scope returned by `standing_rule list`. The approval case captures the real approval callback before and after the owner asks to disable approval.

| First rule-read baseline | Passed | Failed | Skipped |
| --- | ---: | ---: | ---: |
| alpha | 5 | 0 | 0 |
| beta | 5 | 0 | 0 |

`-m after_774` selects the four answer cases for each agent. They run in the default suite. Once #774 supplies the frozen rule section, repeat these questions against a fresh conversation's model input with no explicit list call for the active rules. Keep the explicit history lookup for the old wording and date. The baseline above measures scoped reads and the approval prompt.
