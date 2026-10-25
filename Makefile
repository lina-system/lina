SWIFT ?= swift
SWIFT_FLAGS ?=
export CLANG_MODULE_CACHE_PATH := $(CURDIR)/build/ModuleCache
APP := build/LINA Helper.app

.PHONY: test swift-test app clean
test:
	$(MAKE) -C unlock test
	python3 -m unittest discover -s clients/tests -v
	python3 -m unittest discover -s clients/hermes/tests -t clients/hermes/tests -v
	python3 -m unittest discover -s tools/tests -v

ifeq ($(shell uname -s),Darwin)
test: swift-test
endif
swift-test:
	$(SWIFT) test --package-path app --disable-keychain $(SWIFT_FLAGS)
app:
	test "$$(uname -s)" = Darwin
	$(SWIFT) build --package-path app -c release --disable-keychain $(SWIFT_FLAGS)
	mkdir -p "$(APP)/Contents/MacOS" "$(APP)/Contents/Helpers"
	cp app/Resources/Info.plist "$(APP)/Contents/Info.plist"
	set -e; bin_dir="$$($(SWIFT) build --package-path app -c release --disable-keychain $(SWIFT_FLAGS) --show-bin-path)"; \
		cp "$$bin_dir/lina-helper" "$(APP)/Contents/MacOS/lina-helper"; \
		cp "$$bin_dir/lina-helperctl" "$(APP)/Contents/Helpers/lina-helperctl"
	codesign --force --sign - --identifier app.lina-os.helper.ctl "$(APP)/Contents/Helpers/lina-helperctl"
	codesign --force --sign - "$(APP)"
	codesign --verify --strict "$(APP)/Contents/Helpers/lina-helperctl"
	codesign --verify --strict "$(APP)"
clean:
	rm -rf build app/.build
	$(MAKE) -C unlock clean
