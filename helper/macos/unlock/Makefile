CC = cc
CFLAGS = -std=c11 -Wall -Wextra -Werror -O2 -Isrc
BUILD = build
MACFLAGS = -mmacosx-version-min=14.4
FRAMEWORKS = -framework CoreFoundation -framework IOKit -framework Security
AGENT = $(BUILD)/Hermes Unlock Agent.app
BUNDLE = $(BUILD)/HermesUnlockAuthorizationPlugin.bundle
BINARY = $(BUNDLE)/Contents/MacOS/HermesUnlockAuthorizationPlugin

.PHONY: test macos native-check native-test clean
$(BUILD):
	mkdir -p $(BUILD)

$(BUILD)/grant-test: src/grant.c src/grant.h tests/grant_test.c | $(BUILD)
	$(CC) $(CFLAGS) tests/grant_test.c src/grant.c -o $@

$(BUILD)/flow-test: src/flow.c src/flow.h src/grant.c tests/flow_test.c | $(BUILD)
	$(CC) $(CFLAGS) tests/flow_test.c src/flow.c src/grant.c -o $@

test: $(BUILD)/grant-test $(BUILD)/flow-test
	./$(BUILD)/grant-test
	./$(BUILD)/flow-test
	python3 -m unittest discover -s tests -p 'test_*.py' -v

macos: test | $(BUILD)
	test "$$(uname -s)" = Darwin
	mkdir -p $(BUNDLE)/Contents/MacOS
	$(CC) $(CFLAGS) $(MACFLAGS) -bundle src/authorization_plugin.c src/grant.c src/macos_context.c $(FRAMEWORKS) -o $(BINARY)
	cp Info.plist $(BUNDLE)/Contents/Info.plist
	codesign --force --sign - $(BUNDLE)
	$(CC) $(CFLAGS) $(MACFLAGS) src/unlockctl.c src/flow.c src/grant.c src/macos_context.c src/runtime.c $(FRAMEWORKS) -o $(BUILD)/hermes-unlockctl
	codesign --force --sign - $(BUILD)/hermes-unlockctl
	mkdir -p "$(AGENT)/Contents/MacOS"
	$(CC) $(CFLAGS) $(MACFLAGS) -fobjc-arc src/gui_agent.m src/keychain_probe.m src/runtime.c src/macos_context.c $(FRAMEWORKS) -framework AppKit -framework ApplicationServices -framework LocalAuthentication -o "$(AGENT)/Contents/MacOS/hermes-unlock-agent"
	cp AgentInfo.plist "$(AGENT)/Contents/Info.plist"
	codesign --force --sign - "$(AGENT)"
	$(CC) $(CFLAGS) $(MACFLAGS) -fobjc-arc -DHU_NO_AGENT_MAIN tests/native_safety_test.m src/gui_agent.m src/keychain_probe.m src/runtime.c src/macos_context.c $(FRAMEWORKS) -framework AppKit -framework ApplicationServices -framework LocalAuthentication -o $(BUILD)/native-safety-test
	codesign --force --sign - $(BUILD)/native-safety-test
	$(CC) $(CFLAGS) $(MACFLAGS) tests/plugin_test.c -framework Security -o $(BUILD)/plugin-test
	$(CC) $(CFLAGS) $(MACFLAGS) -Dgeteuid=hu_test_euid tests/plugin_logic_test.c src/authorization_plugin.c -framework Security -o $(BUILD)/plugin-logic-test

native-check: macos
	./$(BUILD)/plugin-test $(BINARY)
	./$(BUILD)/plugin-logic-test
	./$(BUILD)/native-safety-test
	codesign --verify --strict $(BUNDLE)
	codesign --verify --strict "$(AGENT)"

native-test: native-check
	./$(BUILD)/hermes-unlockctl status
	"$(AGENT)/Contents/MacOS/hermes-unlock-agent" permissions

clean:
	rm -rf $(BUILD)
