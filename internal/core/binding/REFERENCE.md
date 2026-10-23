# Input binding

`Resolve` ranks an authorized work snapshot, applies the three explicit-fact rules in order, and records an unresolved default when no rule holds. It does no I/O and makes no execution or permission decision. The historical intake replay calls the same `Rule` evaluator but keeps its original focus fallback.

Core composes the snapshot inside native person admission through the ledger's `AfterAdmission` hook. Transaction-local binding metadata stays inline through `ledger.AppendSystemSnapshot`, preserving full titles even above the ordinary conversation-body threshold without material I/O under the writer. The system `input_binding` event links to the person event through both parent and `view_of`; a unique parent index and deterministic id prevent duplicates. Client retries reuse their inbox receipt. Channel owner input and explicit memory statements use the same hook; forwarded and system events do not.

The current binding enters the mandatory Waiting group of the ContextPacket. Steering carries its own binding at drain. Request evidence retains an `input-binding-v1` source and reconstructs it from the authorized ledger for replay. Record activity is a disposable projection of Git commit times.

`TestM2InputBindingInstalledCore` executes write/read record tools, checks an exact reference and an offered option during a live turn, restarts Core, retries the consumed input and binds a card input. Rule tests cover normalization, ambiguity, finished work, aging and candidate priority. Core tests cover transactional rollback, context assembly and the synthetic production replay.
