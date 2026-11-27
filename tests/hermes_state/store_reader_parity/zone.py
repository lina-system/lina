"""Opt-in pytest plugin for exercising the call phase in a specified time zone."""

import os
import time

import pytest


def pytest_addoption(parser):
    parser.addoption("--reader-zone", choices=("UTC", "Asia/Seoul"), default="UTC")


@pytest.hookimpl(wrapper=True, tryfirst=True)
def pytest_runtest_call(item):
    previous = os.environ.get("TZ")
    os.environ["TZ"] = item.config.getoption("--reader-zone")
    time.tzset()
    try:
        return (yield)
    finally:
        if previous is None:
            os.environ.pop("TZ", None)
        else:
            os.environ["TZ"] = previous
        time.tzset()
