.PHONY: test
test:
	$(MAKE) -C unlock test
	python3 -m unittest discover -s clients/tests -v
