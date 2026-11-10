"""What the goal judge is asked when a worker requests a REVIEW (lina#460).

``kanban_request_review`` / ``kanban request-review`` hand a card to a reviewer; they are not the
card's final completion. The two handoff gates (``tools.kanban_tools._goal_gate`` and
``hermes_cli.kanban._goal_mode_handoff_rejection``) pass the whole card as the goal, so a card that
says "closes only after the reviewer approves" -- the same-card review the lifecycle is built for --
could never be handed to its reviewer: the judge read the reviewer's missing approval as unmet work
and rejected the request, and ``kanban_complete`` is rejected too because no review has happened
(NousResearch/hermes-agent#98160, lina#460).

The contract below makes "ready for the reviewer" the definition of done for that one call. The goal
text stays the card, ``kanban_complete`` keeps judging the whole card, and an unfinished card is
still refused. Both gates import this one definition so the two cannot drift; it is the same module
the Hermes core carries as a local patch, kept out of ``goals.py`` so it can be taken over unchanged.
"""

from __future__ import annotations

from hermes_cli.goals import GoalContract


def review_handoff_contract() -> GoalContract:
    return GoalContract(
        outcome=(
            "Hand the finished work to a reviewer (a review request). This is a handoff for review, "
            "not the final completion of the card."),
        verification=(
            "Every acceptance criterion of the card that can be met before a review is met, and the "
            "handoff shows concrete evidence of it: what was changed or produced, the commands run and "
            "their results, and for a re-review the fix for each point the earlier review raised. "
            "A criterion that needs the reviewer's approval or close-out, or work that only starts "
            "after the review (follow-up or dependent cards), can only happen after this request: "
            "do not ask for it as evidence here and do not withhold DONE because it is missing."))
