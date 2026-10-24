// SPDX-License-Identifier: Apache-2.0
// The guard binary: no arguments starts the daemon, `restore` is the rollback
// command a person can run over SSH or from another administrator account.
int lina_guard_main(int argc, char **argv);

int main(int argc, char **argv) { return lina_guard_main(argc, argv); }
