import json
import os
import subprocess
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))


def run(*args):
    return subprocess.run([sys.executable, os.path.join(HERE, "report.py"), *args], capture_output=True, text=True, check=True).stdout


class ReportTest(unittest.TestCase):
    def setUp(self):
        fd, self.path = tempfile.mkstemp(suffix=".txt")
        with os.fdopen(fd, "w") as f:
            f.write("cat dog cat\n")

    def tearDown(self):
        os.remove(self.path)

    def test_text_output_unchanged(self):
        self.assertEqual(run(self.path), "cat: 2\ndog: 1\n")

    def test_json_flag(self):
        self.assertEqual(json.loads(run("--json", self.path)), {"cat": 2, "dog": 1})

    def test_json_flag_after_paths(self):
        self.assertEqual(json.loads(run(self.path, "--json")), {"cat": 2, "dog": 1})
