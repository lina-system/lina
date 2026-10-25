.PHONY: test
test:
	$(MAKE) -C unlock test
	python3 -m unittest discover -s clients/tests -v
	python3 -m unittest discover -s tools/tests -v
