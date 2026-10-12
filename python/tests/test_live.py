"""The one live test (DESIGN.md, "Tests", item 3): runs only with SMKLOG_LIVE=1 and
calls status() alone. It never quotes, because a quote spends a carrier rating."""

import os
import unittest

import support  # noqa: F401  (puts src on the path)

from smklog import SmklogClient


@unittest.skipUnless(os.environ.get("SMKLOG_LIVE") == "1", "set SMKLOG_LIVE=1 to call production")
class LiveStatusTest(unittest.TestCase):
    def test_production_answers_status(self) -> None:
        liveness = SmklogClient(timeout=15).status()
        self.assertTrue(liveness.ok)
        self.assertEqual(liveness.service, "smklog-quote-api")
        self.assertTrue(liveness.version)


if __name__ == "__main__":
    unittest.main()
