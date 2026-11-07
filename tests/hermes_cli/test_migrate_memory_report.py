"""Migration accounting must detect loss, corruption and unexpected destination rows."""

import pytest


def test_missing_destination_row_fails_accounting():
    from hermes_cli.migrate_memory_report import ManifestRow, SourceReport

    source = (ManifestRow("session:1", "abc"), ManifestRow("session:2", "def"))
    report = SourceReport("agent", source, source[:1], matched=0, inserted=2)
    assert not report.ok
    assert report.as_dict()["missing"] == [{"native_id": "session:2", "sha256": "def"}]


@pytest.mark.parametrize("destination", [
    (("session:1", "changed"),),
    (("session:1", "abc"), ("unexpected", "abc")),
    (("session:1", "abc"), ("session:1", "abc")),
])
def test_reverse_manifest_and_multiplicity_are_checked(destination):
    from hermes_cli.migrate_memory_report import ManifestRow, SourceReport

    report = SourceReport("agent", (ManifestRow("session:1", "abc"),),
                          tuple(ManifestRow(*item) for item in destination), matched=1, inserted=0)
    assert not report.ok


def test_counts_cannot_hide_a_missing_or_unexplained_row():
    from hermes_cli.migrate_memory_report import ManifestRow, SourceReport

    rows = (ManifestRow("one", "abc"),)
    assert not SourceReport("agent", rows, rows, matched=0, inserted=0).ok
    assert SourceReport("agent", rows, rows, matched=1, inserted=0).ok
