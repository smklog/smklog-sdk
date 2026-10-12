"""Lets ``python -m smklog`` behave like the ``smklog`` console script."""

import sys

from .cli import main

sys.exit(main())
