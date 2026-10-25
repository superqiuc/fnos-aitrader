#!/usr/bin/env python3
import json, os, sys, tarfile, tempfile, unittest
HERE = os.path.dirname(os.path.abspath(__file__)); ROOT = os.path.dirname(HERE)
def find_fpk():
    if len(sys.argv) > 1 and sys.argv[1].endswith(".fpk"):
        p = os.path.abspath(sys.argv[1]); del sys.argv[1]; return p
    d = os.path.join(ROOT, "dist")
    c = [os.path.join(d, f) for f in os.listdir(d) if f.endswith(".fpk")] if os.path.isdir(d) else []
    return max(c, key=os.path.getmtime) if c else ""
FPK = find_fpk()
class T(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        if not FPK: raise unittest.SkipTest("no fpk")
        cls.tmp = tempfile.TemporaryDirectory()
        with tarfile.open(FPK) as t: t.extractall(cls.tmp.name)
        cls.root = cls.tmp.name
    @classmethod
    def tearDownClass(cls): cls.tmp.cleanup()
    def test_manifest(self):
        m = {}
        for line in open(os.path.join(self.root, "manifest"), encoding="utf-8"):
            if "=" in line and not line.strip().startswith("#"):
                k, _, v = line.partition("="); m[k.strip()] = v.strip()
        self.assertEqual(m.get("appname"), "aitrader")
        self.assertEqual(m.get("service_port"), "8906")
        self.assertEqual(m.get("desktop_applaunchname"), "aitrader.main")
    def test_cmds(self):
        for n in ["main","install_init","install_callback","upgrade_init","upgrade_callback","uninstall_init","uninstall_callback","config_init","config_callback"]:
            p = os.path.join(self.root, "cmd", n)
            self.assertTrue(os.path.isfile(p), n)
            self.assertTrue(os.access(p, os.X_OK), n)
    def test_wizard(self):
        for n in ["install","config"]:
            d = json.load(open(os.path.join(self.root, "wizard", n), encoding="utf-8"))
            self.assertIsInstance(d, list)
    def test_app(self):
        with tarfile.open(os.path.join(self.root, "app.tgz")) as t:
            names = t.getnames()
        for n in ["bin/aitrader","ui/config"]:
            self.assertIn(n, names)
    def test_ui(self):
        with tarfile.open(os.path.join(self.root, "app.tgz")) as t:
            ui = json.load(t.extractfile("ui/config"))
        self.assertEqual(ui[".url"]["aitrader.main"]["port"], "8906")
if __name__ == "__main__": unittest.main()
