# Drawn mocks

The Core composition in `boot/mocks.go` binds `start_run` to the model, renderer,
assets and result store. `runs` owns admission, capacity, cancellation and durable
phase checkpoints. `Execute` commits the checklist before requesting a fragment,
and each fragment before executing checks. Only an all-passing candidate reaches
asset publication and `results.PresentMock`; the native run id deduplicates a
publication interrupted by restart. Result revisions remain in the ledger.

The renderer owns Chromium through `kit/proc` and a live activity lease. Every
launch has a disposable profile and minimal environment. The browser sandbox is
kept; DevTools attaches to an isolated iframe target when Chromium moves the
sandboxed page into another process. No product endpoint exposes DevTools.
Browser descendants are joined before the profile is removed.

`CheckFragment` accepts a bounded, closed XHTML vocabulary and reconstructs it.
It rejects an entire candidate on unknown tags/attributes, executable content,
remote resources, custom styles, duplicate ids and undeclared controls. The fixed
shared template supplies all scripts/styles/fonts. Browser checks verify visible
labels, actions and overflow at both widths. A missing renderer fails the run.

The ZIP contains `index.html`, `source.xhtml`, phone/desktop sandbox frames and a
`miniapp/` starter from `design.MiniAppFiles`. The starter has local sample
interactions only; the mini-app host supplies its separately authorized gateway.
The PNG and ZIP are distinct assets. The protocol delivers only their pinned
references, and explicit asset reads return bounded binary chunks.

`TestMockFrameIsolationWithNegativeControls` probes both the synthetic Core
endpoint and the parent document, then removes the protections and requires both
probes to succeed. `TestMockEvaluationFirstVersions` measures the research-derived
fixture set; it does not qualify a model judge. The complete installed-Core test
is `go test ./e2e/m1 -run TestM3DrawnMocks`.
