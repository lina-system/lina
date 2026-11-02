# Windows Notes

- Terminal shortcuts may be intercepted before reaching the CLI. For a newline try Ctrl+Enter or Ctrl+J when Alt+Enter is captured by the terminal.
- Preserve UTF-8 configuration without a byte-order mark. Prefer the safe `lina config set <key> <value>` writer over an editor that changes encoding.
- A code-execution child failing with WinError 10106 may be missing `SYSTEMROOT`, `WINDIR`, or `COMSPEC` in its filtered environment. Inspect `tools/code_execution_env.py` before blaming the network stack. Do not dump all environment variables into a report.
- Use portable paths and preserve repository line-ending rules. Resolve active state through `$HERMES_HOME` rather than copying a machine path.
- Run repository tests through `scripts/run_tests.sh` in Git Bash with the separate test environment. Use real Windows for host-specific tests; do not fake platform detection.
