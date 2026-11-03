"""Shared root and profile configuration, independent of active profile."""
from tests.hermes_cli._learned_env import LearnedSandbox

class RootTests(LearnedSandbox):
    def test_active_profile_does_not_hide_other_profiles(self):
        self.env["HERMES_HOME"] = str(self.profiles["amber"])
        for profile in self.profiles:
            self.call("honcho_conclude", {"conclusion": "Synthetic conclusion"}, {"success": True}, profile=profile)
        self.collect()
        self.assertEqual({row["bot"] for row in self.items()}, set(self.profiles))

    def test_missing_database_reports_profile_name_only(self):
        (self.profiles["amber"] / "state.db").unlink()
        result = self.collect(ok=False)
        self.assertEqual((result.returncode, result.stderr), (1, "amber\n"))
        self.assertFalse((self.state / "cursor.json").exists())

    def test_settings_come_from_root_yaml_not_legacy_environment(self):
        from hermes_cli.learned.config import Config
        self.env["LEARNED_OWNER"] = "wrong"
        self.env["HERMES_ROOT"] = "/wrong"
        config = Config()
        self.assertEqual((config.home, config.owner, config.state), (self.hermes, "owner-fixture", self.state))
