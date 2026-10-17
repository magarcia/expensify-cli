import importlib.machinery
import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest import mock


loader = importlib.machinery.SourceFileLoader("dependency_notices", str(Path(__file__).with_name("notices")))
spec = importlib.util.spec_from_loader(loader.name, loader)
notices = importlib.util.module_from_spec(spec)
loader.exec_module(notices)


class NoticeTests(unittest.TestCase):
    def test_root_and_nested_licenses_have_platform_independent_order(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            standard_library = root / "licenses" / "Go-BSD-3-Clause.txt"
            standard_library.parent.mkdir()
            standard_library.write_text("Go standard library license\n", encoding="utf-8")
            module = root / "module"
            nested = module / "internal" / "shellescape" / "LICENSE"
            nested.parent.mkdir(parents=True)
            nested.write_text("Nested dependency license\n", encoding="utf-8")
            (module / "LICENSE").write_text("Module license\n", encoding="utf-8")
            modules = [{"Dir": str(module), "Path": "example.test/module", "Version": "v1.2.3"}]
            with mock.patch.object(notices, "ROOT", root), mock.patch.object(notices, "runtime_modules", return_value=modules):
                rendered = notices.render()

            root_section = "example.test/module v1.2.3 (LICENSE)"
            nested_section = "example.test/module v1.2.3 (internal/shellescape/LICENSE)"
            self.assertLess(rendered.index(root_section), rendered.index(nested_section))
            self.assertIn("Module license\n", rendered)
            self.assertIn("Nested dependency license\n", rendered)


if __name__ == "__main__":
    unittest.main()
