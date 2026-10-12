# Makes the uninstalled package importable however the suite is launched.
# `python -m unittest discover -s tests` imports test modules as top-level modules
# and skips this file, so support.py repeats the same insert; this copy serves the
# `-t .` form (modules named tests.test_*) and IDE runners.
import os
import sys

_HERE = os.path.dirname(os.path.abspath(__file__))
for _path in (_HERE, os.path.join(_HERE, "..", "src")):
    if _path not in sys.path:
        sys.path.insert(0, _path)
